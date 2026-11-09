"""验证完整实验矩阵、共同到达率和不会掩盖失败的测量审计。"""

import copy
import json
import math
import pathlib
import tempfile
import unittest

import experiment


def protocol_fixture():
    return {
        "schema_version": 1, "seed": 20261107,
        "scenarios": ["catalog", "detail", "order", "ticket", "mixed"],
        "repetitions": 3, "warmup_seconds": 10, "warmup_rate": 100,
        "measurement_seconds": 30, "closed_concurrency": 64,
        "open_max_inflight": 256, "open_rate_ratio": 0.5,
        "sample_interval_seconds": 0.5, "cooldown_seconds": 1,
        "variants": [
            {"id": "thinkgo", "framework": "ThinkGo", "version": "test", "engine": "Go", "kind": "go"},
            {"id": "php", "framework": "Laravel", "version": "test", "engine": "FPM", "kind": "fpm", "app": "laravel"},
        ],
    }


def histogram(count):
    return {
        "count": count, "overflow": 0, "p50_ms": 1 if count else 0,
        "p95_ms": 2 if count else 0, "p99_ms": 3 if count else 0,
        "max_ms": 4 if count else 0, "mean_ms": 2 if count else 0,
        "hdr_microseconds": {"LowestTrackableValue": 1, "HighestTrackableValue": 10000,
                             "SignificantFigures": 3, "Counts": [count]},
    }


def load_fixture():
    counts = {"planned": 100, "started": 90, "completed": 90, "success": 70,
              "success_within_window": 60, "status_errors": 10, "transport_errors": 5,
              "semantic_errors": 5, "dropped": 10, "cache_hits": 50, "cache_misses": 10}
    metrics = {"latency": histogram(90), "success_latency": histogram(70),
               "http_duration": histogram(90), "scheduler_lag": histogram(100)}
    return {
        "schema_version": 1, "model": "open", "config": {"scenario": "detail", "rate_per_second": 100, "duration_ns": 1000000000},
        "window_seconds": 1, "elapsed_seconds": 1.2, "drain_seconds": 0.2,
        "canceled": False, "expected": 100, "not_scheduled": 0, "totals": counts,
        "statuses": {"200": 75, "503": 10}, "drop_reasons": {"inflight_limit": 10},
        "success_rps": 60, "issued_success_rps": 70, "elapsed_success_rps": 70 / 1.2,
        "cache_hit_ratio": 50 / 60, **metrics,
        "scenarios": {"detail": {"totals": copy.deepcopy(counts), **copy.deepcopy(metrics)}},
        "seconds": [{"second": 0, "counts": copy.deepcopy(counts)}], "error_samples": [],
    }


class ExperimentTests(unittest.TestCase):
    def test_schedule_is_complete_repeat_blocked_and_reproducible(self):
        protocol = protocol_fixture()
        schedule = experiment.build_schedule(protocol, "closed")
        self.assertEqual(schedule, experiment.build_schedule(protocol, "closed"))
        expected = {(variant["id"], scenario, repeat)
                    for variant in protocol["variants"] for scenario in protocol["scenarios"]
                    for repeat in range(1, 4)}
        self.assertEqual({(item["variant"], item["scenario"], item["repeat"]) for item in schedule}, expected)
        self.assertEqual(len(schedule), 30)
        self.assertEqual(len({item["id"] for item in schedule}), 30)
        self.assertEqual([item["repeat"] for item in schedule], sorted(item["repeat"] for item in schedule))
        self.assertTrue(all(item["model"] == "closed" for item in schedule))
        changed = copy.deepcopy(protocol)
        changed["seed"] += 1
        self.assertNotEqual(schedule, experiment.build_schedule(changed, "closed"))
        self.assertTrue({item["id"] for item in schedule}.isdisjoint(
            item["id"] for item in experiment.build_schedule(protocol, "open")))

    def test_protocol_rejects_ambiguous_or_missing_settings(self):
        changes = [
            lambda p: p.update(seed=True), lambda p: p.update(repetitions=0),
            lambda p: p.update(warmup_rate=0), lambda p: p.update(measurement_seconds=math.inf),
            lambda p: p.update(open_rate_ratio=0), lambda p: p.update(open_rate_ratio=1.1),
            lambda p: p.update(sample_interval_seconds=-1), lambda p: p.update(closed_concurrency=False),
            lambda p: p.update(scenarios=["catalog", "catalog"]),
            lambda p: p["variants"].append(copy.deepcopy(p["variants"][0])),
            lambda p: p["variants"][0].update(id="../escape"),
            lambda p: p["variants"][0].update(version=""),
            lambda p: p.pop("warmup_rate"),
        ]
        for change in changes:
            protocol = protocol_fixture()
            change(protocol)
            with self.subTest(protocol=protocol), self.assertRaises(ValueError):
                experiment.validate_protocol(protocol)
        with self.assertRaises(ValueError):
            experiment.build_schedule(protocol_fixture(), "unknown")

    def test_load_protocol_rejects_duplicate_and_nonfinite_json(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "protocol.json"
            path.write_text(json.dumps(protocol_fixture()), encoding="utf-8")
            self.assertEqual(experiment.load_protocol(path), protocol_fixture())
            for content in ['{"seed":1,"seed":2}', '{"seed":NaN}', '[]']:
                path.write_text(content, encoding="utf-8")
                with self.assertRaises(ValueError):
                    experiment.load_protocol(path)

    def calibration_samples(self):
        samples = []
        for scenario in protocol_fixture()["scenarios"]:
            for variant, values in [("thinkgo", [1000, 800, 900]), ("php", [100, 200, 900])]:
                for repeat, value in enumerate(values, 1):
                    samples.append({"variant": variant, "scenario": scenario, "repeat": repeat,
                                    "model": "closed", "success_rps": value, "valid": True})
        return samples

    def test_rates_use_every_variant_median_and_keep_valid_error_measurements(self):
        protocol = protocol_fixture()
        samples = self.calibration_samples()
        self.assertEqual(experiment.choose_rates(protocol, samples), dict.fromkeys(protocol["scenarios"], 100))
        # 零有效吞吐也必须参与校准，不能删除后提高开放输入。
        for item in samples:
            if item["variant"] == "php":
                item["success_rps"] = 0
        self.assertEqual(experiment.choose_rates(protocol, samples), dict.fromkeys(protocol["scenarios"], 1))
        samples.append({"model": "open", "variant": "php", "scenario": "detail", "valid": True, "success_rps": 999})
        self.assertEqual(experiment.choose_rates(protocol, samples)["detail"], 1)

    def test_rates_refuse_partial_duplicate_invalid_or_unknown_runs(self):
        changes = [
            lambda values: values.pop(), lambda values: values.append(copy.deepcopy(values[0])),
            lambda values: values[0].update(valid=False), lambda values: values[0].update(success_rps=math.nan),
            lambda values: values[0].update(success_rps=-1), lambda values: values[0].update(variant="missing"),
            lambda values: values[0].update(repeat=4), lambda values: values[0].update(model="unknown"),
        ]
        for change in changes:
            samples = self.calibration_samples()
            change(samples)
            with self.subTest(change=change), self.assertRaises(ValueError):
                experiment.choose_rates(protocol_fixture(), samples)

    def test_load_check_preserves_all_unsuccessful_outcomes(self):
        self.assertEqual(experiment.check_load_report(load_fixture()), {"unsuccessful": 30, "failure_fraction": 0.3})

    def test_load_check_rejects_measurement_integrity_failures(self):
        changes = [
            lambda r: r.update(canceled=True), lambda r: r.update(not_scheduled=1),
            lambda r: r["totals"].update(started=91), lambda r: r["totals"].update(semantic_errors=-1),
            lambda r: r["totals"].update(success_within_window=71),
            lambda r: r["latency"].update(overflow=1), lambda r: r["latency"].update(count=89),
            lambda r: r["latency"].update(p50_ms=5), lambda r: r["latency"]["hdr_microseconds"].update(Counts=[89]),
            lambda r: r.update(success_rps=61), lambda r: r.update(window_seconds=0),
            lambda r: r.update(cache_hit_ratio=0), lambda r: r["drop_reasons"].update(inflight_limit=9),
            lambda r: r["seconds"][0]["counts"].update(planned=99),
            lambda r: r["scenarios"]["detail"]["totals"].update(planned=99),
            lambda r: r.update(expected=101), lambda r: r.update(elapsed_seconds=math.inf),
            lambda r: r["statuses"].update(**{"200": 200}),
        ]
        for change in changes:
            report = load_fixture()
            change(report)
            with self.subTest(change=change), self.assertRaises(ValueError):
                experiment.check_load_report(report)


if __name__ == "__main__":
    unittest.main()
