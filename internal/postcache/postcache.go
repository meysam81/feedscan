// Package postcache owns the file that remembers what each feed held the last
// time the posts listing fetched it, so the next listing can skip the network.
//
// It is a cache in the strict sense: deleting the file loses nothing but
// time. Checkpoints (what was scanned, what was read) live elsewhere and are
// never written from here.
package postcache

import (
	"fmt"
	"io"
	"os"
	"time"

	json "github.com/goccy/go-json"

	"feedscan/internal/checkpoint"
)

// Feed is the remembered state of one feed.
type Feed struct {
	// FetchedAt is the last attempt, successful or not: it decides when the
	// feed is due again, so a broken feed is not retried on every run.
	FetchedAt time.Time `json:"fetched_at"`
	// ETag and LastModified are the validators the server sent with Posts.
	ETag         string            `json:"etag,omitempty"`
	LastModified string            `json:"last_modified,omitempty"`
	Posts        []checkpoint.Post `json:"posts,omitempty"`
}

// Cache is the on-disk JSON document, keyed by feed URL.
//
// ponytail: entries are never pruned and concurrent runs are last-writer-wins.
// The file grows only with the number of distinct feeds ever scanned, and a
// lost update costs one refetch; add pruning or a lock if either ever hurts.
type Cache struct {
	Feeds map[string]Feed `json:"feeds"`
}

// Load reads the cache file. A missing file is an empty cache. An unreadable
// one is too, reported through warn: a broken cache must never stop a listing.
func Load(path string) (c *Cache, warn string) {
	c = &Cache{Feeds: make(map[string]Feed)}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			warn = fmt.Sprintf("posts cache unreadable (%v); fetching afresh", err)
		}
		return c, warn
	}
	if len(data) == 0 {
		return c, ""
	}
	loaded := &Cache{}
	if err := json.Unmarshal(data, loaded); err != nil {
		return c, fmt.Sprintf("posts cache unreadable (%v); fetching afresh", err)
	}
	if loaded.Feeds != nil {
		c = loaded
	}
	return c, ""
}

// Save writes the cache atomically, creating its directory if missing.
func (c *Cache) Save(path string) error {
	return checkpoint.WriteFileAtomic(path, func(w io.Writer) error {
		return json.NewEncoder(w).Encode(c)
	})
}
