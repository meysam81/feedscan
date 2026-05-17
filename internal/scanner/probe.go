package scanner

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"

	"feedscan/internal/checkpoint"
)

// fallbackFeedPaths is tried in order if no <link rel="alternate"> is found.
// Stop at first parseable feed.
var fallbackFeedPaths = []string{
	"/feed",
	"/rss",
	"/feed.xml",
	"/rss.xml",
	"/atom.xml",
	"/index.xml",
	"/feeds/posts/default", // Blogger
	"/?feed=rss2",          // WordPress
}

// ProbeAll runs feed probing across todo with bounded concurrency and per-host
// serialization. After each batch completes, onBatch is invoked with the
// batch's results so callers can persist incrementally (so Ctrl-C mid-run
// doesn't lose progress) without paying for a save per URL.
// onBatch may be nil for callers that only want the aggregate return value.
// Progress lines are written to stderr.
func ProbeAll(ctx context.Context, client *http.Client, cfg *Config, todo []string, onBatch func([]checkpoint.Result)) []checkpoint.Result {
	hg := newHostGate(cfg.HostDelay)
	parser := gofeed.NewParser()
	parser.UserAgent = cfg.UserAgent

	results := make([]checkpoint.Result, 0, len(todo))

	for start := 0; start < len(todo); start += cfg.BatchSize {
		end := min(start+cfg.BatchSize, len(todo))
		batch := todo[start:end]

		sem := make(chan struct{}, cfg.Concurrency)
		var wg sync.WaitGroup
		var batchMu sync.Mutex
		batchResults := make([]checkpoint.Result, 0, len(batch))
		canceled := false

		for _, u := range batch {
			select {
			case <-ctx.Done():
				canceled = true
			default:
			}
			if canceled {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(target string) {
				defer wg.Done()
				defer func() { <-sem }()
				r := probeOne(ctx, client, parser, cfg, target, hg)
				batchMu.Lock()
				batchResults = append(batchResults, r)
				batchMu.Unlock()
			}(u)
		}
		wg.Wait()
		results = append(results, batchResults...)
		if onBatch != nil && len(batchResults) > 0 {
			onBatch(batchResults)
		}
		if canceled {
			fmt.Fprintf(os.Stderr, "batch %d-%d/%d canceled\n", start, end, len(todo))
			return results
		}
		fmt.Fprintf(os.Stderr, "batch %d-%d/%d done\n", start, end, len(todo))
	}
	return results
}

// probeOne discovers a feed for target and evaluates the latest post.
func probeOne(ctx context.Context, client *http.Client, parser *gofeed.Parser, cfg *Config, target string, hg *hostGate) checkpoint.Result {
	now := time.Now()
	r := checkpoint.Result{URL: target, CheckedAt: &now}

	feedURL, feedBody, err := discoverFeed(ctx, client, cfg, target, hg)
	if err != nil {
		r.Status = checkpoint.StatusError
		r.Error = err.Error()
		return r
	}
	if feedURL == "" {
		r.Status = checkpoint.StatusNoFeed
		return r
	}
	r.FeedURL = feedURL

	feed, err := parser.ParseString(string(feedBody))
	if err != nil {
		r.Status = checkpoint.StatusError
		r.Error = fmt.Sprintf("parse feed: %v", err)
		return r
	}

	r.ItemCount = len(feed.Items)
	r.LatestPost = timePtr(latestItemDate(feed))
	// Status (active/stale) is set by ApplyFreshness after the cutoff is known.
	r.Status = checkpoint.StatusStale
	return r
}

// discoverFeed tries the page's <link rel="alternate"> first, then a fallback
// list of common paths. Returns the feed URL and raw bytes on success.
func discoverFeed(ctx context.Context, client *http.Client, cfg *Config, target string, hg *hostGate) (string, []byte, error) {
	if err := hg.wait(ctx, target); err != nil {
		return "", nil, err
	}
	res, err := fetch(ctx, client, cfg, target)
	if err != nil {
		return "", nil, fmt.Errorf("fetch homepage: %w", err)
	}
	var body []byte
	if res.StatusCode < 400 {
		body = res.Body
	}
	// Don't give up on >=400 — we can still try fallback paths.

	candidates := feedLinksFromHTML(target, body)
	candidates = append(candidates, fallbackPathsFor(target)...)

	for _, c := range candidates {
		if err := hg.wait(ctx, c); err != nil {
			return "", nil, err
		}
		fr, err := fetch(ctx, client, cfg, c)
		if err != nil {
			continue
		}
		if fr.StatusCode >= 400 || len(fr.Body) == 0 {
			continue
		}
		if looksLikeFeed(fr.Body, fr.ContentType) {
			return c, fr.Body, nil
		}
	}
	return "", nil, nil
}

func feedLinksFromHTML(base string, body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}

	var out []string
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "link" {
			var rel, typ, href string
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "rel":
					rel = strings.ToLower(a.Val)
				case "type":
					typ = strings.ToLower(a.Val)
				case "href":
					href = strings.TrimSpace(a.Val)
				}
			}
			if rel == "alternate" && href != "" &&
				(strings.Contains(typ, "rss") || strings.Contains(typ, "atom") || strings.Contains(typ, "xml")) {
				if ref, err := baseURL.Parse(href); err == nil && ref.Scheme == "https" {
					out = append(out, ref.String())
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	return out
}

func fallbackPathsFor(target string) []string {
	u, err := url.Parse(target)
	if err != nil {
		return nil
	}
	root := u.Scheme + "://" + u.Host
	out := make([]string, 0, len(fallbackFeedPaths))
	for _, p := range fallbackFeedPaths {
		out = append(out, root+p)
	}
	return out
}

// looksLikeFeed is a fast sniff: content type or leading XML markers.
func looksLikeFeed(body []byte, contentType string) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "xml") || strings.Contains(ct, "rss") || strings.Contains(ct, "atom") {
		return true
	}
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	s := strings.ToLower(string(head))
	return strings.Contains(s, "<rss") || strings.Contains(s, "<feed") || strings.Contains(s, "<?xml")
}

func latestItemDate(feed *gofeed.Feed) time.Time {
	var latest time.Time
	for _, item := range feed.Items {
		t := itemTime(item)
		if t.After(latest) {
			latest = t
		}
	}
	return latest
}

func itemTime(item *gofeed.Item) time.Time {
	if item.UpdatedParsed != nil {
		return *item.UpdatedParsed
	}
	if item.PublishedParsed != nil {
		return *item.PublishedParsed
	}
	return time.Time{}
}

// ApplyFreshness sets active/stale based on cutoff. Other statuses pass through.
func ApplyFreshness(r checkpoint.Result, cutoff time.Time) checkpoint.Result {
	if r.Status != checkpoint.StatusStale && r.Status != checkpoint.StatusActive {
		return r
	}
	if r.LatestPost != nil && r.LatestPost.After(cutoff) {
		r.Status = checkpoint.StatusActive
	} else {
		r.Status = checkpoint.StatusStale
	}
	return r
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
