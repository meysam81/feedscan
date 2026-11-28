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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"feedscan/internal/checkpoint"
	"feedscan/internal/postcache"
	"feedscan/internal/scanner"
)

// TestMain keeps every command built here away from the real posts cache,
// including one a test forgets to give a --cache-file.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "feedscan-posts-test")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("FEEDSCAN_POSTS_CACHE", filepath.Join(dir, "posts.json")); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// writeCheckpoint saves a checkpoint with one site holding the given posts.
// As in a real scan, only a site whose feed was found has posts.
func writeCheckpoint(t *testing.T, path, site string, posts ...checkpoint.Post) {
	t.Helper()
	now := time.Now()
	cp := checkpoint.New()
	r := checkpoint.Result{URL: site, Status: checkpoint.StatusNoFeed, CheckedAt: &now}
	if len(posts) > 0 {
		r.Status, r.FeedURL, r.Posts = checkpoint.StatusActive, site+"/feed", posts
	}
	cp.Put(r)
	if err := cp.Save(path); err != nil {
		t.Fatalf("save %s: %v", path, err)
	}
}

// runArgs executes a fresh posts command and returns its stdout and stderr.
// Each call starts with an empty posts cache unless args name a --cache-file.
func runArgs(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	if !slices.Contains(args, "--cache-file") {
		args = append([]string{"--cache-file", filepath.Join(t.TempDir(), "posts.json")}, args...)
	}
	var out, errOut bytes.Buffer
	cmd := Command()
	cmd.Writer = &out
	cmd.ErrWriter = &errOut
	err = cmd.Run(context.Background(), append([]string{"posts"}, args...))
	return out.String(), errOut.String(), err
}

// runCached lists without the network: cached posts, else those a scan saved.
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

// trustTestServers makes live fetches accept the certificate that every
// httptest TLS server shares, for the rest of the test.
func trustTestServers(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prev := newHTTPClient
	newHTTPClient = func(cfg *scanner.Config) *http.Client {
		c := srv.Client()
		c.Timeout = cfg.Timeout
		return c
	}
	t.Cleanup(func() { newHTTPClient = prev })
}

// feedServer serves one RSS item titled after each path ("/alice" → "alice"),
// and HTTP 500 for paths starting with "/down". hits counts requests.
func feedServer(t *testing.T) (srv *httptest.Server, hits *atomic.Int32) {
	t.Helper()
	hits = new(atomic.Int32)
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	trustTestServers(t, srv)
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
	writeCheckpoint(t, path, "https://a.example") // a site without a feed

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
	err := cmd.Run(ctx, []string{"posts", "--checkpoint", path, "--cache-file", filepath.Join(t.TempDir(), "posts.json")})
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Errorf("want context.Canceled and no output, got err=%v out=%q", err, out.String())
	}
}

// --timeout and --concurrency must reach the fetcher, not just be validated.
func TestNetworkFlagsApply(t *testing.T) {
	var inFlight, peak atomic.Int32
	var agent atomic.Value
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		agent.Store(r.UserAgent())
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
		srv := httptest.NewTLSServer(handler)
		t.Cleanup(srv.Close)
		trustTestServers(t, srv)
		cp := filepath.Join(dir, fmt.Sprintf("%d.checkpoint.json", i))
		writeSite(t, cp, fmt.Sprintf("https://site%d.example", i), srv.URL+path)
		args = append(args, "--checkpoint", cp)
	}

	_, stderr, err := runArgs(t, append(args, "--timeout", "500ms")...)
	if err != nil || !strings.Contains(stderr, "1 of 3 feeds failed") {
		t.Errorf("--timeout 500ms: want the slow feed skipped, got err=%v stderr=%q", err, stderr)
	}
	if got := agent.Load(); got != scanner.DefaultUserAgent {
		t.Errorf("User-Agent = %v, want scan's default", got)
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

// writeCache saves a posts cache holding the given feeds.
func writeCache(t *testing.T, path string, feeds map[string]postcache.Feed) {
	t.Helper()
	if err := (&postcache.Cache{Feeds: feeds}).Save(path); err != nil {
		t.Fatalf("save cache: %v", err)
	}
}

// cachedFeed is a cache entry fetched ago before now, holding one post.
func cachedFeed(title string, ago time.Duration) postcache.Feed {
	return postcache.Feed{
		FetchedAt: time.Now().Add(-ago),
		Posts:     []checkpoint.Post{{Title: title, URL: "https://cached.example/" + title, Date: time.Now().Add(-2 * time.Hour)}},
	}
}

func TestCacheIsReusedUntilMaxAge(t *testing.T) {
	srv, hits := feedServer(t)
	dir := t.TempDir()
	cp, cache := filepath.Join(dir, "x.checkpoint.json"), filepath.Join(dir, "cache", "posts.json")
	writeSite(t, cp, "https://alice.example", srv.URL+"/alice")
	args := []string{"--checkpoint", cp, "--cache-file", cache}

	first, stderr, err := runArgs(t, args...)
	if err != nil || hits.Load() != 1 {
		t.Fatalf("first run: err=%v hits=%d, want a fetch", err, hits.Load())
	}
	if strings.Contains(stderr, "posts as of") {
		t.Errorf("a run that fetched needs no freshness line, got %q", stderr)
	}

	second, stderr, err := runArgs(t, args...)
	if err != nil || hits.Load() != 1 {
		t.Fatalf("second run: err=%v hits=%d, want no new request", err, hits.Load())
	}
	if second != first {
		t.Errorf("cached list differs from the fetched one:\n%s\nvs\n%s", second, first)
	}
	if !strings.Contains(stderr, "posts as of moments ago; --refresh to fetch now") {
		t.Errorf("stderr must say the list is cached and how old, got %q", stderr)
	}

	if _, _, err := runArgs(t, append(args, "--refresh")...); err != nil || hits.Load() != 2 {
		t.Errorf("--refresh: err=%v hits=%d, want 2", err, hits.Load())
	}
	if _, _, err := runArgs(t, append(args, "--max-age", "0")...); err != nil || hits.Load() != 3 {
		t.Errorf("--max-age 0: err=%v hits=%d, want 3", err, hits.Load())
	}
	if _, _, err := runArgs(t, append(args, "--max-age", "1h")...); err != nil || hits.Load() != 3 {
		t.Errorf("--max-age 1h right after a fetch: err=%v hits=%d, want 3", err, hits.Load())
	}
}

func TestOnlyFeedsOlderThanMaxAgeAreFetched(t *testing.T) {
	srv, hits := feedServer(t)
	dir := t.TempDir()
	a, b, c := filepath.Join(dir, "a.checkpoint.json"), filepath.Join(dir, "b.checkpoint.json"), filepath.Join(dir, "c.checkpoint.json")
	writeSite(t, a, "https://alice.example", srv.URL+"/alice")
	writeSite(t, b, "https://bob.example", srv.URL+"/bob")
	writeSite(t, c, "https://carol.example", srv.URL+"/carol") // scanned later: not in the cache
	cache := filepath.Join(dir, "posts.json")
	writeCache(t, cache, map[string]postcache.Feed{
		srv.URL + "/alice": cachedFeed("fresh-alice", time.Hour),
		srv.URL + "/bob":   cachedFeed("stale-bob", 25*time.Hour),
	})

	out, stderr, err := runArgs(t, "--checkpoint", a, "--checkpoint", b, "--checkpoint", c, "--cache-file", cache)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("requests = %d, want 2 (bob is stale, carol is new, alice is fresh)", got)
	}
	for _, want := range []string{"fresh-alice", "live bob", "live carol"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stale-bob") {
		t.Errorf("a refetched feed must not keep its old posts:\n%s", out)
	}
	if !strings.Contains(stderr, "fetching 2 feeds") {
		t.Errorf("stderr = %q, want the count of feeds actually fetched", stderr)
	}
}

func TestFailedFeedKeepsItsCachedPostsAndIsNotRetriedAtOnce(t *testing.T) {
	srv, hits := feedServer(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.checkpoint.json"), filepath.Join(dir, "b.checkpoint.json")
	writeSite(t, a, "https://alice.example", srv.URL+"/alice")
	writeSite(t, b, "https://bob.example", srv.URL+"/down")
	c := filepath.Join(dir, "c.checkpoint.json")
	writeSite(t, c, "https://carol.example", srv.URL+"/carol") // fresh: not part of this fetch
	cache := filepath.Join(dir, "posts.json")
	writeCache(t, cache, map[string]postcache.Feed{
		srv.URL + "/down":  cachedFeed("bob-before-the-outage", 25*time.Hour),
		srv.URL + "/carol": cachedFeed("fresh-carol", time.Hour),
	})
	args := []string{"--checkpoint", a, "--checkpoint", b, "--checkpoint", c, "--cache-file", cache}

	out, stderr, err := runArgs(t, args...)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "live alice") || !strings.Contains(out, "bob-before-the-outage") {
		t.Errorf("want alice's live post and bob's cached one:\n%s", out)
	}
	if !strings.Contains(stderr, "1 of 2 feeds failed") {
		t.Errorf("stderr = %q, want the failure count out of the feeds fetched", stderr)
	}

	before := hits.Load()
	if _, _, err := runArgs(t, args...); err != nil || hits.Load() != before {
		t.Errorf("next run: err=%v, %d new requests; a feed that just failed waits for --max-age", err, hits.Load()-before)
	}
}

// unreachable returns an https URL nothing listens on: what every feed looks
// like without a network.
func unreachable(t *testing.T) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL
}

func TestOfflineListsTheCacheAndLeavesItAlone(t *testing.T) {
	gone := unreachable(t)
	dir := t.TempDir()
	cp, cache := filepath.Join(dir, "x.checkpoint.json"), filepath.Join(dir, "posts.json")
	writeSite(t, cp, "https://bob.example", gone+"/feed")
	writeCache(t, cache, map[string]postcache.Feed{gone + "/feed": cachedFeed("from-three-days-ago", 72*time.Hour)})
	before, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}

	out, stderr, err := runArgs(t, "--checkpoint", cp, "--cache-file", cache)
	if err != nil {
		t.Fatalf("offline with a cache must still list it: %v", err)
	}
	if !strings.Contains(out, "from-three-days-ago") {
		t.Errorf("list lacks the cached post:\n%s", out)
	}
	if !strings.Contains(stderr, "no feed could be reached") || !strings.Contains(stderr, "3d ago") {
		t.Errorf("stderr must say nothing was fetched and how old the list is, got %q", stderr)
	}
	// Otherwise coming back online would wait a day before trying again.
	if after, _ := os.ReadFile(cache); !bytes.Equal(before, after) {
		t.Error("a run where no feed could be reached must not rewrite the cache")
	}

	// Offline with nothing cached: an error, not an empty list.
	if out, _, err := runArgs(t, "--checkpoint", cp); err == nil || out != "" {
		t.Errorf("want an error and no list, got err=%v out=%q", err, out)
	}
}

// One broken site is not a missing network: it must wait for --max-age like
// any other feed, even when it is the only one due.
func TestBrokenFeedIsNotMistakenForOffline(t *testing.T) {
	srv, hits := feedServer(t)
	dir := t.TempDir()
	cp, cache := filepath.Join(dir, "x.checkpoint.json"), filepath.Join(dir, "posts.json")
	writeSite(t, cp, "https://bob.example", srv.URL+"/down")
	writeCache(t, cache, map[string]postcache.Feed{srv.URL + "/down": cachedFeed("bob-before-the-outage", 25*time.Hour)})
	args := []string{"--checkpoint", cp, "--cache-file", cache}

	out, stderr, err := runArgs(t, args...)
	if err != nil || !strings.Contains(out, "bob-before-the-outage") {
		t.Fatalf("err=%v, want bob's cached post:\n%s", err, out)
	}
	if !strings.Contains(stderr, "1 of 1 feeds failed") || strings.Contains(stderr, "offline") {
		t.Errorf("stderr = %q, want a plain failure count", stderr)
	}
	if _, _, err := runArgs(t, args...); err != nil || hits.Load() != 1 {
		t.Errorf("next run: err=%v, %d requests in total; want 1 (not retried until --max-age)", err, hits.Load())
	}
}

func TestFetchTimeInTheFutureIsDue(t *testing.T) {
	srv, hits := feedServer(t)
	dir := t.TempDir()
	cp, cache := filepath.Join(dir, "x.checkpoint.json"), filepath.Join(dir, "posts.json")
	writeSite(t, cp, "https://alice.example", srv.URL+"/alice")
	writeCache(t, cache, map[string]postcache.Feed{srv.URL + "/alice": cachedFeed("from-the-future", -48*time.Hour)})

	out, _, err := runArgs(t, "--checkpoint", cp, "--cache-file", cache)
	if err != nil || hits.Load() != 1 || !strings.Contains(out, "live alice") {
		t.Errorf("a copy stamped in the future must be fetched again: err=%v hits=%d\n%s", err, hits.Load(), out)
	}
}

func TestCachedFallsBackToTheScanAfterAFailedFirstFetch(t *testing.T) {
	srv, _ := feedServer(t)
	dir := t.TempDir()
	cp, cache := filepath.Join(dir, "x.checkpoint.json"), filepath.Join(dir, "posts.json")
	writeSite(t, cp, "https://bob.example", srv.URL+"/down")
	args := []string{"--checkpoint", cp, "--cache-file", cache}

	if _, _, err := runArgs(t, args...); err == nil || !strings.Contains(err.Error(), "--cached") {
		t.Fatalf("every feed failing with nothing cached must be an error pointing at --cached, got %v", err)
	}
	out, _, err := runArgs(t, append(args, "--cached")...)
	if err != nil || !strings.Contains(out, "bob.example/saved") {
		t.Errorf("--cached must still list what the scan saved: err=%v\n%s", err, out)
	}
}

// Validators that make a request fail (a damaged cache file) must not be
// sent forever.
func TestFailedFeedForgetsItsValidators(t *testing.T) {
	srv, _ := feedServer(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.checkpoint.json"), filepath.Join(dir, "b.checkpoint.json")
	writeSite(t, a, "https://alice.example", srv.URL+"/alice")
	writeSite(t, b, "https://bob.example", srv.URL+"/bob")
	cache := filepath.Join(dir, "posts.json")
	damaged := cachedFeed("alice-before", 25*time.Hour)
	damaged.ETag = "not\r\na header value"
	writeCache(t, cache, map[string]postcache.Feed{srv.URL + "/alice": damaged})
	args := []string{"--checkpoint", a, "--checkpoint", b, "--cache-file", cache, "--refresh"}

	out, stderr, err := runArgs(t, args...)
	if err != nil || !strings.Contains(stderr, "1 of 2 feeds failed") || !strings.Contains(out, "alice-before") {
		t.Fatalf("first run: err=%v stderr=%q\n%s", err, stderr, out)
	}
	out, stderr, err = runArgs(t, args...)
	if err != nil || strings.Contains(stderr, "failed") || !strings.Contains(out, "live alice") {
		t.Errorf("second run must fetch alice without the bad validator: err=%v stderr=%q\n%s", err, stderr, out)
	}
}

func TestFreshnessLineShowsTheOldestFeed(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.checkpoint.json"), filepath.Join(dir, "b.checkpoint.json")
	writeSite(t, a, "https://alice.example", "https://alice.example/feed")
	writeSite(t, b, "https://bob.example", "https://bob.example/feed")
	cache := filepath.Join(dir, "posts.json")
	writeCache(t, cache, map[string]postcache.Feed{
		"https://alice.example/feed": cachedFeed("alice", time.Hour+time.Minute),
		"https://bob.example/feed":   cachedFeed("bob", 5*time.Hour+time.Minute),
	})

	_, stderr, err := runArgs(t, "--checkpoint", a, "--checkpoint", b, "--cache-file", cache)
	if err != nil || !strings.Contains(stderr, "posts as of 5h ago; --refresh to fetch now") {
		t.Errorf("err=%v stderr=%q, want the age of the oldest copy", err, stderr)
	}
	// --cached and --refresh contradict each other, so --cached doesn't suggest it.
	_, stderr, err = runArgs(t, "--cached", "--checkpoint", a, "--checkpoint", b, "--cache-file", cache)
	if err != nil || !strings.Contains(stderr, "posts as of 5h ago") || strings.Contains(stderr, "--refresh") {
		t.Errorf("--cached: err=%v stderr=%q", err, stderr)
	}
}

// The per-site cap counts posts that would be listed: a site's hidden or
// too-old posts must not use it up.
func TestPerSiteCapAppliesAfterTheFilters(t *testing.T) {
	dir := t.TempDir()
	cp, cache := filepath.Join(dir, "x.checkpoint.json"), filepath.Join(dir, "posts.json")
	writeSite(t, cp, "https://alice.example", "https://alice.example/feed")
	ago := func(d time.Duration) time.Time { return time.Now().Add(-d) }
	writeCache(t, cache, map[string]postcache.Feed{"https://alice.example/feed": {
		FetchedAt: time.Now(),
		Posts: []checkpoint.Post{
			{Title: "linked 1", URL: "https://noisy.example/1", Date: ago(1 * time.Hour)},
			{Title: "linked 2", URL: "https://noisy.example/2", Date: ago(2 * time.Hour)},
			{Title: "linked 3", URL: "https://noisy.example/3", Date: ago(3 * time.Hour)},
			{Title: "own 1", URL: "https://alice.example/1", Date: ago(4 * time.Hour)},
			{Title: "own 2", URL: "https://alice.example/2", Date: ago(5 * time.Hour)},
		},
	}})

	out, _, err := runArgs(t, "--checkpoint", cp, "--cache-file", cache, "--exclude", "noisy.example")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rows(t, out); len(got) != 2 || !strings.HasPrefix(got[0], "own 1") {
		t.Errorf("want alice's two own posts, got %q", got)
	}
}

func TestCachedNeverFetches(t *testing.T) {
	srv, hits := feedServer(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.checkpoint.json"), filepath.Join(dir, "b.checkpoint.json")
	writeSite(t, a, "https://alice.example", srv.URL+"/alice") // in the cache
	writeSite(t, b, "https://bob.example", srv.URL+"/bob")     // never fetched
	cache := filepath.Join(dir, "posts.json")
	writeCache(t, cache, map[string]postcache.Feed{srv.URL + "/alice": cachedFeed("alice-from-last-week", 7*24*time.Hour)})
	before, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}

	out, stderr, err := runArgs(t, "--cached", "--checkpoint", a, "--checkpoint", b, "--cache-file", cache)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("--cached made %d requests, want 0", got)
	}
	if !strings.Contains(out, "alice-from-last-week") || strings.Contains(out, "alice.example/saved") {
		t.Errorf("a cached feed lists its cached posts, not the scan's:\n%s", out)
	}
	if !strings.Contains(out, "bob.example/saved") {
		t.Errorf("a feed never fetched falls back to the posts its scan saved:\n%s", out)
	}
	if !strings.Contains(stderr, "posts as of 7d ago") {
		t.Errorf("stderr = %q, want the age of the cache", stderr)
	}
	if after, _ := os.ReadFile(cache); !bytes.Equal(before, after) {
		t.Error("--cached must not write the cache")
	}
}

func TestUnchangedFeedKeepsItsPosts(t *testing.T) {
	const etag = `"v1"`
	var full, conditional atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("ETag", etag)
		fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>`+
			`<item><title>only post</title><link>/p</link><pubDate>%s</pubDate></item></channel></rss>`,
			time.Now().Add(-time.Hour).UTC().Format(time.RFC1123Z))
	}))
	t.Cleanup(srv.Close)
	trustTestServers(t, srv)
	dir := t.TempDir()
	cp, cache := filepath.Join(dir, "x.checkpoint.json"), filepath.Join(dir, "posts.json")
	writeSite(t, cp, "https://alice.example", srv.URL+"/feed")
	args := []string{"--checkpoint", cp, "--cache-file", cache, "--refresh"}

	first, _, err := runArgs(t, args...)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, stderr, err := runArgs(t, args...)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if full.Load() != 1 || conditional.Load() != 1 {
		t.Errorf("full=%d conditional=%d, want 1 and 1 (the second fetch sends the stored ETag)", full.Load(), conditional.Load())
	}
	if second != first || !strings.Contains(second, "only post") {
		t.Errorf("an unchanged feed must keep its posts:\n%s", second)
	}
	if strings.Contains(stderr, "failed") {
		t.Errorf("not modified is not a failure, got %q", stderr)
	}
	// The 304 counted as a fetch: the feed is fresh again.
	if _, _, err := runArgs(t, "--checkpoint", cp, "--cache-file", cache); err != nil || conditional.Load() != 1 {
		t.Errorf("after a 304 the feed must not be due: err=%v conditional=%d", err, conditional.Load())
	}
}

func TestPerSiteCap(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := ""
		for i := 1; i <= 5; i++ { // post 1 is the newest
			items += fmt.Sprintf("<item><title>%s post %d</title><link>%s/%d</link><pubDate>%s</pubDate></item>",
				r.URL.Path[1:], i, r.URL.Path, i, time.Now().Add(-time.Duration(i)*time.Hour).UTC().Format(time.RFC1123Z))
		}
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>`+items+`</channel></rss>`)
	}))
	t.Cleanup(srv.Close)
	trustTestServers(t, srv)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.checkpoint.json"), filepath.Join(dir, "b.checkpoint.json")
	writeSite(t, a, "https://busy.example", srv.URL+"/busy")
	writeSite(t, b, "https://quiet.example", srv.URL+"/quiet")
	args := []string{"--checkpoint", a, "--checkpoint", b}

	for flags, want := range map[string]int{"": 6, "--per-site=1": 2, "--per-site=0": 10} {
		out, _, err := runArgs(t, append(args, strings.Fields(flags)...)...)
		if err != nil {
			t.Fatalf("%q: %v", flags, err)
		}
		got := rows(t, out)
		if len(got) != want {
			t.Errorf("%q: %d rows, want %d:\n%s", flags, len(got), want, out)
		}
		if flags == "--per-site=1" && (!strings.Contains(out, "busy post 1") || !strings.Contains(out, "quiet post 1")) {
			t.Errorf("the cap must keep each site's newest post:\n%s", out)
		}
	}
	// The cap counts posts that survive the filters: excluding nothing here,
	// but a limit below the cap still wins.
	if out, _, err := runArgs(t, append(args, "-n", "4")...); err != nil || len(rows(t, out)) != 4 {
		t.Errorf("-n 4: err=%v\n%s", err, out)
	}
}

func TestCorruptCacheWarnsAndRefetches(t *testing.T) {
	srv, hits := feedServer(t)
	dir := t.TempDir()
	cp, cache := filepath.Join(dir, "x.checkpoint.json"), filepath.Join(dir, "posts.json")
	writeSite(t, cp, "https://alice.example", srv.URL+"/alice")
	if err := os.WriteFile(cache, []byte("{half a file"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, stderr, err := runArgs(t, "--checkpoint", cp, "--cache-file", cache)
	if err != nil || !strings.Contains(out, "live alice") || hits.Load() != 1 {
		t.Fatalf("a broken cache must not stop the listing: err=%v hits=%d\n%s", err, hits.Load(), out)
	}
	if !strings.Contains(stderr, "posts cache unreadable") {
		t.Errorf("stderr = %q, want a warning", stderr)
	}
	if _, _, err := runArgs(t, "--checkpoint", cp, "--cache-file", cache); err != nil || hits.Load() != 1 {
		t.Errorf("the cache must have been rewritten whole: err=%v hits=%d", err, hits.Load())
	}
}

func TestRejectsContradictoryOrNegativeCacheFlags(t *testing.T) {
	for _, args := range [][]string{{"--cached", "--refresh"}, {"--per-site", "-1"}, {"--max-age", "-1s"}} {
		if _, _, err := runArgs(t, append([]string{"--checkpoint", "x.json"}, args...)...); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestProgress(t *testing.T) {
	var plain bytes.Buffer
	tick, done := progress(&plain, 3, false)
	done()
	if tick != nil || plain.String() != "fetching 3 feeds...\n" {
		t.Errorf("off a terminal: want one line and no ticker, got tick set=%v %q", tick != nil, plain.String())
	}

	var term bytes.Buffer
	tick, done = progress(&term, 2, true)
	tick()
	tick()
	done()
	if got, want := term.String(), "fetching feeds 0/2\rfetching feeds 1/2\rfetching feeds 2/2\n"; got != want {
		t.Errorf("on a terminal: got %q, want %q", got, want)
	}
	if isTerminal(&term) {
		t.Error("a buffer is not a terminal")
	}
}

func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-5 * time.Minute: "moments", 30 * time.Second: "moments", 59 * time.Minute: "59m", 3 * time.Hour: "3h", 47 * time.Hour: "47h", 72 * time.Hour: "3d",
	} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}
