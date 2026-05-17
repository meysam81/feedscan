// Package checkpoint owns the per-target persistence file: probe results keyed
// by URL plus the per-URL reading cursor (VisitedAt).
//
// Probe state (Status, FeedURL, LatestPost, CheckedAt) is set by the scanner.
// Reading state (VisitedAt) is set by the cursor subcommands. They live in the
// same file because they describe the same URL — one file per scan target.
package checkpoint

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	json "github.com/goccy/go-json"
)

// Status describes the outcome of probing a single external URL.
type Status string

const (
	StatusActive Status = "active"  // feed found, fresh entry within window
	StatusStale  Status = "stale"   // feed found, no fresh entry within window
	StatusNoFeed Status = "no_feed" // probed all candidates, no feed found
	StatusError  Status = "error"   // network or parse error
)

// AllStatuses returns Status values in canonical display order. Callers
// rendering by-status tables iterate via this so adding a new status only
// requires updating one slice.
func AllStatuses() []Status {
	return []Status{StatusActive, StatusStale, StatusNoFeed, StatusError}
}

// Result is the per-URL outcome plus reading state.
//
// Time fields are pointers so encoding/json's omitempty correctly drops them
// when zero — time.Time{} is a struct and would otherwise serialize as
// "0001-01-01T00:00:00Z".
type Result struct {
	URL        string     `json:"url"`
	Status     Status     `json:"status,omitempty"`
	FeedURL    string     `json:"feed_url,omitempty"`
	LatestPost *time.Time `json:"latest_post,omitempty"`
	ItemCount  int        `json:"item_count,omitempty"`
	Error      string     `json:"error,omitempty"`
	CheckedAt  *time.Time `json:"checked_at,omitempty"`
	VisitedAt  *time.Time `json:"visited_at,omitempty"`
}

// Checkpoint is the on-disk JSON document.
type Checkpoint struct {
	mu      sync.Mutex
	Entries map[string]Result `json:"entries"`
}

// ErrUnknownURL is returned by Mark/Unmark when the URL is not in the
// checkpoint. The cursor commands operate only on URLs we've already scanned —
// otherwise a typo would silently grow phantom entries.
var ErrUnknownURL = errors.New("url not found in checkpoint")

// New returns an empty checkpoint.
func New() *Checkpoint {
	return &Checkpoint{Entries: make(map[string]Result)}
}

// Load reads a checkpoint file. Missing file → empty checkpoint (not an error).
// Corrupt file is reported via the returned warning string; the checkpoint is
// reset to empty so a bad file doesn't crash the run.
func Load(path string) (cp *Checkpoint, warn string, err error) {
	cp = New()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cp, "", nil
		}
		return nil, "", err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close checkpoint: %w", cerr))
		}
	}()

	if derr := json.NewDecoder(f).Decode(cp); derr != nil && !errors.Is(derr, io.EOF) {
		cp = New()
		warn = fmt.Sprintf("checkpoint unreadable (%v); starting fresh", derr)
		return
	}
	if cp.Entries == nil {
		cp.Entries = make(map[string]Result)
	}
	return
}

// Get returns a cached probe result for u iff it's still fresh (within ttl).
// Reading state is ignored — TTL only governs re-probing.
func (c *Checkpoint) Get(u string, now time.Time, ttl time.Duration) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.Entries[u]
	if !ok {
		return Result{}, false
	}
	if r.CheckedAt == nil || now.Sub(*r.CheckedAt) > ttl {
		return Result{}, false
	}
	return r, true
}

// Put stores or replaces a probe result. Preserves any existing VisitedAt so
// re-scanning never clears the user's reading history.
func (c *Checkpoint) Put(r Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.Entries[r.URL]; ok && r.VisitedAt == nil {
		r.VisitedAt = prev.VisitedAt
	}
	c.Entries[r.URL] = r
}

// Mark stamps an entry's VisitedAt. Returns ErrUnknownURL if u isn't tracked.
func (c *Checkpoint) Mark(u string, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.Entries[u]
	if !ok {
		return ErrUnknownURL
	}
	t := at
	r.VisitedAt = &t
	c.Entries[u] = r
	return nil
}

// Unmark clears an entry's VisitedAt. Returns ErrUnknownURL if u isn't tracked.
func (c *Checkpoint) Unmark(u string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.Entries[u]
	if !ok {
		return ErrUnknownURL
	}
	r.VisitedAt = nil
	c.Entries[u] = r
	return nil
}

// Snapshot returns a copy of all entries as a slice. The caller may sort/filter
// freely without holding the checkpoint lock.
func (c *Checkpoint) Snapshot() []Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Result, 0, len(c.Entries))
	for _, r := range c.Entries {
		out = append(out, r)
	}
	return out
}

// Unvisited returns entries with no VisitedAt, sorted by LatestPost descending
// (matches the user's existing jq workflow). Entries lacking LatestPost sort
// last. If limit > 0, the result is capped.
func (c *Checkpoint) Unvisited(limit int) []Result {
	out := filter(c.Snapshot(), func(r Result) bool {
		return r.VisitedAt == nil && r.LatestPost != nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LatestPost.After(*out[j].LatestPost)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// History returns visited entries sorted by VisitedAt descending (most recent
// first). If limit > 0, the result is capped.
func (c *Checkpoint) History(limit int) []Result {
	out := filter(c.Snapshot(), func(r Result) bool {
		return r.VisitedAt != nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].VisitedAt.After(*out[j].VisitedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Stats is a counts breakdown for the stats subcommand.
type Stats struct {
	Total     int            `json:"total"`
	Visited   int            `json:"visited"`
	Unvisited int            `json:"unvisited"`
	ByStatus  map[Status]int `json:"by_status"`
}

// Stats computes counts across the checkpoint.
func (c *Checkpoint) Stats() Stats {
	entries := c.Snapshot()
	s := Stats{Total: len(entries), ByStatus: make(map[Status]int)}
	for _, r := range entries {
		s.ByStatus[r.Status]++
		if r.VisitedAt != nil {
			s.Visited++
		} else {
			s.Unvisited++
		}
	}
	return s
}

// Save writes atomically: write to temp file then rename. Creates parent dir if
// missing.
func (c *Checkpoint) Save(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".feedscan.checkpoint.*.tmp")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return errors.Join(err, tmp.Close(), os.Remove(tmp.Name()))
	}
	if err := tmp.Close(); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return nil
}

func filter(in []Result, keep func(Result) bool) []Result {
	out := make([]Result, 0, len(in))
	for _, r := range in {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}
