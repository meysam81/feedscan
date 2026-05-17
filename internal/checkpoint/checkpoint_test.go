package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ptrTime(t time.Time) *time.Time { return &t }

func seed(t *testing.T) *Checkpoint {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	cp := New()
	cp.Put(Result{
		URL:        "https://a.example",
		Status:     StatusActive,
		FeedURL:    "https://a.example/feed",
		LatestPost: ptrTime(now.Add(-1 * time.Hour)),
		CheckedAt:  ptrTime(now),
	})
	cp.Put(Result{
		URL:        "https://b.example",
		Status:     StatusStale,
		FeedURL:    "https://b.example/rss",
		LatestPost: ptrTime(now.Add(-30 * 24 * time.Hour)),
		CheckedAt:  ptrTime(now),
	})
	cp.Put(Result{
		URL:       "https://c.example",
		Status:    StatusNoFeed,
		CheckedAt: ptrTime(now),
	})
	return cp
}

func TestSaveLoadRoundTrip(t *testing.T) {
	cp := seed(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")

	if err := cp.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, warn, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if warn != "" {
		t.Fatalf("unexpected warn: %s", warn)
	}
	if got, want := len(loaded.Entries), len(cp.Entries); got != want {
		t.Fatalf("entries: got %d want %d", got, want)
	}
	if loaded.Entries["https://a.example"].FeedURL != "https://a.example/feed" {
		t.Fatalf("feed url not preserved: %+v", loaded.Entries["https://a.example"])
	}
}

func TestLoadMissingFile(t *testing.T) {
	cp, warn, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if warn != "" {
		t.Fatalf("missing file should not warn, got %q", warn)
	}
	if cp == nil || cp.Entries == nil {
		t.Fatal("expected empty checkpoint")
	}
}

func TestLoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")
	// Write garbage.
	if err := (&Checkpoint{}).Save(path); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	// Overwrite with bad content using a fresh write.
	cp := New()
	if err := cp.Save(path); err != nil {
		t.Fatalf("save empty: %v", err)
	}
	// Truncate-and-write garbage:
	if err := writeFile(path, []byte("{not json")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	got, warn, err := Load(path)
	if err != nil {
		t.Fatalf("load corrupt: %v", err)
	}
	if warn == "" {
		t.Fatal("expected warning for corrupt file")
	}
	if len(got.Entries) != 0 {
		t.Fatalf("expected empty entries after corrupt load, got %d", len(got.Entries))
	}
}

func TestMarkUnknownURL(t *testing.T) {
	cp := seed(t)
	err := cp.Mark("https://missing.example", time.Now())
	if !errors.Is(err, ErrUnknownURL) {
		t.Fatalf("got %v, want ErrUnknownURL", err)
	}
}

func TestUnmarkUnknownURL(t *testing.T) {
	cp := seed(t)
	err := cp.Unmark("https://missing.example")
	if !errors.Is(err, ErrUnknownURL) {
		t.Fatalf("got %v, want ErrUnknownURL", err)
	}
}

func TestMarkAndUnmark(t *testing.T) {
	cp := seed(t)
	at := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	if err := cp.Mark("https://a.example", at); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got := cp.Entries["https://a.example"]
	if got.VisitedAt == nil || !got.VisitedAt.Equal(at) {
		t.Fatalf("visited_at not set: %+v", got.VisitedAt)
	}

	if err := cp.Unmark("https://a.example"); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	if cp.Entries["https://a.example"].VisitedAt != nil {
		t.Fatal("expected visited_at to be cleared")
	}
}

func TestPutPreservesVisitedAt(t *testing.T) {
	cp := seed(t)
	at := time.Now().UTC().Truncate(time.Second)
	if err := cp.Mark("https://a.example", at); err != nil {
		t.Fatalf("mark: %v", err)
	}

	// Simulate a re-probe overwriting probe state without touching visited_at.
	cp.Put(Result{
		URL:       "https://a.example",
		Status:    StatusStale,
		CheckedAt: ptrTime(time.Now()),
	})
	got := cp.Entries["https://a.example"]
	if got.VisitedAt == nil || !got.VisitedAt.Equal(at) {
		t.Fatalf("re-probe wiped visited_at: %+v", got.VisitedAt)
	}
	if got.Status != StatusStale {
		t.Fatalf("status not updated by re-probe: %s", got.Status)
	}
}

func TestUnvisitedSortAndLimit(t *testing.T) {
	cp := seed(t)
	// Mark b as visited, c has no LatestPost so it should be excluded.
	if err := cp.Mark("https://b.example", time.Now()); err != nil {
		t.Fatalf("mark: %v", err)
	}

	got := cp.Unvisited(0)
	if len(got) != 1 {
		t.Fatalf("unvisited count: got %d want 1 (b is visited, c has no latest_post)", len(got))
	}
	if got[0].URL != "https://a.example" {
		t.Fatalf("unvisited[0]: got %s", got[0].URL)
	}

	// Add another unvisited entry with an older latest_post.
	cp.Put(Result{
		URL:        "https://d.example",
		Status:     StatusStale,
		LatestPost: ptrTime(time.Now().Add(-2 * time.Hour)),
		CheckedAt:  ptrTime(time.Now()),
	})
	got = cp.Unvisited(0)
	if len(got) != 2 || got[0].URL != "https://a.example" || got[1].URL != "https://d.example" {
		t.Fatalf("expected a,d in that order, got %+v", got)
	}

	limited := cp.Unvisited(1)
	if len(limited) != 1 || limited[0].URL != "https://a.example" {
		t.Fatalf("limit=1: got %+v", limited)
	}
}

func TestHistorySortAndLimit(t *testing.T) {
	cp := seed(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := cp.Mark("https://a.example", now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("mark a: %v", err)
	}
	if err := cp.Mark("https://b.example", now); err != nil {
		t.Fatalf("mark b: %v", err)
	}

	h := cp.History(0)
	if len(h) != 2 || h[0].URL != "https://b.example" {
		t.Fatalf("expected b first (most recent), got %+v", h)
	}

	if h := cp.History(1); len(h) != 1 || h[0].URL != "https://b.example" {
		t.Fatalf("limit=1: got %+v", h)
	}
}

func TestStats(t *testing.T) {
	cp := seed(t)
	if err := cp.Mark("https://a.example", time.Now()); err != nil {
		t.Fatalf("mark: %v", err)
	}
	s := cp.Stats()
	if s.Total != 3 {
		t.Errorf("total: got %d want 3", s.Total)
	}
	if s.Visited != 1 {
		t.Errorf("visited: got %d want 1", s.Visited)
	}
	if s.Unvisited != 2 {
		t.Errorf("unvisited: got %d want 2", s.Unvisited)
	}
	if s.ByStatus[StatusActive] != 1 || s.ByStatus[StatusStale] != 1 || s.ByStatus[StatusNoFeed] != 1 {
		t.Errorf("byStatus: %+v", s.ByStatus)
	}
}

func TestGetTTL(t *testing.T) {
	cp := New()
	now := time.Now()
	cp.Put(Result{URL: "https://x", Status: StatusActive, CheckedAt: &now})

	if _, ok := cp.Get("https://x", now, time.Minute); !ok {
		t.Fatal("fresh entry should hit")
	}
	if _, ok := cp.Get("https://x", now.Add(2*time.Minute), time.Minute); ok {
		t.Fatal("stale entry should miss")
	}
	if _, ok := cp.Get("https://missing", now, time.Minute); ok {
		t.Fatal("missing entry should miss")
	}
}

func writeFile(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}
