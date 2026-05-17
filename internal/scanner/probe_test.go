package scanner

import (
	"testing"
	"time"

	"github.com/mmcdole/gofeed"

	"feedscan/internal/checkpoint"
)

func TestLooksLikeFeed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		body        []byte
		contentType string
		want        bool
	}{
		{"rss content type", nil, "application/rss+xml", true},
		{"atom content type", nil, "application/atom+xml", true},
		{"xml content type", nil, "text/xml; charset=utf-8", true},
		{"html content type, rss body", []byte(`<?xml version="1.0"?><rss>...`), "text/html", true},
		{"html content type, atom body", []byte(`<feed xmlns="http://www.w3.org/2005/Atom">`), "text/html", true},
		{"plain html no markers", []byte(`<html><body><h1>Hi</h1>`), "text/html", false},
		{"empty body", nil, "text/plain", false},
		{"large body, marker in first 512", append(append([]byte("<?xml version=\"1.0\"?>"), make([]byte, 200)...), []byte("<rss>")...), "text/plain", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := looksLikeFeed(tt.body, tt.contentType); got != tt.want {
				t.Errorf("looksLikeFeed(%q, %q) = %v, want %v", tt.body, tt.contentType, got, tt.want)
			}
		})
	}
}

func TestFallbackPathsFor(t *testing.T) {
	t.Parallel()
	got := fallbackPathsFor("https://example.com/blog/post")
	if len(got) != len(fallbackFeedPaths) {
		t.Fatalf("got %d paths, want %d", len(got), len(fallbackFeedPaths))
	}
	for i, want := range fallbackFeedPaths {
		expected := "https://example.com" + want
		if got[i] != expected {
			t.Errorf("[%d] = %q, want %q", i, got[i], expected)
		}
	}
}

func TestFallbackPathsForInvalidURL(t *testing.T) {
	t.Parallel()
	if got := fallbackPathsFor("\x00"); got != nil {
		t.Errorf("expected nil for malformed URL, got %+v", got)
	}
}

func TestFeedLinksFromHTML(t *testing.T) {
	t.Parallel()
	body := []byte(`
		<html><head>
		  <link rel="alternate" type="application/rss+xml" href="/feed.xml">
		  <link rel="alternate" type="application/atom+xml" href="https://example.com/atom">
		  <link rel="alternate" type="text/html" href="/duplicate">
		  <link rel="icon" type="image/x-icon" href="/favicon.ico">
		  <link rel="alternate" type="application/rss+xml" href="http://insecure.com/rss">
		</head></html>`)
	got := feedLinksFromHTML("https://example.com/page", body)
	want := []string{"https://example.com/feed.xml", "https://example.com/atom"}
	if len(got) != len(want) {
		t.Fatalf("got %d links, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestFeedLinksFromHTMLEmpty(t *testing.T) {
	t.Parallel()
	if got := feedLinksFromHTML("https://example.com", nil); got != nil {
		t.Errorf("expected nil for empty body, got %+v", got)
	}
}

func TestApplyFreshness(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cutoff := now.Add(-7 * 24 * time.Hour)
	fresh := now.Add(-1 * time.Hour)
	stale := now.Add(-30 * 24 * time.Hour)

	tests := []struct {
		name string
		in   checkpoint.Result
		want checkpoint.Status
	}{
		{"active feed", checkpoint.Result{Status: checkpoint.StatusStale, LatestPost: &fresh}, checkpoint.StatusActive},
		{"stale feed", checkpoint.Result{Status: checkpoint.StatusStale, LatestPost: &stale}, checkpoint.StatusStale},
		{"no_feed passes through", checkpoint.Result{Status: checkpoint.StatusNoFeed}, checkpoint.StatusNoFeed},
		{"error passes through", checkpoint.Result{Status: checkpoint.StatusError}, checkpoint.StatusError},
		{"nil latest_post → stale", checkpoint.Result{Status: checkpoint.StatusStale, LatestPost: nil}, checkpoint.StatusStale},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ApplyFreshness(tt.in, cutoff)
			if got.Status != tt.want {
				t.Errorf("status = %q, want %q", got.Status, tt.want)
			}
		})
	}
}

func TestLatestItemDate(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)

	feed := &gofeed.Feed{Items: []*gofeed.Item{
		{PublishedParsed: &t1},
		{UpdatedParsed: &t2},
		{PublishedParsed: &t3},
	}}
	got := latestItemDate(feed)
	if !got.Equal(t2) {
		t.Errorf("got %v, want %v", got, t2)
	}
}

func TestLatestItemDateEmptyFeed(t *testing.T) {
	t.Parallel()
	got := latestItemDate(&gofeed.Feed{})
	if !got.IsZero() {
		t.Errorf("empty feed should produce zero time, got %v", got)
	}
}

func TestItemTime(t *testing.T) {
	t.Parallel()
	pub := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	upd := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		item *gofeed.Item
		want time.Time
	}{
		{"updated wins", &gofeed.Item{PublishedParsed: &pub, UpdatedParsed: &upd}, upd},
		{"only published", &gofeed.Item{PublishedParsed: &pub}, pub},
		{"neither", &gofeed.Item{}, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := itemTime(tt.item); !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTimePtr(t *testing.T) {
	t.Parallel()
	if got := timePtr(time.Time{}); got != nil {
		t.Errorf("zero time → expected nil, got %v", got)
	}
	now := time.Now()
	got := timePtr(now)
	if got == nil || !got.Equal(now) {
		t.Errorf("non-zero time → expected ptr to same, got %v", got)
	}
}
