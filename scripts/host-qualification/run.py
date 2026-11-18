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

BASELINE_OVERLAY = "qualification_test.go.txt"
ORIGINALS_TEST = "qualification_originals_test.go.txt"
ORIGINALS_SUPPORT = "qualification_originals_support.go.txt"
ORIGINALS_TEST_NAME = "TestLyricsOriginalsQualification"
DISCOVERY_TEST = "qualification_discovery_test.go.txt"
DISCOVERY_TEST_NAME = "TestLyricsDiscoveryQualification"


def git_output(checkout: Path, *args: str) -> str:
    # Ignore inherited repository/config overrides and prevent fsmonitor execution.
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)
    # Arguments are fixed by this module; the trusted checkout is a separate argv entry.
    return subprocess.check_output(  # nosec B603
        ["git", "-c", "core.fsmonitor=false", "-C", str(checkout), *args],
        text=True, env=env, shell=False,
    )


def host_state(checkout: Path) -> tuple[str, str, str]:
    return (git_output(checkout, "rev-parse", "HEAD").strip(),
            git_output(checkout, "status", "--porcelain=v1"),
            git_output(checkout, "diff", "--no-ext-diff", "--no-textconv"))


def existing_path(parser: argparse.ArgumentParser, path: Path, directory: bool) -> Path:
    try:
        resolved = path.resolve(strict=True)
    except OSError as error:
        parser.error(str(error))
    if not (resolved.is_dir() if directory else resolved.is_file()):
        parser.error(f"Expected a {'directory' if directory else 'regular file'}: {resolved}")
    return resolved


def overlay_files(script_dir: Path, originals: bool, discovery: bool = False) -> dict[str, Path]:
    if not originals and not discovery:
        return {"plugins/zz_betterlyrics_qualification_test.go": script_dir / BASELINE_OVERLAY}
    files = {
        "plugins/zz_betterlyrics_qualification_support.go": script_dir / ORIGINALS_SUPPORT,
        "plugins/zz_betterlyrics_qualification_originals_test.go": script_dir / ORIGINALS_TEST,
    }
    if discovery:
        files["plugins/zz_betterlyrics_qualification_discovery_test.go"] = script_dir / DISCOVERY_TEST
    return files


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host_checkout", type=Path, help="trusted local Navidrome Git checkout")
    parser.add_argument("package", type=Path, help="trusted local plugin .ndp file")
    parser.add_argument("--originals", action="store_true",
                        help="run the optional variant/original runtime qualification")
    parser.add_argument("--discovery", action="store_true",
                        help="run the discovery/refresh/publication runtime qualification")
    parser.add_argument("--expect", choices=("originals", "legacy"), default="originals",
                        help="expected capability state for the originals run (default: originals)")
    args = parser.parse_args()
    if not args.originals and args.expect != "originals":
        parser.error("--expect requires --originals")
    if args.discovery and args.expect != "originals":
        parser.error("--expect requires --originals")
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

    script_dir = Path(__file__).resolve().parent
    overlays = overlay_files(script_dir, args.originals, args.discovery)
    for destination, source in overlays.items():
        if not source.is_file():
            parser.error(f"Missing qualification overlay: {source}")
        if (checkout / destination).exists():
            parser.error(f"Preserving existing host file: {checkout / destination}")
    if shutil.which("go") is None:
        parser.error("Go must be available; run this command through mise exec")
    before = host_state(checkout)
    print(f"Host head: {before[0]}", flush=True)
    with tempfile.TemporaryDirectory(prefix="better-lyrics-host-") as temporary:
        package_bytes = package.read_bytes()
        snapshot = Path(temporary) / "nd-better-lyrics.ndp"
        snapshot.write_bytes(package_bytes)
        print(f"Package SHA-256: {hashlib.sha256(package_bytes).hexdigest()}", flush=True)
        overlay = Path(temporary) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {
            str(checkout / destination): str(source) for destination, source in overlays.items()
        }}))
        env = os.environ | {
            "BETTER_LYRICS_PACKAGE": str(snapshot),
            "BETTER_LYRICS_CACHE": str(Path(temporary) / "compiled"),
            "GOWORK": "off",
        }
        if args.originals or args.discovery:
            repo_root = script_dir.parent.parent
            env |= {
                "BETTER_LYRICS_EXPECT": args.expect,
                "BETTER_LYRICS_TTML_FIXTURE": str(repo_root / "internal/source/testdata/binimum/lyrics.ttml"),
                "BETTER_LYRICS_QRC_FIXTURE": str(repo_root / "internal/source/testdata/portato/lyrics.qrc"),
            }
            tags = "netgo,sqlite_fts5,qualification"
            test_name = ORIGINALS_TEST_NAME
            if args.discovery:
                test_name = DISCOVERY_TEST_NAME
        else:
            tags = "netgo,sqlite_fts5"
            test_name = "TestBetterLyricsQualification"
        # go test executes the explicitly selected trusted checkout and plugin.
        result = subprocess.run(  # nosec B603
            ["go", "test", "-mod=readonly", "-overlay", str(overlay),
             "-tags=" + tags, "./plugins", "-run", "^" + test_name + "$",
             "-count=1", "-timeout=180s", "-v"],
            cwd=checkout, env=env, check=False, shell=False,
        )
    after = host_state(checkout)
    if after != before:
        print("Host checkout changed during qualification; inspect it before continuing.")
        return 2
    print("Host checkout HEAD, status and tracked diff unchanged.")
    return result.returncode


if __name__ == "__main__":
    raise SystemExit(main())
