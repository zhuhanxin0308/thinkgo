"""渲染器只接受完整可核验矩阵；测试数据明确标为 synthetic。"""

import copy
import csv
import gzip
import hashlib
import json
import io
import pathlib
import tempfile
import unittest
from unittest import mock

import render


SCENARIOS = ["catalog", "detail", "order", "ticket", "mixed"]


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    encoded = json.dumps(value, sort_keys=True).encode()
    path.write_bytes(gzip.compress(encoded, mtime=0) if path.suffix == ".gz" else encoded)


def histogram(count, p99):
    return {"count": count, "overflow": 0, "p50_ms": p99 / 3, "p95_ms": p99 / 2,
            "p99_ms": p99, "max_ms": p99 + 1, "mean_ms": p99 / 3,
            "hdr_microseconds": {"LowestTrackableValue": 1, "HighestTrackableValue": 1000000,
                                 "SignificantFigures": 3, "Counts": [count]}}


def load_fixture(scenario, model, repeat, variant_index, rate):
    duration = 30
    success = duration * (100 + repeat * 10 + variant_index * 20)
    if model == "open":
        success = duration * rate
    has_errors = scenario == "mixed" and variant_index == 1 and repeat == 1
    planned = success + (3 if has_errors and model == "closed" else 0)
    if has_errors and model == "open":
        success -= 5
    cached = success if scenario == "detail" else (success // 3 if scenario == "mixed" else 0)
    completed = success + (3 if has_errors else 0)
    totals = {"planned": planned, "started": completed, "completed": completed, "success": success,
              "success_within_window": success, "status_errors": int(has_errors), "transport_errors": int(has_errors),
              "semantic_errors": int(has_errors), "dropped": 2 if has_errors and model == "open" else 0,
              "cache_hits": cached * 9 // 10,
              "cache_misses": cached - cached * 9 // 10}
    latency = histogram(completed, repeat + variant_index + 1)
    success_latency = histogram(success, repeat + variant_index + 1)
    report = {"schema_version": 1, "model": model, "config": {"scenario": scenario, "rate_per_second": rate,
              "duration_ns": duration * 1000000000, "concurrency": 64, "max_inflight": 256},
              "window_seconds": duration, "elapsed_seconds": duration + 0.1, "drain_seconds": 0.1,
              "canceled": False, "expected": planned if model == "open" else 0, "not_scheduled": 0,
              "totals": totals, "statuses": {"200": success + int(has_errors), **({"500": 1} if has_errors else {})},
              "drop_reasons": {"inflight_limit": 2} if has_errors and model == "open" else {},
              "success_rps": success / duration, "issued_success_rps": success / duration,
              "elapsed_success_rps": success / (duration + 0.1),
              "cache_hit_ratio": totals["cache_hits"] / cached if cached else 0,
              "latency": latency, "success_latency": success_latency, "http_duration": latency,
              "scheduler_lag": histogram(planned, 0), "seconds": [{"second": 0, "counts": totals}],
              "scenarios": {scenario: {"totals": totals, "latency": latency, "success_latency": success_latency,
                                        "http_duration": latency, "scheduler_lag": histogram(planned, 0)}},
              "error_samples": []}
    return report


def resources_fixture(repeat, variant_index):
    return {"schema_version": 1, "samples": [], "groups": {
        "application": {"cpu_seconds": repeat + variant_index + 1, "peak_pss_bytes": (50 + variant_index) * 1048576,
                        "peak_rss_bytes": (80 + variant_index) * 1048576, "pss_complete": True,
                        "memory_metric": "pss", "cpu_complete": True, "samples_with_processes": 3,
                        "sample_count": 3, "missing_samples": 0, "errors": []}}}


def synthetic_fixture(root, variant_count=3):
    variants = [
        {"id": "thinkgo", "framework": "ThinkGo", "version": "test", "kind": "go", "engine": "Go net/http"},
        {"id": "laravel-fpm", "framework": "Laravel", "version": "test", "kind": "fpm", "engine": "PHP-FPM + OPcache", "app": "laravel"},
        {"id": "laravel-octane", "framework": "Laravel", "version": "test", "kind": "octane", "engine": "Octane / RoadRunner", "app": "laravel"},
    ]
    for index in range(len(variants), variant_count):
        variants.append({"id": f"test-{index}", "framework": f"Test {index}", "version": "test",
                         "kind": "go", "engine": "Go net/http"})
    variants = variants[:variant_count]
    protocol = {"schema_version": 1, "variants": variants, "scenarios": SCENARIOS, "repetitions": 3,
                "measurement_seconds": 30, "closed_concurrency": 64, "open_max_inflight": 256,
                "open_rate_ratio": 0.5, "seed": 20261107, "warmup_seconds": 10, "warmup_rate": 100,
                "sample_interval_seconds": 0.5}
    write_json(root / "manifest.json", {"schema_version": 1, "mode": "synthetic", "protocol": protocol})
    rates = dict.fromkeys(SCENARIOS, 60)
    write_json(root / "arrival-rates.json", rates)
    rounds = []
    for model in ["closed", "open"]:
        for scenario in SCENARIOS:
            for variant_index, variant in enumerate(variants):
                for repeat in range(1, 4):
                    identifier = f"{model}-{scenario}-{variant['id']}-{repeat}"
                    rate = rates[scenario] if model == "open" else 0
                    load = load_fixture(scenario, model, repeat, variant_index, rate)
                    directory = root / "rounds" / identifier
                    database = {"orders": 0, "audit_rows": 0, "new_comments": 0, "pending_orders": 0,
                                "missing_audits": 0, "invalid_totals": 0, "negative_stock": 0,
                                "inventory_mismatches": 0, "ticket_version_mismatches": 0, "cross_tenant_items": 0}
                    integrity = {"valid": True, "failures": {}, "database": database, "confirmed_orders": 0,
                                 "confirmed_comments": 0, "unconfirmed_orders": 0, "unconfirmed_comments": 0}
                    for name, value in {"load.json.gz": load, "warmup.json.gz": load,
                                        "resources.json.gz": resources_fixture(repeat, variant_index),
                                        "queries.json.gz": [], "audit.json": integrity}.items():
                        write_json(directory / name, value)
                    hashes = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in directory.iterdir()}
                    result = {"id": identifier, "variant": variant["id"], "scenario": scenario, "model": model,
                              "repeat": repeat, "mode": "synthetic", "valid": True, "rate": rate,
                              "framework": variant["framework"], "version": variant["version"], "engine": variant["engine"],
                              "success_rps": load["success_rps"], "latency": {name: load["latency"][name] for name in
                                  ["p50_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms"]},
                              "totals": load["totals"], "cache_hit_ratio": load["cache_hit_ratio"],
                              "window_seconds": load["window_seconds"], "elapsed_seconds": load["elapsed_seconds"],
                              "artifacts_sha256": hashes}
                    write_json(directory / "round.json", result)
                    rounds.append(result)
    write_json(root / "index.json", {"mode": "synthetic", "rounds": rounds})
    return rounds


class RenderTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temporary.name)
        self.source = self.root / "raw"
        self.rounds = synthetic_fixture(self.source)

    def tearDown(self):
        self.temporary.cleanup()

    def rewrite_artifact(self, identifier, name, value):
        directory = self.source / "rounds" / identifier
        write_json(directory / name, value)
        index = json.loads((self.source / "index.json").read_text())
        item = next(item for item in index["rounds"] if item["id"] == identifier)
        item["artifacts_sha256"][name] = hashlib.sha256((directory / name).read_bytes()).hexdigest()
        write_json(directory / "round.json", item)
        write_json(self.source / "index.json", index)

    def test_synthetic_and_pilot_cannot_be_presented_as_measurement(self):
        with self.assertRaisesRegex(ValueError, "synthetic"):
            render.load_results(self.source)
        manifest = json.loads((self.source / "manifest.json").read_text())
        manifest["mode"] = "pilot"
        write_json(self.source / "manifest.json", manifest)
        with self.assertRaisesRegex(ValueError, "measure"):
            render.load_results(self.source, allow_synthetic=True)

    def test_complete_matrix_and_median_range_are_preserved(self):
        result = render.load_results(self.source, allow_synthetic=True)
        summary = render.summarize(result)
        self.assertEqual(len(result["rounds"]), 90)
        self.assertEqual(len(summary["groups"]), 30)
        group = next(group for group in summary["groups"] if group["variant"] == "thinkgo" and
                     group["scenario"] == "catalog" and group["model"] == "closed")
        self.assertEqual(group["metrics"]["success_rps"], {"median": 120.0, "min": 110.0, "max": 130.0, "n": 3})
        self.assertIsNone(group["metrics"]["cache_hit_percent"]["median"])
        self.assertEqual(summary["memory_metric"], "pss")
        labels = [variant["label"] for variant in summary["variants"]]
        self.assertTrue(any("FPM" in label for label in labels))
        self.assertTrue(any("Octane" in label for label in labels))
        retained = next(row for row in result["rounds"] if row["variant"] == "laravel-fpm" and
                        row["scenario"] == "mixed" and row["model"] == "open" and row["repeat"] == 1)
        self.assertEqual(retained["status_errors"], 1)
        self.assertEqual(retained["transport_errors"], 1)
        self.assertEqual(retained["semantic_errors"], 1)
        self.assertEqual(retained["dropped"], 2)

    def test_missing_or_duplicate_round_is_rejected(self):
        for change in [lambda values: values.pop(), lambda values: values.append(values[0])]:
            index = {"mode": "synthetic", "rounds": copy.deepcopy(self.rounds)}
            change(index["rounds"])
            write_json(self.source / "index.json", index)
            with self.assertRaisesRegex(ValueError, "矩阵|重复"):
                render.load_results(self.source, allow_synthetic=True)

    def test_unlisted_round_and_metadata_disagreement_are_rejected(self):
        directory = self.source / "rounds" / self.rounds[0]["id"]
        item = json.loads((directory / "round.json").read_text())
        item["success_rps"] += 1
        write_json(directory / "round.json", item)
        with self.assertRaisesRegex(ValueError, "index|索引"):
            render.load_results(self.source, allow_synthetic=True)

    def test_hash_mismatch_and_path_traversal_are_rejected(self):
        directory = self.source / "rounds" / self.rounds[0]["id"]
        (directory / "audit.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "SHA256"):
            render.load_results(self.source, allow_synthetic=True)

    def test_artifact_names_cannot_escape_the_round_directory(self):
        index = json.loads((self.source / "index.json").read_text())
        item = index["rounds"][0]
        item["artifacts_sha256"] = {"../external.json": "0" * 64, **item["artifacts_sha256"]}
        write_json(self.source / "rounds" / item["id"] / "round.json", item)
        write_json(self.source / "index.json", index)
        with self.assertRaisesRegex(ValueError, "文件名"):
            render.load_results(self.source, allow_synthetic=True)

    def test_counter_conservation_is_checked_after_hash_verification(self):
        identifier = self.rounds[0]["id"]
        path = self.source / "rounds" / identifier / "load.json.gz"
        load = json.loads(gzip.decompress(path.read_bytes()))
        load["totals"]["completed"] += 1
        self.rewrite_artifact(identifier, "load.json.gz", load)
        with self.assertRaisesRegex(ValueError, "守恒|completed|排空"):
            render.load_results(self.source, allow_synthetic=True)

    def test_audit_flag_cannot_hide_database_invariant_failure(self):
        identifier = self.rounds[0]["id"]
        value = json.loads((self.source / "rounds" / identifier / "audit.json").read_text())
        value["database"]["negative_stock"] = 1
        self.rewrite_artifact(identifier, "audit.json", value)
        with self.assertRaisesRegex(ValueError, "审计"):
            render.load_results(self.source, allow_synthetic=True)

    def test_open_rate_must_match_across_variants_and_calibration(self):
        rates = json.loads((self.source / "arrival-rates.json").read_text())
        rates["catalog"] += 1
        write_json(self.source / "arrival-rates.json", rates)
        with self.assertRaisesRegex(ValueError, "到达率"):
            render.load_results(self.source, allow_synthetic=True)

    def test_cpu_uses_drained_success_and_memory_fallback_is_comparable(self):
        result = render.load_results(self.source, allow_synthetic=True)
        first = result["rounds"][0]
        self.assertAlmostEqual(first["cpu_ms_per_success"], 2000 / 3300)
        first["peak_pss_mib"] = None
        summary = render.summarize(result)
        self.assertEqual(summary["memory_metric"], "rss")
        self.assertIn("RSS", summary["memory_note"])
        group = summary["groups"][0]
        self.assertEqual(group["metrics"]["peak_memory_mib"]["median"], 80)

    def test_failed_and_unscheduled_requests_remain_in_error_denominator(self):
        load = load_fixture("catalog", "open", 1, 0, 60)
        load["totals"].update(planned=12, started=10, completed=10, success=7, success_within_window=6,
                              status_errors=1, transport_errors=1, semantic_errors=1, dropped=2)
        load["not_scheduled"] = 3
        row = render.normalize_round(self.rounds[0], load, resources_fixture(1, 0),
                                     {"unconfirmed_orders": 2, "unconfirmed_comments": 1})
        self.assertAlmostEqual(row["unsuccessful_percent"], 100 * 8 / 15)
        self.assertAlmostEqual(row["dropped_percent"], 100 * 2 / 15)
        self.assertAlmostEqual(row["cpu_ms_per_success"], 2000 / 7)
        self.assertEqual(row["unconfirmed_orders"], 2)

    def test_absent_measurements_stay_null_instead_of_becoming_zero(self):
        load = load_fixture("catalog", "open", 1, 0, 60)
        load["totals"] = dict.fromkeys(load["totals"], 0)
        row = render.normalize_round(self.rounds[0], load, resources_fixture(1, 0),
                                     {"unconfirmed_orders": 0, "unconfirmed_comments": 0})
        for field in ["p99_ms", "cpu_ms_per_success", "cache_hit_percent", "unsuccessful_percent"]:
            self.assertIsNone(row[field])
        self.assertEqual(render.range_summary([1, None, 3]), {"median": None, "min": None, "max": None, "n": 2})
        self.assertEqual(render.compact_range({"median": None}), "N/A")
        self.assertEqual(render.compact_range({"median": 1, "min": 1, "max": 1}), "1")
        self.assertEqual(render.compact_range({"median": 2, "min": 1, "max": 3}), "2\n[1, 3]")

    def test_incomplete_cpu_or_process_samples_are_not_ranked_as_low_usage(self):
        resource = resources_fixture(1, 0)
        resource["groups"]["application"].update(cpu_complete=False, missing_samples=1,
                                                samples_with_processes=2, pss_complete=False)
        values = render.resource_metrics(resource)
        self.assertIsNone(values["application_cpu_seconds"])
        self.assertIsNone(values["peak_pss_mib"])
        self.assertIsNone(values["peak_rss_mib"])
        self.assertFalse(values["cpu_complete"])
        self.assertFalse(values["memory_complete"])
        resource = resources_fixture(1, 0)
        resource["groups"]["application"].update(pss_complete=False, peak_pss_bytes=None, memory_metric="rss")
        fallback = render.resource_metrics(resource)
        self.assertIsNone(fallback["peak_pss_mib"])
        self.assertEqual(fallback["peak_rss_mib"], 80)
        self.assertTrue(fallback["memory_complete"])

    def test_nonfinite_or_negative_numbers_are_rejected(self):
        path = self.root / "invalid.json"
        path.write_text('{"value": NaN}')
        with self.assertRaisesRegex(ValueError, "非有限"):
            render.read_json(path)
        for value in [True, float("inf"), -1]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                render.finite(value, "metric")

    def test_command_line_never_enables_synthetic_inputs(self):
        with mock.patch("render.render_results", return_value={"mode": "measure", "groups": []}) as runner:
            with mock.patch("sys.stdout", new_callable=io.StringIO):
                render.main(["--input", "raw", "--output", "figures"])
        runner.assert_called_once_with("raw", "figures")
        with mock.patch("sys.stderr", new_callable=io.StringIO):
            with self.assertRaises(SystemExit):
                render.main(["--input", "raw", "--output", "figures", "--allow-synthetic"])

    def test_dynamic_nine_variant_matrix_is_supported(self):
        source = self.root / "nine"
        synthetic_fixture(source, variant_count=9)
        result = render.load_results(source, allow_synthetic=True)
        self.assertEqual(len(result["rounds"]), 270)
        self.assertEqual(len(render.summarize(result)["variants"]), 9)

    def test_render_writes_shareable_graphs_and_machine_readable_tables(self):
        resource = resources_fixture(1, 0)
        resource["groups"]["application"].update(cpu_complete=False, pss_complete=False,
                                                peak_pss_bytes=None, memory_metric="rss", errors=["synthetic read failure"])
        self.rewrite_artifact(self.rounds[0]["id"], "resources.json.gz", resource)
        destination = self.root / "rendered"
        result = render.render_results(self.source, destination, allow_synthetic=True)
        self.assertEqual(result["mode"], "synthetic")
        self.assertEqual(result["memory_metric"], "rss")
        self.assertEqual(len(result["telemetry_warnings"]), 1)
        self.assertEqual(len(list(destination.glob("*.svg"))), 10)
        self.assertEqual(len(list(destination.glob("*.png"))), 10)
        for path in destination.glob("*.svg"):
            content = path.read_text()
            self.assertIn("SYNTHETIC", content)
            self.assertNotIn("Confidence interval", content)
        with (destination / "rounds.csv").open() as stream:
            rows = list(csv.DictReader(stream))
        self.assertEqual(len(rows), 90)
        self.assertIn("status_errors", rows[0])
        self.assertIn("dropped", rows[0])
        self.assertIn("cache_hit_percent", rows[0])
        with (destination / "summary.csv").open() as stream:
            groups = list(csv.DictReader(stream))
        self.assertEqual(len(groups), 30)
        self.assertIn("success_rps_min", groups[0])
        with self.assertRaisesRegex(ValueError, "覆盖|非空"):
            render.render_results(self.source, destination, allow_synthetic=True)


if __name__ == "__main__":
    unittest.main()
