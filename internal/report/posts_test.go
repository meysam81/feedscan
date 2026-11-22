package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"feedscan/internal/checkpoint"
)

func TestSelectPosts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	const day = 24 * time.Hour

	posts := []checkpoint.Post{
		{Title: "week old", URL: "https://a.example/week", Date: ago(7 * day)},
		{Title: "today, as an older scan saw it", URL: "https://a.example/today", Date: ago(3 * time.Hour)},
		{Title: "today", URL: "https://a.example/today", Date: ago(time.Hour)},
		{Title: "tie b", URL: "https://b.example/tie", Date: ago(2 * day)},
		{Title: "tie a", URL: "https://a.example/tie", Date: ago(2 * day)},
		{Title: "few hours ahead (timezone slack)", URL: "https://a.example/soon", Date: now.Add(3 * time.Hour)},
		{Title: "year 2099", URL: "https://a.example/bogus", Date: now.Add(365 * day)},
	}
	urls := func(ps []checkpoint.Post) string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = strings.TrimPrefix(p.URL, "https://")
		}
		return strings.Join(out, " ")
	}

	tests := []struct {
		name        string
		days, limit int
		want        string
	}{
		{"all: newest first, deduped, bogus future dropped", 0, 0, "a.example/soon a.example/today a.example/tie b.example/tie a.example/week"},
		{"limit", 0, 2, "a.example/soon a.example/today"},
		{"days", 3, 0, "a.example/soon a.example/today a.example/tie b.example/tie"},
		{"days and limit", 3, 3, "a.example/soon a.example/today a.example/tie"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := urls(SelectPosts(posts, now, tt.days, tt.limit)); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}

	if kept := SelectPosts(posts, now, 0, 0)[1]; kept.Title != "today" {
		t.Errorf("of two copies of a URL the newest must win whatever the input order, got %q", kept.Title)
	}
}

func TestWritePostsTable(t *testing.T) {
	t.Parallel()
	posts := []checkpoint.Post{
		{Title: "Short title", URL: "https://a.example/short", Date: time.Date(2026, 11, 21, 23, 59, 0, 0, time.UTC)},
		{Title: strings.Repeat("long ", 20), URL: "http://b.example/" + strings.Repeat("x", 60), Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		// 01:00 on Jan 1 in UTC+9 is still Dec 31 in UTC, which is what's shown.
		{URL: "https://c.example/untitled", Date: time.Date(2026, 1, 1, 1, 0, 0, 0, time.FixedZone("JST", 9*3600))},
	}
	var buf bytes.Buffer
	if err := WritePosts(&buf, "table", posts); err != nil {
		t.Fatalf("WritePosts: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 5 { // header, separator, 3 rows
		t.Fatalf("got %d lines, want 5:\n%s", len(lines), buf.String())
	}
	if f := strings.Fields(lines[0]); strings.Join(f, " ") != "TITLE URL DATE" {
		t.Errorf("header: %q", lines[0])
	}
	if want := "Short title" + strings.Repeat(" ", 41) + "a.example/short" + strings.Repeat(" ", 27) + "2026-11-21"; lines[2] != want {
		t.Errorf("row 1:\ngot  %q\nwant %q", lines[2], want)
	}
	if !strings.Contains(lines[3], "long long…  b.example/") || !strings.HasSuffix(lines[3], "xxx…  2026-01-02") {
		t.Errorf("row 2 not truncated as expected: %q", lines[3])
	}
	if got := len([]rune(lines[3])); got != 50+2+40+2+10 {
		t.Errorf("row 2 width: got %d runes, want 104", got)
	}
	if !strings.HasPrefix(lines[4], "(untitled)") || !strings.HasSuffix(lines[4], "2025-12-31") {
		t.Errorf("row 3 should fall back to (untitled): %q", lines[4])
	}
}

func TestWritePostsJSON(t *testing.T) {
	t.Parallel()
	long := "https://a.example/" + strings.Repeat("x", 80)
	posts := []checkpoint.Post{{Title: strings.Repeat("t", 80), URL: long, Date: time.Date(2026, 11, 21, 8, 30, 0, 0, time.UTC)}}

	var buf bytes.Buffer
	if err := WritePosts(&buf, "json", posts); err != nil {
		t.Fatalf("WritePosts: %v", err)
	}
	var got []checkpoint.Post
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0] != posts[0] {
		t.Errorf("JSON must carry posts untruncated: %+v", got)
	}

	buf.Reset()
	if err := WritePosts(&buf, "json", nil); err != nil {
		t.Fatalf("WritePosts: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("empty list: got %q, want []", got)
	}

	if err := WritePosts(&buf, "xml", nil); err == nil {
		t.Error("expected an error for an unknown format")
	}
}
