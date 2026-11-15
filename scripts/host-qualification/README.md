# Disposable WASI host qualification

Build the plugin with `mise run package-check`, then run:

```sh
mise exec -- python3 scripts/host-qualification/run.py /path/to/navidrome dist/nd-better-lyrics.ndp
```

The host checkout needs its normal Go test dependencies and native SQLite build
prerequisites. Both arguments and the Git/Go executables on `PATH` must be trusted.
`go test` executes the host checkout and the selected plugin; temporary service
directories do not sandbox that code. Do not use this runner to inspect an unknown
checkout or downloaded plugin. The runner accepts any explicitly selected local
Navidrome Git worktree, including a trusted checkout outside this plugin workspace.

This harness targets the reviewed host APIs at Navidrome commit
`690a2343888467d5fbe1b22ba97fcb4ce571013c`. It uses `go test -mod=readonly`
and overlays one standalone test into the host's `plugins` package. It runs only
that test, so Navidrome's Ginkgo suite does not rebuild its fixture plugins.

The Go fixture is stored as `.go.txt` so the plugin's own `go test ./...` does not
try to build it as part of this module. Format changes with `gofmt -w` explicitly.

All data, compiled-WASM caches, and SQLite stores use test-owned temporary
directories. Provider HTTP is replaced at the host transport boundary with
deterministic responses; there are no real provider requests, account changes,
DNS changes, or production configuration reads. The original manifest allowlist,
loader, WASI runtime, RPC functions, KV service, and task workers remain active.

The runner prints the package hash and host head and checks that the host's Git
status and tracked diff remain unchanged. It does not establish UI support,
live-provider availability, or API-router cache behavior. The overlay calls the
real plugin capability directly to isolate plugin/host behavior.

Run the runner's input and cleanup regressions with:

```sh
mise run qualification-runner-check
```
