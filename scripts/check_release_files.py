"""Reject tracked credential filenames without Git's printable-path quoting."""

import os
import subprocess
import sys


CREDENTIAL_SUFFIXES = (b".pem", b".key", b".p12", b".pfx")


def credential_paths(raw_paths):
    """Read NUL-delimited Git paths as bytes, including non-UTF-8 filenames."""
    if raw_paths and not raw_paths.endswith(b"\0"):
        raise ValueError("git ls-files output is not NUL terminated")
    forbidden = []
    for path in raw_paths.split(b"\0"):
        if not path:
            continue
        name = path.rsplit(b"/", 1)[-1]
        if name == b".env" or name.endswith(CREDENTIAL_SUFFIXES):
            forbidden.append(path)
    return forbidden


def tracked_credential_paths(repository="."):
    result = subprocess.run(
        ["git", "ls-files", "-z"],
        cwd=repository,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return credential_paths(result.stdout)


def main():
    try:
        forbidden = tracked_credential_paths()
    except (OSError, subprocess.CalledProcessError, ValueError) as error:
        print(f"Cannot verify tracked release files: {error}", file=sys.stderr)
        return 1
    if forbidden:
        print("Release contains tracked environment or credential files:", file=sys.stderr)
        for path in forbidden:
            # repr prevents newlines and control bytes in filenames from forging log lines.
            print(f"  {os.fsdecode(path)!r}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
