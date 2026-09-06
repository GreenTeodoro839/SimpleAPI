// Package concurrency caps how many upstream requests may be in flight per
// provider (DEVELOPMENT.md §9). Counters live in memory for the process
// lifetime and survive config reloads, so raising or lowering a provider's
// limit applies to the next acquire without disturbing in-flight requests.
package concurrency

import "sync"

// Limiter counts in-flight upstream requests per provider name.
type Limiter struct {
	mu       sync.Mutex
	inFlight map[string]int
}

// New returns an empty Limiter.
func New() *Limiter {
	return &Limiter{inFlight: make(map[string]int)}
}

// Acquire takes a slot for provider. A limit <= 0 means unlimited, and takes no
// slot at all so the common case stays lock-free-ish on the hot path.
//
// It never blocks: ok is false when the provider is already at its limit, and
// the caller should move on to the next candidate (or report the provider busy)
// rather than queue behind it. release must be called exactly once when the
// upstream call is finished -- for a streaming request, after the stream ends.
func (l *Limiter) Acquire(provider string, limit int) (release func(), ok bool) {
	if limit <= 0 {
		return func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[provider] >= limit {
		return nil, false
	}
	l.inFlight[provider]++
	var once sync.Once
	return func() { once.Do(func() { l.release(provider) }) }, true
}

func (l *Limiter) release(provider string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := l.inFlight[provider]; n > 1 {
		l.inFlight[provider] = n - 1
		return
	}
	delete(l.inFlight, provider)
}

// InFlight reports how many slots provider currently holds. Providers with no
// limit are not counted.
func (l *Limiter) InFlight(provider string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inFlight[provider]
}
