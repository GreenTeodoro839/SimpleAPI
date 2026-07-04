// Package indexes builds the immutable lookup tables derived from a validated
// config: a provider index, an internal-model index, and a per-API-key routing
// table mapping each client-visible aliasB to a priority-sorted candidate list.
package indexes

import (
	"fmt"
	"sort"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
)

// ResolvedModel is an upstream model with all defaults applied.
type ResolvedModel struct {
	ProviderName    string
	ProviderType    string
	UpstreamModel   string
	AliasA          string // effective alias (model name when aliasA was empty)
	InternalID      string // providerName + "_" + AliasA
	WebSearch       *config.WebSearchForward
}

// ProviderEntry is one upstream provider with resolved auth material.
type ProviderEntry struct {
	Name    string
	Type    string
	URL     string
	Key     string
	Headers map[string]string
}

// Candidate is one aliasB resolution within an API key.
type Candidate struct {
	InternalID string
	AliasB     string // effective alias (aliasA when aliasB was empty)
	Priority   int
	Order      int // config order, for stable tiebreak
}

// KeyContext is the resolved authorization for one inbound API key.
type KeyContext struct {
	Name             string
	Key              string
	AllowedProtocols map[string]struct{}
	Routing          map[string][]Candidate // aliasB -> candidates sorted by priority desc
	AliasBs          []string               // unique aliasBs, sorted, for /v1/models
}

// Indexes is the immutable set of lookup tables built from one config revision.
type Indexes struct {
	Providers     map[string]*ProviderEntry
	Models        map[string]*ResolvedModel
	Keys          map[string]*KeyContext // expanded key string -> context
	KeyByName     map[string]*KeyContext
	InternalIDs   []string // sorted, for /-/api/models
}

// Build constructs Indexes from a config that has already passed Validate.
// It returns an error only on a structural inconsistency that Validate should
// have caught; callers may treat it as fatal.
func Build(cfg *config.Config) (*Indexes, error) {
	idx := &Indexes{
		Providers: make(map[string]*ProviderEntry, len(cfg.Providers)),
		Models:    make(map[string]*ResolvedModel),
		Keys:      make(map[string]*KeyContext, len(cfg.APIKeys)),
		KeyByName: make(map[string]*KeyContext, len(cfg.APIKeys)),
	}

	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		idx.Providers[p.Name] = &ProviderEntry{
			Name: p.Name, Type: p.Type, URL: p.URL, Key: p.Key, Headers: p.Headers,
		}
		for j := range p.Models {
			m := &p.Models[j]
			eff := config.EffectiveAliasA(*m)
			id := config.ModelInternalID(p.Name, *m)
			idx.Models[id] = &ResolvedModel{
				ProviderName:  p.Name,
				ProviderType:  p.Type,
				UpstreamModel: m.Model,
				AliasA:        eff,
				InternalID:    id,
				WebSearch:     m.AnthropicWebSearchForward,
			}
			idx.InternalIDs = append(idx.InternalIDs, id)
		}
	}
	sort.Strings(idx.InternalIDs)

	for i := range cfg.APIKeys {
		k := &cfg.APIKeys[i]
		kc := &KeyContext{
			Name:             k.Name,
			Key:              k.Key,
			AllowedProtocols: make(map[string]struct{}, len(k.AllowedProtocols)),
			Routing:          make(map[string][]Candidate),
		}
		for _, pr := range k.AllowedProtocols {
			kc.AllowedProtocols[pr] = struct{}{}
		}
		aliasBSeen := map[string]struct{}{}
		for order, cm := range k.Models {
			rm, ok := idx.Models[cm.Model]
			if !ok {
				// Validate should have caught this; skip defensively.
				return nil, fmt.Errorf("indexes: api key %q references unknown model %q", k.Name, cm.Model)
			}
			aliasB := cm.AliasB
			if aliasB == "" {
				aliasB = rm.AliasA
			}
			kc.Routing[aliasB] = append(kc.Routing[aliasB], Candidate{
				InternalID: cm.Model,
				AliasB:     aliasB,
				Priority:   cm.Priority,
				Order:      order,
			})
			if _, seen := aliasBSeen[aliasB]; !seen {
				aliasBSeen[aliasB] = struct{}{}
				kc.AliasBs = append(kc.AliasBs, aliasB)
			}
		}
		for aliasB := range kc.Routing {
			sort.SliceStable(kc.Routing[aliasB], func(a, b int) bool {
				ca, cb := kc.Routing[aliasB][a], kc.Routing[aliasB][b]
				if ca.Priority != cb.Priority {
					return ca.Priority > cb.Priority
				}
				return ca.Order < cb.Order
			})
		}
		sort.Strings(kc.AliasBs)
		idx.Keys[k.Key] = kc
		idx.KeyByName[k.Name] = kc
	}

	return idx, nil
}
