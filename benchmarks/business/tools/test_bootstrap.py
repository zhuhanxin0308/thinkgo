"""安装入口的目录所有权、来源校验、恢复和生产配置测试。"""

import hashlib
import io
import json
import pathlib
import tarfile
import tempfile
import unittest
import sys
from unittest import mock

import bootstrap


class BootstrapTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = pathlib.Path(self.temporary.name)
        self.manifest = bootstrap.load_manifest(bootstrap.DEFAULT_MANIFEST)
        self.installer = bootstrap.Installer(self.base / "runtime", self.manifest, self.base / "cache")
        self.addCleanup(self.installer.close)

    def archive_component(self):
        # 可执行夹具只模拟安装协议，真实依赖另由官方来源集成自检覆盖。
        archive = self.base / "fixture.tar.gz"
        payload = b"#!/bin/sh\nprintf 'fixture 1.2.3\\n'\n"
        with tarfile.open(archive, "w:gz") as stream:
            entry = tarfile.TarInfo("fixture/bin/fixture")
            entry.size, entry.mode = len(payload), 0o755
            stream.addfile(entry, io.BytesIO(payload))
        component = {"name": "fixture", "version": "1.2.3", "kind": "archive", "prefix": "fixture",
                     "archive_root": "fixture", "dependencies": [], "url": "https://example.invalid/fixture.tar.gz",
                     "sha256": bootstrap.digest(archive), "outputs": ["fixture/bin/fixture"],
                     "probe": ["{prefix}/bin/fixture", "--version"]}
        cache = self.base / "cache"
        cache.mkdir(exist_ok=True)
        (cache / archive.name).write_bytes(archive.read_bytes())
        return component

    def test_manifest_contains_exact_complete_runtime_and_fixed_parallelism(self):
        names = {component["name"] for component in self.manifest["components"]}
        self.assertEqual(names, {"go", "m4", "autoconf", "bison", "flex", "libxml2", "oniguruma",
                                "postgresql", "redis", "nginx", "php", "phpredis", "composer", "roadrunner", "pcov"})
        self.assertEqual(self.manifest["jobs"], 2)
        self.assertTrue(all(component["url"].startswith("https://") for component in self.manifest["components"]))

    def test_dependency_order_and_optional_coverage(self):
        default = [component["name"] for component in bootstrap.select(self.manifest, None, False)]
        self.assertNotIn("pcov", default)
        for before, after in [("m4", "bison"), ("bison", "postgresql"), ("postgresql", "php"), ("php", "phpredis"), ("phpredis", "composer")]:
            self.assertLess(default.index(before), default.index(after))
        self.assertIn("pcov", [component["name"] for component in bootstrap.select(self.manifest, None, True)])
        self.assertEqual(["go"], [component["name"] for component in bootstrap.select(self.manifest, ["go"], False)])
        with self.assertRaises(ValueError):
            bootstrap.select(self.manifest, ["missing"], False)

    def test_unknown_runtime_and_unknown_prefix_are_not_overwritten(self):
        self.installer.runtime.mkdir()
        (self.installer.runtime / "keep.txt").write_text("保留")
        with self.assertRaises(ValueError):
            self.installer.prepare()
        self.assertEqual((self.installer.runtime / "keep.txt").read_text(), "保留")

    def test_matching_owner_reopens_and_different_manifest_is_rejected(self):
        self.installer.prepare()
        self.installer.close()
        self.installer.prepare()
        self.installer.close()
        changed = dict(self.manifest, verified_at_utc="different")
        another = bootstrap.Installer(self.installer.runtime, changed)
        self.addCleanup(another.close)
        with self.assertRaises(ValueError):
            another.prepare()

    def test_concurrent_installer_is_rejected(self):
        self.installer.prepare()
        another = bootstrap.Installer(self.installer.runtime, self.manifest)
        self.addCleanup(another.close)
        with self.assertRaises(ValueError):
            another.prepare()

    def test_verified_cache_installs_reuses_and_rejects_modified_binary(self):
        component = self.archive_component()
        self.installer.prepare()
        self.assertEqual(self.installer.install(component), "installed")
        with mock.patch.object(self.installer, "download", side_effect=AssertionError("不应重新下载")):
            self.assertEqual(self.installer.install(component), "reused")
        (self.installer.runtime / "fixture/bin/fixture").write_text("changed")
        with self.assertRaises(ValueError):
            self.installer.install(component)

    def test_unknown_component_prefix_is_preserved(self):
        component = self.archive_component()
        self.installer.prepare()
        prefix = self.installer.runtime / component["prefix"]
        prefix.mkdir()
        (prefix / "unknown").write_text("保留")
        with self.assertRaises(ValueError):
            self.installer.install(component)
        self.assertEqual((prefix / "unknown").read_text(), "保留")

    def test_wrong_cached_hash_and_wrong_download_hash_are_rejected(self):
        component = self.archive_component()
        self.installer.prepare()
        (self.base / "cache/fixture.tar.gz").write_bytes(b"wrong")
        with self.assertRaises(ValueError):
            self.installer.download(component)
        self.installer.cache = None
        def fake_download(command, **kwargs):
            pathlib.Path(command[command.index("--output") + 1]).write_bytes(b"wrong")
            return ""
        with mock.patch.object(self.installer, "command", side_effect=fake_download):
            with self.assertRaises(ValueError):
                self.installer.download(component)
        self.assertFalse((self.installer.runtime / "downloads/fixture.tar.gz").exists())

    def test_download_uses_https_and_preserves_tls_verification(self):
        component = self.archive_component()
        content = (self.base / "cache/fixture.tar.gz").read_bytes()
        self.installer.cache = None
        self.installer.prepare()
        commands = []
        def fake_download(command, **kwargs):
            commands.append(command)
            pathlib.Path(command[command.index("--output") + 1]).write_bytes(content)
            return ""
        with mock.patch.object(self.installer, "command", side_effect=fake_download):
            path = self.installer.download(component)
        self.assertEqual(bootstrap.digest(path), component["sha256"])
        self.assertNotIn("--insecure", commands[0])
        self.assertIn("=https", commands[0])

    def test_archive_cannot_write_outside_runtime(self):
        archive = self.base / "bad.tar.gz"
        with tarfile.open(archive, "w:gz") as stream:
            entry = tarfile.TarInfo("../../escaped")
            entry.size = 1
            stream.addfile(entry, io.BytesIO(b"x"))
        component = self.archive_component()
        component["sha256"] = bootstrap.digest(archive)
        self.installer.prepare()
        with self.assertRaises((ValueError, tarfile.TarError)):
            self.installer.unpack(component, archive)
        self.assertFalse((self.base / "escaped").exists())

    def test_recipe_environment_keeps_proxy_and_fixes_build_parallelism(self):
        php = next(component for component in self.manifest["components"] if component["name"] == "php")
        with mock.patch.dict("os.environ", {"HTTPS_PROXY": "https://proxy.invalid", "MAKEFLAGS": "-j80", "CFLAGS": "-O0"}):
            environment = self.installer.environment(php)
        self.assertEqual(environment["HTTPS_PROXY"], "https://proxy.invalid")
        self.assertNotIn("MAKEFLAGS", environment)
        self.assertNotIn("CFLAGS", environment)
        self.assertIn("postgresql/lib", environment["LDFLAGS"])

    def test_php_configuration_disables_measurement_coverage_and_jit(self):
        self.installer.prepare()
        php = next(component for component in self.manifest["components"] if component["name"] == "php")
        self.installer.configure_php(php)
        configuration = (self.installer.runtime / "php/etc/php.ini").read_text()
        self.assertIn("opcache.jit_buffer_size = 0", configuration)
        self.assertIn("opcache.enable_cli = 1", configuration)
        self.assertNotIn("extension = pcov", configuration)
        self.assertNotIn("extension = swoole", configuration)

    def test_preflight_fails_clearly_for_missing_tool_or_headers(self):
        with mock.patch("shutil.which", return_value=None):
            with self.assertRaisesRegex(ValueError, "缺少"):
                bootstrap.preflight(self.manifest, [self.manifest["components"][1]])
        with mock.patch("shutil.which", return_value="/bin/tool"), mock.patch("subprocess.run") as command:
            command.return_value.returncode = 1
            command.return_value.stderr = "missing zstd.h"
            with self.assertRaisesRegex(ValueError, "开发头文件"):
                bootstrap.preflight(self.manifest, [self.manifest["components"][1]])

    def test_recipe_preserves_postgresql_extension_and_redis_build_target(self):
        self.installer.prepare()
        for name in ["postgresql", "redis", "phpredis", "pcov"]:
            component = next(item for item in self.manifest["components"] if item["name"] == name)
            source = self.base / ("source-" + name)
            source.mkdir()
            commands = []
            def capture(command, **kwargs):
                commands.append(list(map(str, command)))
                return ""
            with mock.patch.object(self.installer, "command", side_effect=capture):
                self.installer.build(component, source)
            self.assertTrue(any("-j2" in command for command in commands))
            if name == "postgresql":
                self.assertTrue(any("contrib/pg_stat_statements" in command for command in commands))
            if name == "redis":
                self.assertIn(["make", "-j2", "build", "redis", "BUILD_TLS=yes"], commands)
            if name == "pcov":
                self.assertFalse((self.installer.runtime / "php/etc/conf.d/pcov.ini").exists())

    def test_python_lock_contains_all_actual_dependencies_and_hashes(self):
        requirements = bootstrap.python_requirements()
        self.assertEqual(requirements["matplotlib"], "3.11.2")
        self.assertEqual(requirements["psutil"], "7.2.2")
        self.assertEqual(requirements["coverage"], "7.17.2")
        self.assertEqual(len(requirements), 13)

    def test_matching_python_environment_is_not_reinstalled(self):
        from test_python_reuse import populate_packages
        self.installer.prepare()
        self.installer.claim_prefix({"prefix": "python-packages"})
        prefix = self.installer.runtime / "python-packages"
        populate_packages(prefix)
        bootstrap.write_json(self.installer.runtime / "receipts/python-packages.json",
                             {"requirements_sha256": bootstrap.digest(bootstrap.PYTHON_REQUIREMENTS),
                              "snapshot": self.installer.python_snapshot(prefix)})
        with mock.patch.object(self.installer, "command", side_effect=AssertionError("不应重装")):
            result = self.installer.python_install()
        self.assertEqual(result["state"], "reused")

    def test_python_install_is_user_scoped_hashed_and_reports_pythonpath(self):
        from test_python_reuse import populate_packages
        self.installer.prepare()
        commands = []
        def capture(command, **kwargs):
            commands.append(list(map(str, command)))
            populate_packages(pathlib.Path(command[command.index("--target") + 1]))
            return ""
        with mock.patch.object(self.installer, "command", side_effect=capture):
            result = self.installer.python_install()
        self.assertEqual(result["state"], "installed")
        self.assertIn(str(self.installer.runtime / "python-packages"), result["pythonpath"])
        self.assertIn("--target", commands[0])
        self.assertIn("--require-hashes", commands[0])
        self.assertIn("--only-binary=:all:", commands[0])
        self.assertNotIn("--break-system-packages", commands[0])

    def test_command_failure_keeps_diagnostic_log(self):
        self.installer.prepare()
        with self.assertRaises(RuntimeError):
            self.installer.command(["/bin/sh", "-c", "echo expected-failure; exit 7"])
        self.assertIn("expected-failure", (self.installer.runtime / "logs/bootstrap.log").read_text())

    def test_composer_installs_only_locks_and_checks_both_apps(self):
        self.installer.prepare()
        project = self.base / "project"
        for name in ["thinkphp", "laravel", ""]:
            directory = project / "php" / name
            directory.mkdir(parents=True, exist_ok=True)
            (directory / "composer.lock").write_text('{"packages":[]}')
        with mock.patch.object(bootstrap, "ROOT", project), mock.patch.object(self.installer, "command") as command:
            self.installer.composer_install(True)
        self.assertEqual(command.call_count, 7)
        installs = [list(map(str, call.args[0])) for call in command.call_args_list if "install" in call.args[0]]
        self.assertEqual(len(installs), 3)
        self.assertTrue(all("--no-scripts" in arguments for arguments in installs))
        self.assertNotIn("--no-dev", installs[-1])

    def test_composer_detects_unexpected_lock_changes(self):
        self.installer.prepare()
        project = self.base / "project"
        directory = project / "php/thinkphp"
        directory.mkdir(parents=True)
        lock = directory / "composer.lock"
        lock.write_text("original")
        def changed(*args, **kwargs):
            lock.write_text("modified")
        with mock.patch.object(bootstrap, "ROOT", project), mock.patch.object(self.installer, "command", side_effect=changed):
            with self.assertRaisesRegex(RuntimeError, "锁文件"):
                self.installer.composer_install(False)

    def test_cli_plan_is_read_only_and_install_failure_releases_lock(self):
        arguments = ["bootstrap.py", "--runtime", str(self.installer.runtime), "--components", "go", "--plan"]
        output = io.StringIO()
        with mock.patch.object(sys, "argv", arguments), mock.patch("sys.stdout", output):
            bootstrap.main()
        self.assertFalse(self.installer.runtime.exists())
        self.assertEqual(json.loads(output.getvalue())[0]["name"], "go")
        with mock.patch.object(sys, "argv", arguments[:-1]), mock.patch.object(bootstrap, "preflight"), \
             mock.patch.object(bootstrap.Installer, "install", side_effect=RuntimeError("failed")), mock.patch("sys.stdout", io.StringIO()):
            with self.assertRaises(RuntimeError):
                bootstrap.main()
        self.installer.prepare()

    def test_cli_success_selects_dependencies_and_python_only(self):
        arguments = ["bootstrap.py", "--runtime", str(self.installer.runtime), "--python-only"]
        with mock.patch.object(sys, "argv", arguments), mock.patch.object(bootstrap, "preflight"), \
             mock.patch.object(bootstrap.Installer, "python_install", return_value={"state": "reused"}) as python, \
             mock.patch("sys.stdout", io.StringIO()):
            bootstrap.main()
        python.assert_called_once()
        arguments = ["bootstrap.py", "--runtime", str(self.installer.runtime), "--components", "composer"]
        with mock.patch.object(sys, "argv", arguments), mock.patch.object(bootstrap, "preflight"), \
             mock.patch.object(bootstrap.Installer, "install", return_value="reused") as install, \
             mock.patch.object(bootstrap.Installer, "composer_install") as composer, mock.patch("sys.stdout", io.StringIO()):
            bootstrap.main()
        self.assertGreater(install.call_count, 1)
        composer.assert_called_once_with(False)

    def test_interrupted_build_reuses_owned_source_and_refuses_changed_configuration(self):
        component = self.archive_component()
        self.installer.prepare()
        archive = self.installer.download(component)
        first = self.installer.unpack(component, archive)
        self.assertEqual(first, self.installer.unpack(component, archive))
        (first.parent / ".archive.json").write_text("{}")
        with self.assertRaises(ValueError):
            self.installer.unpack(component, archive)
        configuration = self.installer.runtime / "test.ini"
        self.installer.write_configuration(configuration, "expected")
        self.installer.write_configuration(configuration, "expected")
        with self.assertRaises(ValueError):
            self.installer.write_configuration(configuration, "different")


if __name__ == "__main__":
    unittest.main()
