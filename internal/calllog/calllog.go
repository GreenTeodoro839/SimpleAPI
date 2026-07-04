// Package calllog keeps a bounded, in-memory ring buffer of recent upstream
// request attempts for the management "call-log" endpoint. It is intentionally
// lossy: only the most recent entries are retained, and everything is dropped
// on restart. This mirrors the per-request view CLIProxyAPI exposes via its
// usage-queue, but as a non-destructive recent-N ring (SimpleAPI has no Redis
// queue to drain).
package calllog

import (
	"sync"
	"time"
)

// Tokens is the per-attempt token breakdown surfaced in an Entry.
type Tokens struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// Entry is one recorded upstream attempt. A single client request may produce
// several entries (one per failover attempt) sharing the same RequestID.
type Entry struct {
	RequestID      string    `json:"request_id"`
	Timestamp      time.Time `json:"timestamp"`
	Endpoint       string    `json:"endpoint"` // e.g. "POST /v1/messages"
	APIKey         string    `json:"api_key"`  // the api key NAME, never the secret
	SourceProtocol string    `json:"source_protocol"`
	Alias          string    `json:"alias"` // client-visible model (aliasB)
	Provider       string    `json:"provider"`
	ProviderType   string    `json:"provider_type"`
	Model          string    `json:"model"` // upstream model
	InternalModel  string    `json:"internal_model"`
	HTTPStatus     int       `json:"http_status"`
	LatencyMS      int64     `json:"latency_ms"`
	Failed         bool      `json:"failed"`
	Error          string    `json:"error,omitempty"` // failure reason (upstream message / network / timeout / disconnect)
	Tokens         Tokens    `json:"tokens"`
}

// Recorder is a thread-safe fixed-capacity ring buffer of Entries. A capacity
// of 0 means recording is disabled: Record is a no-op and Recent returns nil.
type Recorder struct {
	mu    sync.Mutex
	buf   []Entry
	head  int // index of next write
	count int // number of valid entries (<= cap)
	cap   int
}

// NewRecorder builds a ring buffer holding up to capacity entries. capacity <= 0
// produces a disabled recorder.
func NewRecorder(capacity int) *Recorder {
	if capacity < 0 {
		capacity = 0
	}
	return &Recorder{buf: make([]Entry, capacity), cap: capacity}
}

// Enabled reports whether the recorder retains anything.
func (r *Recorder) Enabled() bool {
	return r != nil && r.cap > 0
}

// Record appends an entry, overwriting the oldest when full. No-op when disabled.
func (r *Recorder) Record(e Entry) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	r.buf[r.head] = e
	r.head = (r.head + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
	r.mu.Unlock()
}

// Recent returns up to limit entries, newest first. limit <= 0 uses a sensible
// default. Returns nil when disabled or empty.
func (r *Recorder) Recent(limit int) []Entry {
	if !r.Enabled() || r.count == 0 {
		return nil
	}
	if limit <= 0 || limit > r.count {
		limit = r.count
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, limit)
	// head points one past the newest entry; walk backwards.
	for i := 0; i < limit; i++ {
		idx := (r.head - 1 - i + r.cap) % r.cap
		out = append(out, r.buf[idx])
	}
	return out
}
