# Bounded public provider probes

Run one source against a public track:

```sh
mise exec -- go run ./scripts/provider-probe -source lrclib
```

The default metadata is Shape of You by Ed Sheeran, album ÷ (Deluxe), 234 seconds.
The command sends that metadata and the machine's IP address to the selected
provider. Override it with `-title`, `-artist`, `-album`, and `-duration`.
It reads no Navidrome library, account, or administrator credentials.

Without `-source`, the command checks default-enabled sources sequentially.
Add `-include-disabled` to deliberately contact the opt-in sources too. Each
source has a twelve-second total allowance and each request uses the provider's
six-second ceiling. Requests and redirects remain inside the catalog's HTTPS
host allowlist; the probe uses the real adapters and payload limits.

Results are JSON lines containing UTC time, source ID, health, formats, request
statuses, body sizes and recognized field names. It emits no lyrics, query
strings, headers, arbitrary upstream field names, or raw errors. Redirect output
to a private file when recording a dated result. There is no persistent cache.

Exit zero means the probe completed and wrote its observations. Individual
provider outages and misses are represented in those observations, rather than
turning third-party uptime into a repository gate. These checks complement the
deterministic [WASI host qualification](../host-qualification/README.md).
