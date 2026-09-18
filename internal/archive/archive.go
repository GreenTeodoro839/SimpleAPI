// Package archive persists successfully proxied client conversations to disk,
// one JSON file per client request. It follows the runtime long-lived-object
// pattern (like concurrency.Limiter): constructed once, survives config
// reloads, and never blocks the proxy hot path. Recording is best-effort:
// records are dropped with a warning when the queue is full or a write fails.
// The destination directory rides inside each Record (resolved from the live
// config snapshot by the caller), so the writer itself holds no config state.
package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Tokens is the per-record token breakdown. It mirrors calllog.Tokens (a local
// copy keeps this package decoupled from calllog).
type Tokens struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// Record is one archived client request: the metadata (when, which models,
// which key, token usage) plus the user's original request body and the
// remote's original response — pre-translation and pre-model-rewrite on the
// response side. Exactly one of Response / ResponseStream is non-nil; the
// other marshals as JSON null. Request/Response are embedded as raw JSON and
// get re-indented by MarshalIndent for readability — whitespace differs from
// the wire bytes, but the JSON content is preserved verbatim; ResponseStream
// is kept byte-exact.
type Record struct {
	Dir string `json:"-"` // destination dir, resolved by the caller per request

	Timestamp      time.Time       `json:"timestamp"` // request start (call time)
	RequestID      string          `json:"request_id"`
	APIKey         string          `json:"api_key"`        // the api key NAME, never the secret
	Model          string          `json:"model"`          // aliasB the client sent
	InternalModel  string          `json:"internal_model"` // provider/aliasA that actually served
	ProviderType   string          `json:"provider_type"`
	Tokens         Tokens          `json:"tokens"`
	Request        json.RawMessage `json:"request"`         // the user's original request body, verbatim
	Response       json.RawMessage `json:"response"`        // the remote's original non-stream body (pre-translation, pre-model-rewrite); null for streams
	ResponseStream *string         `json:"response_stream"` // the remote's raw SSE stream as received; null for non-streams
}

// queueSize bounds pending records; beyond that Record drops instead of
// blocking the proxy hot path.
const queueSize = 256

// Writer drains a buffered channel on one background goroutine and writes one
// JSON file per record. Record never blocks.
type Writer struct {
	logger *logrus.Logger
	ch     chan Record
	done   chan struct{}

	mu     sync.RWMutex // makes sends mutually exclusive with Close, so a
	closed bool         // Record racing a Close never panics on a closed channel
}

// New starts the writer's background goroutine. logger must be non-nil.
func New(logger *logrus.Logger) *Writer {
	w := &Writer{logger: logger, ch: make(chan Record, queueSize), done: make(chan struct{})}
	go w.run()
	return w
}

// Record enqueues rec. Non-blocking: the record is dropped with a Warn log
// when the queue is full or the writer is already closed.
func (w *Writer) Record(rec Record) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return
	}
	select {
	case w.ch <- rec:
	default:
		w.logger.WithField("request_id", rec.RequestID).
			Warn("request archive queue full; dropping record")
	}
}

// Close stops accepting records and waits until everything already queued has
// been written (for-range drains the buffered channel after close). Nothing in
// production calls it today: pending records are lost on process exit, which
// is accepted.
func (w *Writer) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
	<-w.done
}

func (w *Writer) run() {
	defer close(w.done)
	for rec := range w.ch {
		w.write(rec)
	}
}

func (w *Writer) write(rec Record) {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		w.logger.WithField("request_id", rec.RequestID).WithError(err).
			Warn("request archive: marshal failed; dropping record")
		return
	}
	// Lazy creation: enabling the feature never fails at request time, and a
	// dir configured via reload (or recreated after deletion) just works.
	if err := os.MkdirAll(rec.Dir, 0o700); err != nil {
		w.logger.WithField("request_id", rec.RequestID).WithError(err).
			Warn("request archive: mkdir failed; dropping record")
		return
	}
	path := filepath.Join(rec.Dir, fileName(rec))
	// 0600: bodies may contain sensitive conversation content (precedent:
	// config.WriteFileAtomic chmods 0600 for the same reason).
	if err := os.WriteFile(path, data, 0o600); err != nil {
		w.logger.WithField("request_id", rec.RequestID).WithError(err).
			Warn("request archive: write failed; dropping record")
	}
}

// fileName is <local timestamp to ms>_<request_id>.json, e.g.
// 20260918T142530-123_req-42.json. Go's time layout cannot express a '-'
// before the milliseconds, so the ms part is formatted separately.
func fileName(rec Record) string {
	t := rec.Timestamp // local (originates from time.Now())
	return t.Format("20060102T150405") + "-" +
		fmt.Sprintf("%03d", t.Nanosecond()/int(time.Millisecond)) + "_" +
		rec.RequestID + ".json"
}
