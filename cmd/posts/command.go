// Package posts exposes the feedscan posts subcommand: a newest-first list of
// the blog posts found by earlier scans. main also mounts its flags and action
// on the root command, so a bare `feedscan` runs it.
//
// It only reads checkpoint files — no network, no writes. Run `feedscan scan`
// to fetch or refresh what it shows.
package posts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v3"

	"feedscan/cmd/cliflags"
	"feedscan/internal/checkpoint"
	"feedscan/internal/report"
)

// checkpointGlob matches the files `feedscan scan` writes by default.
const checkpointGlob = "*.checkpoint.json"

// Command returns the "posts" subcommand. Flag destinations are scoped inside
// this constructor — no package-level mutable state.
//
// Every flag is Local: when mounted on the root command they must not be
// inherited by scan and cursor, which define --checkpoint, --days and --format
// with their own meanings.
func Command() *cli.Command {
	var (
		paths   []string
		exclude []string
		format  string
		limit   int
		days    int
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
			formatFlag,
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return fmt.Errorf("unknown command %q (see --help)", cmd.Args().First())
			}
			if limit < 0 || days < 0 {
				return errors.New("--limit and --days must be >= 0")
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

			var all []checkpoint.Post
			for _, path := range paths {
				cp, warn, err := checkpoint.Load(path)
				if err != nil {
					return fmt.Errorf("load checkpoint: %w", err)
				}
				if warn != "" {
					fmt.Fprintf(os.Stderr, "warning: %s: %s\n", path, warn)
				}
				all = append(all, cp.AllPosts()...)
			}

			selected := report.SelectPosts(report.ExcludeHosts(all, exclude), time.Now(), days, limit)
			if len(all) == 0 {
				fmt.Fprintln(os.Stderr, "no posts saved yet; run `feedscan scan --url <page> --no-cache` to fetch them")
			}
			return report.WritePosts(cmd.Root().Writer, format, selected)
		},
	}
}
