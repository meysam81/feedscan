package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"feedscan/internal/checkpoint"
)

// The root command lists posts and owns the posts flags, some of which share
// a name with scan/cursor flags. This pins both halves: the bare command takes
// flags, and the subcommands still get their own.
func TestRootListsPostsAndSubcommandsKeepTheirFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.checkpoint.json")
	now := time.Now()
	cp := checkpoint.New()
	cp.Put(checkpoint.Result{URL: "https://a.example", CheckedAt: &now, Posts: []checkpoint.Post{
		{Title: "Newer", URL: "https://a.example/newer", Date: now.Add(-time.Hour)},
		{Title: "Older", URL: "https://a.example/older", Date: now.Add(-48 * time.Hour)},
	}})
	if err := cp.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	var out bytes.Buffer
	root := newRoot()
	root.Writer = &out
	if err := root.Run(context.Background(), []string{"feedscan", "--cached", "--checkpoint", path, "-n", "1"}); err != nil {
		t.Fatalf("bare feedscan with flags: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "Newer") || strings.Contains(got, "Older") {
		t.Errorf("want only the newest post, got:\n%s", got)
	}

	// scan's own --days/--format must win over the root's: a validation error
	// from scan (not a flag-parsing one) proves they reached it.
	err := newRoot().Run(context.Background(), []string{"feedscan", "scan", "--days", "0", "--format", "json", "--checkpoint", path})
	if err == nil || !strings.Contains(err.Error(), "input URL is required") || !strings.Contains(err.Error(), "days must be >= 1") {
		t.Errorf("scan did not receive its own flags: %v", err)
	}

	if err := newRoot().Run(context.Background(), []string{"feedscan", "cursor", "--checkpoint", path, "--format", "json", "stats"}); err != nil {
		t.Errorf("cursor with its own --checkpoint/--format: %v", err)
	}

	if err := newRoot().Run(context.Background(), []string{"feedscan", "bogus"}); err == nil {
		t.Error("an unknown subcommand must be an error, not a posts listing")
	}
}
