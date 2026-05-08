# feedscan

Fetch a URL, extract external HTTPS links, probe each for an RSS/Atom feed, and report which feeds have published within a freshness window.

## Install

```sh
go install github.com/meysam81/feedscan@latest
```

Or build from source:

```sh
git clone https://github.com/meysam81/feedscan
cd feedscan
go build -o feedscan .
```

## Usage

```sh
feedscan -url https://example.com/blog
```

Common flags:

```
-url           input URL to scan (required)
-days          freshness window in days (default 7)
-timeout       per-request HTTP timeout (default 10s)
-concurrency   max concurrent workers (default 8)
-batch         URLs per batch (default 32)
-cache-ttl     checkpoint TTL (default 24h)
-checkpoint    checkpoint file path (default ./feedscan.checkpoint.json)
-dry-run       extract URLs but skip feed probing
-no-cache      ignore checkpoint cache
-format        output format: json|table (default json)
-user-agent    HTTP User-Agent (default feedscan/1.0)
-host-delay    min delay between requests to same host (default 500ms)
-max-body      max response body bytes (default 10MB)
```

### Examples

Dry run — just list external URLs found on the page:

```sh
feedscan -url https://example.com/blog -dry-run
```

30-day window with higher concurrency, table output:

```sh
feedscan -url https://example.com/blog -days 30 -concurrency 16 -format table
```

Force a fresh scan, ignoring cached results:

```sh
feedscan -url https://example.com/blog -no-cache
```

### Output

Each result has one of four statuses:

- `active` — feed found, latest post within the window
- `stale` — feed found, no posts within the window
- `no_feed` — probed all candidates, no feed found
- `error` — network or parse error (see `error` field)

## Notes

- Checkpoints are keyed per external URL and persist after each probe, so an interrupted run resumes without re-fetching.
- "External" means a different registrable domain (eTLD+1) than the input URL.
- TLS 1.2 minimum. HTTPS-only.
- Feed discovery: HTML `<link rel="alternate">` first, then a fallback list of common paths (`/feed`, `/rss`, `/atom.xml`, etc.).

## License

Apache License 2.0. See [LICENSE](LICENSE).
