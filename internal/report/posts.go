package report

import (
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"feedscan/internal/checkpoint"
)

// futureSlack is how far ahead of now a post may be dated before it's treated
// as bogus (scheduled post, broken clock) and hidden. A day covers timezones.
const futureSlack = 24 * time.Hour

// ExcludeHosts drops the posts published on any of hosts or on a subdomain of
// one: "example.com" also covers "www.example.com", never "notexample.com".
// Matching ignores case; an entry pasted as a URL counts by its host.
func ExcludeHosts(posts []checkpoint.Post, hosts []string) []checkpoint.Post {
	var excluded []string
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if _, rest, ok := strings.Cut(h, "://"); ok {
			h = rest
		}
		if i := strings.IndexAny(h, "/:"); i >= 0 { // path or port
			h = h[:i]
		}
		if h != "" {
			excluded = append(excluded, h)
		}
	}
	if len(excluded) == 0 {
		return posts
	}

	out := make([]checkpoint.Post, 0, len(posts))
	for _, p := range posts {
		host := ""
		if u, err := url.Parse(p.URL); err == nil {
			host = strings.ToLower(u.Hostname())
		}
		skip := false
		for _, h := range excluded {
			if host == h || strings.HasSuffix(host, "."+h) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, p)
		}
	}
	return out
}

// SelectPosts turns the merged posts of one or more checkpoints into the list
// to show: de-duplicated by URL, newest first, without future-dated posts.
// The input slice is left untouched.
// days > 0 keeps only posts from the last days days; limit > 0 caps the list.
func SelectPosts(posts []checkpoint.Post, now time.Time, days, limit int) []checkpoint.Post {
	latest := now.Add(futureSlack)
	var earliest time.Time
	if days > 0 {
		earliest = now.Add(-time.Duration(days) * 24 * time.Hour)
	}

	// Sort before de-duplicating: checkpoints arrive in map order, and two of
	// them may disagree on a URL's title or date. The newest copy wins, always.
	sorted := append([]checkpoint.Post(nil), posts...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if !a.Date.Equal(b.Date) {
			return a.Date.After(b.Date)
		}
		if a.URL != b.URL {
			return a.URL < b.URL
		}
		return a.Title < b.Title
	})

	seen := make(map[string]struct{}, len(sorted))
	out := make([]checkpoint.Post, 0, len(sorted))
	for _, p := range sorted {
		if p.Date.After(latest) || p.Date.Before(earliest) {
			continue
		}
		if _, dup := seen[p.URL]; dup {
			continue
		}
		seen[p.URL] = struct{}{}
		out = append(out, p)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

const (
	postTitleWidth = 50
	postURLWidth   = 40
)

// ponytail: widths count runes, so double-width (CJK) titles misalign their
// row; switch to a display-width library if that ever matters.
var postColumns = []Column[checkpoint.Post]{
	{Name: "TITLE", Width: postTitleWidth, Get: func(p checkpoint.Post) string {
		if p.Title == "" {
			return "(untitled)"
		}
		return Truncate(p.Title, postTitleWidth)
	}},
	{Name: "URL", Width: postURLWidth, Get: func(p checkpoint.Post) string {
		u := strings.TrimPrefix(strings.TrimPrefix(p.URL, "https://"), "http://")
		return Truncate(u, postURLWidth)
	}},
	{Name: "DATE", Width: len(time.DateOnly), Get: func(p checkpoint.Post) string { return p.Date.UTC().Format(time.DateOnly) }},
}

// WritePosts writes posts in the chosen format. The table shortens titles and
// URLs to fit; JSON carries them in full.
func WritePosts(w io.Writer, format string, posts []checkpoint.Post) error {
	switch strings.ToLower(format) {
	case "table":
		return WriteTable(w, postColumns, posts)
	case "json", "":
		if posts == nil {
			posts = []checkpoint.Post{} // "[]", not "null"
		}
		return WriteJSON(w, posts)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}
