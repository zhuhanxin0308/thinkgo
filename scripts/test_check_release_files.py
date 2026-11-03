"""Regression fixtures for the release credential filename gate."""

import pathlib
import subprocess
import tempfile
import unittest

from check_release_files import credential_paths, tracked_credential_paths


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
        ]
        allowed = [b"README.md", b"server.key.example", b".env.example", b"keys.go"]
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
            ]
            for name in names + [".env.example", "README.md"]:
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


if __name__ == "__main__":
    unittest.main()
