package report

import (
	"sort"
	"time"

	"feedscan/internal/checkpoint"
)

// SortLessFns is the registry of supported sort-by fields. Each function
// returns true when a should sort before b in ascending order; descending is
// applied by swapping arguments at the call site.
//
// Nil time pointers are treated as the zero value. As a consequence, asc
// places nils first (oldest) and desc places nils last. Callers wanting a
// different placement should filter first.
var SortLessFns = map[string]func(a, b checkpoint.Result) bool{
	"url":         func(a, b checkpoint.Result) bool { return a.URL < b.URL },
	"status":      func(a, b checkpoint.Result) bool { return string(a.Status) < string(b.Status) },
	"item_count":  func(a, b checkpoint.Result) bool { return a.ItemCount < b.ItemCount },
	"latest_post": func(a, b checkpoint.Result) bool { return derefTime(a.LatestPost).Before(derefTime(b.LatestPost)) },
	"checked_at":  func(a, b checkpoint.Result) bool { return derefTime(a.CheckedAt).Before(derefTime(b.CheckedAt)) },
}

// SortFields returns the supported sort-by keys in stable order, for help text.
func SortFields() []string {
	return []string{"url", "status", "latest_post", "item_count", "checked_at"}
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// Sort sorts in place. Stable so equal keys preserve input order. Unknown
// fields are silently ignored to keep callers simple — validate in the CLI.
func Sort(results []checkpoint.Result, by, order string) {
	less, ok := SortLessFns[by]
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
