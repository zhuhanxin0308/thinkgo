"""验证模块隔离、扫描覆盖及失败关闭行为，不依赖网络或真实业务数据。"""

import contextlib
import io
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import gosec_modules as gate


def clean_report():
    return {"Stats": {"files": 3, "found": 0}, "Issues": [], "Golang errors": {}}


class ModuleDiscoveryTests(unittest.TestCase):
    def test_root_and_nested_modules_are_discovered_once(self):
        raw = b"go.mod\0benchmarks/business/go/go.mod\0README.md\0go.mod\0"
        self.assertEqual(gate.discover_modules(raw), [Path("."), Path("benchmarks/business/go")])

    def test_fixture_vendor_and_existing_exclusions_are_not_scan_modules(self):
        raw = b"go.mod\0testdata/case/go.mod\0vendor/pkg/go.mod\0tmp/go.mod\0bin/go.mod\0example/go.mod\0"
        self.assertEqual(gate.discover_modules(raw), [Path("."), Path("example")])

    def test_empty_missing_root_unterminated_and_escaping_lists_fail(self):
        for raw in (b"", b"nested/go.mod\0", b"go.mod", b"go.mod\0../go.mod\0", b"go.mod\0/other/go.mod\0"):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                gate.discover_modules(raw)


class PackageBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / "go.mod").write_text("module example.test/root\n", encoding="utf-8")
        (self.root / "library with space").mkdir()
        (self.root / "nested").mkdir()
        (self.root / "nested/go.mod").write_text("module example.test/nested\n", encoding="utf-8")
        (self.root / "tmp").mkdir()

    def test_explicit_directories_preserve_spaces_without_recursive_patterns(self):
        source = f"{self.root}\n{self.root / 'library with space'}\n"
        self.assertEqual(gate.package_directories(source, self.root), [str(self.root), str(self.root / "library with space")])

    def test_nested_module_cannot_leak_into_parent_scan(self):
        with self.assertRaises(ValueError):
            gate.package_directories(str(self.root / "nested"), self.root)

    def test_outside_missing_and_empty_packages_fail(self):
        for source in (str(self.root.parent), str(self.root / "missing"), ""):
            with self.subTest(source=source), self.assertRaises(ValueError):
                gate.package_directories(source, self.root)

    def test_excluded_packages_do_not_silently_make_scan_empty(self):
        with self.assertRaises(ValueError):
            gate.package_directories(str(self.root / "tmp"), self.root)
        self.assertEqual(gate.package_directories(f"{self.root}\n{self.root / 'tmp'}\n", self.root), [str(self.root)])

    def test_module_without_manifest_is_rejected(self):
        (self.root / "go.mod").unlink()
        with self.assertRaises(ValueError):
            gate.package_directories(str(self.root), self.root)

    def test_scanner_receives_module_relative_patterns_after_directory_validation(self):
        # 中文注释：将真实 go list 目录转为包模式，避免绝对路径触发 command-line-arguments 类型加载问题。
        (self.root / "pkg").mkdir()
        checked = gate.package_directories(f"{self.root}\n{self.root / 'pkg'}\n", self.root)
        self.assertEqual(gate.module_package_patterns(checked, self.root), [".", "./pkg"])

    def test_module_relative_patterns_reject_escaping_and_nested_modules(self):
        # 中文注释：包模式转换必须再次守住模块边界，不让调用方跳过前置校验。
        for source in (self.root.parent, self.root / "nested"):
            with self.subTest(source=source), self.assertRaises(ValueError):
                gate.module_package_patterns([str(source)], self.root)


class ReportValidationTests(unittest.TestCase):
    def test_clean_nonempty_report_passes(self):
        with contextlib.redirect_stderr(io.StringIO()):
            gate.validate_report(clean_report())
            report = clean_report()
            report["Issues"], report["Golang errors"] = None, None
            gate.validate_report(report)

    def test_empty_coverage_findings_and_loading_errors_fail_independently(self):
        reports = []
        for key, value in (("files", 0), ("found", 1), ("files", True), ("files", "3"), ("found", -1)):
            report = clean_report()
            report["Stats"][key] = value
            reports.append(report)
        report = clean_report()
        report["Issues"] = [{"rule_id": "G104", "details": "未处理错误"}]
        reports.append(report)
        report = clean_report()
        report["Golang errors"] = {"nested": [{"error": "父模块不包含该包"}]}
        reports.append(report)
        for report in reports:
            with self.subTest(report=report), contextlib.redirect_stderr(io.StringIO()), self.assertRaises(ValueError):
                gate.validate_report(report)

    def test_malformed_report_is_never_accepted_as_zero_findings(self):
        malformed = [None, [], {}, {"Stats": {}}, {"Stats": None}]
        for key, value in (("Issues", {}), ("Golang errors", []), ("Stats", [])):
            report = clean_report()
            report[key] = value
            malformed.append(report)
        for key in ("Issues", "Golang errors", "Stats"):
            report = clean_report()
            del report[key]
            malformed.append(report)
        for report in malformed:
            with self.subTest(report=report), contextlib.redirect_stderr(io.StringIO()), self.assertRaises(ValueError):
                gate.validate_report(report)

    def test_diagnostic_limit_does_not_hide_gate_failure(self):
        report = clean_report()
        report["Issues"] = [{"details": "发现"}] * (gate.MAX_VISIBLE_FINDINGS + 1)
        report["Golang errors"] = {"package": ["x" * (gate.MAX_ERROR_TEXT_LENGTH + 1)]}
        with contextlib.redirect_stderr(io.StringIO()) as output, self.assertRaises(ValueError):
            gate.validate_report(report)
        self.assertIn("另有 1", output.getvalue())


class ScanExecutionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / "go.mod").write_text("module example.test/root\n", encoding="utf-8")
        (self.root / "nested").mkdir()
        (self.root / "nested/go.mod").write_text("module example.test/nested\n", encoding="utf-8")
        self.reports = self.root / "reports"

    def fake_command(self, command, **kwargs):
        if command[0] == "git":
            return subprocess.CompletedProcess(command, 0, stdout=b"go.mod\0nested/go.mod\0")
        module = Path(kwargs["cwd"])
        self.assertEqual(kwargs["env"]["GOWORK"], "off")
        self.assertEqual(kwargs["env"]["CGO_ENABLED"], "0")
        if command[1] == "list":
            self.assertIn("-mod=readonly", command)
            return subprocess.CompletedProcess(command, 0, stdout=str(module) + "\n")
        self.assertEqual(command[-1], ".")
        self.assertNotIn("./...", command)
        self.assertIn(gate.GOSEC_TOOL, command)
        report_path = Path(next(value.split("=", 1)[1] for value in command if value.startswith("-out=")))
        report_path.write_text(json.dumps(clean_report()), encoding="utf-8")
        return subprocess.CompletedProcess(command, 0)

    def test_each_module_is_scanned_in_its_own_dependency_context(self):
        with patch.object(gate.subprocess, "run", side_effect=self.fake_command) as run, contextlib.redirect_stderr(io.StringIO()):
            self.assertTrue(gate.run_scans(self.root, self.reports))
        scans = [call for call in run.call_args_list if call.args[0][:2] == ["go", "run"]]
        self.assertEqual([Path(call.kwargs["cwd"]) for call in scans], [self.root, self.root / "nested"])
        self.assertEqual(len(list(self.reports.glob("*.json"))), 2)

    def test_nested_module_uses_its_frozen_toolchain_without_changing_parent(self):
        # 中文注释：根模块保持固定 Go 版本，嵌套基准模块允许下载其 go.mod 指定的版本。
        seen = []

        def command(arguments, **kwargs):
            if arguments[:2] in (["go", "list"], ["go", "run"]):
                seen.append((Path(kwargs["cwd"]), kwargs["env"]["GOTOOLCHAIN"]))
            return self.fake_command(arguments, **kwargs)

        with patch.dict(gate.os.environ, {"GOTOOLCHAIN": "local"}), \
                patch.object(gate.subprocess, "run", side_effect=command), \
                contextlib.redirect_stderr(io.StringIO()):
            self.assertTrue(gate.run_scans(self.root, self.reports))

        self.assertEqual(
            seen,
            [(self.root, "local"), (self.root, "local"),
             (self.root / "nested", "auto"), (self.root / "nested", "auto")],
        )

    def test_one_module_failure_does_not_skip_other_modules_or_pass_gate(self):
        def command(arguments, **kwargs):
            if arguments[:2] == ["go", "list"] and Path(kwargs["cwd"]) == self.root:
                raise subprocess.CalledProcessError(1, arguments)
            return self.fake_command(arguments, **kwargs)
        with patch.object(gate.subprocess, "run", side_effect=command), contextlib.redirect_stderr(io.StringIO()):
            self.assertFalse(gate.run_scans(self.root, self.reports))
        self.assertEqual(len(list(self.reports.glob("*.json"))), 1)

    def test_scanner_exit_failure_remains_failure_even_with_clean_json(self):
        def command(arguments, **kwargs):
            result = self.fake_command(arguments, **kwargs)
            if arguments[:2] == ["go", "run"]:
                result.returncode = 2
            return result
        with patch.object(gate.subprocess, "run", side_effect=command), contextlib.redirect_stderr(io.StringIO()):
            self.assertFalse(gate.run_scans(self.root, self.reports))

    def test_missing_or_corrupt_report_fails(self):
        for corrupt in (False, True):
            def command(arguments, **kwargs):
                result = self.fake_command(arguments, **kwargs)
                if arguments[:2] == ["go", "run"]:
                    path = Path(next(value.split("=", 1)[1] for value in arguments if value.startswith("-out=")))
                    if corrupt:
                        path.write_text("invalid json", encoding="utf-8")
                    else:
                        path.unlink()
                return result
            with self.subTest(corrupt=corrupt), patch.object(gate.subprocess, "run", side_effect=command), contextlib.redirect_stderr(io.StringIO()):
                self.assertFalse(gate.run_scans(self.root, self.reports))

    def test_stale_report_is_deleted_before_scanner_runs(self):
        self.reports.mkdir()
        (self.reports / "module-0.json").write_text(json.dumps(clean_report()), encoding="utf-8")
        def command(arguments, **kwargs):
            if arguments[:2] == ["go", "run"]:
                return subprocess.CompletedProcess(arguments, 0)
            return self.fake_command(arguments, **kwargs)
        with patch.object(gate.subprocess, "run", side_effect=command), contextlib.redirect_stderr(io.StringIO()):
            self.assertFalse(gate.run_scans(self.root, self.reports))

    def test_main_reports_discovery_errors_as_failure(self):
        with patch.object(gate.subprocess, "run", side_effect=OSError("git 不可用")), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(gate.main(["--repository", str(self.root), "--reports-dir", str(self.reports)]), 1)

    def test_main_success(self):
        with patch.object(gate.subprocess, "run", side_effect=self.fake_command), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(gate.main(["--repository", str(self.root), "--reports-dir", str(self.reports)]), 0)


if __name__ == "__main__":
    unittest.main()
