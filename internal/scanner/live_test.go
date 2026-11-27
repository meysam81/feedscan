package scanner

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// rssFeed renders a minimal RSS document with one dated item per title.
func rssFeed(titles ...string) string {
	items := ""
	for i, title := range titles {
		items += fmt.Sprintf("<item><title>%s</title><link>/%s</link><pubDate>%s</pubDate></item>",
			title, title, time.Date(2026, 11, 1+i, 0, 0, 0, 0, time.UTC).Format(time.RFC1123Z))
	}
	return `<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>` + items + `</channel></rss>`
}

func liveConfig() *Config {
	return &Config{
		Timeout:      5 * time.Second,
		Concurrency:  4,
		UserAgent:    DefaultUserAgent,
		MaxBodyBytes: DefaultMaxBodyBytes,
		MaxPosts:     math.MaxInt,
	}
}

func TestFetchPostsMergesFeedsAndCountsFailures(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/a.xml":
			fmt.Fprint(w, rssFeed("a1", "a2"))
		case "/b.xml":
			fmt.Fprint(w, rssFeed("b1"))
		case "/junk.xml":
			fmt.Fprint(w, "<html><body>not a feed</body></html>")
		default: // an error status fails the feed even when the body parses
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, rssFeed("from-an-error-page"))
		}
	}))
	defer srv.Close()

	cfg := liveConfig()
	feeds := []string{srv.URL + "/a.xml", srv.URL + "/b.xml", srv.URL + "/junk.xml", srv.URL + "/500.xml"}
	posts, failed := FetchPosts(context.Background(), NewHTTPClient(cfg), cfg, feeds)

	if failed != 2 {
		t.Errorf("failed = %d, want 2 (junk + 500)", failed)
	}
	var titles []string
	for _, p := range posts {
		titles = append(titles, p.Title)
	}
	sort.Strings(titles)
	if got, want := fmt.Sprint(titles), "[a1 a2 b1]"; got != want {
		t.Errorf("titles = %s, want %s", got, want)
	}
	if got := hits.Load(); got != 4 {
		t.Errorf("server hits = %d, want 4 (one GET per feed)", got)
	}
	// Relative item links resolve against the feed URL.
	for _, p := range posts {
		if p.Title == "b1" && p.URL != srv.URL+"/b1" {
			t.Errorf("b1 url = %q, want %q", p.URL, srv.URL+"/b1")
		}
	}
}

// A feed that moved: relative links belong to where the feed lives now.
func TestFetchPostsResolvesLinksAgainstRedirectTarget(t *testing.T) {
	t.Parallel()
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, rssFeed("p1"))
	}))
	defer moved.Close()
	old := httptest.NewServer(http.RedirectHandler(moved.URL+"/feed.xml", http.StatusMovedPermanently))
	defer old.Close()

	cfg := liveConfig()
	posts, failed := FetchPosts(context.Background(), NewHTTPClient(cfg), cfg, []string{old.URL + "/feed.xml"})
	if failed != 0 || len(posts) != 1 {
		t.Fatalf("got %d posts, %d failed; want 1, 0", len(posts), failed)
	}
	if want := moved.URL + "/p1"; posts[0].URL != want {
		t.Errorf("url = %q, want %q (the redirect target's host)", posts[0].URL, want)
	}
}

func TestFetchPostsSpacesRequestsToOneHost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, rssFeed("x"))
	}))
	defer srv.Close()

	cfg := liveConfig()
	cfg.HostDelay = 100 * time.Millisecond
	start := time.Now()
	FetchPosts(context.Background(), NewHTTPClient(cfg), cfg, []string{srv.URL + "/1", srv.URL + "/2"})
	if elapsed := time.Since(start); elapsed < cfg.HostDelay {
		t.Errorf("two feeds on one host took %v, want >= %v", elapsed, cfg.HostDelay)
	}
}

func TestFetchPostsStopsStartingFeedsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		cancel()             // the user hits Ctrl-C while the first feed is in flight
		<-r.Context().Done() // ...and the request is abandoned
	}))
	defer srv.Close()

	cfg := liveConfig()
	cfg.Concurrency = 1 // feeds start one at a time, so the count below is exact
	feeds := []string{srv.URL + "/1", srv.URL + "/2", srv.URL + "/3"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		FetchPosts(ctx, NewHTTPClient(cfg), cfg, feeds)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second): // well under the 5s request timeout
		t.Fatal("FetchPosts did not return promptly after cancel")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1 (no feed starts after cancel)", got)
	}
}
