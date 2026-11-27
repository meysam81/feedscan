package scanner

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/mmcdole/gofeed"

	"feedscan/internal/checkpoint"
)

// FetchPosts fetches every feed URL once and returns the posts of all of them
// in no particular order, plus how many feeds could not be fetched or parsed.
// A failed feed is skipped, never fatal: with hundreds of third-party feeds
// some are always down. It does no feed discovery; feedURLs come from an
// earlier scan, and one that is not https counts as failed without a request. cfg supplies Timeout (via client), Concurrency, UserAgent,
// MaxBodyBytes, HostDelay and MaxPosts (the per-feed cap).
//
// Once ctx is canceled no new fetch starts; the caller checks ctx.Err() to
// tell a complete result from an interrupted one.
func FetchPosts(ctx context.Context, client *http.Client, cfg *Config, feedURLs []string) (posts []checkpoint.Post, failed int) {
	hg := newHostGate(cfg.HostDelay)
	parser := gofeed.NewParser()
	sem := make(chan struct{}, cfg.Concurrency)
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, u := range feedURLs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(feedURL string) {
			defer wg.Done()
			defer func() { <-sem }()
			got, err := fetchFeedPosts(ctx, client, parser, cfg, feedURL, hg)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				return
			}
			posts = append(posts, got...)
		}(u)
	}
	wg.Wait()
	return posts, failed
}

// fetchFeedPosts fetches and parses one feed.
func fetchFeedPosts(ctx context.Context, client *http.Client, parser *gofeed.Parser, cfg *Config, feedURL string, hg *hostGate) ([]checkpoint.Post, error) {
	// The URL comes from a checkpoint file, which may not be one scan wrote.
	// scan only ever stores https feeds; anything else is not fetched.
	if !strings.HasPrefix(feedURL, "https://") {
		return nil, fmt.Errorf("fetch %s: not an https URL", feedURL)
	}
	if err := hg.wait(ctx, feedURL); err != nil {
		return nil, err
	}
	res, err := fetch(ctx, client, cfg, feedURL)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch %s: HTTP %d", feedURL, res.StatusCode)
	}
	feed, err := parser.ParseString(string(res.Body))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", feedURL, err)
	}
	return postsFrom(feed, res.FinalURL, cfg.MaxPosts), nil
}
