package scanner

import (
	"context"
	"fmt"
	"os"
	"time"

	"feedscan/internal/checkpoint"
)

// PipelineResult is the structured return from Run. Callers (typically
// cmd/scan) decide whether to format probe results or the dry-run URL list.
type PipelineResult struct {
	URLs    []string            // all extracted URLs (also populated in dry-run)
	Results []checkpoint.Result // populated only when !DryRun
	DryRun  bool
}

// Run executes the scan pipeline: extract → load checkpoint → probe → persist
// → apply freshness. Output formatting is the caller's responsibility — Run
// returns the data structures the caller needs to emit.
func Run(ctx context.Context, cfg *Config) (*PipelineResult, error) {
	client := NewHTTPClient(cfg)

	urls, err := ExtractExternalURLs(ctx, client, cfg)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	if cfg.MaxURLs > 0 && len(urls) > cfg.MaxURLs {
		fmt.Fprintf(os.Stderr, "capping to %d URLs (of %d extracted; -max-urls)\n", cfg.MaxURLs, len(urls))
		urls = urls[:cfg.MaxURLs]
	}

	if cfg.DryRun {
		fmt.Fprintf(os.Stderr, "found %d external URLs (dry run; remove --dry-run to probe feeds)\n", len(urls))
		return &PipelineResult{URLs: urls, DryRun: true}, nil
	}

	cp, warn, err := checkpoint.Load(cfg.CheckpointPath)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint: %w", err)
	}
	if warn != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warn)
	}

	now := time.Now()
	var todo []string
	results := make(map[string]checkpoint.Result, len(urls))

	for _, u := range urls {
		if !cfg.NoCache {
			if r, ok := cp.Get(u, now, cfg.CacheTTL); ok {
				results[u] = r
				continue
			}
		}
		todo = append(todo, u)
	}

	fmt.Fprintf(os.Stderr, "extracted=%d cached=%d todo=%d\n", len(urls), len(results), len(todo))

	if len(todo) > 0 {
		probed := ProbeAll(ctx, client, cfg, todo, func(batch []checkpoint.Result) {
			for _, r := range batch {
				cp.Put(r)
			}
			if err := cp.Save(cfg.CheckpointPath); err != nil {
				fmt.Fprintf(os.Stderr, "warning: checkpoint save failed: %v\n", err)
			}
		})
		for _, r := range probed {
			results[r.URL] = r
		}
	}

	out := make([]checkpoint.Result, 0, len(results))
	for _, u := range urls {
		out = append(out, results[u])
	}
	cutoff := now.Add(-time.Duration(cfg.WindowDays) * 24 * time.Hour)
	for i := range out {
		out[i] = ApplyFreshness(out[i], cutoff)
	}
	return &PipelineResult{URLs: urls, Results: out}, nil
}
