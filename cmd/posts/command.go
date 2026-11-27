// Package posts exposes the feedscan posts subcommand: a newest-first list of
// the blog posts of the sites found by earlier scans. main also mounts its
// flags and action on the root command, so a bare `feedscan` runs it.
//
// By default it fetches each site's feed (the feed_url a scan stored) and
// lists what the feeds hold right now, so an old scan still gives a current
// list. With --cached it lists the posts saved by the scan instead: no
// network. Either way checkpoint files are only read, never written.
package posts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v3"

	"feedscan/cmd/cliflags"
	"feedscan/internal/checkpoint"
	"feedscan/internal/report"
	"feedscan/internal/scanner"
)

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
		cached      bool
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
			&cli.BoolFlag{
				Name:        "cached",
				Usage:       "list the posts saved by the last scan instead of fetching the feeds (no network)",
				Sources:     cli.EnvVars("FEEDSCAN_CACHED"),
				Destination: &cached,
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
			if limit < 0 || days < 0 {
				return errors.New("--limit and --days must be >= 0")
			}
			// --cached makes no requests, so a bad FEEDSCAN_CONCURRENCY must not break it.
			if !cached && (concurrency < 1 || timeout <= 0) {
				return errors.New("--concurrency must be >= 1 and --timeout > 0")
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

			var (
				saved    []checkpoint.Post
				feedURLs []string
				seen     = make(map[string]struct{})
			)
			for _, path := range paths {
				cp, warn, err := checkpoint.Load(path)
				if err != nil {
					return fmt.Errorf("load checkpoint: %w", err)
				}
				if warn != "" {
					fmt.Fprintf(stderr, "warning: %s: %s\n", path, warn)
				}
				saved = append(saved, cp.AllPosts()...)
				for _, u := range cp.FeedURLs() {
					if _, dup := seen[u]; !dup { // one fetch per feed, however many scans found it
						seen[u] = struct{}{}
						feedURLs = append(feedURLs, u)
					}
				}
			}

			all := saved
			if cached {
				if len(all) == 0 {
					fmt.Fprintln(stderr, "no posts saved yet; run `feedscan scan --url <page> --no-cache` to fetch them")
				}
			} else {
				var err error
				if all, err = fetchLive(ctx, stderr, feedURLs, concurrency, timeout); err != nil {
					return err
				}
			}

			selected := report.SelectPosts(report.ExcludeHosts(all, exclude), time.Now(), days, limit)
			return report.WritePosts(cmd.Root().Writer, format, selected)
		},
	}
}

// fetchLive fetches the current posts of feedURLs, reporting progress and
// skipped feeds on stderr. A failed feed is skipped; an interrupted run, or
// one where no feed could be fetched at all, is an error.
func fetchLive(ctx context.Context, stderr io.Writer, feedURLs []string, concurrency int, timeout time.Duration) ([]checkpoint.Post, error) {
	if len(feedURLs) == 0 {
		fmt.Fprintln(stderr, "no feeds found by earlier scans; run `feedscan scan --url <page>` to find some")
		return nil, nil
	}
	cfg := &scanner.Config{
		Timeout:      timeout,
		Concurrency:  concurrency,
		UserAgent:    scanner.DefaultUserAgent,
		MaxBodyBytes: scanner.DefaultMaxBodyBytes,
		HostDelay:    scanner.DefaultHostDelay,
		MaxPosts:     math.MaxInt, // nothing is saved, so no per-site cap; --limit caps the list
	}
	fmt.Fprintf(stderr, "fetching %d feeds...\n", len(feedURLs))
	posts, failed := scanner.FetchPosts(ctx, scanner.NewHTTPClient(cfg), cfg, feedURLs)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Every feed failing is no network, not a thousand broken sites: an empty
	// list would pass for "nothing new".
	if failed == len(feedURLs) {
		return nil, fmt.Errorf("all %d feeds failed (offline?); use --cached for the posts saved by the last scan", failed)
	}
	if failed > 0 {
		fmt.Fprintf(stderr, "%d of %d feeds failed\n", failed, len(feedURLs))
	}
	return posts, nil
}
