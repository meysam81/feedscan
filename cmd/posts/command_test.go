package posts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// runArgs executes a fresh posts command and returns its stdout and stderr.
func runArgs(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := Command()
	cmd.Writer = &out
	cmd.ErrWriter = &errOut
	err = cmd.Run(context.Background(), append([]string{"posts"}, args...))
	return out.String(), errOut.String(), err
}

// runCached lists the posts saved in checkpoints (no network).
func runCached(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, _, err := runArgs(t, append([]string{"--cached"}, args...)...)
	return out, err
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

	out, err := runCached(t, "--checkpoint", a, "--checkpoint", b)
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

	out, err = runCached(t, "--checkpoint", a, "--checkpoint", b, "-n", "1", "--days", "7")
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

	out, err := runCached(t)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rows(t, out); len(got) != 3 {
		t.Errorf("got %d rows, want 3:\n%s", len(got), out)
	}
}

func TestNoCheckpointFilesIsAnError(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := runCached(t)
	if err == nil || !strings.Contains(err.Error(), "feedscan scan --url") {
		t.Fatalf("want an error pointing at `feedscan scan --url`, got %v", err)
	}
}

func TestCheckpointWithoutPostsPrintsEmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.checkpoint.json")
	writeCheckpoint(t, path, "https://a.example") // scanned before posts were saved

	out, err := runCached(t, "--checkpoint", path, "--format", "json")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(out); got != "[]" {
		t.Errorf("got %q, want []", got)
	}
}

func TestRejectsNegativeLimit(t *testing.T) {
	if _, err := runCached(t, "--checkpoint", "x.json", "-n", "-1"); err == nil {
		t.Fatal("expected an error for a negative limit")
	}
}

func TestExcludeHidesHostBeforeLimit(t *testing.T) {
	a, b := seedTwo(t, t.TempDir())

	// Without the exclusion the single row would be b.example's "Newest".
	out, err := runCached(t, "--checkpoint", a, "--checkpoint", b, "--exclude", "b.example", "-n", "1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rows(t, out); len(got) != 1 || !strings.HasPrefix(got[0], "Shared") {
		t.Errorf("got %q, want the next-newest post from another host", got)
	}

	t.Setenv("FEEDSCAN_EXCLUDE", "b.example,shared.example")
	out, err = runCached(t, "--checkpoint", a, "--checkpoint", b)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rows(t, out); len(got) != 1 || !strings.HasPrefix(got[0], "Oldest") {
		t.Errorf("FEEDSCAN_EXCLUDE with two hosts: got %q", got)
	}
}

// feedServer serves one RSS item titled after each path ("/alice" → "alice"),
// and HTTP 500 for paths starting with "/down". hits counts requests.
func feedServer(t *testing.T) (srv *httptest.Server, hits *atomic.Int32) {
	t.Helper()
	hits = new(atomic.Int32)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if strings.HasPrefix(r.URL.Path, "/down") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>`+
			`<item><title>live %s</title><link>/post-%s</link><pubDate>%s</pubDate></item></channel></rss>`,
			name, name, time.Now().Add(-time.Hour).UTC().Format(time.RFC1123Z))
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

// writeSite saves a checkpoint with one scanned site: its feed URL and one
// post saved at scan time.
func writeSite(t *testing.T, path, site, feedURL string) {
	t.Helper()
	now := time.Now()
	cp := checkpoint.New()
	cp.Put(checkpoint.Result{URL: site, Status: checkpoint.StatusStale, FeedURL: feedURL, CheckedAt: &now, Posts: []checkpoint.Post{
		{Title: "saved at scan time", URL: site + "/saved", Date: now.Add(-365 * 24 * time.Hour)},
	}})
	if err := cp.Save(path); err != nil {
		t.Fatalf("save %s: %v", path, err)
	}
}

func TestLiveByDefaultCachedOnRequest(t *testing.T) {
	srv, hits := feedServer(t)
	path := filepath.Join(t.TempDir(), "x.checkpoint.json")
	writeSite(t, path, "https://alice.example", srv.URL+"/alice")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out, _, err := runArgs(t, "--checkpoint", path)
	if err != nil {
		t.Fatalf("live run: %v", err)
	}
	if !strings.Contains(out, "live alice") || strings.Contains(out, "saved at scan time") {
		t.Errorf("default run must list the feed's current posts only, got:\n%s", out)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("a live run must not modify the checkpoint file")
	}

	hits.Store(0)
	out, err = runCached(t, "--checkpoint", path)
	if err != nil {
		t.Fatalf("cached run: %v", err)
	}
	if !strings.Contains(out, "saved at scan time") || strings.Contains(out, "live alice") {
		t.Errorf("--cached must list the saved posts only, got:\n%s", out)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("--cached made %d requests, want 0", got)
	}
}

func TestLiveFetchesASharedFeedOnce(t *testing.T) {
	srv, hits := feedServer(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.checkpoint.json"), filepath.Join(dir, "b.checkpoint.json")
	writeSite(t, a, "https://alice.example", srv.URL+"/alice")
	writeSite(t, b, "https://alice.example", srv.URL+"/alice")

	out, _, err := runArgs(t, "--checkpoint", a, "--checkpoint", b)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("feed fetched %d times, want 1", got)
	}
	if got := rows(t, out); len(got) != 1 {
		t.Errorf("got %d rows, want 1:\n%s", len(got), out)
	}
}

func TestLiveSkipsFailedFeedsAndReportsThem(t *testing.T) {
	srv, _ := feedServer(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.checkpoint.json"), filepath.Join(dir, "b.checkpoint.json")
	writeSite(t, a, "https://alice.example", srv.URL+"/alice")
	writeSite(t, b, "https://bob.example", srv.URL+"/down")

	out, stderr, err := runArgs(t, "--checkpoint", a, "--checkpoint", b)
	if err != nil {
		t.Fatalf("a failed feed must not fail the run: %v", err)
	}
	if got := rows(t, out); len(got) != 1 || !strings.HasPrefix(got[0], "live alice") {
		t.Errorf("got rows %q, want only alice's post", got)
	}
	if !strings.Contains(stderr, "fetching 2 feeds") {
		t.Errorf("stderr must say what the wait is for, got %q", stderr)
	}
	if !strings.Contains(stderr, "1 of 2 feeds failed") {
		t.Errorf("stderr must report the failure count, got %q", stderr)
	}
	if strings.Contains(stderr, "--cached") {
		t.Errorf("the --cached hint is only for a run where every feed failed, got %q", stderr)
	}

	// Every feed down (offline) is an error: an empty list would read as
	// "nothing new".
	out, _, err = runArgs(t, "--checkpoint", b, "--format", "json")
	if err == nil || !strings.Contains(err.Error(), "all 1 feeds failed") || !strings.Contains(err.Error(), "--cached") {
		t.Errorf("want an error pointing at --cached, got %v", err)
	}
	if out != "" {
		t.Errorf("a failed run must print no list, got %q", out)
	}
}

func TestLiveFiltersLikeCached(t *testing.T) {
	srv, _ := feedServer(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.checkpoint.json"), filepath.Join(dir, "b.checkpoint.json")
	writeSite(t, a, "https://alice.example", srv.URL+"/alice")
	writeSite(t, b, "https://bob.example", srv.URL+"/bob")

	out, _, err := runArgs(t, "--checkpoint", a, "--checkpoint", b, "-n", "1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rows(t, out); len(got) != 1 {
		t.Errorf("-n 1: got %d rows, want 1:\n%s", len(got), out)
	}
}

func TestLiveWithNoFeedsSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.checkpoint.json")
	writeCheckpoint(t, path, "https://a.example") // a site with no feed_url

	out, stderr, err := runArgs(t, "--checkpoint", path, "--format", "json")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(out); got != "[]" {
		t.Errorf("got %q, want []", got)
	}
	if !strings.Contains(stderr, "no feeds") {
		t.Errorf("stderr must explain the empty list, got %q", stderr)
	}
}

func TestRejectsBadNetworkFlags(t *testing.T) {
	for _, args := range [][]string{{"--concurrency", "0"}, {"--timeout", "0s"}} {
		if _, _, err := runArgs(t, append([]string{"--checkpoint", "x.json"}, args...)...); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
	// They don't apply to --cached, which makes no requests.
	path := filepath.Join(t.TempDir(), "x.checkpoint.json")
	writeSite(t, path, "https://alice.example", "https://alice.example/feed")
	if _, err := runCached(t, "--checkpoint", path, "--concurrency", "0"); err != nil {
		t.Errorf("--cached must ignore network flags: %v", err)
	}
}

func TestInterruptedLiveRunIsAnErrorWithNoList(t *testing.T) {
	srv, _ := feedServer(t)
	path := filepath.Join(t.TempDir(), "x.checkpoint.json")
	writeSite(t, path, "https://alice.example", srv.URL+"/alice")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	cmd := Command()
	cmd.Writer = &out
	cmd.ErrWriter = &bytes.Buffer{}
	err := cmd.Run(ctx, []string{"posts", "--checkpoint", path})
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Errorf("want context.Canceled and no output, got err=%v out=%q", err, out.String())
	}
}

// --timeout and --concurrency must reach the fetcher, not just be validated.
func TestNetworkFlagsApply(t *testing.T) {
	var inFlight, peak atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		delay := 50 * time.Millisecond
		if r.URL.Path == "/slow" {
			delay = 2 * time.Second
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>`+
			`<item><title>ok</title><link>/p</link><pubDate>%s</pubDate></item></channel></rss>`,
			time.Now().Add(-time.Hour).UTC().Format(time.RFC1123Z))
	})
	// One server per feed: feeds on the same host are spaced out anyway.
	dir := t.TempDir()
	var args []string
	for i, path := range []string{"/a", "/b", "/slow"} {
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		cp := filepath.Join(dir, fmt.Sprintf("%d.checkpoint.json", i))
		writeSite(t, cp, fmt.Sprintf("https://site%d.example", i), srv.URL+path)
		args = append(args, "--checkpoint", cp)
	}

	_, stderr, err := runArgs(t, append(args, "--timeout", "500ms")...)
	if err != nil || !strings.Contains(stderr, "1 of 3 feeds failed") {
		t.Errorf("--timeout 500ms: want the slow feed skipped, got err=%v stderr=%q", err, stderr)
	}
	if got := peak.Load(); got < 2 {
		t.Errorf("default concurrency: peak = %d, want feeds fetched in parallel", got)
	}

	peak.Store(0)
	if _, _, err := runArgs(t, append(args, "--timeout", "500ms", "--concurrency", "1")...); err != nil {
		t.Fatalf("--concurrency 1: %v", err)
	}
	if got := peak.Load(); got != 1 {
		t.Errorf("--concurrency 1: peak = %d, want 1", got)
	}
}
