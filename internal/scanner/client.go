package scanner

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// NewHTTPClient builds the shared HTTP client for fetching pages and feeds.
func NewHTTPClient(cfg *Config) *http.Client {
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
	}
}

// fetchResult is what callers actually need — status, content type, body.
// The HTTP response body is read and closed inside fetch.
type fetchResult struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// fetch performs a GET with the configured UA and returns the body capped at
// MaxBodyBytes.
func fetch(ctx context.Context, client *http.Client, cfg *Config, rawURL string) (res *fetchResult, err error) {
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
			err = errors.Join(err, fmt.Errorf("close body %s: %w", rawURL, cerr))
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
