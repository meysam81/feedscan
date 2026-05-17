// Package cursor exposes per-checkpoint reading-cursor subcommands:
// mark, unmark, unread, history, stats.
//
// All commands operate on a single checkpoint file (--checkpoint) and persist
// only the user's reading state — never the probe state. The scan command
// preserves any existing VisitedAt when re-probing, so the two flows compose
// safely. Mutations (mark/unmark) acquire an advisory file lock to prevent
// lost updates from concurrent terminal invocations.
package cursor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"feedscan/cmd/cliflags"
	"feedscan/internal/checkpoint"
	"feedscan/internal/report"
)

// state holds the flag destinations for a single CLI invocation. Scoped
// inside Command() so tests and embedders can build independent commands.
type state struct {
	CheckpointPath string
	Format         string
	Limit          int
}

// defaultLimit is shared by `unread` and `history` for UX symmetry.
const defaultLimit = 20

// parentFlagsHint is printed in each subcommand's help so users know how to
// pass the parent --checkpoint and --format flags. cli/v3 doesn't list
// parent flags in subcommand help by default.
const parentFlagsHint = `Pass --checkpoint and --format on the parent:
   feedscan cursor [--checkpoint <path>] [--format json|table] <subcommand> ...`

// Command returns the parent "cursor" subcommand.
//
// --checkpoint and --format are declared on the parent only. urfave/cli/v3
// applies parent flags to subcommands by default (Local defaults to false),
// so `feedscan cursor mark --help` shows them and `feedscan cursor
// --checkpoint X mark <url>` works without re-passing the flag.
func Command() *cli.Command {
	s := &state{}
	return &cli.Command{
		Name:    "cursor",
		Usage:   "Track which scanned URLs you've personally visited",
		Aliases: []string{"c"},
		Flags: []cli.Flag{
			cliflags.CheckpointFlag(&s.CheckpointPath, true, ""),
			cliflags.FormatFlag(&s.Format, "table"),
		},
		Commands: []*cli.Command{
			markCommand(s),
			unmarkCommand(s),
			unreadCommand(s),
			historyCommand(s),
			statsCommand(s),
		},
	}
}

func markCommand(s *state) *cli.Command {
	return &cli.Command{
		Name:        "mark",
		Usage:       "Mark a URL as visited (sets visited_at = now)",
		ArgsUsage:   "<url>",
		Description: parentFlagsHint,
		Action: func(_ context.Context, cmd *cli.Command) error {
			u, err := requireOneURL(cmd)
			if err != nil {
				return err
			}
			return mutateCheckpoint(s, u, func(cp *checkpoint.Checkpoint) error {
				return cp.Mark(u, time.Now().UTC())
			})
		},
	}
}

func unmarkCommand(s *state) *cli.Command {
	return &cli.Command{
		Name:        "unmark",
		Usage:       "Clear the visited timestamp for a URL",
		ArgsUsage:   "<url>",
		Description: parentFlagsHint,
		Action: func(_ context.Context, cmd *cli.Command) error {
			u, err := requireOneURL(cmd)
			if err != nil {
				return err
			}
			return mutateCheckpoint(s, u, func(cp *checkpoint.Checkpoint) error {
				return cp.Unmark(u)
			})
		},
	}
}

func unreadCommand(s *state) *cli.Command {
	return &cli.Command{
		Name:        "unread",
		Usage:       "List unvisited entries sorted by latest_post desc (replaces the jq | sort | head workflow)",
		Description: parentFlagsHint,
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:        "limit",
				Aliases:     []string{"n"},
				Usage:       "max entries (0 = all)",
				Value:       defaultLimit,
				Destination: &s.Limit,
			},
		},
		Action: func(_ context.Context, _ *cli.Command) error {
			cp, err := loadOrFail(s)
			if err != nil {
				return err
			}
			return emitResults(os.Stdout, s.Format, cp.Unvisited(s.Limit))
		},
	}
}

func historyCommand(s *state) *cli.Command {
	return &cli.Command{
		Name:        "history",
		Usage:       "List visited entries sorted by visited_at desc (most recent first)",
		Description: parentFlagsHint,
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:        "limit",
				Aliases:     []string{"n"},
				Usage:       "max entries (0 = all)",
				Value:       defaultLimit,
				Destination: &s.Limit,
			},
		},
		Action: func(_ context.Context, _ *cli.Command) error {
			cp, err := loadOrFail(s)
			if err != nil {
				return err
			}
			return emitResults(os.Stdout, s.Format, cp.History(s.Limit))
		},
	}
}

func statsCommand(s *state) *cli.Command {
	return &cli.Command{
		Name:        "stats",
		Usage:       "Print counts: total, visited, unvisited, and by-status breakdown",
		Description: parentFlagsHint,
		Action: func(_ context.Context, _ *cli.Command) error {
			cp, err := loadOrFail(s)
			if err != nil {
				return err
			}
			return emitStats(os.Stdout, s.Format, cp.Stats())
		},
	}
}

// requireOneURL extracts a single positional URL arg or returns a usage error.
func requireOneURL(cmd *cli.Command) (string, error) {
	args := cmd.Args().Slice()
	if len(args) != 1 {
		return "", errors.New("exactly one URL argument is required")
	}
	return args[0], nil
}

// loadOrFail resolves and loads the checkpoint for read-only commands.
func loadOrFail(s *state) (*checkpoint.Checkpoint, error) {
	path, err := resolvePath(s.CheckpointPath)
	if err != nil {
		return nil, err
	}
	cp, warn, err := checkpoint.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint: %w", err)
	}
	if warn != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warn)
	}
	return cp, nil
}

// mutateCheckpoint takes an exclusive file lock, loads, applies fn, and saves
// atomically. The lock serializes concurrent mark/unmark invocations so
// neither loses the other's update.
//
// targetURL identifies the URL the caller is mutating; it's only used to
// produce a clearer error message when the URL is missing.
func mutateCheckpoint(s *state, targetURL string, fn func(*checkpoint.Checkpoint) error) (err error) {
	path, err := resolvePath(s.CheckpointPath)
	if err != nil {
		return err
	}

	lock, err := checkpoint.AcquireLock(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := lock.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("release lock: %w", cerr))
		}
	}()

	cp, warn, err := checkpoint.Load(path)
	if err != nil {
		return fmt.Errorf("load checkpoint: %w", err)
	}
	if warn != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warn)
	}

	if err := fn(cp); err != nil {
		if errors.Is(err, checkpoint.ErrUnknownURL) {
			return fmt.Errorf("%w: %q (run a scan first to populate the checkpoint)", err, targetURL)
		}
		return err
	}
	return cp.Save(path)
}

func resolvePath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Abs(p)
}

func emitResults(w io.Writer, format string, results []checkpoint.Result) error {
	switch strings.ToLower(format) {
	case "json", "":
		return report.WriteJSON(w, results)
	case "table":
		return report.WriteTable(w, report.CursorColumns(), results)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}

func emitStats(w io.Writer, format string, s checkpoint.Stats) error {
	switch strings.ToLower(format) {
	case "json", "":
		return report.WriteJSON(w, s)
	case "table":
		if _, err := fmt.Fprintf(w, "Total:     %d\n", s.Total); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Visited:   %d\n", s.Visited); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Unvisited: %d\n", s.Unvisited); err != nil {
			return err
		}
		return report.WriteByStatus(w, s.ByStatus)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}
