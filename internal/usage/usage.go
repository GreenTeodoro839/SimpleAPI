// Package usage records in-memory request statistics aggregated by internal
// model dimensions (DEVELOPMENT.md §13). aliasB is intentionally NOT a key.
// Counters live only in memory and are lost on restart.
package usage

import (
	"sort"
	"sync"
)

// Key is the set of internal dimensions a request is aggregated by.
type Key struct {
	Provider           string `json:"provider"`
	ProviderType       string `json:"provider_type"`
	AliasA             string `json:"aliasA"`
	UpstreamModel      string `json:"upstream_model"`
	InternalModelID    string `json:"internal_model"`
	SourceProtocol     string `json:"source_protocol"`
	TargetProviderType string `json:"target_provider_type"`
	HTTPStatus         int    `json:"http_status"`
}

// Item is one aggregated row.
type Item struct {
	Key
	Requests     int64 `json:"requests"`
	Failures     int64 `json:"failures"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type agg struct {
	requests, failures, input, output int64
}

// Recorder is a thread-safe in-memory usage aggregator.
type Recorder struct {
	mu sync.Mutex
	m  map[Key]*agg
}

func NewRecorder() *Recorder {
	return &Recorder{m: make(map[Key]*agg)}
}

// Record adds one request attempt to the aggregate for k.
func (r *Recorder) Record(k Key, inputTokens, outputTokens int64, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.m[k]
	if a == nil {
		a = &agg{}
		r.m[k] = a
	}
	a.requests++
	if failed {
		a.failures++
	}
	a.input += inputTokens
	a.output += outputTokens
}

// Snapshot returns all aggregated rows, sorted for stable output.
func (r *Recorder) Snapshot() []Item {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Item, 0, len(r.m))
	for k, a := range r.m {
		out = append(out, Item{
			Key:          k,
			Requests:     a.requests,
			Failures:     a.failures,
			InputTokens:  a.input,
			OutputTokens: a.output,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.InternalModelID != b.InternalModelID {
			return a.InternalModelID < b.InternalModelID
		}
		if a.SourceProtocol != b.SourceProtocol {
			return a.SourceProtocol < b.SourceProtocol
		}
		return a.HTTPStatus < b.HTTPStatus
	})
	return out
}
