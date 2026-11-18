#!/usr/bin/env python3
"""Build a real Navidrome binary with deterministic provider HTTP fixtures.

The production host source is never modified. Go's -overlay replaces only
plugins/host_httpclient.go in the build with a qualification copy whose HTTP
transport reads a strict fixture file. Missing matches fail closed, and the
real manifest allowlist, WASI loader, KV, router, auth and UI remain active.

An optional --clock-offset also overlays plugins/host_kvstore.go with the same
build, advancing only the plugin KV's TTL clock. It exists for disposable
qualification expiry runs: no rows are deleted and the instance salt and main
database are retained.
"""

import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import subprocess  # nosec B404

HTTPCLIENT = "plugins/host_httpclient.go"
KVSTORE = "plugins/host_kvstore.go"
HOOK = "\tsvc.client = &http.Client{Transport: httpclient.NewTransport(svc.transport)}\n"
KV_FILTER = "const notExpiredFilter = \"(expires_at IS NULL OR expires_at >= datetime('now'))\""
KV_TTL = "VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, datetime('now', ?))"
CLOCK_OFFSET_PATTERN = re.compile(r"^[+-][0-9]{1,4} (seconds?|minutes?|hours?|days?)$")
FIXTURE_HELPER = """

// Qualification-only fixture transport. It is injected into the overlay build
// after the production HTTP client is constructed; production source keeps the
// real transport.
func newQualificationFixtureTransport(path string) (http.RoundTripper, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read qualification fixtures: %w", err)
	}
	var document struct {
		Fixtures []qualificationFixture `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode qualification fixtures: %w", err)
	}
	if len(document.Fixtures) == 0 {
		return nil, fmt.Errorf("qualification fixtures are empty")
	}
	return &qualificationFixtureTransport{fixtures: document.Fixtures}, nil
}

type qualificationFixture struct {
	Host          string `json:"host"`
	PathPrefix    string `json:"pathPrefix"`
	QueryContains string `json:"queryContains"`
	Status        int    `json:"status"`
	ContentType   string `json:"contentType"`
	BodyBase64    string `json:"bodyBase64"`
}

type qualificationFixtureTransport struct {
	fixtures []qualificationFixture
}

func (t *qualificationFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	for _, fixture := range t.fixtures {
		if !strings.EqualFold(request.URL.Hostname(), fixture.Host) {
			continue
		}
		if !strings.HasPrefix(request.URL.Path, fixture.PathPrefix) {
			continue
		}
		if fixture.QueryContains != "" && !strings.Contains(request.URL.RawQuery, fixture.QueryContains) {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(fixture.BodyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode qualification fixture %s: %w", fixture.Host, err)
		}
		status := fixture.Status
		if status == 0 {
			status = http.StatusOK
		}
		contentType := fixture.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": {contentType}},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	}
	return nil, fmt.Errorf("qualification fixture missing for %s%s", request.URL.Host, request.URL.Path)
}
"""


def git_output(checkout: Path, *args: str) -> str:
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)
    return subprocess.check_output(  # nosec B603
        ["git", "-c", "core.fsmonitor=false", "-C", str(checkout), *args],
        text=True,
        env=env,
        shell=False,
    )


def host_state(checkout: Path) -> tuple[str, str, str]:
    return (
        git_output(checkout, "rev-parse", "HEAD").strip(),
        git_output(checkout, "status", "--porcelain=v1"),
        git_output(checkout, "diff", "--no-ext-diff", "--no-textconv"),
    )


def rewrite_httpclient(source: str) -> str:
    if source.count(HOOK) != 1:
        raise ValueError("expected exactly one production HTTP client assignment")
    if "newQualificationFixtureTransport" in source:
        raise ValueError("source is already a qualification transport")
    hook = HOOK + """\tif fixturePath := os.Getenv("BETTER_LYRICS_FIXTURES"); fixturePath != "" {
\t\ttransport, err := newQualificationFixtureTransport(fixturePath)
\t\tif err != nil {
\t\t\tpanic(err)
\t\t}
\t\tsvc.client.Transport = transport
\t}
"""
    rewritten = source.replace(HOOK, hook, 1)
    marker = '\t"encoding/json"\n'
    if marker in rewritten:
        rewritten = rewritten.replace(marker, marker + '\t"encoding/base64"\n\t"os"\n', 1)
    elif '\t"errors"\n' in rewritten:
        rewritten = rewritten.replace(
            '\t"errors"\n',
            '\t"encoding/base64"\n\t"encoding/json"\n\t"errors"\n\t"os"\n',
            1,
        )
    else:
        raise ValueError("expected an import block for qualification dependencies")
    return rewritten + FIXTURE_HELPER


KV_CLOCK_HELPER = """

// Qualification-only clock advance for the plugin KV store. The disposable
// browser host uses it to force real TTL expiry without deleting rows or
// touching the instance salt. Production host source keeps the real clock.
const qualificationKVClockOffset = "%s"

func qualificationKVClockTTLModifier() string {
	if qualificationKVClockOffset == "" {
		return ""
	}
	return ", '" + qualificationKVClockOffset + "'"
}
"""


def rewrite_kvstore(source: str, clock_offset: str) -> str:
    if clock_offset and not CLOCK_OFFSET_PATTERN.match(clock_offset):
        raise ValueError("invalid qualification clock offset")
    if source.count(KV_FILTER) != 1:
        raise ValueError("expected exactly one plugin KV expiry filter")
    if source.count(KV_TTL) != 1:
        raise ValueError("expected exactly one plugin KV TTL write")
    if "qualificationKVClockOffset" in source:
        raise ValueError("source is already a qualification clock")
    replacement = (
        "var notExpiredFilter = \"(expires_at IS NULL OR expires_at >= datetime('now'\" + "
        "qualificationKVClockTTLModifier() + \"))\""
    )
    rewritten = source.replace(KV_FILTER, replacement, 1)
    rewritten = rewritten.replace(
        KV_TTL,
        "VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, datetime('now', ?` + "
        "qualificationKVClockTTLModifier() + `))",
        1,
    )
    return rewritten + (KV_CLOCK_HELPER % clock_offset)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host_checkout", type=Path)
    parser.add_argument("package", type=Path)
    parser.add_argument("fixtures", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--tags", default="netgo,sqlite_fts5")
    parser.add_argument("--clock-offset", default="")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    checkout = args.host_checkout.resolve(strict=True)
    package = args.package.resolve(strict=True)
    fixtures = args.fixtures.resolve(strict=True)
    if not (checkout / "go.mod").is_file():
        raise SystemExit("host checkout must contain go.mod")
    if not (checkout / "ui/build/index.html").is_file():
        raise SystemExit("host UI is not built; run `make buildjs` first")
    source = (checkout / HTTPCLIENT).read_text()
    generated = rewrite_httpclient(source)
    kv_source = (checkout / KVSTORE).read_text()
    kv_generated = rewrite_kvstore(kv_source, args.clock_offset)
    args.output.mkdir(parents=True, exist_ok=True)
    overlay_dir = args.output / "qualification"
    overlay_dir.mkdir(parents=True, exist_ok=True)
    overlay_source = overlay_dir / "host_httpclient.go"
    overlay_source.write_text(generated)
    kv_overlay_source = overlay_dir / "host_kvstore.go"
    kv_overlay_source.write_text(kv_generated)
    fixtures_copy = overlay_dir / "fixtures.json"
    shutil.copyfile(fixtures, fixtures_copy)
    package_copy = overlay_dir / "nd-better-lyrics.ndp"
    shutil.copyfile(package, package_copy)
    overlay_path = overlay_dir / "overlay.json"
    overlay_path.write_text(
        json.dumps(
            {
                "Replace": {
                    str(checkout / HTTPCLIENT): str(overlay_source),
                    str(checkout / KVSTORE): str(kv_overlay_source),
                }
            }
        )
    )
    before = host_state(checkout)
    binary = args.output / "navidrome"
    subprocess.run(  # nosec B603
        [
            "go",
            "build",
            "-mod=readonly",
            "-overlay",
            str(overlay_path),
            "-tags=" + args.tags,
            "-o",
            str(binary),
            ".",
        ],
        cwd=checkout,
        env=os.environ | {"GOWORK": "off"},
        check=True,
    )
    after = host_state(checkout)
    if after != before:
        raise SystemExit("host checkout changed during qualification build")
    receipt = {
        "hostHead": before[0],
        "hostStatus": before[1],
        "packageSHA256": sha256(package_copy),
        "fixturesSHA256": sha256(fixtures_copy),
        "overlaySHA256": sha256(overlay_source),
        "kvOverlaySHA256": sha256(kv_overlay_source),
        "qualificationClockOffset": args.clock_offset,
        "binarySHA256": sha256(binary),
        "fixturePath": str(fixtures_copy),
        "packagePath": str(package_copy),
        "binaryPath": str(binary),
    }
    (overlay_dir / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps(receipt, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
