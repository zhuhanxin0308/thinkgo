"""Unit tests for scripts/check_release_version.py."""

import contextlib
import io
from pathlib import Path
import tempfile
import unittest

from check_release_version import main, read_declared_version, verify_release_version


class CheckReleaseVersionTests(unittest.TestCase):
    def _write_version_go(self, directory: str, body: str) -> Path:
        path = Path(directory) / "version.go"
        path.write_text(body, encoding="utf-8")
        return path

    def test_repository_version_file_passes_format_and_matching_tag(self):
        repo_version_file = Path(__file__).resolve().parent.parent / "version" / "version.go"
        declared = read_declared_version(repo_version_file)
        self.assertEqual(verify_release_version(repo_version_file), declared)
        self.assertEqual(verify_release_version(repo_version_file, f"v{declared}"), declared)
        self.assertEqual(main(["--version-file", str(repo_version_file), "--tag", f"v{declared}"]), 0)

    def test_rejects_tag_mismatch_like_v3_0_7_against_3_0_6(self):
        with tempfile.TemporaryDirectory() as directory:
            version_file = self._write_version_go(
                directory,
                'package version\n\nconst (\n\tProductName = "ThinkGo"\n\tNumber = "3.0.6"\n)\n',
            )
            stderr = io.StringIO()
            with contextlib.redirect_stderr(stderr):
                code = main(["--version-file", str(version_file), "--tag", "v3.0.7"])
            self.assertEqual(code, 1)
            self.assertIn("v3.0.7", stderr.getvalue())
            self.assertIn("3.0.6", stderr.getvalue())

    def test_rejects_malformed_tags_and_declarations(self):
        with tempfile.TemporaryDirectory() as directory:
            valid_file = self._write_version_go(
                directory,
                'package version\nconst Number = "3.12.0" // current version\n',
            )
            for bad_tag in ("", "3.12.0", "v2.0.0", "v3.01.0", "v3.12.0-rc1", "refs/tags/v3.12.0"):
                with self.subTest(bad_tag=bad_tag):
                    with self.assertRaises(ValueError):
                        verify_release_version(valid_file, bad_tag)

            for name, bad_source in (
                ("missing_number", 'package version\nconst ProductName = "ThinkGo"\n'),
                ("duplicate_number", 'package version\nconst Number = "3.0.6"\nconst Number = "3.0.7"\n'),
                ("wrong_major", 'package version\nconst Number = "2.9.0"\n'),
                ("leading_zero", 'package version\nconst Number = "3.0.07"\n'),
                ("prefixed_v", 'package version\nconst Number = "v3.0.6"\n'),
            ):
                with self.subTest(bad_source=name):
                    bad_file = Path(directory) / f"{name}.go"
                    bad_file.write_text(bad_source, encoding="utf-8")
                    with self.assertRaises(ValueError):
                        read_declared_version(bad_file)

            missing = Path(directory) / "nonexistent.go"
            stderr = io.StringIO()
            with contextlib.redirect_stderr(stderr):
                self.assertEqual(main(["--version-file", str(missing)]), 1)


if __name__ == "__main__":
    unittest.main()
