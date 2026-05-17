// Package report owns scan output formatting: emitters, sort, and summaries.
// All output functions take io.Writer so callers can test or redirect freely.
// This package has no dependency on scanner or external resolvers — domain
// extraction is the caller's job.
package report

import (
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	json "github.com/goccy/go-json"

	"feedscan/internal/checkpoint"
)

// ─── Formatting helpers (shared with cmd/cursor and cmd/scan) ───────────────

// Truncate returns s clamped to n runes, with an ellipsis for truncation.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// FormatTimePtr returns t in RFC3339 or "" if nil. Used by every table cell
// that displays an optional timestamp.
func FormatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// WriteJSON emits v as pretty-printed JSON.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteByStatus prints a "By status:" block in canonical Status order. Empty
// buckets are skipped.
func WriteByStatus(w io.Writer, counts map[checkpoint.Status]int) error {
	if len(counts) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\nBy status:"); err != nil {
		return err
	}
	for _, st := range checkpoint.AllStatuses() {
		n, ok := counts[st]
		if !ok {
			continue
		}
		if _, err := fmt.Fprintf(w, "  %-8s %d\n", st, n); err != nil {
			return err
		}
	}
	return nil
}

// ─── Table renderer ─────────────────────────────────────────────────────────

// Column describes one column of a Result table. Width is the field-width for
// the printf format (negative would left-align — we always left-align).
type Column struct {
	Name  string
	Width int
	Get   func(checkpoint.Result) string
}

// resultColumns are the canonical columns for a Result table. Callers pick
// which subset they want.
var resultColumns = struct {
	URL, Status, Latest, Visited, Feed Column
}{
	URL:     Column{Name: "URL", Width: 60, Get: func(r checkpoint.Result) string { return Truncate(r.URL, 60) }},
	Status:  Column{Name: "STATUS", Width: 10, Get: func(r checkpoint.Result) string { return string(r.Status) }},
	Latest:  Column{Name: "LATEST", Width: 25, Get: func(r checkpoint.Result) string { return FormatTimePtr(r.LatestPost) }},
	Visited: Column{Name: "VISITED", Width: 25, Get: func(r checkpoint.Result) string { return FormatTimePtr(r.VisitedAt) }},
	Feed:    Column{Name: "FEED", Width: 0, Get: func(r checkpoint.Result) string { return r.FeedURL }},
}

// FullColumns returns URL, STATUS, LATEST, VISITED, FEED — for scan's
// verbose output.
func FullColumns() []Column {
	return []Column{resultColumns.URL, resultColumns.Status, resultColumns.Latest, resultColumns.Visited, resultColumns.Feed}
}

// CursorColumns returns URL, STATUS, LATEST, VISITED — for cursor's emitters
// (feed URL is uninteresting when triaging which feeds to read).
func CursorColumns() []Column {
	return []Column{resultColumns.URL, resultColumns.Status, resultColumns.Latest, resultColumns.Visited}
}

// WriteTable renders results in a fixed-width table with the supplied columns.
func WriteTable(w io.Writer, cols []Column, results []checkpoint.Result) error {
	if len(cols) == 0 {
		return nil
	}
	headerFmt, rowFmt, sepWidth := buildTableFormats(cols)
	headerArgs := make([]any, len(cols))
	for i, c := range cols {
		headerArgs[i] = c.Name
	}
	if _, err := fmt.Fprintf(w, headerFmt, headerArgs...); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, strings.Repeat("-", sepWidth)); err != nil {
		return err
	}
	rowArgs := make([]any, len(cols))
	for _, r := range results {
		for i, c := range cols {
			rowArgs[i] = c.Get(r)
		}
		if _, err := fmt.Fprintf(w, rowFmt, rowArgs...); err != nil {
			return err
		}
	}
	return nil
}

// buildTableFormats returns (headerFmt, rowFmt, sepWidth) for the columns.
// Width 0 columns are rendered without padding — used for the trailing column.
func buildTableFormats(cols []Column) (header, row string, sep int) {
	var hb, rb strings.Builder
	for i, c := range cols {
		if i > 0 {
			hb.WriteString("  ")
			rb.WriteString("  ")
			sep += 2
		}
		if c.Width > 0 {
			fmt.Fprintf(&hb, "%%-%ds", c.Width)
			fmt.Fprintf(&rb, "%%-%ds", c.Width)
			sep += c.Width
		} else {
			hb.WriteString("%s")
			rb.WriteString("%s")
		}
	}
	hb.WriteString("\n")
	rb.WriteString("\n")
	return hb.String(), rb.String(), sep
}

// ─── Results emitter (used by scan --verbose) ──────────────────────────────

// EmitResults writes results in the chosen format.
func EmitResults(w io.Writer, format string, results []checkpoint.Result) error {
	switch format {
	case "table":
		return WriteTable(w, FullColumns(), results)
	case "json", "":
		return WriteJSON(w, results)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}

// URLsAsResults wraps URLs as bare Result entries for dry-run output.
func URLsAsResults(urls []string) []checkpoint.Result {
	out := make([]checkpoint.Result, len(urls))
	for i, u := range urls {
		out[i] = checkpoint.Result{URL: u}
	}
	return out
}

// ─── Summaries ─────────────────────────────────────────────────────────────

// DomainCount is one entry in the top-N domain breakdown.
type DomainCount struct {
	Domain string `json:"domain"`
	Count  int    `json:"count"`
}

// DryRunSummary aggregates extracted URLs by registrable domain so non-verbose
// dry-run output shows shape, not noise.
type DryRunSummary struct {
	TotalURLs     int           `json:"total_urls"`
	UniqueDomains int           `json:"unique_domains"`
	TopDomains    []DomainCount `json:"top_domains"`
}

const dryRunTopN = 3

// SummarizeDryRun groups urls by registrable domain (resolved via the caller's
// resolver) and returns a top-N summary.
func SummarizeDryRun(urls []string, registrableDomain func(string) (string, error)) DryRunSummary {
	counts := make(map[string]int)
	for _, u := range urls {
		parsed, err := url.Parse(u)
		if err != nil {
			continue
		}
		dom, err := registrableDomain(parsed.Hostname())
		if err != nil {
			continue
		}
		counts[dom]++
	}
	domains := make([]DomainCount, 0, len(counts))
	for d, c := range counts {
		domains = append(domains, DomainCount{Domain: d, Count: c})
	}
	sort.Slice(domains, func(i, j int) bool {
		if domains[i].Count != domains[j].Count {
			return domains[i].Count > domains[j].Count
		}
		return domains[i].Domain < domains[j].Domain
	})
	top := domains
	if len(top) > dryRunTopN {
		top = top[:dryRunTopN]
	}
	return DryRunSummary{
		TotalURLs:     len(urls),
		UniqueDomains: len(counts),
		TopDomains:    top,
	}
}

// WriteDryRunSummary prints s in the chosen format.
func WriteDryRunSummary(w io.Writer, format string, s DryRunSummary) error {
	switch format {
	case "table":
		if _, err := fmt.Fprintf(w, "Total URLs:     %d\n", s.TotalURLs); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Unique domains: %d\n", s.UniqueDomains); err != nil {
			return err
		}
		if len(s.TopDomains) == 0 {
			return nil
		}
		if _, err := fmt.Fprintf(w, "\nTop %d domains:\n", len(s.TopDomains)); err != nil {
			return err
		}
		for _, d := range s.TopDomains {
			if _, err := fmt.Fprintf(w, "  %5d  %s\n", d.Count, d.Domain); err != nil {
				return err
			}
		}
		return nil
	case "json", "":
		return WriteJSON(w, s)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}

// ProbeSummary aggregates probe results by status and returns the top N
// entries according to whatever order the caller already sorted by.
type ProbeSummary struct {
	Total       int                       `json:"total"`
	StatusCount map[checkpoint.Status]int `json:"status_count"`
	TopResults  []checkpoint.Result       `json:"top_results"`
}

const probeTopN = 3

// SummarizeProbe builds a status breakdown and takes the first N entries from
// the already-sorted results slice as "top results".
func SummarizeProbe(results []checkpoint.Result) ProbeSummary {
	counts := make(map[checkpoint.Status]int)
	for _, r := range results {
		counts[r.Status]++
	}
	top := results
	if len(top) > probeTopN {
		top = top[:probeTopN]
	}
	return ProbeSummary{
		Total:       len(results),
		StatusCount: counts,
		TopResults:  top,
	}
}

// WriteProbeSummary prints s in the chosen format.
func WriteProbeSummary(w io.Writer, format string, s ProbeSummary) error {
	switch format {
	case "table":
		if _, err := fmt.Fprintf(w, "Total results: %d\n", s.Total); err != nil {
			return err
		}
		if err := WriteByStatus(w, s.StatusCount); err != nil {
			return err
		}
		if len(s.TopResults) > 0 {
			if _, err := fmt.Fprintf(w, "\nTop %d results:\n", len(s.TopResults)); err != nil {
				return err
			}
			return WriteTable(w, FullColumns(), s.TopResults)
		}
		return nil
	case "json", "":
		return WriteJSON(w, s)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}
