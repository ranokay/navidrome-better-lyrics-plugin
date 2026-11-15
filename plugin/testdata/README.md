# Host manifest schema

`navidrome-manifest-schema.json` is copied byte-for-byte from
[Navidrome v0.64.2](https://raw.githubusercontent.com/navidrome/navidrome/v0.64.2/plugins/manifest-schema.json).
It is covered by [Navidrome's GPL-3.0 license](https://github.com/navidrome/navidrome/blob/v0.64.2/LICENSE).

SHA-256: `529b9ed7285f157d2777c18694f9ca6e1952bcb0c210896e8bb92d72328ef0a6`.
The released and upstream schemas matched on 2026-11-15. Keep this immutable
release reference until the supported host schema changes.

Tests compile this local copy and the plugin's configuration schema, then
validate the manifest and default configuration without network access.
These files are test inputs and are excluded from the `.ndp` package.
