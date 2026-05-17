package checkpoint

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestFileLockSerializesConcurrentWriters verifies that AcquireLock blocks
// until the prior holder calls Close. Two goroutines mark different URLs;
// without the lock the writes would race and lose updates. Mirrors the
// real-world scenario from C3.
func TestFileLockSerializesConcurrentWriters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")

	// Seed.
	cp := New()
	now := time.Now().UTC().Truncate(time.Second)
	cp.Put(Result{URL: "a", Status: StatusActive, CheckedAt: &now})
	cp.Put(Result{URL: "b", Status: StatusActive, CheckedAt: &now})
	if err := cp.Save(path); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	var wg sync.WaitGroup
	mutate := func(u string) {
		defer wg.Done()
		l, err := AcquireLock(path)
		if err != nil {
			t.Errorf("lock: %v", err)
			return
		}
		defer func() {
			if cerr := l.Close(); cerr != nil {
				t.Errorf("unlock: %v", cerr)
			}
		}()
		got, _, err := Load(path)
		if err != nil {
			t.Errorf("load: %v", err)
			return
		}
		if err := got.Mark(u, time.Now().UTC()); err != nil {
			t.Errorf("mark %s: %v", u, err)
			return
		}
		// Brief sleep widens the race window — without the lock, the second
		// load would clobber the first save.
		time.Sleep(10 * time.Millisecond)
		if err := got.Save(path); err != nil {
			t.Errorf("save: %v", err)
		}
	}

	wg.Add(2)
	go mutate("a")
	go mutate("b")
	wg.Wait()

	final, _, err := Load(path)
	if err != nil {
		t.Fatalf("final load: %v", err)
	}
	if final.Entries["a"].VisitedAt == nil {
		t.Errorf("a not marked: %+v", final.Entries["a"])
	}
	if final.Entries["b"].VisitedAt == nil {
		t.Errorf("b not marked: %+v", final.Entries["b"])
	}
}

func TestFileLockNilCloseSafe(t *testing.T) {
	t.Parallel()
	var l *FileLock
	if err := l.Close(); err != nil {
		t.Errorf("nil Close should be safe, got %v", err)
	}
}
