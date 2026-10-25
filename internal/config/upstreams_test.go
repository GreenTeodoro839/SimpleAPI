package config

import (
	"reflect"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
)

func TestProviderUpstreamFor(t *testing.T) {
	for _, fallback := range protocol.All {
		for _, incoming := range protocol.All {
			t.Run(fallback+"/"+incoming, func(t *testing.T) {
				p := Provider{Type: fallback, URL: "https://legacy"}
				pr, u := p.UpstreamFor(incoming)
				if pr != fallback || u != p.URL {
					t.Fatalf("legacy: got %q %q", pr, u)
				}
				p.URLs = map[string]string{fallback: "https://default"}
				pr, u = p.UpstreamFor(incoming)
				if pr != fallback || u != "https://default" {
					t.Fatalf("default override: got %q %q", pr, u)
				}
				p.URLs[incoming] = "https://native"
				pr, u = p.UpstreamFor(incoming)
				if pr != incoming || u != "https://native" {
					t.Fatalf("native: got %q %q", pr, u)
				}
				p.URLs[incoming] = ""
				pr, u = p.UpstreamFor(incoming)
				want := "https://default"
				if incoming == fallback {
					want = p.URL
				}
				if pr != fallback || u != want {
					t.Fatalf("empty native: got %q %q, want %q %q", pr, u, fallback, want)
				}
			})
		}
	}
}

func TestValidateProviderURLs(t *testing.T) {
	cases := []struct {
		name   string
		urls   map[string]string
		legacy string
		code   string
	}{
		{"legacy", nil, "https://legacy", ""},
		{"native default only", map[string]string{protocol.Anthropic: "https://native"}, "", ""},
		{"all native", map[string]string{protocol.Anthropic: "https://a", protocol.OpenAICompletion: "https://o/v1", protocol.Codex: "https://c/v1"}, "", ""},
		{"missing default", map[string]string{protocol.Codex: "https://c/v1"}, "", "provider_url_required"},
		{"empty default", map[string]string{protocol.Anthropic: ""}, "", "provider_url_required"},
		{"empty optional", map[string]string{protocol.Codex: ""}, "https://legacy", ""},
		{"invalid protocol", map[string]string{"typo": "https://x"}, "https://legacy", "invalid_protocol"},
		{"empty invalid protocol", map[string]string{"": ""}, "https://legacy", "invalid_protocol"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Providers[0].URL = tc.legacy
			cfg.Providers[0].URLs = tc.urls
			errs := Validate(cfg)
			if tc.code == "" {
				if len(errs) > 0 {
					t.Fatalf("unexpected errors: %+v", errs)
				}
			} else if !hasCode(errs, tc.code) {
				t.Fatalf("want %s, got %+v", tc.code, errs)
			}
		})
	}
}

func TestProviderURLsRoundTripAndCopy(t *testing.T) {
	cfg := validConfig()
	cfg.Providers[0].URL = ""
	cfg.Providers[0].URLs = map[string]string{protocol.Anthropic: "https://a", protocol.Codex: "https://c/v1"}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Providers[0].URLs, cfg.Providers[0].URLs) {
		t.Fatalf("lost URLs: %+v", decoded)
	}
	cp := DeepCopy(cfg)
	cp.Providers[0].URLs[protocol.Anthropic] = "https://changed"
	if cfg.Providers[0].URLs[protocol.Anthropic] != "https://a" {
		t.Fatal("copy aliases URL map")
	}
	if errs := Validate(decoded); len(errs) > 0 {
		t.Fatalf("roundtrip invalid: %+v", errs)
	}
}
