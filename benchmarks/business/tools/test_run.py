"""结果写出与实验身份的回归测试。"""

import copy
import hashlib
import json
import gzip
import pathlib
import tempfile
import unittest
from unittest import mock

import run
from run import write_json, file_digest, load_command, validate_artifacts, ensure_document, preserve_bytes


ROOT = pathlib.Path(__file__).resolve().parents[1]


class RunTest(unittest.TestCase):
    def setUp(self):
        self.protocol = json.loads((ROOT / "contracts/protocol.json").read_text())
        self.dataset = json.loads((ROOT / "contracts/dataset.json").read_text())

    def test_evidence_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "proof.json"
            write_json(path, {"中文": [1, 2]})
            self.assertEqual(json.loads(path.read_text()), {"中文": [1, 2]})
            self.assertEqual(len(file_digest(path)), 64)
            with self.assertRaises(FileExistsError):
                write_json(path, {"replaced": True})
            self.assertEqual(json.loads(path.read_text()), {"中文": [1, 2]})

    def test_compressed_evidence_preserves_all_values(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "samples.json.gz"
            expected = {"样本": [{"pid": 10, "cpu_seconds": 1.25}]}
            write_json(path, expected)
            with gzip.open(path, "rt", encoding="utf-8") as source:
                self.assertEqual(json.load(source), expected)
            self.assertEqual(len(file_digest(path)), 64)
            with self.assertRaises(FileExistsError):
                write_json(path, {"replaced": True})
            with gzip.open(path, "rt", encoding="utf-8") as source:
                self.assertEqual(json.load(source), expected)

    def test_closed_and_open_share_the_same_business_parameters(self):
        common = dict(root=ROOT, protocol=self.protocol, dataset=self.dataset,
                      url="http://127.0.0.1:18081", run_id="run-1", scenario="order",
                      duration=30, output=pathlib.Path("result.json.gz"))
        closed = load_command(**common, rate=0)
        opened = load_command(**common, rate=100)
        self.assertEqual(closed[closed.index("-concurrency") + 1], "64")
        self.assertEqual(opened[opened.index("-rate") + 1], "100")
        for option, expected in [("-tenant-count", "4"), ("-products-per-tenant", "25000"),
                                 ("-tickets-per-tenant", "5000"), ("-timeout", "5s")]:
            self.assertEqual(closed[closed.index(option) + 1], expected)
            self.assertEqual(opened[opened.index(option) + 1], expected)
        self.assertNotIn("BENCH_SECRET=", " ".join(closed))

    def test_resume_rejects_modified_or_invalid_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = pathlib.Path(directory)
            path = folder / "audit.json"
            write_json(path, {"valid": True})
            record = {"valid": True, "artifacts_sha256": {path.name: file_digest(path)}}
            validate_artifacts(folder, record)
            path.write_text('{"valid":false}')
            with self.assertRaises(ValueError):
                validate_artifacts(folder, record)
            record["valid"] = False
            with self.assertRaises(ValueError):
                validate_artifacts(folder, record)

    def test_partial_metadata_set_is_completed_without_rewriting_existing_proof(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = pathlib.Path(directory)
            first = folder / "settings.json"
            second = folder / "plans.json"
            write_json(first, {"fsync": True})
            previous = first.read_bytes()
            ensure_document(first, lambda: self.fail("已有证据不应重新采集"))
            ensure_document(second, lambda: {"index": "catalog"})
            self.assertEqual(first.read_bytes(), previous)
            self.assertEqual(json.loads(second.read_text()), {"index": "catalog"})
            contract = folder / "schema.sql"
            preserve_bytes(contract, b"original")
            preserve_bytes(contract, b"original")
            with self.assertRaises(ValueError):
                preserve_bytes(contract, b"changed")


class RoundSnapshotTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.folder = pathlib.Path(self.temporary.name)
        self.root = self.folder / "root"
        self.output = self.folder / "output"
        for folder in [self.root / "contracts", self.output / "contracts"]:
            folder.mkdir(parents=True)
            for name in ["dataset.json", "queries.json"]:
                (folder / name).write_bytes((ROOT / "contracts" / name).read_bytes())
        self.protocol = json.loads((ROOT / "contracts/protocol.json").read_text())
        self.variant = next(variant for variant in self.protocol["variants"] if variant["id"] == "laravel-fpm")
        self.item = {"id": "closed-catalog-laravel-fpm-1", "variant": self.variant["id"],
                     "scenario": "catalog", "repeat": 1, "model": "closed"}
        self.directory = self.output / "rounds" / self.item["id"]
        self.source = b"<?php\nreturn ['schema_version' => 1];\n"
        source_digests = {name: file_digest(self.root / "contracts" / name) for name in ["dataset.json", "queries.json"]}
        content_digest = hashlib.sha256(b"thinkgo-contract-v1\0" + "".join(source_digests.values()).encode("ascii")).hexdigest()
        self.snapshot = self.folder / f"contract-{content_digest}.php"
        self.snapshot.write_bytes(self.source)
        self.metadata = {"schema_version": 1, "path": str(self.snapshot),
                         "file_sha256": hashlib.sha256(self.source).hexdigest(),
                         "content_sha256": content_digest,
                         "source_sha256": source_digests}
        self.environment = mock.Mock(root=self.root, protocol=self.protocol, php_contract_snapshot=self.metadata)
        self.environment.start_app.return_value = {"BENCH_SECRET": "private-secret", "BENCH_CONTRACT_SNAPSHOT": str(self.snapshot)}
        self.report = {"success_rps": 100, "latency": {key: 1 for key in ["p50_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms"]},
                       "totals": {"success": 300}, "cache_hit_ratio": 0.9, "window_seconds": 3, "elapsed_seconds": 3}
        self.load = mock.patch.object(run, "run_load", side_effect=self.fake_load).start()
        mock.patch("verify.preheat", return_value={"passed": True}).start()
        mock.patch.object(run, "clear_cache").start()
        mock.patch.object(run.audit, "query_statistics", return_value=[]).start()
        mock.patch.object(run.audit, "capture", return_value={"valid": True}).start()
        self.addCleanup(mock.patch.stopall)

    def fake_load(self, environment, variables, path, run_id, scenario, duration, rate, measured):
        write_json(path, self.report)
        return self.report, {"unsuccessful": 0}, {"samples": []} if measured else None, ["load", "-secret-env", "BENCH_SECRET"]

    def execute_round(self):
        return run.run_round(self.environment, self.output, self.item, self.variant, "private-secret", 0, "pilot")

    def test_round_preserves_snapshot_metadata_bytes_and_digests_without_secret(self):
        result = self.execute_round()
        self.assertEqual(result["php_contract_snapshot"], self.metadata)
        self.assertEqual((self.directory / "contract-snapshot.php").read_bytes(), self.source)
        self.assertEqual(result["artifacts_sha256"]["contract-snapshot.php"], self.metadata["file_sha256"])
        self.assertEqual(json.loads((self.directory / "round.json").read_text()), result)
        for path in self.directory.iterdir():
            self.assertNotIn(b"private-secret", path.read_bytes())
        validate_artifacts(self.directory, result)
        self.environment.stop_app.assert_called_once_with()

    def test_go_round_records_null_without_php_artifact(self):
        self.variant = next(variant for variant in self.protocol["variants"] if variant["kind"] == "go")
        self.item.update(id="closed-catalog-thinkgo-1", variant=self.variant["id"])
        self.directory = self.output / "rounds" / self.item["id"]
        self.environment.php_contract_snapshot = None
        result = self.execute_round()
        self.assertIsNone(result["php_contract_snapshot"])
        self.assertNotIn("contract-snapshot.php", result["artifacts_sha256"])
        validate_artifacts(self.directory, result)

    def test_resume_uses_archived_snapshot_when_runtime_snapshot_is_gone(self):
        expected = self.execute_round()
        self.snapshot.unlink()
        previous = (self.directory / "round.json").read_bytes()
        self.environment.reset.reset_mock()
        self.environment.start_app.reset_mock()
        self.assertEqual(self.execute_round(), expected)
        self.assertEqual((self.directory / "round.json").read_bytes(), previous)
        self.environment.reset.assert_not_called()
        self.environment.start_app.assert_not_called()

    def test_resume_rejects_missing_or_changed_snapshot_and_preserves_round(self):
        self.execute_round()
        saved = self.directory / "contract-snapshot.php"
        previous = (self.directory / "round.json").read_bytes()
        self.environment.reset.reset_mock()
        saved.write_bytes(b"changed")
        with self.assertRaises(ValueError):
            self.execute_round()
        saved.unlink()
        with self.assertRaises(ValueError):
            self.execute_round()
        self.assertEqual((self.directory / "round.json").read_bytes(), previous)
        self.environment.reset.assert_not_called()

    def test_resume_rejects_missing_metadata_artifact_binding_and_source_drift(self):
        result = self.execute_round()
        invalid = []
        for value in [None, {**self.metadata, "file_sha256": "0" * 64},
                      {**self.metadata, "source_sha256": {**self.metadata["source_sha256"], "queries.json": "0" * 64}}]:
            invalid.append({**result, "php_contract_snapshot": value})
        missing_metadata = dict(result)
        del missing_metadata["php_contract_snapshot"]
        invalid.append(missing_metadata)
        missing_artifact = copy.deepcopy(result)
        del missing_artifact["artifacts_sha256"]["contract-snapshot.php"]
        invalid.append(missing_artifact)
        for record in invalid:
            with self.subTest(record=record), self.assertRaises(ValueError):
                validate_artifacts(self.directory, record)
        contract = self.output / "contracts/queries.json"
        contract.write_bytes(b"changed")
        with self.assertRaises(ValueError):
            validate_artifacts(self.directory, result)
        contract.unlink()
        with self.assertRaises(ValueError):
            validate_artifacts(self.directory, result)

    def test_round_rejects_changed_runtime_snapshot_before_load_and_stops_app(self):
        self.snapshot.write_bytes(b"changed")
        with self.assertRaises(ValueError):
            self.execute_round()
        self.load.assert_not_called()
        self.environment.stop_app.assert_called_once_with()
        self.assertFalse((self.directory / "round.json").exists())

    def test_round_rejects_contract_drift_against_frozen_experiment_before_load(self):
        (self.output / "contracts/dataset.json").write_bytes(b"earlier-contract")
        with self.assertRaises(ValueError):
            self.execute_round()
        self.load.assert_not_called()
        self.environment.stop_app.assert_called_once_with()
        self.assertFalse((self.directory / "contract-snapshot.php").exists())

    def test_resume_rejects_php_metadata_or_snapshot_artifact_on_go_round(self):
        result = self.execute_round()
        result["framework"] = "ThinkGo"
        for metadata in [self.metadata, None]:
            result["php_contract_snapshot"] = metadata
            with self.subTest(metadata=metadata), self.assertRaises(ValueError):
                validate_artifacts(self.directory, result)

    def test_php_round_requires_snapshot_and_go_rejects_leftover_php_metadata(self):
        self.environment.php_contract_snapshot = None
        with self.assertRaises(ValueError):
            self.execute_round()
        self.environment.php_contract_snapshot = self.metadata
        self.variant = next(variant for variant in self.protocol["variants"] if variant["kind"] == "go")
        with self.assertRaises(ValueError):
            self.execute_round()
        self.load.assert_not_called()

    def test_archive_never_overwrites_existing_snapshot(self):
        self.directory.mkdir(parents=True)
        saved = self.directory / "contract-snapshot.php"
        saved.write_bytes(b"earlier-proof")
        with self.assertRaises(FileExistsError):
            run.preserve_php_contract_snapshot(self.environment, self.directory, self.variant)
        self.assertEqual(saved.read_bytes(), b"earlier-proof")


if __name__ == "__main__":
    unittest.main()
