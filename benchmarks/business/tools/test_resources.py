"""资源证据测试覆盖共享子进程、读取失败、进程变化和真实psutil采样。"""

import contextlib
import os
import time
import types
import unittest
from unittest import mock

import psutil

import resources


class FakeProcess:
    def __init__(self, pid, cpu=0, rss=100, pss=60, created=None):
        self.pid = pid
        self.cpu = cpu
        self.rss = rss
        self.pss = pss
        self.created = time.time() - 100 if created is None else created
        self.descendants = []
        self.denied = {}

    def value(self, field, value):
        if field in self.denied:
            raise self.denied[field]
        return value

    def oneshot(self):
        return contextlib.nullcontext()

    def create_time(self):
        return self.value("create_time", self.created)

    def children(self, recursive=False):
        return self.value("children", self.descendants)

    def cpu_times(self):
        # children_user故意很大，采样器不能再把后代CPU累计一次。
        return self.value("cpu_times", types.SimpleNamespace(user=self.cpu, system=0, children_user=10000, children_system=10000))

    def memory_info(self):
        return self.value("memory_info", types.SimpleNamespace(rss=self.rss))

    def memory_full_info(self):
        return self.value("memory_full_info", types.SimpleNamespace(pss=self.pss, rss=self.rss))

    def ppid(self):
        return self.value("ppid", 1)

    def name(self):
        return self.value("name", f"process-{self.pid}")

    def cmdline(self):
        return self.value("cmdline", ["worker", "--serve"])

    def cpu_affinity(self):
        return self.value("cpu_affinity", [2, 4])


def process_lookup(processes):
    def lookup(pid):
        if pid not in processes:
            raise psutil.NoSuchProcess(pid)
        return processes[pid]
    return lookup


class ResourceTests(unittest.TestCase):
    def test_existing_process_cpu_delta_and_shared_descendants_are_counted_once(self):
        parent = FakeProcess(101, cpu=10, rss=100, pss=80)
        child = FakeProcess(102, cpu=4, rss=80, pss=40)
        parent.descendants = [child, child]
        with mock.patch.object(resources.psutil, "Process", side_effect=process_lookup({101: parent, 102: child})):
            sampler = resources.Sampler({"application": [101, 102, 101]}, 60).start()
            parent.cpu, child.cpu = 12, 7
            parent.rss, child.rss = 140, 100
            parent.pss, child.pss = 90, 60
            report = sampler.stop()
            self.assertIs(sampler.stop(), report)
        application = report["groups"]["application"]
        self.assertEqual(application["cpu_seconds"], 5)
        self.assertEqual(application["peak_rss_bytes"], 240)
        self.assertEqual(application["peak_pss_bytes"], 150)
        self.assertEqual(application["peak_memory_bytes"], 150)
        self.assertEqual(application["memory_metric"], "pss")
        self.assertTrue(application["pss_complete"] and application["cpu_complete"])
        self.assertEqual(application["sample_count"], 2)
        self.assertEqual(application["missing_samples"], 0)
        for sample in report["samples"]:
            rows = sample["groups"]["application"]["processes"]
            self.assertEqual([row["pid"] for row in rows], [101, 102])
            self.assertEqual(rows[0]["affinity"], [2, 4])
            self.assertEqual(rows[0]["cmdline"], ["worker", "--serve"])

    def test_new_child_cpu_begins_at_birth_within_sampling_window(self):
        parent = FakeProcess(201, cpu=10)
        with mock.patch.object(resources.psutil, "Process", return_value=parent):
            sampler = resources.Sampler({"application": [201]}, 60).start()
            child = FakeProcess(202, cpu=3, created=time.time())
            parent.descendants = [child]
            parent.cpu = 11
            report = sampler.stop()
        self.assertEqual(report["groups"]["application"]["cpu_seconds"], 4)

    def test_pss_read_failure_uses_explicit_rss_fallback(self):
        process = FakeProcess(301, cpu=1, rss=200)
        process.denied["memory_full_info"] = psutil.AccessDenied(process.pid)
        process.denied["cpu_affinity"] = psutil.AccessDenied(process.pid)
        with mock.patch.object(resources.psutil, "Process", return_value=process):
            sampler = resources.Sampler({"application": [301]}, 60).start()
            process.cpu = 3
            report = sampler.stop()
        group = report["groups"]["application"]
        self.assertEqual(group["memory_metric"], "rss")
        self.assertEqual(group["peak_memory_bytes"], 200)
        self.assertIsNone(group["peak_pss_bytes"])
        self.assertFalse(group["pss_complete"])
        self.assertTrue(group["cpu_complete"])
        self.assertEqual(group["cpu_seconds"], 2)
        self.assertEqual(group["missing_samples"], 0)
        self.assertIsNone(report["samples"][0]["groups"]["application"]["processes"][0]["pss_bytes"])
        self.assertTrue(group["errors"])

    def test_cpu_and_rss_failures_are_unknown_instead_of_zero(self):
        process = FakeProcess(401)
        process.denied["cpu_times"] = psutil.AccessDenied(process.pid)
        process.denied["memory_info"] = psutil.AccessDenied(process.pid)
        with mock.patch.object(resources.psutil, "Process", return_value=process):
            report = resources.Sampler({"application": [401]}, 60).start().stop()
        group = report["groups"]["application"]
        self.assertFalse(group["cpu_complete"])
        self.assertIsNone(group["cpu_seconds"])
        self.assertIsNone(group["peak_rss_bytes"])
        self.assertGreater(group["missing_samples"], 0)

    def test_missing_root_and_pid_reuse_leave_visible_gaps(self):
        with mock.patch.object(resources.psutil, "Process", side_effect=psutil.NoSuchProcess(501)):
            report = resources.Sampler({"application": [501]}, 60).start().stop()
        self.assertEqual(report["groups"]["application"]["missing_samples"], 2)
        process = FakeProcess(502)
        with mock.patch.object(resources.psutil, "Process", return_value=process):
            sampler = resources.Sampler({"application": [502]}, 60).start()
            process.created += 10
            report = sampler.stop()
        self.assertFalse(report["groups"]["application"]["cpu_complete"])
        self.assertEqual(report["groups"]["application"]["missing_samples"], 1)
        self.assertTrue(any(error["error"] == "RootPIDReused" for error in report["groups"]["application"]["errors"]))

    def test_exited_child_and_decreased_cpu_counter_do_not_fabricate_precision(self):
        process = FakeProcess(601, cpu=5)
        child = FakeProcess(602, cpu=3)
        process.descendants = [child]
        with mock.patch.object(resources.psutil, "Process", return_value=process):
            sampler = resources.Sampler({"application": [601]}, 60).start()
            process.descendants = []
            process.cpu = 4
            report = sampler.stop()
        group = report["groups"]["application"]
        self.assertFalse(group["cpu_complete"])
        self.assertIsNone(group["cpu_seconds"])
        errors = {error["error"] for error in group["errors"]}
        self.assertIn("CPUCounterDecreased", errors)
        self.assertIn("ProcessDisappeared", errors)

    def test_overlapping_groups_are_not_double_counted(self):
        parent, child = FakeProcess(701), FakeProcess(702)
        parent.descendants = [child]
        with mock.patch.object(resources.psutil, "Process", side_effect=process_lookup({701: parent, 702: child})):
            report = resources.Sampler({"application": [701], "database": [702]}, 60).start().stop()
        for group in report["groups"].values():
            self.assertFalse(group["cpu_complete"])
        self.assertEqual(len(report["samples"][0]["overlapping_processes"]), 1)

    def test_invalid_configuration_and_lifecycle_are_rejected(self):
        for groups, interval in [({}, 1), ({"app": []}, 1), ({"app": [0]}, 1),
                                 ({"app": [True]}, 1), ({"app": [1], "db": [1]}, 1),
                                 ({"app": [1]}, 0), ({"app": [1]}, float("nan"))]:
            with self.subTest(groups=groups, interval=interval), self.assertRaises(ValueError):
                resources.Sampler(groups, interval)
        sampler = resources.Sampler({"self": [os.getpid()]}, 1)
        with self.assertRaises(RuntimeError):
            sampler.stop()
        sampler.start()
        try:
            with self.assertRaises(RuntimeError):
                sampler.start()
        finally:
            sampler.stop()

    def test_real_periodic_sampler_preserves_process_evidence(self):
        sampler = resources.Sampler({"self": [os.getpid()]}, 0.01).start()
        time.sleep(0.035)
        report = sampler.stop()
        self.assertGreaterEqual(len(report["samples"]), 3)
        self.assertGreater(report["elapsed_seconds"], 0)
        self.assertGreater(report["groups"]["self"]["peak_rss_bytes"], 0)
        self.assertTrue(report["started_at"] and report["finished_at"])
        self.assertTrue(all(sample["groups"]["self"]["processes"] for sample in report["samples"]))


if __name__ == "__main__":
    unittest.main()
