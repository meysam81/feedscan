package report

import (
	"testing"
	"time"

	"feedscan/internal/checkpoint"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestSortByLatestPostDesc(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := []checkpoint.Result{
		{URL: "b", LatestPost: ptrTime(now.Add(-1 * time.Hour))},
		{URL: "a", LatestPost: ptrTime(now)},
		{URL: "c"}, // nil — sorts to bottom on desc
	}
	Sort(in, "latest_post", "desc")
	if in[0].URL != "a" || in[1].URL != "b" || in[2].URL != "c" {
		t.Fatalf("unexpected order: %+v", in)
	}
}

func TestSortByLatestPostAsc(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := []checkpoint.Result{
		{URL: "a", LatestPost: ptrTime(now)},
		{URL: "b", LatestPost: ptrTime(now.Add(-1 * time.Hour))},
		{URL: "c"}, // nil — zero time, sorts to top on asc (oldest)
	}
	Sort(in, "latest_post", "asc")
	// Order: nil/c (zero) first, then b, then a.
	if in[0].URL != "c" || in[1].URL != "b" || in[2].URL != "a" {
		t.Fatalf("unexpected order: %+v", in)
	}
}

func TestSortByURLAsc(t *testing.T) {
	t.Parallel()
	in := []checkpoint.Result{{URL: "c"}, {URL: "a"}, {URL: "b"}}
	Sort(in, "url", "asc")
	if in[0].URL != "a" || in[1].URL != "b" || in[2].URL != "c" {
		t.Fatalf("unexpected order: %+v", in)
	}
}

func TestSortUnknownFieldIsNoop(t *testing.T) {
	t.Parallel()
	in := []checkpoint.Result{{URL: "c"}, {URL: "a"}, {URL: "b"}}
	Sort(in, "bogus", "asc")
	if in[0].URL != "c" {
		t.Fatalf("expected no-op, got %+v", in)
	}
}

func TestSummarizeProbeCountsAndTopN(t *testing.T) {
	t.Parallel()
	in := []checkpoint.Result{
		{URL: "a", Status: checkpoint.StatusActive},
		{URL: "b", Status: checkpoint.StatusActive},
		{URL: "c", Status: checkpoint.StatusStale},
		{URL: "d", Status: checkpoint.StatusNoFeed},
		{URL: "e", Status: checkpoint.StatusError},
	}
	s := SummarizeProbe(in)
	if s.Total != 5 {
		t.Errorf("total: %d", s.Total)
	}
	if s.StatusCount[checkpoint.StatusActive] != 2 {
		t.Errorf("active: %d", s.StatusCount[checkpoint.StatusActive])
	}
	if len(s.TopResults) != 3 {
		t.Errorf("top: %d", len(s.TopResults))
	}
}

func TestURLsAsResults(t *testing.T) {
	t.Parallel()
	in := []string{"https://a", "https://b"}
	out := URLsAsResults(in)
	if len(out) != 2 || out[0].URL != "https://a" || out[1].URL != "https://b" {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestSortFields(t *testing.T) {
	t.Parallel()
	got := SortFields()
	if len(got) == 0 {
		t.Fatal("expected non-empty field list")
	}
	for _, f := range got {
		if _, ok := SortLessFns[f]; !ok {
			t.Errorf("SortFields lists %q but SortLessFns lacks it", f)
		}
	}
}
