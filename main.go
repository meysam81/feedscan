// Command feedscan fetches a URL, extracts external HTTPS links, probes each
// for an RSS/Atom feed, and reports which feeds have been active within a
// configurable freshness window. A per-checkpoint reading cursor (mark,
// unmark, unread, history, stats) lives under the "cursor" subcommand.
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
	"feedscan/cmd/scan"
)

// Build metadata, populated by goreleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli.VersionPrinter = func(_ *cli.Command) {
		fmt.Printf("feedscan %s (commit %s, built %s)\n", version, commit, date)
	}

	cmd := &cli.Command{
		Name:                  "feedscan",
		Usage:                 "Scan a page for external sites with active RSS/Atom feeds",
		Version:               version,
		EnableShellCompletion: true,
		Suggest:               true,
		DefaultCommand:        "scan",
		Commands: []*cli.Command{
			scan.Command(),
			cursor.Command(),
		},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "feedscan: %v\n", err)
		cancel()
		os.Exit(1)
	}
	cancel()
}
