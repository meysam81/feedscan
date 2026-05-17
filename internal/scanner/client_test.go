package scanner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchRespectsMaxBodyBytes(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Send 100 bytes; we'll cap at 32.
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	t.Cleanup(srv.Close)

	cfg := &Config{
		Timeout:      2 * time.Second,
		UserAgent:    "test/1.0",
		MaxBodyBytes: 32,
	}
	res, err := fetch(context.Background(), NewHTTPClient(cfg), cfg, srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(res.Body) != 32 {
		t.Errorf("body length = %d, want 32", len(res.Body))
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(res.ContentType, "text/plain") {
		t.Errorf("content type = %q, want text/plain", res.ContentType)
	}
}

func TestFetchSetsUserAgent(t *testing.T) {
	t.Parallel()
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
	}))
	t.Cleanup(srv.Close)

	cfg := &Config{Timeout: 2 * time.Second, UserAgent: "feedscan-test/9.9", MaxBodyBytes: 1024}
	if _, err := fetch(context.Background(), NewHTTPClient(cfg), cfg, srv.URL); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotUA != "feedscan-test/9.9" {
		t.Errorf("UA = %q, want feedscan-test/9.9", gotUA)
	}
}

func TestFetchCtxCancel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)

	cfg := &Config{Timeout: 2 * time.Second, UserAgent: "t", MaxBodyBytes: 1024}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := fetch(ctx, NewHTTPClient(cfg), cfg, srv.URL)
	if err == nil {
		t.Fatal("expected error on ctx cancel")
	}
}
