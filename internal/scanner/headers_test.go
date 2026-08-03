package scanner

import "testing"

func TestParseHeaders(t *testing.T) {
	h, err := ParseHeaders([]string{
		"",
		"# comment",
		"x-custom-header: custom-value",
		"Authorization:Bearer tok:en",
		"X-Multi: a",
		"X-Multi: b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get("X-Custom-Header"); got != "custom-value" {
		t.Errorf("X-Custom-Header = %q", got)
	}
	if got := h.Get("Authorization"); got != "Bearer tok:en" {
		t.Errorf("Authorization = %q", got)
	}
	if got := h.Values("X-Multi"); len(got) != 2 {
		t.Errorf("X-Multi = %v", got)
	}

	for _, bad := range []string{"no-colon", ": value"} {
		if _, err := ParseHeaders([]string{bad}); err == nil {
			t.Errorf("ParseHeaders(%q) = nil error, want error", bad)
		}
	}
}
