# Disposable WASI host qualification

Build the plugin with `mise run package-check`, then run the released-host
baseline:

```sh
mise exec -- python3 scripts/host-qualification/run.py /path/to/navidrome dist/nd-better-lyrics.ndp
```

The host checkout needs its normal Go test dependencies and native SQLite build
prerequisites. Both arguments and the Git/Go executables on `PATH` must be trusted.
`go test` executes the host checkout and the selected plugin; temporary service
directories do not sandbox that code. Do not use this runner to inspect an unknown
checkout or downloaded plugin. The runner accepts any explicitly selected local
Navidrome Git worktree, including a trusted checkout outside this plugin workspace.

The baseline overlay targets the reviewed released-host APIs at Navidrome commit
`690a2343888467d5fbe1b22ba97fcb4ce571013c`. It uses `go test -mod=readonly` and
overlays one standalone test into the host's `plugins` package. It runs only that
test, so Navidrome's Ginkgo suite does not rebuild its fixture plugins.

## Optional variant/original runtime mode

The optional lyrics-variants capability is qualified against the companion host
branch (the published provenance head plus the companion API/plugin changes):

```sh
mise exec -- python3 scripts/host-qualification/run.py --originals \
  /path/to/companion-navidrome dist/nd-better-lyrics.ndp
```

`--expect legacy` uses the same host to verify an older plugin package: playback
still works, while the variant listing is empty and original retrieval reports
unavailable.

The originals mode overlays two temporary Go files (a build-tagged support file
and an external test) through Go's `-overlay`; it never writes into the host
checkout. It loads the production `.ndp` into the real WASI loader, KV and task
services, then drives the real Subsonic router and auth middleware over an
`httptest` server. Provider HTTP is replaced at the host transport boundary with
deterministic responses; there are no real provider requests, account changes,
DNS changes, or production configuration reads. The original manifest allowlist,
loader, WASI runtime, RPC functions, KV service, and persistence remain active.

The runtime test covers:

- LRCLib's ELRC, LRC and plain representations from one adapter, each independently
  listed and retrieved with exact bytes;
- exact TTML and QQ QRC native-document bytes, including their committed fixtures,
  through the real plugin/host path;
- authenticated retrieval plus unauthenticated and invalid-selection rejection;
- artifact eviction followed by a same-selection refetch, and a changed document
  that reports unavailable instead of replacing the selection;
- the legacy-plugin fallback.

The runner prints the package hash and host head and checks that the host's Git
HEAD, status and tracked diff remain unchanged. It does not establish UI support,
live-provider availability, or API-router cache behavior beyond the routes it
drives. The overlay calls the real Subsonic routes for the new endpoints.

Run the runner's input and cleanup regressions with:

```sh
mise run qualification-runner-check
```
