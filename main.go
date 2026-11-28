// Command feedscan lists the newest blog posts published by the sites linked
// from a page. The "scan" subcommand fetches a URL, extracts external HTTPS
// links, probes each for an RSS/Atom feed, and remembers the feeds; the
// default "posts" subcommand lists the feeds' posts newest first, fetching each
// feed at most once a day (--refresh fetches now, --cached never does). A
// per-checkpoint reading cursor (mark, unmark, unread, history, stats) lives
// under the "cursor" subcommand.
//
// Design notes:
//   - Checkpoints are keyed by external URL (not by run), so unrelated runs
//     that share targets reuse cached probe results until TTL expires.
//   - "External" means a different registrable domain than the input URL.
//   - Reading state (visited_at) is stored alongside probe state in the same
//     file; re-scanning preserves it.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"

	"feedscan/cmd/cursor"
	"feedscan/cmd/posts"
	"feedscan/cmd/scan"
)

// Build metadata, populated by goreleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// newRoot builds the feedscan command tree. With no subcommand feedscan lists
// posts: the posts flags and action are mounted on the root itself, because
// cli's DefaultCommand hands the default no flags (`feedscan -n 5` would fail).
func newRoot() *cli.Command {
	list := posts.Command()
	return &cli.Command{
		Name:                  "feedscan",
		Usage:                 "List the newest blog posts from the sites a page links to",
		Version:               version,
		EnableShellCompletion: true,
		Suggest:               true,
		Flags:                 list.Flags,
		Action:                list.Action,
		Commands: []*cli.Command{
			posts.Command(),
			scan.Command(),
			cursor.Command(),
		},
	}
}

func main() {
	cli.VersionPrinter = func(_ *cli.Command) {
		fmt.Printf("feedscan %s (commit %s, built %s)\n", version, commit, date)
	}

	cmd := newRoot()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "feedscan: %v\n", err)
		cancel()
		os.Exit(1)
	}
	cancel()
}
