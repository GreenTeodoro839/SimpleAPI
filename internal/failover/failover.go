// Package failover tracks consecutive upstream failures per (API key, internal
// model id) so the router can skip a candidate that has repeatedly failed and
// try the next-priority candidate (DEVELOPMENT.md §9). Counters live only in
// memory and are lost on process restart.
package failover

import (
	"sync"
	"time"
)

type counterKey struct {
	apiKey  string
	modelID string
}

type entry struct {
	consec   int
	lastFail time.Time
}

// Counter is a thread-safe consecutive-failure tracker.
type Counter struct {
	mu  sync.Mutex
	m   map[counterKey]entry
	now func() time.Time
}

// New returns a Counter using the system clock.
func New() *Counter {
	return &Counter{m: make(map[counterKey]entry), now: time.Now}
}

// OnFailure records a consecutive failure for (apiKey, modelID).
func (c *Counter) OnFailure(apiKey, modelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := counterKey{apiKey, modelID}
	e := c.m[k]
	e.consec++
	e.lastFail = c.now()
	c.m[k] = e
}

// OnSuccess clears the failure state for (apiKey, modelID).
func (c *Counter) OnSuccess(apiKey, modelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, counterKey{apiKey, modelID})
}

// ShouldSkip reports whether (apiKey, modelID) has reached maxFail consecutive
// failures and should be skipped for a fresh request. A stale entry older than
// resetSec (when resetSec > 0) is cleared first and not skipped.
func (c *Counter) ShouldSkip(apiKey, modelID string, maxFail, resetSec int) bool {
	if maxFail < 1 {
		maxFail = 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := counterKey{apiKey, modelID}
	e, ok := c.m[k]
	if !ok {
		return false
	}
	if resetSec > 0 {
		if c.now().Sub(e.lastFail) > time.Duration(resetSec)*time.Second {
			delete(c.m, k)
			return false
		}
	}
	return e.consec >= maxFail
}
