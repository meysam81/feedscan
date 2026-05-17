package scanner

import (
	"context"
	"testing"
	"time"
)

func TestHostGateZeroDelay(t *testing.T) {
	t.Parallel()
	g := newHostGate(0)
	start := time.Now()
	if err := g.wait(context.Background(), "https://example.com"); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Millisecond {
		t.Errorf("zero-delay gate slept %v, expected ~0", elapsed)
	}
}

func TestHostGateEnforcesDelayPerHost(t *testing.T) {
	t.Parallel()
	g := newHostGate(50 * time.Millisecond)

	// First call: no wait.
	start := time.Now()
	if err := g.wait(context.Background(), "https://example.com/a"); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("first call should not sleep, slept %v", elapsed)
	}

	// Second call same host: should wait ~50ms.
	start = time.Now()
	if err := g.wait(context.Background(), "https://example.com/b"); err != nil {
		t.Fatalf("second wait: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Errorf("second call slept %v, expected at least 40ms", elapsed)
	}
}

func TestHostGateIsolatesHosts(t *testing.T) {
	t.Parallel()
	g := newHostGate(50 * time.Millisecond)

	if err := g.wait(context.Background(), "https://a.com"); err != nil {
		t.Fatalf("a: %v", err)
	}
	start := time.Now()
	if err := g.wait(context.Background(), "https://b.com"); err != nil {
		t.Fatalf("b: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("different host should not block, slept %v", elapsed)
	}
}

func TestHostGateRespectsContext(t *testing.T) {
	t.Parallel()
	g := newHostGate(500 * time.Millisecond)

	// Prime the gate so the next call must wait.
	if err := g.wait(context.Background(), "https://example.com"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := g.wait(ctx, "https://example.com")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context error")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("ctx-cancel should have returned quickly, took %v", elapsed)
	}
}

func TestHostGateInvalidURL(t *testing.T) {
	t.Parallel()
	g := newHostGate(50 * time.Millisecond)
	// Malformed URL — wait should return nil without sleeping.
	start := time.Now()
	if err := g.wait(context.Background(), "://malformed"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("malformed URL should not sleep, slept %v", elapsed)
	}
}
