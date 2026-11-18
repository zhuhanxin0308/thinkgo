"""Verify that version/version.go Number matches the release tag contract."""

from __future__ import annotations

import argparse
from pathlib import Path
import re
import sys


NUMBER_DECLARATION_RE = re.compile(r'^[ \t]*Number[ \t]*=[ \t]*"([^"]*)"[ \t]*(?://.*)?$', re.MULTILINE)
SEMVER_V3_NUMBER_RE = re.compile(r"^3\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$")
RELEASE_TAG_RE = re.compile(r"^v3\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$")


def read_declared_version(version_file: Path) -> str:
    """Extract and validate the single Number constant from version/version.go."""
    if not version_file.is_file() or version_file.is_symlink():
        raise ValueError(f"version source must be a regular non-symlink file: {str(version_file)!r}")
    content = version_file.read_text(encoding="utf-8")
    matches = NUMBER_DECLARATION_RE.findall(content)
    if len(matches) != 1:
        raise ValueError(
            f"expected exactly one Number constant in {str(version_file)!r}, found {len(matches)}"
        )
    declared = matches[0]
    if not SEMVER_V3_NUMBER_RE.fullmatch(declared):
        raise ValueError(f"invalid v3 version Number {declared!r} in {str(version_file)!r}")
    return declared


def verify_release_version(version_file: Path, tag: str | None = None) -> str:
    """Validate version/version.go and optionally enforce exact tag parity."""
    declared = read_declared_version(version_file)
    if tag is not None:
        if not RELEASE_TAG_RE.fullmatch(tag):
            raise ValueError(f"invalid v3 release tag {tag!r}; expected v3.X.Y")
        expected_tag = f"v{declared}"
        if tag != expected_tag:
            raise ValueError(
                f"release tag {tag!r} does not match declared version Number {declared!r} (expected {expected_tag!r})"
            )
    return declared


def main(arguments: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--version-file",
        type=Path,
        default=Path("version/version.go"),
        help="Path to version/version.go",
    )
    parser.add_argument(
        "--tag",
        default=None,
        help="Target release tag (e.g. v3.0.6) to compare against version.Number",
    )
    args = parser.parse_args(arguments)
    try:
        verify_release_version(args.version_file, args.tag)
    except (OSError, UnicodeDecodeError, ValueError) as error:
        print(f"Release version verification failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
