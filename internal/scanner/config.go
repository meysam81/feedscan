// Package scanner owns the scan pipeline: extract external URLs, probe each
// for a feed, and update the checkpoint. It does NOT format or emit output —
// that's the caller's job (typically cmd/scan).
package scanner

import (
	"errors"
	"fmt"
	"time"
)

// Config holds pipeline tunables. CLI-only concerns (sort key, output format,
// verbosity) live in cmd/scan — they're not pipeline inputs.
type Config struct {
	InputURL       string
	WindowDays     int
	Timeout        time.Duration
	Concurrency    int
	BatchSize      int
	CacheTTL       time.Duration
	CheckpointPath string
	UserAgent      string
	MaxBodyBytes   int64
	HostDelay      time.Duration
	DryRun         bool
	NoCache        bool
	MaxURLs        int // cap on URLs to process; 0 = unlimited
}

// Validate accumulates and returns all range/value errors at once so the CLI
// can show every problem in one shot.
func (c *Config) Validate() error {
	var errs []error
	if c.InputURL == "" {
		errs = append(errs, errors.New("input URL is required"))
	}
	if c.Concurrency < 1 {
		errs = append(errs, errors.New("concurrency must be >= 1"))
	}
	if c.BatchSize < 1 {
		errs = append(errs, errors.New("batch must be >= 1"))
	}
	if c.WindowDays < 1 {
		errs = append(errs, errors.New("days must be >= 1"))
	}
	if c.MaxURLs < 0 {
		errs = append(errs, errors.New("max-urls must be >= 0"))
	}
	if c.Timeout <= 0 {
		errs = append(errs, errors.New("timeout must be > 0"))
	}
	if c.CacheTTL <= 0 {
		errs = append(errs, errors.New("cache-ttl must be > 0"))
	}
	if c.MaxBodyBytes <= 0 {
		errs = append(errs, fmt.Errorf("max-body must be > 0 (got %d)", c.MaxBodyBytes))
	}
	if c.HostDelay < 0 {
		errs = append(errs, errors.New("host-delay must be >= 0"))
	}
	return errors.Join(errs...)
}
