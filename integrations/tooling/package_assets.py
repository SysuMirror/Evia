#!/usr/bin/env python3
"""Build only explicitly allowed public assets, without following symlinks."""
import argparse
import os
from pathlib import Path, PurePosixPath
import stat
import sys
import zipfile

ALLOWLIST = (
    "README.md",
    "skills/activity-event-plan/SKILL.md",
    "skills/activity-publish-events/SKILL.md",
    "skills/activity-integrate-verify/SKILL.md",
    "tooling/activity_api.py",
    "tooling/activity_manifest.py",
    "tooling/package_assets.py",
    "schemas/desired-events.schema.json",
    "examples/desired-events.json",
)
MAX_FILE = 256 * 1024


def collect(root, names=ALLOWLIST):
    root = Path(root)
    if root.is_symlink() or not root.is_dir():
        raise ValueError("invalid public asset root")
    result = []
    for name in names:
        path = PurePosixPath(name)
        if not isinstance(name, str) or str(path) != name or path.is_absolute() or any(p.startswith(".") or p in ("AGENTS.md", "..") for p in path.parts) or "\\" in name:
            raise ValueError("invalid public asset path")
        current = root
        for part in path.parts:
            current = current / part
            if current.is_symlink():
                raise ValueError("symlink in public assets")
        flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
        fd = os.open(current, flags)
        with os.fdopen(fd, "rb") as handle:
            info = os.fstat(handle.fileno())
            if not stat.S_ISREG(info.st_mode):
                raise ValueError("non-regular public asset")
            data = handle.read(MAX_FILE + 1)
        if len(data) > MAX_FILE:
            raise ValueError("public asset too large")
        result.append((name, data))
    return result


def package(root, destination):
    # Read and validate every allowlisted asset before opening the archive.
    files = collect(root)
    fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    with os.fdopen(fd, "wb") as output:
        with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
            for name, data in files:
                info = zipfile.ZipInfo(name, date_time=(2020, 1, 1, 0, 0, 0))
                info.compress_type = zipfile.ZIP_DEFLATED
                info.external_attr = (stat.S_IFREG | 0o644) << 16
                archive.writestr(info, data)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, help="new archive path; existing files are refused")
    args = parser.parse_args(argv)
    try:
        package(Path(__file__).resolve().parent.parent, args.output)
        print("Public integration archive created from the explicit allowlist.")
        return 0
    except (OSError, ValueError):
        print("Packaging failed; check allowlisted files and choose a new output path.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
