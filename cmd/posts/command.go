// Package posts exposes the feedscan posts subcommand: a newest-first list of
// the blog posts of the sites found by earlier scans. main also mounts its
// flags and action on the root command, so a bare `feedscan` runs it.
//
// It lists what each site's feed (the feed_url a scan stored) holds, so an old
// scan still gives a current list. Feeds are fetched at most once per
// --max-age: what they held is remembered in a cache file, and a feed whose
// copy is recent enough is not requested again. --refresh fetches everything
// now; --cached never touches the network. Checkpoint files are only read.
package posts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/urfave/cli/v3"

	"feedscan/cmd/cliflags"
	"feedscan/internal/checkpoint"
	"feedscan/internal/postcache"
	"feedscan/internal/report"
	"feedscan/internal/scanner"
)

// newHTTPClient builds the client for live fetches. A variable only so tests
// can trust their own TLS test servers.
var newHTTPClient = scanner.NewHTTPClient

// checkpointGlob matches the files `feedscan scan` writes by default.
const checkpointGlob = "*.checkpoint.json"

// Command returns the "posts" subcommand. Flag destinations are scoped inside
// this constructor — no package-level mutable state.
//
// Every flag is Local: when mounted on the root command they must not be
// inherited by scan and cursor, which define --checkpoint, --days, --format,
// --concurrency and --timeout themselves.
func Command() *cli.Command {
	var (
		paths       []string
		exclude     []string
		format      string
		limit       int
		days        int
		perSite     int
		cached      bool
		refresh     bool
		maxAge      time.Duration
		cacheFile   string
		concurrency int
		timeout     time.Duration
	)
	formatFlag := cliflags.FormatFlag(&format, "table")
	formatFlag.Local = true
	return &cli.Command{
		Name:  "posts",
		Usage: "List the newest blog posts from the sites found by earlier scans",
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:        "checkpoint",
				Usage:       "checkpoint file to read (repeatable; default: every " + checkpointGlob + " in the current directory)",
				Sources:     cli.EnvVars(cliflags.EnvCheckpointPath),
				Destination: &paths,
				Local:       true,
			},
			&cli.IntFlag{
				Name:        "limit",
				Aliases:     []string{"n"},
				Usage:       "max posts (0 = all)",
				Value:       50,
				Sources:     cli.EnvVars("FEEDSCAN_LIMIT"),
				Destination: &limit,
				Local:       true,
			},
			&cli.IntFlag{
				Name:  "days",
				Usage: "only posts from the last N days (0 = any age)",
				// Not FEEDSCAN_DAYS: that is scan's freshness window, and
				// exporting it for scan must not silently shorten this list.
				Sources:     cli.EnvVars("FEEDSCAN_POSTS_DAYS"),
				Destination: &days,
				Local:       true,
			},
			&cli.StringSliceFlag{
				Name:        "exclude",
				Usage:       "hide posts from this host and its subdomains (repeatable)",
				Sources:     cli.EnvVars("FEEDSCAN_EXCLUDE"),
				Destination: &exclude,
				Local:       true,
			},
			&cli.IntFlag{
				Name:        "per-site",
				Usage:       "max posts per scanned site, so one busy site can't fill the list (0 = all)",
				Value:       3,
				Sources:     cli.EnvVars("FEEDSCAN_PER_SITE"),
				Destination: &perSite,
				Local:       true,
			},
			&cli.DurationFlag{
				Name:        "max-age",
				Usage:       "fetch a feed again once its cached copy is older than this (0 = always)",
				Value:       24 * time.Hour,
				Sources:     cli.EnvVars("FEEDSCAN_MAX_AGE"),
				Destination: &maxAge,
				Local:       true,
			},
			&cli.BoolFlag{
				Name:        "refresh",
				Usage:       "fetch every feed now, whatever the age of its cached copy",
				Sources:     cli.EnvVars("FEEDSCAN_REFRESH"),
				Destination: &refresh,
				Local:       true,
			},
			&cli.BoolFlag{
				Name:        "cached",
				Usage:       "never fetch: list cached posts whatever their age (no network)",
				Sources:     cli.EnvVars("FEEDSCAN_CACHED"),
				Destination: &cached,
				Local:       true,
			},
			&cli.StringFlag{
				Name:        "cache-file",
				Usage:       "where fetched posts are remembered (default: feedscan/posts.json in the user cache directory)",
				Sources:     cli.EnvVars("FEEDSCAN_POSTS_CACHE"),
				Destination: &cacheFile,
				Local:       true,
			},
			&cli.IntFlag{
				Name:        "concurrency",
				Usage:       "max feeds fetched at once",
				Value:       32,
				Sources:     cli.EnvVars("FEEDSCAN_CONCURRENCY"),
				Destination: &concurrency,
				Local:       true,
			},
			&cli.DurationFlag{
				Name:        "timeout",
				Usage:       "per-feed HTTP timeout",
				Value:       scanner.DefaultTimeout,
				Sources:     cli.EnvVars("FEEDSCAN_TIMEOUT"),
				Destination: &timeout,
				Local:       true,
			},
			formatFlag,
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return fmt.Errorf("unknown command %q (see --help)", cmd.Args().First())
			}
			if limit < 0 || days < 0 || perSite < 0 {
				return errors.New("--limit, --days and --per-site must be >= 0")
			}
			if cached && refresh {
				return errors.New("--cached and --refresh contradict each other; pick one")
			}
			// --cached makes no requests, so a bad FEEDSCAN_CONCURRENCY must not break it.
			if !cached && (concurrency < 1 || timeout <= 0 || maxAge < 0) {
				return errors.New("--concurrency must be >= 1, --timeout > 0 and --max-age >= 0")
			}
			if len(paths) == 0 {
				found, err := filepath.Glob(checkpointGlob)
				if err != nil {
					return err
				}
				if len(found) == 0 {
					return errors.New("no " + checkpointGlob + " files here; run `feedscan scan --url <page>` first, or pass --checkpoint")
				}
				paths = found
			}
			stderr := cmd.Root().ErrWriter

			// One entry per feed, however many scans found it.
			scanned := make(map[string][]checkpoint.Post)
			for _, path := range paths {
				cp, warn, err := checkpoint.Load(path)
				if err != nil {
					return fmt.Errorf("load checkpoint: %w", err)
				}
				if warn != "" {
					fmt.Fprintf(stderr, "warning: %s: %s\n", path, warn)
				}
				for feedURL, posts := range cp.Feeds() {
					scanned[feedURL] = append(scanned[feedURL], posts...)
				}
			}
			feedURLs := make([]string, 0, len(scanned))
			for u := range scanned {
				feedURLs = append(feedURLs, u)
			}
			sort.Strings(feedURLs)

			if cacheFile == "" {
				dir, err := os.UserCacheDir()
				if err != nil {
					return fmt.Errorf("no user cache directory (%w); pass --cache-file", err)
				}
				cacheFile = filepath.Join(dir, "feedscan", "posts.json")
			}
			cache, warn := postcache.Load(cacheFile)
			if warn != "" {
				fmt.Fprintf(stderr, "warning: %s: %s\n", cacheFile, warn)
			}

			now := time.Now()
			var due []scanner.FeedRequest
			if !cached {
				for _, u := range feedURLs {
					f, known := cache.Feeds[u]
					// A fetch time in the future (a clock set back since) says
					// nothing about the copy's age: fetch.
					if refresh || !known || f.FetchedAt.After(now) || now.Sub(f.FetchedAt) >= maxAge {
						due = append(due, scanner.FeedRequest{URL: u, ETag: f.ETag, LastModified: f.LastModified})
					}
				}
			}
			allFailed, offline := false, false
			if len(due) > 0 {
				results, err := fetchLive(ctx, stderr, due, concurrency, timeout)
				if err != nil {
					return err
				}
				failed, answered := 0, 0
				for _, r := range results {
					if r.Err != nil {
						failed++
					}
					if r.Err == nil || r.Answered {
						answered++
					}
				}
				allFailed = failed == len(due)
				// Not one server answering is no network, not a thousand broken
				// sites. The cache is left as it was, so the next run tries again
				// at once instead of waiting out --max-age.
				if offline = answered == 0; !offline {
					for _, r := range results {
						f := cache.Feeds[r.URL]
						f.FetchedAt = now
						switch {
						case r.Err != nil:
							// A failed feed keeps the posts of its last good fetch,
							// but not the validators: if they were what broke the
							// request, the next one must go without.
							f.ETag, f.LastModified = "", ""
						case !r.NotModified:
							f.Posts, f.ETag, f.LastModified = r.Posts, r.ETag, r.LastModified
						}
						cache.Feeds[r.URL] = f
					}
					if err := cache.Save(cacheFile); err != nil {
						fmt.Fprintf(stderr, "warning: posts cache not saved: %v\n", err)
					}
					if failed > 0 {
						fmt.Fprintf(stderr, "%d of %d feeds failed\n", failed, len(due))
					}
				}
			}

			var (
				all    []checkpoint.Post
				have   int       // posts available before any filter
				oldest time.Time // least recently fetched feed in the list
			)
			for _, u := range feedURLs {
				f, known := cache.Feeds[u]
				posts := f.Posts
				if cached && len(posts) == 0 {
					posts = scanned[u] // never fetched with success: what the scan saw is all there is
				}
				if known && (oldest.IsZero() || f.FetchedAt.Before(oldest)) {
					oldest = f.FetchedAt
				}
				have += len(posts)
				// The per-site cap applies to what would be listed, hence after the filters.
				all = append(all, report.SelectPosts(report.ExcludeHosts(posts, exclude), now, days, perSite)...)
			}

			switch {
			case len(feedURLs) == 0:
				fmt.Fprintln(stderr, "no feeds found by earlier scans; run `feedscan scan --url <page>` to find some")
			case allFailed && have == 0:
				// An empty list would pass for "nothing new".
				return fmt.Errorf("all %d feeds failed and none of their posts are cached; --cached lists the posts saved by the last scan", len(due))
			case offline:
				fmt.Fprintf(stderr, "no feed could be reached (offline?); listing cached posts, some from %s ago\n", age(now.Sub(oldest)))
			case cached && have == 0:
				fmt.Fprintln(stderr, "no posts cached yet; run `feedscan` without --cached to fetch them")
			case cached && !oldest.IsZero():
				fmt.Fprintf(stderr, "posts as of %s ago\n", age(now.Sub(oldest)))
			case len(due) == 0 && !oldest.IsZero():
				fmt.Fprintf(stderr, "posts as of %s ago; --refresh to fetch now\n", age(now.Sub(oldest)))
			}

			selected := report.SelectPosts(all, now, days, limit)
			return report.WritePosts(cmd.Root().Writer, format, selected)
		},
	}
}

// fetchLive fetches the feeds that are due, reporting progress on stderr.
// Failed feeds are in the results; only an interrupted run is an error.
func fetchLive(ctx context.Context, stderr io.Writer, due []scanner.FeedRequest, concurrency int, timeout time.Duration) ([]scanner.FeedResult, error) {
	cfg := &scanner.Config{
		Timeout:      timeout,
		Concurrency:  concurrency,
		UserAgent:    scanner.DefaultUserAgent,
		MaxBodyBytes: scanner.DefaultMaxBodyBytes,
		HostDelay:    scanner.DefaultHostDelay,
		MaxPosts:     math.MaxInt, // --per-site and --limit cap the list, not the cache
	}
	tick, done := progress(stderr, len(due), isTerminal(stderr))
	results := scanner.FetchFeeds(ctx, newHTTPClient(cfg), cfg, due, tick)
	done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// progress reports a fetch of total feeds on w. On a terminal it is a counter
// that tick advances in place and done ends; anywhere else (a pipe, a log) it
// is one line up front, so nothing is rewritten in a file.
func progress(w io.Writer, total int, terminal bool) (tick, done func()) {
	if !terminal {
		fmt.Fprintf(w, "fetching %d feeds...\n", total)
		return nil, func() {}
	}
	var (
		mu sync.Mutex // tick is called from the fetching goroutines
		n  int
	)
	fmt.Fprintf(w, "fetching feeds 0/%d", total)
	tick = func() {
		mu.Lock()
		defer mu.Unlock()
		n++
		fmt.Fprintf(w, "\rfetching feeds %d/%d", n, total)
	}
	return tick, func() { fmt.Fprintln(w) }
}

// isTerminal reports whether w is an interactive terminal.
// ponytail: any character device counts, /dev/null included, where a
// rewritten counter is as invisible as a line would be.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// age renders how long ago something was, coarsely: "5m", "3h", "2d".
func age(d time.Duration) string {
	switch {
	case d < time.Minute: // including a fetch time in the future
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
