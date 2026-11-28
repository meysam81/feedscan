package postcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"feedscan/internal/checkpoint"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "posts.json") // the directory does not exist yet
	at := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	want := Feed{
		FetchedAt:    at,
		ETag:         `"v1"`,
		LastModified: "Sat, 28 Nov 2026 08:00:00 GMT",
		Posts:        []checkpoint.Post{{Title: "t", URL: "https://a.example/1", Date: at.Add(-time.Hour)}},
	}
	c, warn := Load(path)
	if warn != "" || len(c.Feeds) != 0 {
		t.Fatalf("a missing file must load as an empty cache without a warning, got %d feeds, warn %q", len(c.Feeds), warn)
	}
	c.Feeds["https://a.example/feed"] = want
	if err := c.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, warn := Load(path)
	got := loaded.Feeds["https://a.example/feed"]
	if warn != "" || !got.FetchedAt.Equal(want.FetchedAt) || got.ETag != want.ETag || got.LastModified != want.LastModified ||
		len(got.Posts) != 1 || got.Posts[0].URL != want.Posts[0].URL {
		t.Errorf("round trip lost data: %+v (warn %q)", got, warn)
	}
}

func TestLoadCorruptFileIsAnEmptyCacheWithAWarning(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{"garbage": "{not json", "empty": "", "wrong shape": `{"feeds": null}`} {
		path := filepath.Join(t.TempDir(), "posts.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		c, warn := Load(path)
		if c == nil || c.Feeds == nil || len(c.Feeds) != 0 {
			t.Errorf("%s: want a usable empty cache, got %+v", name, c)
		}
		if (warn != "") != (name == "garbage") {
			t.Errorf("%s: warn = %q", name, warn)
		}
	}
}
