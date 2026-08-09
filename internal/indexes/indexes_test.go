package indexes

import (
	"strings"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
)

// TestWildcardRouting covers: wildcard/exact partitioning at build time,
// declaration-order preservation, same-pattern candidate grouping + priority
// sort, AliasBs excluding wildcards, and ResolveCandidates semantics (exact
// wins; wildcard fallback; overlap resolves by declaration order; no match).
func TestWildcardRouting(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{
				Name: "deepseek",
				Type: "openai_completion",
				URL:  "https://example.com",
				Models: []config.ProviderModel{
					{Model: "deepseek-v4-flash"}, // id: deepseek/deepseek-v4-flash
					{Model: "deepseek-v4-pro"},   // id: deepseek/deepseek-v4-pro
				},
			},
			{
				Name: "anthropic",
				Type: "anthropic",
				URL:  "https://example.com",
				Models: []config.ProviderModel{
					{Model: "sonnet"}, // id: anthropic/sonnet
				},
			},
		},
		APIKeys: []config.ClientApiKey{
			{
				Name: "dev",
				Key:  "k-dev",
				Models: []config.ClientModel{
					// wildcard pattern with two grouped candidates (priority-sorted)
					{Model: "deepseek/deepseek-v4-flash", AliasB: "deepseek-*", Priority: 100},
					{Model: "deepseek/deepseek-v4-pro", AliasB: "deepseek-*", Priority: 50},
					// a second, overlapping wildcard declared AFTER, to test order
					{Model: "anthropic/sonnet", AliasB: "*-chat", Priority: 10},
					// an exact aliasB
					{Model: "deepseek/deepseek-v4-flash", AliasB: "exact-flash", Priority: 1},
				},
			},
		},
	}

	idx, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	kc := idx.Keys["k-dev"]

	// 1. Wildcards partitioned, declaration order preserved.
	if len(kc.Wildcards) != 2 {
		t.Fatalf("expected 2 wildcard routes, got %d (%+v)", len(kc.Wildcards), kc.Wildcards)
	}
	if kc.Wildcards[0].Pattern != "deepseek-*" || kc.Wildcards[1].Pattern != "*-chat" {
		t.Errorf("wildcard declaration order = %q,%q; want deepseek-*,*-chat",
			kc.Wildcards[0].Pattern, kc.Wildcards[1].Pattern)
	}

	// 2. Same-pattern candidates grouped and priority-sorted.
	if got := kc.Wildcards[0].Candidates; len(got) != 2 ||
		got[0].InternalID != "deepseek/deepseek-v4-flash" ||
		got[1].InternalID != "deepseek/deepseek-v4-pro" {
		t.Errorf("deepseek-* candidates = %+v; want [flash(100), pro(50)]", got)
	}

	// 3. AliasBs excludes wildcards but includes the exact name.
	sawExact := false
	for _, a := range kc.AliasBs {
		if strings.ContainsRune(a, '*') {
			t.Errorf("AliasBs must not contain wildcards; found %q", a)
		}
		if a == "exact-flash" {
			sawExact = true
		}
	}
	if !sawExact {
		t.Errorf("AliasBs missing exact name; got %v", kc.AliasBs)
	}

	// 4. ResolveCandidates: exact aliasB wins over wildcard.
	cs := kc.ResolveCandidates("exact-flash")
	if len(cs) != 1 || cs[0].InternalID != "deepseek/deepseek-v4-flash" {
		t.Errorf("ResolveCandidates(exact-flash) = %+v; want deepseek/deepseek-v4-flash", cs)
	}

	// 5. ResolveCandidates: "deepseek-chat" matches BOTH wildcards; the
	//    first-declared (deepseek-*, 2 candidates) must win over *-chat (1).
	cs = kc.ResolveCandidates("deepseek-chat")
	if len(cs) != 2 || cs[0].InternalID != "deepseek/deepseek-v4-flash" {
		t.Errorf("ResolveCandidates(deepseek-chat) overlap should pick deepseek-*; got %+v", cs)
	}

	// 6. ResolveCandidates: a name that only matches the second wildcard.
	cs = kc.ResolveCandidates("claude-chat")
	if len(cs) != 1 || cs[0].InternalID != "anthropic/sonnet" {
		t.Errorf("ResolveCandidates(claude-chat) = %+v; want anthropic/sonnet", cs)
	}

	// 7. ResolveCandidates: no match -> nil.
	if got := kc.ResolveCandidates("nope"); got != nil {
		t.Errorf("ResolveCandidates(nope) = %+v; want nil", got)
	}
}

// TestAliasBEmptyFallsBackToAliasAAsExact confirms the pre-existing behavior
// (empty aliasB -> aliasA) still lands in exact Routing, not Wildcards.
func TestAliasBEmptyFallsBackToAliasAAsExact(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "p", Type: "openai_completion", URL: "u",
				Models: []config.ProviderModel{{Model: "real-model"}}}, // id: p/real-model
		},
		APIKeys: []config.ClientApiKey{
			{Name: "k", Key: "kk", Models: []config.ClientModel{
				{Model: "p/real-model"}, // aliasB empty -> aliasA "real-model"
			}},
		},
	}
	idx, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	kc := idx.Keys["kk"]
	if len(kc.Wildcards) != 0 {
		t.Errorf("expected no wildcards, got %+v", kc.Wildcards)
	}
	if cs := kc.ResolveCandidates("real-model"); len(cs) != 1 {
		t.Errorf("ResolveCandidates(real-model) = %+v; want 1 candidate", cs)
	}
	if len(kc.AliasBs) != 1 || kc.AliasBs[0] != "real-model" {
		t.Errorf("AliasBs = %v; want [real-model]", kc.AliasBs)
	}
}
