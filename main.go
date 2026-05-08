// Command feedscan fetches a URL, extracts external HTTPS links, probes each
// for an RSS/Atom feed, and reports which feeds have been active within a
// configurable freshness window.
//
// Design notes:
//   - Checkpoints are keyed by external URL (not by run), so unrelated runs
//     that share targets reuse cached probe results until TTL expires.
//   - "External" means a different registrable domain than the input URL.
//   - Feed discovery: HTML <link rel="alternate"> first, then a fallback list
//     of common feed paths. First valid feed per host wins.
//   - Per-host serialization via a tiny gate map provides politeness without
//     a global rate limiter; combined with a bounded worker pool, this gives
//     concurrent throughput across hosts and orderly access within a host.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

// Status describes the outcome of probing a single external URL.
type Status string

const (
	StatusActive Status = "active"  // feed found, fresh entry within window
	StatusStale  Status = "stale"   // feed found, no fresh entry within window
	StatusNoFeed Status = "no_feed" // probed all candidates, no feed found
	StatusError  Status = "error"   // network or parse error
)

// Result is the per-URL outcome, also used as the checkpoint cache value.
//
// Time fields are pointers so encoding/json's `omitempty` correctly drops them
// when zero — `time.Time{}` is a struct and would otherwise serialize as
// "0001-01-01T00:00:00Z".
type Result struct {
	URL        string     `json:"url"`
	Status     Status     `json:"status,omitempty"`
	FeedURL    string     `json:"feed_url,omitempty"`
	LatestPost *time.Time `json:"latest_post,omitempty"`
	ItemCount  int        `json:"item_count,omitempty"`
	Error      string     `json:"error,omitempty"`
	CheckedAt  *time.Time `json:"checked_at,omitempty"`
}

// timePtr returns nil for zero times so they serialize as omitted JSON fields.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Config holds all tunables. Defaults are set in parseFlags.
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
	Verbose        bool
	MaxURLs        int    // cap on URLs to process; 0 = unlimited
	SortBy         string // url|status|latest_post|item_count|checked_at
	SortOrder      string // asc|desc
	Format         string // "json" or "table"
}

func main() {
	if err := mainErr(); err != nil {
		log.Fatalf("%v", err)
	}
}

func mainErr() error {
	cfg, err := parseFlags()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := run(ctx, cfg); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

func parseFlags() (*Config, error) {
	cfg := &Config{}

	flag.StringVar(&cfg.InputURL, "url", "", "input URL to scan (required)")
	flag.IntVar(&cfg.WindowDays, "days", 7, "freshness window in days")
	flag.DurationVar(&cfg.Timeout, "timeout", 10*time.Second, "per-request HTTP timeout")
	flag.IntVar(&cfg.Concurrency, "concurrency", 8, "max concurrent workers")
	flag.IntVar(&cfg.BatchSize, "batch", 32, "URLs per batch")
	flag.DurationVar(&cfg.CacheTTL, "cache-ttl", 24*time.Hour, "checkpoint TTL")
	flag.StringVar(&cfg.CheckpointPath, "checkpoint", "feedscan.checkpoint.json", "checkpoint file path")
	flag.StringVar(&cfg.UserAgent, "user-agent", "feedscan/1.0", "HTTP User-Agent")
	flag.Int64Var(&cfg.MaxBodyBytes, "max-body", 10<<20, "max response body bytes")
	flag.DurationVar(&cfg.HostDelay, "host-delay", 500*time.Millisecond, "min delay between requests to same host")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "extract URLs but skip feed probing")
	flag.BoolVar(&cfg.NoCache, "no-cache", false, "ignore checkpoint cache")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "print all URLs in dry-run mode (default: aggregated summary)")
	flag.IntVar(&cfg.MaxURLs, "max-urls", 0, "cap on extracted URLs to process (0 = unlimited); useful for trying small batches")
	flag.StringVar(&cfg.SortBy, "sort-by", "latest_post", "sort field: url|status|latest_post|item_count|checked_at")
	flag.StringVar(&cfg.SortOrder, "sort-order", "desc", "sort order: asc|desc")
	flag.StringVar(&cfg.Format, "format", "json", "output format: json|table")
	flag.Parse()

	if cfg.InputURL == "" {
		flag.Usage()
		return nil, errors.New("-url is required")
	}
	// Default to https if the user passed a bare host like "example.com".
	if !strings.Contains(cfg.InputURL, "://") {
		cfg.InputURL = "https://" + cfg.InputURL
	}
	if cfg.Concurrency < 1 {
		return nil, errors.New("-concurrency must be >= 1")
	}
	if cfg.BatchSize < 1 {
		return nil, errors.New("-batch must be >= 1")
	}
	if cfg.WindowDays < 1 {
		return nil, errors.New("-days must be >= 1")
	}
	if cfg.MaxURLs < 0 {
		return nil, errors.New("-max-urls must be >= 0")
	}
	if _, ok := sortLessFns[cfg.SortBy]; !ok {
		return nil, fmt.Errorf("-sort-by must be one of: url, status, latest_post, item_count, checked_at (got %q)", cfg.SortBy)
	}
	if cfg.SortOrder != "asc" && cfg.SortOrder != "desc" {
		return nil, fmt.Errorf("-sort-order must be asc or desc (got %q)", cfg.SortOrder)
	}
	if !filepath.IsAbs(cfg.CheckpointPath) {
		abs, err := filepath.Abs(cfg.CheckpointPath)
		if err != nil {
			return nil, fmt.Errorf("resolve checkpoint path: %w", err)
		}
		cfg.CheckpointPath = abs
	}
	return cfg, nil
}

func run(ctx context.Context, cfg *Config) error {
	client := newHTTPClient(cfg)

	// Step 1: fetch input page and extract external HTTPS URLs.
	urls, err := extractExternalURLs(ctx, client, cfg)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Optional cap so users can validate behavior on a small batch before
	// committing to a full run.
	if cfg.MaxURLs > 0 && len(urls) > cfg.MaxURLs {
		fmt.Fprintf(os.Stderr, "capping to %d URLs (of %d extracted; -max-urls)\n", cfg.MaxURLs, len(urls))
		urls = urls[:cfg.MaxURLs]
	}

	if cfg.DryRun {
		fmt.Fprintf(os.Stderr, "found %d external URLs (dry run; remove -dry-run to probe feeds)\n", len(urls))
		if cfg.Verbose {
			return emit(cfg.Format, urlsAsResults(urls))
		}
		return emitDryRunSummary(cfg.Format, urls)
	}

	// Step 2: load checkpoint, filter URLs needing fresh probes.
	cp, err := loadCheckpoint(cfg.CheckpointPath)
	if err != nil {
		return fmt.Errorf("load checkpoint: %w", err)
	}

	now := time.Now()
	var todo []string
	results := make(map[string]Result, len(urls))

	for _, u := range urls {
		if !cfg.NoCache {
			if r, ok := cp.get(u, now, cfg.CacheTTL); ok {
				results[u] = r
				continue
			}
		}
		todo = append(todo, u)
	}

	fmt.Fprintf(os.Stderr, "extracted=%d cached=%d todo=%d\n", len(urls), len(results), len(todo))

	// Step 3: probe in batches with bounded concurrency and per-host gating.
	if len(todo) > 0 {
		probed := probeAll(ctx, client, cfg, todo)
		for _, r := range probed {
			results[r.URL] = r
			cp.put(r)
			// Persist incrementally so a Ctrl-C mid-run doesn't lose progress.
			if err := cp.save(cfg.CheckpointPath); err != nil {
				log.Printf("warning: checkpoint save failed: %v", err)
			}
		}
	}

	// Step 4: emit.
	out := make([]Result, 0, len(results))
	for _, u := range urls {
		out = append(out, results[u])
	}
	cutoff := now.Add(-time.Duration(cfg.WindowDays) * 24 * time.Hour)
	for i := range out {
		out[i] = applyFreshness(out[i], cutoff)
	}
	sortResults(out, cfg.SortBy, cfg.SortOrder)
	return emit(cfg.Format, out)
}

// ─── HTTP client ─────────────────────────────────────────────────────────────

func newHTTPClient(cfg *Config) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		// Default redirect policy (max 10) is fine.
	}
}

// fetchResult is what callers actually need from fetch — status, content type,
// and body. The HTTP response body is read and closed inside fetch.
type fetchResult struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// fetch performs a GET with the configured UA and returns the body, capped at
// MaxBodyBytes.
func fetch(ctx context.Context, client *http.Client, cfg *Config, rawURL string) (*fetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml,application/rss+xml,application/atom+xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("warning: response body close (%s): %v", rawURL, cerr)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, cfg.MaxBodyBytes))
	if err != nil {
		return nil, err
	}
	return &fetchResult{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
	}, nil
}

// ─── URL extraction ──────────────────────────────────────────────────────────

// extractExternalURLs fetches the input page, parses HTML, and returns deduped
// external HTTPS URLs (different registrable domain than input).
func extractExternalURLs(ctx context.Context, client *http.Client, cfg *Config) ([]string, error) {
	base, err := url.Parse(cfg.InputURL)
	if err != nil {
		return nil, fmt.Errorf("parse input URL: %w", err)
	}
	baseDomain, err := registrableDomain(base.Hostname())
	if err != nil {
		return nil, fmt.Errorf("registrable domain: %w", err)
	}

	res, err := fetch(ctx, client, cfg, cfg.InputURL)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("input URL returned %d", res.StatusCode)
	}

	doc, err := html.Parse(strings.NewReader(string(res.Body)))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}

	seen := make(map[string]struct{})
	var out []string

	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				ref, err := base.Parse(strings.TrimSpace(a.Val))
				if err != nil {
					continue
				}
				if ref.Scheme != "https" {
					continue
				}
				host := ref.Hostname()
				if host == "" {
					continue
				}
				dom, err := registrableDomain(host)
				if err != nil || dom == baseDomain {
					continue
				}
				// Normalize: scheme + host + path, drop fragment & query for dedup.
				ref.Fragment = ""
				ref.RawQuery = ""
				normalized := ref.Scheme + "://" + ref.Host
				if _, ok := seen[normalized]; ok {
					continue
				}
				seen[normalized] = struct{}{}
				out = append(out, normalized)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)

	sort.Strings(out)
	return out, nil
}

// registrableDomain returns the eTLD+1 (e.g. "blog.example.co.uk" -> "example.co.uk").
func registrableDomain(host string) (string, error) {
	if host == "" {
		return "", errors.New("empty host")
	}
	return publicsuffix.EffectiveTLDPlusOne(host)
}

// ─── Feed probing ────────────────────────────────────────────────────────────

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

// probeAll runs feed probing across todoURLs with bounded concurrency and
// per-host serialization (politeness).
func probeAll(ctx context.Context, client *http.Client, cfg *Config, todo []string) []Result {
	hostGate := newHostGate(cfg.HostDelay)
	parser := gofeed.NewParser()
	parser.UserAgent = cfg.UserAgent
	// gofeed's parser is goroutine-safe for ParseURL; we use ParseString instead
	// for explicit control over the HTTP layer.

	results := make([]Result, 0, len(todo))
	var mu sync.Mutex

	for start := 0; start < len(todo); start += cfg.BatchSize {
		end := min(start+cfg.BatchSize, len(todo))
		batch := todo[start:end]

		sem := make(chan struct{}, cfg.Concurrency)
		var wg sync.WaitGroup

		for _, u := range batch {
			select {
			case <-ctx.Done():
				return results
			default:
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(target string) {
				defer wg.Done()
				defer func() { <-sem }()
				r := probeOne(ctx, client, parser, cfg, target, hostGate)
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}(u)
		}
		wg.Wait()
		fmt.Fprintf(os.Stderr, "batch %d-%d/%d done\n", start, end, len(todo))
	}
	return results
}

// probeOne discovers a feed for target and evaluates the latest post.
func probeOne(ctx context.Context, client *http.Client, parser *gofeed.Parser, cfg *Config, target string, hg *hostGate) Result {
	now := time.Now()
	r := Result{URL: target, CheckedAt: &now}

	feedURL, feedBody, err := discoverFeed(ctx, client, cfg, target, hg)
	if err != nil {
		r.Status = StatusError
		r.Error = err.Error()
		return r
	}
	if feedURL == "" {
		r.Status = StatusNoFeed
		return r
	}
	r.FeedURL = feedURL

	feed, err := parser.ParseString(string(feedBody))
	if err != nil {
		r.Status = StatusError
		r.Error = fmt.Sprintf("parse feed: %v", err)
		return r
	}

	r.ItemCount = len(feed.Items)
	r.LatestPost = timePtr(latestItemDate(feed))
	// Status (active/stale) is set by applyFreshness after we know the cutoff.
	r.Status = StatusStale
	return r
}

// discoverFeed tries the page's <link rel="alternate"> first, then a fallback
// list of common paths. Returns the feed URL and raw bytes on success.
func discoverFeed(ctx context.Context, client *http.Client, cfg *Config, target string, hg *hostGate) (string, []byte, error) {
	hg.wait(target)
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
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		default:
		}
		hg.wait(c)
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

// feedLinksFromHTML extracts <link rel="alternate" type="application/rss+xml|atom+xml">
// hrefs from the HTML body, resolved against base.
func feedLinksFromHTML(base string, body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
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

// fallbackPathsFor returns the standard candidate URLs for a host's root.
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

// latestItemDate returns the most recent published/updated time across items.
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

// applyFreshness sets active/stale based on cutoff. Other statuses pass through.
func applyFreshness(r Result, cutoff time.Time) Result {
	if r.Status != StatusStale && r.Status != StatusActive {
		return r
	}
	if r.LatestPost != nil && r.LatestPost.After(cutoff) {
		r.Status = StatusActive
	} else {
		r.Status = StatusStale
	}
	return r
}

// ─── Per-host gate ───────────────────────────────────────────────────────────

// hostGate enforces a minimum delay between requests to the same host.
type hostGate struct {
	delay time.Duration
	mu    sync.Mutex
	last  map[string]time.Time
}

func newHostGate(delay time.Duration) *hostGate {
	return &hostGate{delay: delay, last: make(map[string]time.Time)}
}

func (g *hostGate) wait(rawURL string) {
	if g.delay <= 0 {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	host := u.Host

	g.mu.Lock()
	last := g.last[host]
	now := time.Now()
	wait := time.Duration(0)
	if !last.IsZero() {
		elapsed := now.Sub(last)
		if elapsed < g.delay {
			wait = g.delay - elapsed
		}
	}
	g.last[host] = now.Add(wait)
	g.mu.Unlock()

	if wait > 0 {
		time.Sleep(wait)
	}
}

// ─── Checkpoint ──────────────────────────────────────────────────────────────

type checkpoint struct {
	mu      sync.Mutex
	Entries map[string]Result `json:"entries"`
}

func loadCheckpoint(path string) (*checkpoint, error) {
	cp := &checkpoint{Entries: make(map[string]Result)}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cp, nil
		}
		return nil, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Printf("warning: checkpoint file close: %v", cerr)
		}
	}()
	if err := json.NewDecoder(f).Decode(cp); err != nil {
		// Corrupt checkpoint shouldn't crash the run; start fresh.
		log.Printf("warning: checkpoint unreadable (%v); starting fresh", err)
		return &checkpoint{Entries: make(map[string]Result)}, nil
	}
	if cp.Entries == nil {
		cp.Entries = make(map[string]Result)
	}
	return cp, nil
}

func (c *checkpoint) get(u string, now time.Time, ttl time.Duration) (Result, bool) {
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

func (c *checkpoint) put(r Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Entries[r.URL] = r
}

// save writes atomically: write to temp file then rename.
func (c *checkpoint) save(path string) error {
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
		if cerr := tmp.Close(); cerr != nil {
			log.Printf("warning: checkpoint temp close after encode error: %v", cerr)
		}
		if rerr := os.Remove(tmp.Name()); rerr != nil {
			log.Printf("warning: checkpoint temp remove after encode error: %v", rerr)
		}
		return err
	}
	if err := tmp.Close(); err != nil {
		if rerr := os.Remove(tmp.Name()); rerr != nil {
			log.Printf("warning: checkpoint temp remove after close error: %v", rerr)
		}
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ─── Output ──────────────────────────────────────────────────────────────────

func emit(format string, results []Result) error {
	switch format {
	case "table":
		return emitTable(results)
	case "json", "":
		return emitJSON(results)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}

func emitJSON(results []Result) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func emitTable(results []Result) error {
	fmt.Printf("%-60s  %-10s  %-25s  %s\n", "URL", "STATUS", "LATEST", "FEED")
	fmt.Println(strings.Repeat("-", 130))
	for _, r := range results {
		latest := ""
		if r.LatestPost != nil {
			latest = r.LatestPost.Format(time.RFC3339)
		}
		fmt.Printf("%-60s  %-10s  %-25s  %s\n", truncate(r.URL, 60), r.Status, latest, r.FeedURL)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ─── Sorting ─────────────────────────────────────────────────────────────────

// sortLessFns is the registry of supported -sort-by fields. Each function
// returns true when a should sort before b in ascending order; descending is
// applied by swapping arguments at the call site.
//
// Nil time pointers are treated as the zero value so they consistently land at
// the "oldest" end regardless of order direction.
var sortLessFns = map[string]func(a, b Result) bool{
	"url":         func(a, b Result) bool { return a.URL < b.URL },
	"status":      func(a, b Result) bool { return string(a.Status) < string(b.Status) },
	"item_count":  func(a, b Result) bool { return a.ItemCount < b.ItemCount },
	"latest_post": func(a, b Result) bool { return derefTime(a.LatestPost).Before(derefTime(b.LatestPost)) },
	"checked_at":  func(a, b Result) bool { return derefTime(a.CheckedAt).Before(derefTime(b.CheckedAt)) },
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// sortResults sorts in place. Stable so equal keys preserve input order.
func sortResults(results []Result, by, order string) {
	less, ok := sortLessFns[by]
	if !ok {
		return
	}
	sort.SliceStable(results, func(i, j int) bool {
		if order == "asc" {
			return less(results[i], results[j])
		}
		return less(results[j], results[i])
	})
}

// urlsAsResults wraps a list of URLs as bare Result entries for dry-run output.
func urlsAsResults(urls []string) []Result {
	out := make([]Result, len(urls))
	for i, u := range urls {
		out[i] = Result{URL: u}
	}
	return out
}

// ─── Dry-run summary ─────────────────────────────────────────────────────────

// DomainCount is one entry in the top-N domain breakdown.
type DomainCount struct {
	Domain string `json:"domain"`
	Count  int    `json:"count"`
}

// DryRunSummary aggregates extracted URLs by registrable domain so non-verbose
// dry-run output shows shape, not noise.
type DryRunSummary struct {
	TotalURLs     int           `json:"total_urls"`
	UniqueDomains int           `json:"unique_domains"`
	TopDomains    []DomainCount `json:"top_domains"`
}

// dryRunTopN caps how many top domains the summary prints.
const dryRunTopN = 3

// summarizeDryRun groups urls by registrable domain and returns a top-N summary.
func summarizeDryRun(urls []string) DryRunSummary {
	counts := make(map[string]int)
	for _, u := range urls {
		parsed, err := url.Parse(u)
		if err != nil {
			continue
		}
		dom, err := registrableDomain(parsed.Hostname())
		if err != nil {
			continue
		}
		counts[dom]++
	}
	domains := make([]DomainCount, 0, len(counts))
	for d, c := range counts {
		domains = append(domains, DomainCount{Domain: d, Count: c})
	}
	sort.Slice(domains, func(i, j int) bool {
		if domains[i].Count != domains[j].Count {
			return domains[i].Count > domains[j].Count
		}
		return domains[i].Domain < domains[j].Domain
	})
	top := domains
	if len(top) > dryRunTopN {
		top = top[:dryRunTopN]
	}
	return DryRunSummary{
		TotalURLs:     len(urls),
		UniqueDomains: len(counts),
		TopDomains:    top,
	}
}

// emitDryRunSummary prints the aggregated dry-run summary in the chosen format.
func emitDryRunSummary(format string, urls []string) error {
	s := summarizeDryRun(urls)
	switch format {
	case "table":
		fmt.Printf("Total URLs:     %d\n", s.TotalURLs)
		fmt.Printf("Unique domains: %d\n", s.UniqueDomains)
		if len(s.TopDomains) > 0 {
			fmt.Printf("\nTop %d domains:\n", len(s.TopDomains))
			for _, d := range s.TopDomains {
				fmt.Printf("  %5d  %s\n", d.Count, d.Domain)
			}
		}
		fmt.Fprintln(os.Stderr, "\n(use -verbose to print all URLs)")
		return nil
	case "json", "":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}
