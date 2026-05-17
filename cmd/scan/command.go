// Package scan exposes the top-level feedscan scan subcommand (the default).
//
// Output formatting lives here, not in internal/scanner — the pipeline
// produces data structures; this Action turns them into bytes.
package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/net/publicsuffix"

	"feedscan/cmd/cliflags"
	"feedscan/internal/report"
	"feedscan/internal/scanner"
)

// presentation collects CLI-only output knobs that don't belong on the
// pipeline Config.
type presentation struct {
	SortBy    string
	SortOrder string
	Format    string
	Verbose   bool
}

// Command returns the "scan" subcommand. Flag destinations are scoped inside
// this constructor — no package-level mutable state.
func Command() *cli.Command {
	cfg := &scanner.Config{}
	pres := &presentation{}

	return &cli.Command{
		Name:  "scan",
		Usage: "Fetch a URL, extract external links, probe each for an active RSS/Atom feed",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "url",
				Usage:       "input URL to scan (required)",
				Sources:     cli.EnvVars("FEEDSCAN_URL"),
				Destination: &cfg.InputURL,
			},
			&cli.IntFlag{
				Name:        "days",
				Usage:       "freshness window in days",
				Value:       7,
				Sources:     cli.EnvVars("FEEDSCAN_DAYS"),
				Destination: &cfg.WindowDays,
			},
			&cli.DurationFlag{
				Name:        "timeout",
				Usage:       "per-request HTTP timeout",
				Value:       10 * time.Second,
				Sources:     cli.EnvVars("FEEDSCAN_TIMEOUT"),
				Destination: &cfg.Timeout,
			},
			&cli.IntFlag{
				Name:        "concurrency",
				Usage:       "max concurrent workers",
				Value:       8,
				Sources:     cli.EnvVars("FEEDSCAN_CONCURRENCY"),
				Destination: &cfg.Concurrency,
			},
			&cli.IntFlag{
				Name:        "batch",
				Usage:       "URLs per batch",
				Value:       32,
				Sources:     cli.EnvVars("FEEDSCAN_BATCH"),
				Destination: &cfg.BatchSize,
			},
			&cli.DurationFlag{
				Name:        "cache-ttl",
				Usage:       "checkpoint TTL",
				Value:       24 * time.Hour,
				Sources:     cli.EnvVars("FEEDSCAN_CACHE_TTL"),
				Destination: &cfg.CacheTTL,
			},
			cliflags.CheckpointFlag(&cfg.CheckpointPath, false, "feedscan.checkpoint.json"),
			&cli.StringFlag{
				Name:        "user-agent",
				Usage:       "HTTP User-Agent",
				Value:       "feedscan/1.0",
				Sources:     cli.EnvVars("FEEDSCAN_USER_AGENT"),
				Destination: &cfg.UserAgent,
			},
			&cli.Int64Flag{
				Name:        "max-body",
				Usage:       "max response body bytes",
				Value:       10 << 20,
				Sources:     cli.EnvVars("FEEDSCAN_MAX_BODY"),
				Destination: &cfg.MaxBodyBytes,
			},
			&cli.DurationFlag{
				Name:        "host-delay",
				Usage:       "min delay between requests to same host",
				Value:       500 * time.Millisecond,
				Sources:     cli.EnvVars("FEEDSCAN_HOST_DELAY"),
				Destination: &cfg.HostDelay,
			},
			&cli.BoolFlag{
				Name:        "dry-run",
				Usage:       "extract URLs but skip feed probing",
				Sources:     cli.EnvVars("FEEDSCAN_DRY_RUN"),
				Destination: &cfg.DryRun,
			},
			&cli.BoolFlag{
				Name:        "no-cache",
				Usage:       "ignore checkpoint cache",
				Sources:     cli.EnvVars("FEEDSCAN_NO_CACHE"),
				Destination: &cfg.NoCache,
			},
			&cli.BoolFlag{
				Name:        "verbose",
				Usage:       "print all URLs/results (default: aggregated summary)",
				Sources:     cli.EnvVars("FEEDSCAN_VERBOSE"),
				Destination: &pres.Verbose,
			},
			&cli.IntFlag{
				Name:        "max-urls",
				Usage:       "cap on extracted URLs to process (0 = unlimited)",
				Sources:     cli.EnvVars("FEEDSCAN_MAX_URLS"),
				Destination: &cfg.MaxURLs,
			},
			&cli.StringFlag{
				Name:        "sort-by",
				Usage:       "sort field: " + strings.Join(report.SortFields(), "|"),
				Value:       "latest_post",
				Sources:     cli.EnvVars("FEEDSCAN_SORT_BY"),
				Destination: &pres.SortBy,
			},
			&cli.StringFlag{
				Name:        "sort-order",
				Usage:       "sort order: asc|desc",
				Value:       "desc",
				Sources:     cli.EnvVars("FEEDSCAN_SORT_ORDER"),
				Destination: &pres.SortOrder,
			},
			cliflags.FormatFlag(&pres.Format, "json"),
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			if err := normalize(cfg, pres); err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := validatePresentation(pres); err != nil {
				return err
			}

			pr, err := scanner.Run(ctx, cfg)
			if err != nil {
				return err
			}
			return emit(os.Stdout, pres, pr)
		},
	}
}

// normalize mutates cfg/pres for CLI conveniences (scheme default, absolute
// checkpoint path) before validation.
func normalize(c *scanner.Config, _ *presentation) error {
	if c.InputURL != "" && !strings.Contains(c.InputURL, "://") {
		c.InputURL = "https://" + c.InputURL
	}
	if !filepath.IsAbs(c.CheckpointPath) {
		abs, err := filepath.Abs(c.CheckpointPath)
		if err != nil {
			return fmt.Errorf("resolve checkpoint path: %w", err)
		}
		c.CheckpointPath = abs
	}
	return nil
}

// validatePresentation checks CLI-only knobs that scanner.Config doesn't know
// about.
func validatePresentation(p *presentation) error {
	var errs []error
	if _, ok := report.SortLessFns[p.SortBy]; !ok {
		errs = append(errs, fmt.Errorf("--sort-by must be one of %s (got %q)", strings.Join(report.SortFields(), ", "), p.SortBy))
	}
	if p.SortOrder != "asc" && p.SortOrder != "desc" {
		errs = append(errs, fmt.Errorf("--sort-order must be asc or desc (got %q)", p.SortOrder))
	}
	switch p.Format {
	case "json", "table":
	default:
		errs = append(errs, fmt.Errorf("--format must be json or table (got %q)", p.Format))
	}
	return errors.Join(errs...)
}

// emit renders the pipeline result. Dry-run gets domain-aggregated output;
// scan gets sorted-and-summarized output.
func emit(w *os.File, pres *presentation, pr *scanner.PipelineResult) error {
	if pr.DryRun {
		if pres.Verbose {
			return report.EmitResults(w, pres.Format, report.URLsAsResults(pr.URLs))
		}
		return report.WriteDryRunSummary(w, pres.Format, report.SummarizeDryRun(pr.URLs, publicsuffix.EffectiveTLDPlusOne))
	}
	report.Sort(pr.Results, pres.SortBy, pres.SortOrder)
	if pres.Verbose {
		return report.EmitResults(w, pres.Format, pr.Results)
	}
	return report.WriteProbeSummary(w, pres.Format, report.SummarizeProbe(pr.Results))
}
