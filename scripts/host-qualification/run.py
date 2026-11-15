#!/usr/bin/env python3
"""Run the plugin in Navidrome's real WASI loader with disposable host services."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
# This local developer tool deliberately executes trusted Git and Go binaries.
import subprocess  # nosec B404
import tempfile


def git_output(checkout: Path, *args: str) -> str:
    # Ignore inherited repository/config overrides and prevent fsmonitor execution.
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)
    # Arguments are fixed by this module; the trusted checkout is a separate argv entry.
    return subprocess.check_output(  # nosec B603
        ["git", "-c", "core.fsmonitor=false", "-C", str(checkout), *args],
        text=True, env=env, shell=False,
    )


def host_state(checkout: Path) -> tuple[str, str]:
    return (git_output(checkout, "status", "--porcelain=v1"),
            git_output(checkout, "diff", "--no-ext-diff", "--no-textconv"))


def existing_path(parser: argparse.ArgumentParser, path: Path, directory: bool) -> Path:
    try:
        resolved = path.resolve(strict=True)
    except OSError as error:
        parser.error(str(error))
    if not (resolved.is_dir() if directory else resolved.is_file()):
        parser.error(f"Expected a {'directory' if directory else 'regular file'}: {resolved}")
    return resolved


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host_checkout", type=Path, help="trusted local Navidrome Git checkout")
    parser.add_argument("package", type=Path, help="trusted local plugin .ndp file")
    args = parser.parse_args()
    checkout = existing_path(parser, args.host_checkout, directory=True)
    package = existing_path(parser, args.package, directory=False)
    module_file = checkout / "go.mod"
    if not module_file.is_file() or "module github.com/navidrome/navidrome" not in module_file.read_text().splitlines():
        parser.error("Expected a Navidrome checkout with its canonical Go module")
    if not (checkout / "plugins").is_dir():
        parser.error("Expected the Navidrome plugins directory")
    try:
        worktree_root = git_output(checkout, "rev-parse", "--show-toplevel").strip()
    except subprocess.CalledProcessError:
        parser.error("The host checkout must be a Git worktree")
    if worktree_root != str(checkout):
        parser.error("The host checkout must be the root of its Git worktree")
    fixture = Path(__file__).with_name("qualification_test.go.txt").resolve(strict=True)
    virtual_test = checkout / "plugins" / "zz_betterlyrics_qualification_test.go"
    if virtual_test.exists():
        parser.error(f"Preserving existing host file: {virtual_test}")
    if shutil.which("go") is None:
        parser.error("Go must be available; run this command through mise exec")
    before = host_state(checkout)
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
        # go test executes the explicitly selected trusted checkout and plugin.
        result = subprocess.run(  # nosec B603
            ["go", "test", "-mod=readonly", "-overlay", str(overlay),
             "-tags=netgo,sqlite_fts5", "./plugins", "-run", "^TestBetterLyricsQualification$",
             "-count=1", "-timeout=180s", "-v"],
            cwd=checkout, env=env, check=False, shell=False,
        )
    after = host_state(checkout)
    if after != before:
        print("Host checkout changed during qualification; inspect it before continuing.")
        return 2
    print("Host checkout status and tracked diff unchanged.")
    return result.returncode


if __name__ == "__main__":
    raise SystemExit(main())
