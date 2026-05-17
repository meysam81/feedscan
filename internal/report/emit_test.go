package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"feedscan/internal/checkpoint"
)

func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a long string", 10, "this is a…"},
		{"x", 1, "x"},
		{"abc", 0, ""},
	}
	for _, tt := range tests {
		if got := Truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestFormatTimePtr(t *testing.T) {
	t.Parallel()
	if got := FormatTimePtr(nil); got != "" {
		t.Errorf("nil → %q, want empty", got)
	}
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	got := FormatTimePtr(&now)
	if got != "2026-05-17T12:00:00Z" {
		t.Errorf("got %q", got)
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	type p struct {
		Name string `json:"name"`
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, p{Name: "test"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got p
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if got.Name != "test" {
		t.Errorf("name = %q", got.Name)
	}
}

func TestWriteByStatus(t *testing.T) {
	t.Parallel()
	counts := map[checkpoint.Status]int{
		checkpoint.StatusActive: 3,
		checkpoint.StatusError:  1,
	}
	var buf bytes.Buffer
	if err := WriteByStatus(&buf, counts); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.String()
	// Canonical order: active, then error (stale and no_feed missing).
	if !strings.Contains(out, "active") || !strings.Contains(out, "error") {
		t.Errorf("missing statuses: %s", out)
	}
	if strings.Index(out, "active") > strings.Index(out, "error") {
		t.Errorf("expected active before error: %s", out)
	}
}

func TestWriteByStatusEmpty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteByStatus(&buf, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output, got: %s", buf.String())
	}
}

func TestWriteTableFullColumns(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	results := []checkpoint.Result{
		{URL: "https://a", Status: checkpoint.StatusActive, FeedURL: "https://a/feed", LatestPost: &now, VisitedAt: &now},
	}
	var buf bytes.Buffer
	if err := WriteTable(&buf, FullColumns(), results); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"URL", "STATUS", "LATEST", "VISITED", "FEED", "https://a", "active", "https://a/feed"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in: %s", want, out)
		}
	}
}

func TestWriteTableCursorColumnsOmitsFeed(t *testing.T) {
	t.Parallel()
	results := []checkpoint.Result{
		{URL: "https://a", Status: checkpoint.StatusActive, FeedURL: "https://a/feed"},
	}
	var buf bytes.Buffer
	if err := WriteTable(&buf, CursorColumns(), results); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "FEED") || strings.Contains(out, "https://a/feed") {
		t.Errorf("cursor columns should omit FEED, got: %s", out)
	}
}

func TestEmitResultsUnknownFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := EmitResults(&buf, "yaml", nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteDryRunSummaryJSON(t *testing.T) {
	t.Parallel()
	s := DryRunSummary{
		TotalURLs:     5,
		UniqueDomains: 3,
		TopDomains:    []DomainCount{{Domain: "example.com", Count: 2}},
	}
	var buf bytes.Buffer
	if err := WriteDryRunSummary(&buf, "json", s); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got DryRunSummary
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if got.TotalURLs != 5 || len(got.TopDomains) != 1 {
		t.Errorf("decoded summary: %+v", got)
	}
}

func TestWriteDryRunSummaryTable(t *testing.T) {
	t.Parallel()
	s := DryRunSummary{
		TotalURLs:     5,
		UniqueDomains: 3,
		TopDomains:    []DomainCount{{Domain: "example.com", Count: 2}},
	}
	var buf bytes.Buffer
	if err := WriteDryRunSummary(&buf, "table", s); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Total URLs:     5", "Unique domains: 3", "example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in: %s", want, out)
		}
	}
}

func TestWriteProbeSummaryJSON(t *testing.T) {
	t.Parallel()
	s := ProbeSummary{
		Total:       2,
		StatusCount: map[checkpoint.Status]int{checkpoint.StatusActive: 1, checkpoint.StatusStale: 1},
		TopResults:  []checkpoint.Result{{URL: "https://a"}},
	}
	var buf bytes.Buffer
	if err := WriteProbeSummary(&buf, "json", s); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got ProbeSummary
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if got.Total != 2 || got.StatusCount[checkpoint.StatusActive] != 1 {
		t.Errorf("decoded: %+v", got)
	}
}

func TestSummarizeDryRunWithStubResolver(t *testing.T) {
	t.Parallel()
	urls := []string{
		"https://a.example.com",
		"https://b.example.com",
		"https://other.com",
	}
	// Resolver that returns eTLD+1 manually.
	resolver := func(host string) (string, error) {
		if strings.HasSuffix(host, ".example.com") || host == "example.com" {
			return "example.com", nil
		}
		return host, nil
	}
	s := SummarizeDryRun(urls, resolver)
	if s.TotalURLs != 3 {
		t.Errorf("total: %d", s.TotalURLs)
	}
	if s.UniqueDomains != 2 {
		t.Errorf("unique: %d, want 2", s.UniqueDomains)
	}
	if len(s.TopDomains) != 2 || s.TopDomains[0].Domain != "example.com" || s.TopDomains[0].Count != 2 {
		t.Errorf("top domains: %+v", s.TopDomains)
	}
}
