package concurrency

import (
	"sync"
	"testing"
)

func TestAcquireRejectsAtLimitAndRecoversOnRelease(t *testing.T) {
	l := New()

	first, ok := l.Acquire("anthropic-main", 2)
	if !ok {
		t.Fatalf("first Acquire() ok = false, want true")
	}
	second, ok := l.Acquire("anthropic-main", 2)
	if !ok {
		t.Fatalf("second Acquire() ok = false, want true")
	}
	if _, ok := l.Acquire("anthropic-main", 2); ok {
		t.Fatalf("third Acquire() ok = true, want false (at limit)")
	}
	if got := l.InFlight("anthropic-main"); got != 2 {
		t.Fatalf("InFlight() = %d, want 2", got)
	}

	// A different provider has its own budget.
	if _, ok := l.Acquire("openai-main", 2); !ok {
		t.Fatalf("other provider Acquire() ok = false, want true")
	}

	first()
	if _, ok := l.Acquire("anthropic-main", 2); !ok {
		t.Fatalf("Acquire() after release ok = false, want true")
	}
	second()
}

func TestAcquireUnlimitedWhenLimitIsZeroOrNegative(t *testing.T) {
	l := New()
	for _, limit := range []int{0, -1} {
		for i := 0; i < 100; i++ {
			if _, ok := l.Acquire("anthropic-main", limit); !ok {
				t.Fatalf("Acquire(limit=%d) ok = false, want true", limit)
			}
		}
	}
	// Unlimited providers take no slot, so they can never exhaust one.
	if got := l.InFlight("anthropic-main"); got != 0 {
		t.Fatalf("InFlight() = %d, want 0", got)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	l := New()
	release, ok := l.Acquire("anthropic-main", 1)
	if !ok {
		t.Fatalf("Acquire() ok = false, want true")
	}
	release()
	release() // a double release must not hand out a slot that isn't free
	if got := l.InFlight("anthropic-main"); got != 0 {
		t.Fatalf("InFlight() = %d, want 0", got)
	}
	if _, ok := l.Acquire("anthropic-main", 1); !ok {
		t.Fatalf("Acquire() ok = false, want true")
	}
	if _, ok := l.Acquire("anthropic-main", 1); ok {
		t.Fatalf("Acquire() ok = true, want false (slot still held)")
	}
}

func TestAcquireIsConcurrencySafe(t *testing.T) {
	l := New()
	const limit = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int
	)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := l.Acquire("anthropic-main", limit); ok {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != limit {
		t.Fatalf("granted = %d, want %d", granted, limit)
	}
}
