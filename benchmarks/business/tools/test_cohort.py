"""显式对照协议保留完整矩阵，不能替换默认协议或历史证据。"""

import copy
import json
import pathlib
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

import experiment
import render
import run
from test_render import synthetic_fixture, write_json
from test_provenance import CURRENT, CURRENT_TREE, SHARED_TREE, build_info, protocol_fixture


ROOT = pathlib.Path(__file__).resolve().parents[1]
COMPARISON = ROOT / "contracts/protocol-log-close.json"


class CohortTests(unittest.TestCase):
    def test_explicit_comparison_retains_workload_and_all_three_hundred_rounds(self):
        default = experiment.load_protocol(ROOT / "contracts/protocol.json")
        comparison = experiment.load_protocol(COMPARISON)
        self.assertEqual(len(default["variants"]), 9)
        self.assertEqual(len(comparison["variants"]), 10)
        for key in default.keys() - {"variants", "interpretation"}:
            self.assertEqual(comparison[key], default[key], key)
        variants = {item["id"]: item for item in comparison["variants"]}
        self.assertEqual(set(variants), {item["id"] for item in default["variants"]} | {"thinkgo-before"})
        self.assertEqual(variants["thinkgo-before"]["command"], "thinkgo")
        self.assertNotEqual(render.label_variant(variants["thinkgo-before"]), render.label_variant(variants["thinkgo"]))
        schedule = [item for model in experiment.MODELS for item in experiment.build_schedule(comparison, model)]
        self.assertEqual(len(schedule), 300)
        self.assertEqual(len({item["id"] for item in schedule}), 300)
        for variant in variants:
            self.assertEqual(sum(item["variant"] == variant for item in schedule), 30)
        samples = [dict(item, valid=True, success_rps=200 if item["variant"] == "thinkgo-before" else 1000)
                   for item in schedule if item["model"] == "closed"]
        # 对照组也参与新的共同校准，不复用旧硬件的速率或悄悄删除较慢配置。
        self.assertEqual(experiment.choose_rates(comparison, samples), dict.fromkeys(experiment.SCENARIOS, 100))

    def test_selected_protocol_bytes_are_frozen_before_any_service_starts(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "raw"
            environment = mock.Mock()
            environment.start.side_effect = RuntimeError("服务没有启动")
            arguments = SimpleNamespace(mode="verify", variants="", protocol=str(COMPARISON),
                                        go_tool="specified-go", runtime="runtime", work="work", output=output, resume=False)
            def identity(current, protocol, mode, **kwargs):
                self.assertEqual(kwargs["go_tool"], "specified-go")
                self.assertEqual(len(protocol["variants"]), 10)
                return {"mode": mode, "protocol": protocol}
            with mock.patch.object(run, "Environment", return_value=environment), \
                    mock.patch.object(run, "identity", side_effect=identity):
                with self.assertRaisesRegex(RuntimeError, "没有启动"):
                    run.execute(arguments)
            self.assertEqual((output / "contracts/protocol.json").read_bytes(), COMPARISON.read_bytes())
            environment.close.assert_called_once_with()

    def test_invalid_labels_and_build_identity_configuration_are_rejected(self):
        original = experiment.load_protocol(COMPARISON)
        changes = [
            lambda value: value["variants"][0].update(label=123),
            lambda value: value["variants"][0].update(label=""),
            lambda value: value["variants"][0].update(label="two\nlines"),
            lambda value: value["variants"][0].update(source_revision="main~1"),
            lambda value: value["variants"][0].update(command="../outside"),
            lambda value: value.update(build_provenance=True),
            lambda value: value["build_provenance"].update(settings=[]),
            lambda value: value["build_provenance"].update(go_version=""),
            lambda value: value["build_provenance"].update(module=""),
            lambda value: value["build_provenance"].update(absent_settings="-race"),
            lambda value: value["build_provenance"].update(absent_settings=["-race", "-race"]),
            lambda value: value["build_provenance"].update(absent_settings=["CGO_ENABLED"]),
        ]
        for change in changes:
            value = copy.deepcopy(original)
            change(value)
            with self.subTest(change=change), self.assertRaises(ValueError):
                experiment.validate_protocol(value)

    def test_ten_variant_renderer_keeps_every_round_and_separate_labels(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory)
            synthetic_fixture(source, variant_count=10)
            manifest = json.loads((source / "manifest.json").read_text())
            manifest["protocol"]["variants"][0]["label"] = "ThinkGo · log.close fixed"
            write_json(source / "manifest.json", manifest)
            result = render.load_results(source, allow_synthetic=True)
            summary = render.summarize(result)
            self.assertEqual(len(result["rounds"]), 300)
            self.assertEqual(len(summary["groups"]), 100)
            self.assertEqual(summary["groups"][0]["label"], "ThinkGo · log.close fixed")
            manifest["protocol"]["variants"][0]["label"] = {"invalid": True}
            write_json(source / "manifest.json", manifest)
            with self.assertRaises(ValueError):
                render.load_results(source, allow_synthetic=True)

    def test_renderer_requires_provenance_when_the_protocol_requests_it(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory)
            synthetic_fixture(source)
            manifest = json.loads((source / "manifest.json").read_text())
            manifest["protocol"]["build_provenance"] = experiment.load_protocol(COMPARISON)["build_provenance"]
            write_json(source / "manifest.json", manifest)
            with self.assertRaisesRegex(ValueError, "构建|来源"):
                render.load_results(source, allow_synthetic=True)

    def test_measure_dispatches_complete_cohort_and_recalibrates_all_variants(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "raw"
            protocol = experiment.load_protocol(COMPARISON)
            manifest = {"mode": "measure", "protocol": protocol}
            environment = mock.Mock(seed_counts={"products": 100000})
            arguments = SimpleNamespace(mode="measure", variants="", protocol=str(COMPARISON),
                                        runtime="runtime", work="work", output=output, resume=False)
            called = []
            def round_call(current, destination, item, variant, secret, rate, mode):
                called.append((item, rate))
                throughput = 200 if variant["id"] == "thinkgo-before" else 1000
                return dict(item, valid=True, success_rps=throughput, latency={"p99_ms": 1},
                            load_check={"unsuccessful": 0}, rate=rate)
            with mock.patch.object(run, "Environment", return_value=environment), \
                    mock.patch.object(run, "identity", return_value=manifest), \
                    mock.patch.object(run.subprocess, "check_output", return_value=""), \
                    mock.patch.object(run.os, "sched_setaffinity"), \
                    mock.patch.object(run, "ensure_document"), \
                    mock.patch.object(run, "verify_all") as verify, \
                    mock.patch.object(run, "run_round", side_effect=round_call), \
                    mock.patch.object(run, "progress"), mock.patch.object(run.time, "sleep"):
                run.execute(arguments)
            self.assertEqual(len(called), 300)
            self.assertTrue(all(rate == (0 if item["model"] == "closed" else 100) for item, rate in called))
            self.assertEqual(json.loads((output / "arrival-rates.json").read_text()), dict.fromkeys(experiment.SCENARIOS, 100))
            self.assertEqual(len(json.loads((output / "index.json").read_text())["rounds"]), 300)
            verify.assert_called_once()
            self.assertEqual(len(verify.call_args.args[2]["variants"]), 10)
            environment.close.assert_called_once_with()

    def test_existing_experiment_refuses_changed_protocol_and_preserves_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            original = {"mode": "measure", "protocol": experiment.load_protocol(ROOT / "contracts/protocol.json")}
            write_json(output / "manifest.json", original)
            archived = (output / "manifest.json").read_bytes()
            environment = mock.Mock()
            arguments = SimpleNamespace(mode="measure", variants="", protocol=str(COMPARISON),
                                        runtime="runtime", work="work", output=output, resume=True)
            with mock.patch.object(run, "Environment", return_value=environment), \
                    mock.patch.object(run.subprocess, "check_output", return_value=""), \
                    mock.patch.object(run, "identity", return_value={"mode": "measure", "protocol": experiment.load_protocol(COMPARISON)}):
                with self.assertRaisesRegex(ValueError, "恢复"):
                    run.execute(arguments)
            environment.start.assert_not_called()
            self.assertEqual((output / "manifest.json").read_bytes(), archived)

    def test_identity_uses_actual_provenance_and_keeps_default_manifest_compatible(self):
        with tempfile.TemporaryDirectory() as directory:
            environment = SimpleNamespace(root=ROOT, runtime=pathlib.Path(directory),
                                          binary=lambda component, name: pathlib.Path(directory) / component / name)
            def git(arguments, **kwargs):
                return CURRENT if arguments[1] == "rev-parse" else b"benchmarks/business/tools/run.py\0"
            with mock.patch.object(run.subprocess, "check_output", side_effect=git), \
                    mock.patch.object(run, "file_digest", return_value="f" * 64), \
                    mock.patch("provenance.collect", return_value={"actual": "provenance"}) as collect:
                default = run.identity(environment, experiment.load_protocol(ROOT / "contracts/protocol.json"), "measure")
                self.assertNotIn("go_build_provenance", default)
                collect.assert_not_called()
                comparison = experiment.load_protocol(COMPARISON)
                result = run.identity(environment, comparison, "measure", go_tool="specified-go")
            self.assertEqual(result["go_build_provenance"], {"actual": "provenance"})
            self.assertEqual(collect.call_args.args[-1], "specified-go")
            self.assertIn("thinkgo-before", result["binaries_sha256"])
            self.assertEqual(result["git_commit"], CURRENT)

    def test_renderer_offline_provenance_and_archived_protocol_reach_summary(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory)
            synthetic_fixture(source)
            manifest = json.loads((source / "manifest.json").read_text())
            manifest["protocol"]["build_provenance"] = protocol_fixture()["build_provenance"]
            manifest["git_commit"] = CURRENT
            manifest["host"] = {"cpu_model": "synthetic test host"}
            manifest["binaries_sha256"] = {"thinkgo": "1" * 64, "load": "2" * 64}
            manifest["go_build_provenance"] = {
                name: {"source_commit": CURRENT, "source_tree": CURRENT_TREE, "benchmark_go_tree": SHARED_TREE,
                       "binary_sha256": checksum, "build_info": build_info(CURRENT, name)}
                for name, checksum in manifest["binaries_sha256"].items()
            }
            write_json(source / "manifest.json", manifest)
            write_json(source / "contracts/protocol.json", manifest["protocol"])
            summary = render.summarize(render.load_results(source, allow_synthetic=True))
            self.assertEqual(summary["go_build_provenance"], manifest["go_build_provenance"])
            self.assertEqual(summary["host"], manifest["host"])
            altered = copy.deepcopy(manifest["protocol"])
            altered["measurement_seconds"] += 1
            write_json(source / "contracts/protocol.json", altered)
            with self.assertRaisesRegex(ValueError, "归档协议"):
                render.load_results(source, allow_synthetic=True)


if __name__ == "__main__":
    unittest.main()
