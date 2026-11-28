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

// FeedRequest names one feed to fetch. ETag and LastModified are the
// validators of the copy the caller already holds, if any: with them the
// server may answer "not modified" instead of sending the feed again.
type FeedRequest struct {
	URL          string
	ETag         string
	LastModified string
}

// FeedResult is the outcome for one FeedRequest. Exactly one of these holds:
// Err is set (nothing else is), NotModified is true (the caller's copy is
// current), or Posts with the feed's new validators are filled in.
type FeedResult struct {
	URL          string
	Posts        []checkpoint.Post
	ETag         string
	LastModified string
	NotModified  bool
	Err          error
}

// FetchFeeds fetches every requested feed once and returns one result per
// request, in the same order. A failed feed is reported in its result, never
// fatal: with hundreds of third-party feeds some are always down. It does no
// feed discovery; the URLs come from an earlier scan, and one that is not
// https fails without a request. cfg supplies Timeout (via client),
// Concurrency, UserAgent, MaxBodyBytes, HostDelay and MaxPosts (the per-feed
// cap). onDone, if not nil, is called as each feed finishes, from several
// goroutines.
//
// Once ctx is canceled no new fetch starts; the caller checks ctx.Err() to
// tell a complete result from an interrupted one.
func FetchFeeds(ctx context.Context, client *http.Client, cfg *Config, reqs []FeedRequest, onDone func()) []FeedResult {
	hg := newHostGate(cfg.HostDelay)
	parser := gofeed.NewParser()
	sem := make(chan struct{}, cfg.Concurrency)
	results := make([]FeedResult, len(reqs))
	var wg sync.WaitGroup
	for i, req := range reqs {
		if err := ctx.Err(); err != nil {
			results[i] = FeedResult{URL: req.URL, Err: err}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = fetchFeed(ctx, client, parser, cfg, req, hg) // each goroutine owns its slot
			if onDone != nil {
				onDone()
			}
		}()
	}
	wg.Wait()
	return results
}

// fetchFeed fetches and parses one feed.
func fetchFeed(ctx context.Context, client *http.Client, parser *gofeed.Parser, cfg *Config, req FeedRequest, hg *hostGate) FeedResult {
	failed := func(err error) FeedResult { return FeedResult{URL: req.URL, Err: err} }
	// The URL comes from a checkpoint file, which may not be one scan wrote.
	// scan only ever stores https feeds; anything else is not fetched.
	if !strings.HasPrefix(req.URL, "https://") {
		return failed(fmt.Errorf("fetch %s: not an https URL", req.URL))
	}
	if err := hg.wait(ctx, req.URL); err != nil {
		return failed(err)
	}
	validators := http.Header{}
	if req.ETag != "" {
		validators.Set("If-None-Match", req.ETag)
	}
	if req.LastModified != "" {
		validators.Set("If-Modified-Since", req.LastModified)
	}
	res, err := fetchWith(ctx, client, cfg, req.URL, validators)
	if err != nil {
		return failed(err)
	}
	// Only a server we sent validators to can mean it: an unprompted 304 has
	// no copy behind it, and falls through to the parse error it deserves.
	if res.StatusCode == http.StatusNotModified && len(validators) > 0 {
		return FeedResult{URL: req.URL, NotModified: true}
	}
	if res.StatusCode >= 400 {
		return failed(fmt.Errorf("fetch %s: HTTP %d", req.URL, res.StatusCode))
	}
	feed, err := parser.ParseString(string(res.Body))
	if err != nil {
		return failed(fmt.Errorf("parse %s: %w", req.URL, err))
	}
	return FeedResult{
		URL:          req.URL,
		Posts:        postsFrom(feed, res.FinalURL, cfg.MaxPosts),
		ETag:         res.ETag,
		LastModified: res.LastModified,
	}
}
