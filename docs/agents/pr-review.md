# Pull request review guidance

Use the linked issue as the acceptance criteria. Read `docs/current-state.md`
for settled contracts and compatibility holds, and README.md for supported
provider behavior. Historical plans and stale status snapshots do not create
new requirements; verify current issue and PR state before making delivery claims.

Prioritize bounds, cache freshness and failure backoff, source ordering and
disabled-source behavior, recording/variant identity, exact native-document
provenance, and old-host compatibility. Retain configured request budgets and
reserved KV control capacity. Watch for post-await or deferred work that acts on
stale configuration, selection, credentials or cache generations.

Also propose useful cleanup and style improvements, naming the convention they
serve and marking them as optional where behavior is already correct. Avoid
formatting and lint findings covered by `mise run check`.

The full local gate includes race tests, vet, module verification, workflow lint,
qualification-runner tests, and WASI package checks. A passing build or mocked
transport does not prove released-host behavior or current upstream availability.
Companion host/API/UI changes may be on an unreleased fork; keep those claims
separate from plugin delivery and a published release.

Provider identifiers are not automatically private credentials. Establish the
actual exposure and capability before alleging a secret leak; never reproduce
private values in review output.

PR-Agent is an optional reviewer. Its suggestions need source verification and
appropriate tests before applying. It cannot replace local independent reviews,
repository gates, runtime qualification, or maintainers' merge decisions.
