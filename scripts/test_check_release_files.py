"""Regression fixtures for the release credential filename gate."""

import contextlib
import io
import os
import pathlib
import subprocess
import tempfile
import unittest

from check_release_files import credential_paths, main, tracked_credential_paths


@contextlib.contextmanager
def working_directory(path):
    """临时切换测试工作目录，同时兼容 CI 使用的 Python 3.10。"""
    previous = os.getcwd()
    try:
        os.chdir(path)
        yield
    finally:
        os.chdir(previous)


class ReleaseCredentialPathTests(unittest.TestCase):
    def test_raw_unicode_quote_newline_and_non_utf8_paths(self):
        forbidden = [
            b"ascii/server.key",
            "证书/server.pem".encode(),
            "部署/.env".encode(),
            b'quote"/server.p12',
            b"new\nline/server.pfx",
            b'server".key',
            b"cert\nname.pem",
            b"bad-utf8-\xff/server.key",
            b"deploy/.env.production",
            b".env.local",
            b".ENV.TEST",
            b".env.example.backup",
            b"CERT.PEM",
            b"server.KeY",
            b"client.P12",
            b"client.PfX",
            b"bad-utf8-\xff/.env.local",
        ]
        allowed = [
            b"README.md", b"server.key.example", b".env.example", b"keys.go",
            b".env.production.example", b".ENV.LOCAL.EXAMPLE", b".environment",
            b".env.directory/config.json", b"certificate.PEM.example",
        ]
        raw = b"\0".join(forbidden + allowed) + b"\0"
        self.assertEqual(credential_paths(raw), forbidden)

    def test_empty_and_incomplete_streams(self):
        self.assertEqual(credential_paths(b""), [])
        with self.assertRaises(ValueError):
            credential_paths(b"server.key")

    def test_tracked_paths_do_not_depend_on_core_quotepath(self):
        with tempfile.TemporaryDirectory() as directory:
            subprocess.run(["git", "init", "-q", directory], check=True)
            names = [
                "ascii/server.key",
                "证书/server.key",
                "部署/.env",
                'quote"/server.pem',
                "new\nline/server.p12",
                'server".key',
                "cert\nname.pem",
                "deploy/.env.production",
                ".env.local",
                ".ENV.TEST",
                "server.KeY",
                "server.PEM",
                "client.P12",
                "client.PFX",
            ]
            for name in names + [".env.example", ".env.production.example", ".ENV.TEST.EXAMPLE", "README.md"]:
                path = pathlib.Path(directory, name)
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("dummy fixture\n", encoding="utf-8")
            subprocess.run(["git", "-C", directory, "add", "-f", "--", "."], check=True)
            for quoting in ("true", "false"):
                subprocess.run(["git", "-C", directory, "config", "core.quotePath", quoting], check=True)
                self.assertCountEqual(tracked_credential_paths(directory), [name.encode() for name in names])

    def test_git_failure_is_not_a_clean_scan(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(subprocess.CalledProcessError):
                tracked_credential_paths(directory)

    def test_gate_exit_status_and_safe_diagnostics(self):
        # 使用临时索引和哑数据，验证真实门禁结果与控制字符路径日志。
        with tempfile.TemporaryDirectory() as directory, working_directory(directory):
            errors = io.StringIO()
            with contextlib.redirect_stderr(errors):
                self.assertEqual(main(), 1)
            subprocess.run(["git", "init", "-q"], check=True)
            self.assertEqual(main(), 0)
            name = "test\ncredential.KEY"
            pathlib.Path(name).write_text("synthetic fixture", encoding="utf-8")
            subprocess.run(["git", "add", "-f", "--", name], check=True)
            errors = io.StringIO()
            with contextlib.redirect_stderr(errors):
                self.assertEqual(main(), 1)
            self.assertIn(repr(name), errors.getvalue())
            self.assertEqual(len(errors.getvalue().splitlines()), 2)


if __name__ == "__main__":
    unittest.main()
