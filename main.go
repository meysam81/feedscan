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
	StatusActive Status = "active"   // feed found, fresh entry within window
	StatusStale  Status = "stale"    // feed found, no fresh entry within window
	StatusNoFeed Status = "no_feed"  // probed all candidates, no feed found
	StatusError  Status = "error"    // network or parse error
)

// Result is the per-URL outcome, also used as the checkpoint cache value.
type Result struct {
	URL         string    `json:"url"`
	Status      Status    `json:"status"`
	FeedURL     string    `json:"feed_url,omitempty"`
	LatestPost  time.Time `json:"latest_post,omitempty"`
	ItemCount   int       `json:"item_count,omitempty"`
	Error       string    `json:"error,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
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
	Format         string // "json" or "table"
}

func main() {
	cfg, err := parseFlags()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := run(ctx, cfg); err != nil {
		log.Fatalf("run: %v", err)
	}
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
	flag.StringVar(&cfg.Format, "format", "json", "output format: json|table")
	flag.Parse()

	if cfg.InputURL == "" {
		flag.Usage()
		return nil, errors.New("-url is required")
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

	if cfg.DryRun {
		fmt.Fprintf(os.Stderr, "found %d external URLs (dry run; remove -dry-run to probe feeds)\n", len(urls))
		return emit(cfg.Format, urlsAsResults(urls))
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

// fetch performs a GET with the configured UA and returns the body, capped at
// MaxBodyBytes. Caller must close the returned body.
func fetch(ctx context.Context, client *http.Client, cfg *Config, rawURL string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml,application/rss+xml,application/atom+xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cfg.MaxBodyBytes))
	resp.Body.Close()
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
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

	resp, body, err := fetch(ctx, client, cfg, cfg.InputURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("input URL returned %d", resp.StatusCode)
	}

	doc, err := html.Parse(strings.NewReader(string(body)))
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
		end := start + cfg.BatchSize
		if end > len(todo) {
			end = len(todo)
		}
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
	r := Result{URL: target, CheckedAt: time.Now()}

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
	r.LatestPost = latestItemDate(feed)
	// Status (active/stale) is set by applyFreshness after we know the cutoff.
	r.Status = StatusStale
	return r
}

// discoverFeed tries the page's <link rel="alternate"> first, then a fallback
// list of common paths. Returns the feed URL and raw bytes on success.
func discoverFeed(ctx context.Context, client *http.Client, cfg *Config, target string, hg *hostGate) (string, []byte, error) {
	hg.wait(target)
	resp, body, err := fetch(ctx, client, cfg, target)
	if err != nil {
		return "", nil, fmt.Errorf("fetch homepage: %w", err)
	}
	if resp.StatusCode >= 400 {
		// Don't give up — we can still try fallback paths.
		body = nil
	}

	candidates := feedLinksFromHTML(target, body)
	candidates = append(candidates, fallbackPathsFor(target)...)

	for _, c := range candidates {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		default:
		}
		hg.wait(c)
		fr, fb, err := fetch(ctx, client, cfg, c)
		if err != nil {
			continue
		}
		if fr.StatusCode >= 400 || len(fb) == 0 {
			continue
		}
		if looksLikeFeed(fb, fr.Header.Get("Content-Type")) {
			return c, fb, nil
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
	if !r.LatestPost.IsZero() && r.LatestPost.After(cutoff) {
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
	defer f.Close()
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
	if now.Sub(r.CheckedAt) > ttl {
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
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
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
		if !r.LatestPost.IsZero() {
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

// urlsAsResults wraps a list of URLs as bare Result entries for dry-run output.
func urlsAsResults(urls []string) []Result {
	out := make([]Result, len(urls))
	for i, u := range urls {
		out[i] = Result{URL: u}
	}
	return out
}
