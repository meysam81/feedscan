package posts

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"feedscan/internal/checkpoint"
)

// writeCheckpoint saves a checkpoint with one site holding the given posts.
func writeCheckpoint(t *testing.T, path, site string, posts ...checkpoint.Post) {
	t.Helper()
	now := time.Now()
	cp := checkpoint.New()
	cp.Put(checkpoint.Result{URL: site, Status: checkpoint.StatusActive, CheckedAt: &now, Posts: posts})
	if err := cp.Save(path); err != nil {
		t.Fatalf("save %s: %v", path, err)
	}
}

// run executes a fresh posts command and returns what it printed to stdout.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := Command()
	cmd.Writer = &out
	err := cmd.Run(context.Background(), append([]string{"posts"}, args...))
	return out.String(), err
}

// rows returns the table's data rows (header and separator dropped).
func rows(t *testing.T, out string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("no table in output:\n%s", out)
	}
	return lines[2:]
}

func seedTwo(t *testing.T, dir string) (a, b string) {
	t.Helper()
	now := time.Now()
	shared := checkpoint.Post{Title: "Shared", URL: "https://shared.example/post", Date: now.Add(-48 * time.Hour)}
	a = filepath.Join(dir, "a.checkpoint.json")
	b = filepath.Join(dir, "b.checkpoint.json")
	writeCheckpoint(t, a, "https://shared.example", shared,
		checkpoint.Post{Title: "Oldest", URL: "https://a.example/oldest", Date: now.Add(-30 * 24 * time.Hour)})
	writeCheckpoint(t, b, "https://shared.example", shared,
		checkpoint.Post{Title: "Newest", URL: "https://b.example/newest", Date: now.Add(-time.Hour)})
	return a, b
}

func TestMergesCheckpointsNewestFirst(t *testing.T) {
	a, b := seedTwo(t, t.TempDir())

	out, err := run(t, "--checkpoint", a, "--checkpoint", b)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := rows(t, out)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 (shared post listed once):\n%s", len(got), out)
	}
	for i, title := range []string{"Newest", "Shared", "Oldest"} {
		if !strings.HasPrefix(got[i], title) {
			t.Errorf("row %d: got %q, want title %q", i, got[i], title)
		}
	}

	out, err = run(t, "--checkpoint", a, "--checkpoint", b, "-n", "1", "--days", "7")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rows(t, out); len(got) != 1 || !strings.HasPrefix(got[0], "Newest") {
		t.Errorf("-n 1 --days 7: got %q", got)
	}
}

func TestDefaultsToCheckpointsInWorkingDir(t *testing.T) {
	dir := t.TempDir()
	seedTwo(t, dir)
	t.Chdir(dir)

	out, err := run(t)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rows(t, out); len(got) != 3 {
		t.Errorf("got %d rows, want 3:\n%s", len(got), out)
	}
}

func TestNoCheckpointFilesIsAnError(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := run(t)
	if err == nil || !strings.Contains(err.Error(), "feedscan scan --url") {
		t.Fatalf("want an error pointing at `feedscan scan --url`, got %v", err)
	}
}

func TestCheckpointWithoutPostsPrintsEmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.checkpoint.json")
	writeCheckpoint(t, path, "https://a.example") // scanned before posts were saved

	out, err := run(t, "--checkpoint", path, "--format", "json")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(out); got != "[]" {
		t.Errorf("got %q, want []", got)
	}
}

func TestRejectsNegativeLimit(t *testing.T) {
	if _, err := run(t, "--checkpoint", "x.json", "-n", "-1"); err == nil {
		t.Fatal("expected an error for a negative limit")
	}
}
