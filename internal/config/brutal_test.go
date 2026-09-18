package config

// Corner-case additions: regression coverage for the DeepCopy nil-slice
// landmine and request_archive.dir environment expansion.

import "testing"

// Regression for the DeepCopy nil-slice landmine (the RetryCodes class of
// bug): an omitted upstream_retry_status_codes round-trips through YAML — as
// every management PUT does — and must still mean "use the default codes".
func TestRetryCodesDefaultSurvivesDeepCopy(t *testing.T) {
	cfg := &Config{Proxy: ProxyConfig{}} // retry codes omitted
	if got := cfg.Proxy.RetryCodes(); len(got) != 6 {
		t.Fatalf("direct RetryCodes() = %v, want the 6-code default", got)
	}
	cp := DeepCopy(cfg)
	got := cp.Proxy.RetryCodes()
	if len(got) != 6 || got[0] != 408 || got[5] != 504 {
		t.Fatalf("RetryCodes() after DeepCopy = %v, want the 6-code default (a nil slice must not become a meaningful empty override)", got)
	}
	// An explicitly configured non-empty list survives the round-trip.
	cfg.Proxy.UpstreamRetryStatusCodes = []int{500}
	if got := DeepCopy(cfg).Proxy.RetryCodes(); len(got) != 1 || got[0] != 500 {
		t.Fatalf("explicit list after DeepCopy = %v, want [500]", got)
	}
	// An explicitly empty list is indistinguishable from omitted (len-based
	// check), so it too falls back to the default.
	cfg.Proxy.UpstreamRetryStatusCodes = []int{}
	if got := DeepCopy(cfg).Proxy.RetryCodes(); len(got) != 6 {
		t.Fatalf("empty list after DeepCopy = %v, want the default", got)
	}
}

// ${VAR:-default} works for request_archive.dir: the default rescues an unset
// variable instead of silently disabling the feature.
func TestExpandRequestArchiveDirDefault(t *testing.T) {
	cfg := &Config{RequestArchive: RequestArchiveConfig{Dir: "${ARCH_DIR_SAPI_BRUTAL:-/tmp/arch-default}"}}
	Expand(cfg)
	if cfg.RequestArchive.Dir != "/tmp/arch-default" {
		t.Fatalf("Dir = %q, want /tmp/arch-default", cfg.RequestArchive.Dir)
	}
	t.Setenv("ARCH_DIR_SAPI_BRUTAL", "/from-env")
	cfg2 := &Config{RequestArchive: RequestArchiveConfig{Dir: "${ARCH_DIR_SAPI_BRUTAL:-/tmp/arch-default}"}}
	Expand(cfg2)
	if cfg2.RequestArchive.Dir != "/from-env" {
		t.Fatalf("Dir = %q, want /from-env (set variable beats the default)", cfg2.RequestArchive.Dir)
	}
}
