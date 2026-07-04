// Package runtime holds the single swappable application state behind an
// RWMutex. It stores both the raw (placeholder) config — the on-disk form used
// as the base for management edits — and the expanded config + indexes used by
// the hot path. failover/usage counters are long-lived across reloads.
package runtime

import (
	"sync"

	"github.com/GreenTeodoro839/SimpleAPI/internal/calllog"
	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/failover"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/usage"
)

type Runtime struct {
	mu      sync.RWMutex
	raw     *config.Config // placeholders; on-disk representation
	cfg     *config.Config // expanded; runtime
	indexes *indexes.Indexes
	cfgPath string

	failover *failover.Counter
	usage    *usage.Recorder
	callLog  *calllog.Recorder
}

// New constructs a Runtime from the raw (placeholder) and expanded configs.
func New(raw, cfg *config.Config, idx *indexes.Indexes, cfgPath string) *Runtime {
	return &Runtime{
		raw:      raw,
		cfg:      cfg,
		indexes:  idx,
		cfgPath:  cfgPath,
		failover: failover.New(),
		usage:    usage.NewRecorder(),
		callLog:  calllog.NewRecorder(cfg.Proxy.CallLogMax()),
	}
}

// Snapshot is an immutable point-in-time view.
type Snapshot struct {
	Raw     *config.Config // placeholder form (raw, pre-expansion config; returned verbatim by management GET)
	Config  *config.Config // expanded form (proxy runtime settings)
	Indexes *indexes.Indexes
}

func (r *Runtime) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Snapshot{Raw: r.raw, Config: r.cfg, Indexes: r.indexes}
}

// Replace swaps raw, expanded config, and indexes under a write lock.
func (r *Runtime) Replace(raw, cfg *config.Config, idx *indexes.Indexes) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.raw = raw
	r.cfg = cfg
	r.indexes = idx
}

func (r *Runtime) ConfigPath() string          { return r.cfgPath }
func (r *Runtime) Failover() *failover.Counter { return r.failover }
func (r *Runtime) Usage() *usage.Recorder      { return r.usage }
func (r *Runtime) CallLog() *calllog.Recorder  { return r.callLog }
