"""验证 Python 依赖复用必须绑定受控目录、锁文件和文件摘要。"""

import json
import os
import pathlib
import py_compile
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

import bootstrap
import python_packages


def populate_packages(prefix):
    prefix.mkdir(parents=True, exist_ok=True)
    for name, version in bootstrap.python_requirements().items():
        package = prefix / (name.replace("-", "_") + ".py")
        package.write_text("# 隔离测试发行包，不执行真实第三方业务逻辑。\n")
        metadata = prefix / (name.replace("-", "_") + "-" + version + ".dist-info")
        metadata.mkdir()
        (metadata / "METADATA").write_text(f"Metadata-Version: 2.1\nName: {name}\nVersion: {version}\n")
        (metadata / "RECORD").write_text("")


class PythonReuseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name)
        self.installer = bootstrap.Installer(self.root / "runtime",
                                             bootstrap.load_manifest(bootstrap.DEFAULT_MANIFEST))
        self.installer.prepare()
        self.addCleanup(self.installer.close)

    def install(self):
        def capture(command, **kwargs):
            populate_packages(pathlib.Path(command[command.index("--target") + 1]))
        with mock.patch.object(self.installer, "command", side_effect=capture) as command:
            result = self.installer.python_install()
        return result, command

    def test_matching_host_packages_do_not_count_as_controlled_installation(self):
        host = self.root / "host"
        populate_packages(host)
        with mock.patch.dict(os.environ, {"PYTHONPATH": str(host)}):
            result, command = self.install()
        self.assertEqual(result["state"], "installed")
        self.assertEqual(command.call_count, 1)
        self.assertNotIn(str(host), result["pythonpath"])
        self.assertTrue((self.installer.runtime / "receipts/python-packages.json").is_file())

    def test_valid_receipt_reuses_and_modified_files_are_rejected(self):
        self.install()
        with mock.patch.object(self.installer, "command", side_effect=AssertionError("不应重装")):
            self.assertEqual(self.installer.python_install()["state"], "reused")
            package = self.installer.runtime / "python-packages/psutil.py"
            package.write_text("# 同版本文件内容已变更\n")
            with self.assertRaises(ValueError):
                self.installer.python_install()

    def test_receipt_is_required_and_lock_identity_is_checked(self):
        self.install()
        receipt = self.installer.runtime / "receipts/python-packages.json"
        saved = json.loads(receipt.read_text())
        saved["requirements_sha256"] = "0" * 64
        receipt.write_text(json.dumps(saved))
        with self.assertRaises(ValueError):
            self.installer.python_install()
        receipt.unlink()
        with self.assertRaises(ValueError):
            self.installer.python_install()

    def test_added_package_and_external_symlink_are_not_reused(self):
        self.install()
        prefix = self.installer.runtime / "python-packages"
        extra = prefix / "unexpected.py"
        extra.write_text("# 未经收据验证的额外代码\n")
        with self.assertRaises(ValueError):
            self.installer.python_install()
        extra.unlink()
        extra.symlink_to(self.root / "outside")
        with self.assertRaises(ValueError):
            self.installer.python_install()

    def test_unrecorded_bytecode_cache_is_not_reused(self):
        self.install()
        cache = self.installer.runtime / "python-packages/__pycache__"
        cache.mkdir()
        (cache / "psutil.cpython-312.pyc").write_bytes(b"unrecorded executable cache")
        with self.assertRaises(ValueError):
            self.installer.python_install()

    def test_changed_executable_cache_cannot_reuse_unchanged_source_and_receipt(self):
        def capture(command, **kwargs):
            prefix = pathlib.Path(command[command.index("--target") + 1])
            populate_packages(prefix)
            source = prefix / "psutil.py"
            source.write_text("value = 1\n")
            py_compile.compile(str(source), doraise=True)
        with mock.patch.object(self.installer, "command", side_effect=capture):
            self.installer.python_install()
        prefix = self.installer.runtime / "python-packages"
        source = prefix / "psutil.py"
        original = source.read_bytes()
        original_stat = source.stat()
        receipt = self.installer.runtime / "receipts/python-packages.json"
        original_receipt = receipt.read_bytes()
        command = [sys.executable, "-I", "-S", "-B", "-c",
                   "import sys; sys.path.insert(0, sys.argv[1]); import psutil; print(psutil.value)", str(prefix)]
        self.assertEqual(subprocess.check_output(command, text=True).strip(), "1")
        with mock.patch.object(self.installer, "command", side_effect=AssertionError("不应重装")):
            self.assertEqual(self.installer.python_install()["state"], "reused")
            # 两份源码长度和时间戳相同，仅让有效缓存携带另一段无害逻辑，再恢复原源码。
            source.write_text("value = 2\n")
            os.utime(source, ns=(original_stat.st_atime_ns, original_stat.st_mtime_ns))
            py_compile.compile(str(source), doraise=True)
            source.write_bytes(original)
            os.utime(source, ns=(original_stat.st_atime_ns, original_stat.st_mtime_ns))
            self.assertEqual(subprocess.check_output(command, text=True).strip(), "2")
            self.assertEqual(source.read_bytes(), original)
            self.assertEqual(receipt.read_bytes(), original_receipt)
            with self.assertRaises(ValueError):
                self.installer.python_install()


class PythonPackageSnapshotTests(unittest.TestCase):
    def test_snapshot_tracks_sources_and_bytecode_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            prefix = pathlib.Path(directory) / "packages"
            populate_packages(prefix)
            original = python_packages.snapshot(prefix)
            self.assertEqual(original["versions"], bootstrap.python_requirements())
            self.assertEqual(original["files_sha256"]["psutil.py"], bootstrap.digest(prefix / "psutil.py"))
            cache = prefix / "__pycache__"
            cache.mkdir()
            (cache / "psutil.pyc").write_bytes(b"runtime cache")
            with_cache = python_packages.snapshot(prefix)
            self.assertNotEqual(with_cache, original)
            self.assertIn("__pycache__/psutil.pyc", with_cache["files_sha256"])
            (cache / "psutil.pyc").write_bytes(b"modified executable cache")
            self.assertNotEqual(python_packages.snapshot(prefix), with_cache)
            (prefix / "psutil.py").write_text("# 已修改\n")
            self.assertNotEqual(python_packages.snapshot(prefix), original)

    def test_snapshot_rejects_missing_empty_external_and_duplicate_distributions(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            with self.assertRaises(ValueError):
                python_packages.snapshot(root / "missing")
            with self.assertRaises(ValueError):
                python_packages.snapshot(root)
            prefix = root / "packages"
            populate_packages(prefix)
            external = SimpleNamespace(locate_file=lambda _: root, metadata={"Name": "psutil"}, version="7.2.2")
            with mock.patch.object(python_packages.importlib.metadata, "distributions", return_value=[external]):
                with self.assertRaises(ValueError):
                    python_packages.snapshot(prefix)
            duplicate = SimpleNamespace(locate_file=lambda _: prefix, metadata={"Name": "psutil"}, version="7.2.2")
            with mock.patch.object(python_packages.importlib.metadata, "distributions", return_value=[duplicate, duplicate]):
                with self.assertRaises(ValueError):
                    python_packages.snapshot(prefix)
            (prefix / "link").symlink_to(root / "outside")
            with self.assertRaises(ValueError):
                python_packages.snapshot(prefix)
            (prefix / "link").unlink()
            os.mkfifo(prefix / "pipe")
            with self.assertRaises(ValueError):
                python_packages.snapshot(prefix)
