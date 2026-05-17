package cursor

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"feedscan/internal/checkpoint"
)

// seedCheckpoint writes a 3-entry checkpoint to a temp path and returns it.
func seedCheckpoint(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")

	cp := checkpoint.New()
	now := time.Now().UTC().Truncate(time.Second)
	a := now.Add(-1 * time.Hour)
	b := now.Add(-30 * 24 * time.Hour)
	cp.Put(checkpoint.Result{
		URL: "https://a.example", Status: checkpoint.StatusActive,
		FeedURL: "https://a.example/feed", LatestPost: &a, CheckedAt: &now,
	})
	cp.Put(checkpoint.Result{
		URL: "https://b.example", Status: checkpoint.StatusStale,
		FeedURL: "https://b.example/rss", LatestPost: &b, CheckedAt: &now,
	})
	cp.Put(checkpoint.Result{
		URL: "https://c.example", Status: checkpoint.StatusNoFeed, CheckedAt: &now,
	})
	if err := cp.Save(path); err != nil {
		t.Fatalf("save seed: %v", err)
	}
	return path
}

// runCmd builds a fresh Command and executes it with args. Since flag dests
// live inside the closure, multiple invocations don't share state and tests
// can run with t.Parallel().
func runCmd(args ...string) error {
	cmd := Command()
	return cmd.Run(context.Background(), append([]string{"cursor"}, args...))
}

// markResult invokes mark by directly calling the checkpoint API. Used for
// fast test setup that doesn't need CLI parsing.
func markEntry(t *testing.T, path, u string, at time.Time) {
	t.Helper()
	cp, _, err := checkpoint.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cp.Mark(u, at); err != nil {
		t.Fatalf("mark %s: %v", u, err)
	}
	if err := cp.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// loadResults returns checkpoint entries via the public API, used to assert
// state without depending on stdout capture.
func loadResults(t *testing.T, path string) []checkpoint.Result {
	t.Helper()
	cp, _, err := checkpoint.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cp.Snapshot()
}

func TestMarkSetsVisitedAt(t *testing.T) {
	t.Parallel()
	path := seedCheckpoint(t)

	if err := runCmd("--checkpoint", path, "mark", "https://a.example"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	results := loadResults(t, path)
	var visited int
	for _, r := range results {
		if r.URL == "https://a.example" && r.VisitedAt != nil {
			visited++
		}
	}
	if visited != 1 {
		t.Fatalf("expected a.example visited, snapshot: %+v", results)
	}
}

func TestUnmarkClearsVisitedAt(t *testing.T) {
	t.Parallel()
	path := seedCheckpoint(t)
	markEntry(t, path, "https://a.example", time.Now().UTC())

	if err := runCmd("--checkpoint", path, "unmark", "https://a.example"); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	for _, r := range loadResults(t, path) {
		if r.URL == "https://a.example" && r.VisitedAt != nil {
			t.Fatalf("expected a.example unmarked: %+v", r)
		}
	}
}

func TestMarkUnknownURLErrors(t *testing.T) {
	t.Parallel()
	path := seedCheckpoint(t)
	err := runCmd("--checkpoint", path, "mark", "https://missing.example")
	if err == nil {
		t.Fatal("expected error for unknown URL")
	}
	if !strings.Contains(err.Error(), "url not found") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "missing.example") {
		t.Fatalf("error should mention the URL, got: %v", err)
	}
}

func TestEmitResultsJSON(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	in := []checkpoint.Result{
		{URL: "https://a", Status: checkpoint.StatusActive, LatestPost: &now},
		{URL: "https://b", Status: checkpoint.StatusStale},
	}
	var buf bytes.Buffer
	if err := emitResults(&buf, "json", in); err != nil {
		t.Fatalf("emit json: %v", err)
	}
	var got []checkpoint.Result
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if len(got) != 2 || got[0].URL != "https://a" {
		t.Fatalf("unexpected results: %+v", got)
	}
}

func TestEmitResultsTable(t *testing.T) {
	t.Parallel()
	in := []checkpoint.Result{{URL: "https://a", Status: checkpoint.StatusActive}}
	var buf bytes.Buffer
	if err := emitResults(&buf, "table", in); err != nil {
		t.Fatalf("emit table: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "URL") || !strings.Contains(out, "STATUS") {
		t.Fatalf("table header missing: %s", out)
	}
	if !strings.Contains(out, "https://a") {
		t.Fatalf("row missing: %s", out)
	}
}

func TestEmitStatsJSON(t *testing.T) {
	t.Parallel()
	s := checkpoint.Stats{Total: 5, Visited: 2, Unvisited: 3, ByStatus: map[checkpoint.Status]int{checkpoint.StatusActive: 5}}
	var buf bytes.Buffer
	if err := emitStats(&buf, "json", s); err != nil {
		t.Fatalf("emit stats: %v", err)
	}
	var got checkpoint.Stats
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if got.Total != 5 || got.ByStatus[checkpoint.StatusActive] != 5 {
		t.Fatalf("stats: %+v", got)
	}
}

func TestEmitStatsTable(t *testing.T) {
	t.Parallel()
	s := checkpoint.Stats{Total: 5, Visited: 2, Unvisited: 3, ByStatus: map[checkpoint.Status]int{checkpoint.StatusActive: 5}}
	var buf bytes.Buffer
	if err := emitStats(&buf, "table", s); err != nil {
		t.Fatalf("emit stats: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Total:     5", "Visited:   2", "Unvisited: 3", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in: %s", want, out)
		}
	}
}

func TestUnknownFormatErrors(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := emitResults(&buf, "yaml", nil); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestConcurrentMarksDoNotLoseUpdates(t *testing.T) {
	// Regression test for C3 (lost-update race). Two concurrent mark
	// invocations against the same checkpoint must both persist.
	t.Parallel()
	path := seedCheckpoint(t)

	done := make(chan error, 2)
	go func() {
		done <- runCmd("--checkpoint", path, "mark", "https://a.example")
	}()
	go func() {
		done <- runCmd("--checkpoint", path, "mark", "https://b.example")
	}()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent mark: %v", err)
		}
	}

	visited := map[string]bool{}
	for _, r := range loadResults(t, path) {
		if r.VisitedAt != nil {
			visited[r.URL] = true
		}
	}
	if !visited["https://a.example"] || !visited["https://b.example"] {
		t.Fatalf("lost update: %+v", visited)
	}
}
