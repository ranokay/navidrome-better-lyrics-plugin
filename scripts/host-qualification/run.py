#!/usr/bin/env python3
"""Run the plugin in Navidrome's real WASI loader with disposable host services."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def git_output(checkout: Path, *args: str) -> str:
    return subprocess.check_output(["git", "-C", str(checkout), *args], text=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host_checkout", type=Path)
    parser.add_argument("package", type=Path)
    args = parser.parse_args()
    checkout = args.host_checkout.resolve(strict=True)
    package = args.package.resolve(strict=True)
    fixture = Path(__file__).with_name("qualification_test.go.txt").resolve(strict=True)
    virtual_test = checkout / "plugins" / "zz_betterlyrics_qualification_test.go"
    if virtual_test.exists():
        parser.error(f"Preserving existing host file: {virtual_test}")
    go = shutil.which("go")
    if go is None:
        parser.error("Go must be available; run this command through mise exec")
    before = (git_output(checkout, "status", "--porcelain=v1"), git_output(checkout, "diff"))
    print(f"Host head: {git_output(checkout, 'rev-parse', 'HEAD').strip()}", flush=True)
    with tempfile.TemporaryDirectory(prefix="better-lyrics-host-") as temporary:
        package_bytes = package.read_bytes()
        snapshot = Path(temporary) / "nd-better-lyrics.ndp"
        snapshot.write_bytes(package_bytes)
        print(f"Package SHA-256: {hashlib.sha256(package_bytes).hexdigest()}", flush=True)
        overlay = Path(temporary) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(virtual_test): str(fixture)}}))
        env = os.environ | {
            "BETTER_LYRICS_PACKAGE": str(snapshot),
            "BETTER_LYRICS_CACHE": str(Path(temporary) / "compiled"),
            "GOWORK": "off",
        }
        result = subprocess.run(
            [go, "test", "-mod=readonly", "-overlay", str(overlay),
             "-tags=netgo,sqlite_fts5", "./plugins", "-run", "^TestBetterLyricsQualification$",
             "-count=1", "-timeout=180s", "-v"],
            cwd=checkout, env=env, check=False,
        )
    after = (git_output(checkout, "status", "--porcelain=v1"), git_output(checkout, "diff"))
    if after != before:
        print("Host checkout changed during qualification; inspect it before continuing.")
        return 2
    print("Host checkout status and tracked diff unchanged.")
    return result.returncode


if __name__ == "__main__":
    raise SystemExit(main())
