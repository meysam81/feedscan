# feedscan

[![CI](https://img.shields.io/github/actions/workflow/status/meysam81/feedscan/ci.yml?branch=main&label=CI&logo=githubactions&logoColor=white&style=flat-square)](https://github.com/meysam81/feedscan/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/meysam81/feedscan?style=flat-square)](https://goreportcard.com/report/github.com/meysam81/feedscan)
[![Go Reference](https://img.shields.io/badge/pkg.go.dev-reference-007d9c?logo=go&logoColor=white&style=flat-square)](https://pkg.go.dev/github.com/meysam81/feedscan)
[![OpenSSF Scorecard](https://img.shields.io/ossf-scorecard/github.com/meysam81/feedscan?style=flat-square&label=Scorecard&logo=securityscorecard&logoColor=white)](https://scorecard.dev/viewer/?uri=github.com/meysam81/feedscan)
[![Latest Release](https://img.shields.io/github/v/release/meysam81/feedscan?logo=github&label=release&style=flat-square)](https://github.com/meysam81/feedscan/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/meysam81/feedscan/total?logo=github&label=downloads&style=flat-square)](https://github.com/meysam81/feedscan/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/meysam81/feedscan?logo=go&logoColor=white&style=flat-square)](go.mod)
[![License](https://img.shields.io/github/license/meysam81/feedscan?style=flat-square)](LICENSE)

[![Homebrew](https://img.shields.io/badge/homebrew-meysam81%2Ftap%2Ffeedscan-FBB040?logo=homebrew&logoColor=white&style=flat-square)](https://github.com/meysam81/homebrew-tap)
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-blue?logo=linux&logoColor=white&style=flat-square)](#install)
[![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-FE5196?logo=conventionalcommits&logoColor=white&style=flat-square)](https://www.conventionalcommits.org)
[![Renovate](https://img.shields.io/badge/renovate-enabled-1f8b4c?logo=renovatebot&logoColor=white&style=flat-square)](https://developer.mend.io/github/meysam81/feedscan)
[![Last Commit](https://img.shields.io/github/last-commit/meysam81/feedscan?logo=github&style=flat-square)](https://github.com/meysam81/feedscan/commits/main)
[![Stars](https://img.shields.io/github/stars/meysam81/feedscan?logo=github&style=flat-square)](https://github.com/meysam81/feedscan/stargazers)

Point it at a page full of links (a blogroll, a directory, a webring). It finds the RSS/Atom feed of every site linked there and gives you one newest-first list of their blog posts.

## Install

### Homebrew (macOS, Linux)

```sh
brew install meysam81/tap/feedscan
```

### Go

```sh
go install github.com/meysam81/feedscan@latest
```

### Prebuilt binary (GitHub Releases)

Pick the archive for your platform from the [latest release](https://github.com/meysam81/feedscan/releases/latest) and drop the binary on your `PATH`.

```sh
# Linux x86_64
curl -fsSL https://github.com/meysam81/feedscan/releases/latest/download/feedscan_$(curl -fsSL https://api.github.com/repos/meysam81/feedscan/releases/latest | grep -oP '"tag_name":\s*"v\K[^"]+')_linux_amd64.tar.gz \
  | tar xz feedscan && sudo mv feedscan /usr/local/bin/

# macOS Apple Silicon
curl -fsSL https://github.com/meysam81/feedscan/releases/latest/download/feedscan_$(curl -fsSL https://api.github.com/repos/meysam81/feedscan/releases/latest | grep -oP '"tag_name":\s*"v\K[^"]+')_darwin_arm64.tar.gz \
  | tar xz feedscan && sudo mv feedscan /usr/local/bin/
```

Every release ships `linux | darwin | windows` × `amd64 | arm64` archives plus a `checksums.txt` (SHA-256). Verify with `sha256sum -c checksums.txt`.

### From source

```sh
git clone https://github.com/meysam81/feedscan
cd feedscan
go build -o feedscan .
```

## Usage

Two steps: `scan` finds the sites and their feeds once, the default command lists their posts whenever you ask.

```sh
# 1. Scan once: find the sites linked from a page and their feeds.
feedscan scan --url https://example.com/blogroll

# 2. List: the newest posts those sites have published, fetched right now.
feedscan
```

```
TITLE                                               URL                                       DATE
--------------------------------------------------------------------------------------------------------
Why I moved my blog off a static site generator     alice.example/posts/why-i-moved-my-blog   2026-05-16
Notes on running a tiny DNS server at home, part …  bob.example/2026/05/notes-on-running-a-…  2026-05-14
```

### Listing posts

`feedscan` (short for `feedscan posts`) reads every `*.checkpoint.json` in the current directory, fetches the feed of every site those scans found, and prints the newest posts first. A post linked from several scans is listed once. The list is always current, however long ago you scanned; run `scan` again only to pick up sites added to the page since.

Fetching takes a moment (under a minute for a thousand sites; feeds that share a host are fetched one at a time). A feed that can't be reached is skipped and counted on stderr; if none can be reached, `feedscan` exits with an error. Add `--cached` to list the posts saved by the last scan instead: instant, offline, as old as that scan.

```
--checkpoint    checkpoint file to read (repeatable; default: every *.checkpoint.json here)
--limit, -n     max posts (default 50, 0 = all)
--days          only posts from the last N days (default 0 = any age)
--exclude       hide posts from this host and its subdomains (repeatable)
--cached        list the posts saved by the last scan instead of fetching (default false)
--concurrency   max feeds fetched at once (default 32)
--timeout       per-feed HTTP timeout (default 10s)
--format        output format: json|table (default table)
```

These also read `FEEDSCAN_CHECKPOINT`, `FEEDSCAN_LIMIT`, `FEEDSCAN_POSTS_DAYS`, `FEEDSCAN_EXCLUDE` (comma-separated), `FEEDSCAN_CACHED`, `FEEDSCAN_CONCURRENCY`, `FEEDSCAN_TIMEOUT` and `FEEDSCAN_FORMAT`. `scan` reads `FEEDSCAN_CONCURRENCY` and `FEEDSCAN_TIMEOUT` too, so setting them changes both.

```sh
# The 10 newest posts:
feedscan -n 10

# Everything from the last week:
feedscan --days 7 -n 0

# One source only:
feedscan --checkpoint 512kb.club.checkpoint.json

# Offline, or in a hurry: the posts as of the last scan:
feedscan --cached

# Hide a site that drowns out the rest (also hides www.example.com, blog.example.com):
feedscan --exclude example.com --exclude noisy.example.org
export FEEDSCAN_EXCLUDE=example.com,noisy.example.org   # or once, for every run

# Full titles and URLs (the table shortens both), e.g. to open the newest post:
feedscan --format json | jq -r '.[0].url'
```

Posts are dated by when they were published, so editing an old post doesn't move it up. Posts without a date, and posts dated more than a day in the future, are left out.

### Scanning

`scan` fetches the page, probes every external site for a feed, saves each site's feed URL and newest posts, and prints a per-site summary.

Common flags for `scan`:

```
--url           input URL to scan (required; bare hosts get https:// prepended)
--days          freshness window in days (default 7)
--timeout       per-request HTTP timeout (default 10s)
--concurrency   max concurrent workers (default 8)
--batch         URLs per batch (default 32)
--cache-ttl     checkpoint TTL (default 24h)
--checkpoint    checkpoint file path (default ./<domain>.checkpoint.json, from --url)
--dry-run       extract URLs but skip feed probing
--no-cache      ignore checkpoint cache
--verbose       print full result list (default: aggregated summary + top 3)
--max-urls      cap on extracted URLs to process (0 = unlimited)
--max-posts     newest posts to save per site for feedscan --cached (default 50, 0 = none)
--sort-by       sort field: url|status|latest_post|item_count|checked_at (default latest_post)
--sort-order    sort order: asc|desc (default desc)
--format        output format: json|table (default json)
--user-agent    HTTP User-Agent (default feedscan/1.0)
--host-delay    min delay between requests to same host (default 500ms)
--max-body      max response body bytes (default 10MB)
--version       print version and exit
```

Every flag has a `FEEDSCAN_*` env-var equivalent (e.g. `FEEDSCAN_URL`, `FEEDSCAN_CHECKPOINT`).

> **Upgrading from 0.0.x:** a bare `feedscan` now lists posts instead of scanning, also when `FEEDSCAN_URL` is set; scanning always needs the subcommand (`feedscan scan --url example.com`). Listing fetches the feeds live; `feedscan --cached` lists the posts saved at scan time instead, offline.

### Examples

Aggregated summary (default — total + breakdown by status + top 3 results):

```sh
feedscan scan --url example.com
```

Verbose output — every probed URL:

```sh
feedscan scan --url example.com --verbose
```

Try a small batch first:

```sh
feedscan scan --url example.com --max-urls 10
```

Dry run — list external URLs without probing feeds:

```sh
feedscan scan --url example.com --dry-run
```

30-day window with higher concurrency, table output:

```sh
feedscan scan --url example.com --days 30 --concurrency 16 --format table --verbose
```

Force a fresh scan, ignoring cached results:

```sh
feedscan scan --url example.com --no-cache
```

## Reading cursor

Track which scanned URLs you've personally visited. Each checkpoint file stores a per-URL `visited_at` timestamp; subcommands operate on that state.

```sh
# List the top 20 unvisited URLs sorted by latest_post desc:
feedscan cursor --checkpoint 512.club.checkpoint.json unread

# Same, but only the top 5:
feedscan cursor --checkpoint 512.club.checkpoint.json unread -n 5

# Mark a URL as visited (sets visited_at = now, UTC):
feedscan cursor --checkpoint 512.club.checkpoint.json mark https://example.com

# Clear the mark:
feedscan cursor --checkpoint 512.club.checkpoint.json unmark https://example.com

# Show what you've read, most recent first:
feedscan cursor --checkpoint 512.club.checkpoint.json history

# Totals + by-status breakdown:
feedscan cursor --checkpoint 512.club.checkpoint.json stats
```

All cursor commands honor `--format json|table` (default `table`) and the `FEEDSCAN_CHECKPOINT`/`FEEDSCAN_FORMAT` env vars.

Mutating commands (`mark`/`unmark`) acquire an advisory file lock (`<checkpoint>.lock`) so concurrent invocations from multiple terminals can't lose updates.

Re-running `scan` preserves your reading state — `visited_at` is never overwritten by probing.

### Output

Each scanned site has one of four statuses:

- `active` — feed found, latest post within the window
- `stale` — feed found, no posts within the window
- `no_feed` — probed all candidates, no feed found
- `error` — network or parse error (see `error` field)

## Notes

- Checkpoints are keyed per external URL and persisted once per batch, so an interrupted run resumes without re-fetching whole batches.
- "External" means a different registrable domain (eTLD+1) than the input URL.
- TLS 1.2 minimum. HTTPS-only.
- Feed discovery: HTML `<link rel="alternate">` first, then a fallback list of common paths (`/feed`, `/rss`, `/atom.xml`, etc.).

### Checkpoint file shape

```json
{
  "entries": {
    "https://example.com": {
      "url": "https://example.com",
      "status": "active",
      "feed_url": "https://example.com/feed",
      "latest_post": "2026-05-08T15:54:49Z",
      "item_count": 12,
      "checked_at": "2026-05-17T05:00:00Z",
      "visited_at": "2026-05-17T05:28:09Z"
    }
  },
  "posts": {
    "https://example.com": [
      {
        "title": "Hello, world",
        "url": "https://example.com/hello-world",
        "date": "2026-05-08T15:54:49Z"
      }
    ]
  }
}
```

`posts` holds each site's newest posts as of the scan (up to `--max-posts`), newest first; it is what `feedscan --cached` lists and is replaced whenever the site is re-scanned. The live list reads only `feed_url`.

`visited_at` is only set when you run `feedscan cursor mark`; it's UTC RFC3339.

## License

Apache License 2.0. See [LICENSE](LICENSE).
