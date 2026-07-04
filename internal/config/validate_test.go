package config

import (
	"testing"
)

func ptrInt(v int) *int    { return &v }
func ptrStr(v string) *string { return &v }
func ptrBool(v bool) *bool { return &v }

func validConfig() *Config {
	return &Config{
		Version: 1,
		Server:  ServerConfig{Listen: ptrStr("127.0.0.1:8317")},
		Proxy:   ProxyConfig{MaxConsecutiveFailures: ptrInt(2)},
		Management: ManagementConfig{Enabled: ptrBool(true), AdminKey: "admin"},
		Providers: []Provider{
			{Name: "p1", Type: "anthropic", URL: "https://a", Key: "k1",
				Models: []ProviderModel{{Model: "m1", AliasA: "a1"}}},
		},
		APIKeys: []ClientApiKey{
			{Name: "dev", Key: "ck1", AllowedProtocols: []string{"anthropic"},
				Models: []ClientModel{{Model: "p1/a1"}}},
		},
	}
}

func hasCode(errs []ValidationError, code string) bool {
	for _, e := range errs {
		if e.Code == code {
			return true
		}
	}
	return false
}

func TestValidConfig(t *testing.T) {
	if errs := Validate(validConfig()); len(errs) != 0 {
		t.Fatalf("expected valid, got: %+v", errs)
	}
}

func TestDuplicateProviderName(t *testing.T) {
	cfg := validConfig()
	cfg.Providers = append(cfg.Providers, Provider{Name: "p1", Type: "anthropic", URL: "https://b", Models: []ProviderModel{{Model: "m2"}}})
	if !hasCode(Validate(cfg), "duplicate_provider") {
		t.Error("expected duplicate_provider")
	}
}

func TestDuplicateAliasA(t *testing.T) {
	cfg := validConfig()
	cfg.Providers[0].Models = append(cfg.Providers[0].Models, ProviderModel{Model: "m2", AliasA: "a1"})
	if !hasCode(Validate(cfg), "duplicate_aliasA") {
		t.Error("expected duplicate_aliasA")
	}
}

func TestProviderNameSlash(t *testing.T) {
	cfg := validConfig()
	cfg.Providers[0].Name = "bad/name"
	if !hasCode(Validate(cfg), "provider_name_slash") {
		t.Error("expected provider_name_slash")
	}
}

func TestInvalidProviderType(t *testing.T) {
	cfg := validConfig()
	cfg.Providers[0].Type = "gemini"
	if !hasCode(Validate(cfg), "invalid_provider_type") {
		t.Error("expected invalid_provider_type")
	}
}

func TestClientModelNotFound(t *testing.T) {
	cfg := validConfig()
	cfg.APIKeys[0].Models[0].Model = "p1_nonexistent"
	if !hasCode(Validate(cfg), "model_not_found") {
		t.Error("expected model_not_found")
	}
}

func TestWebSearchSelfLoop(t *testing.T) {
	cfg := validConfig()
	cfg.Providers[0].Models[0].AnthropicWebSearchForward = &WebSearchForward{Enabled: true, TargetModel: "p1/a1"}
	if !hasCode(Validate(cfg), "invalid_web_search_target") {
		t.Error("expected invalid_web_search_target for self-loop")
	}
}

func TestPayloadRawJSONInvalid(t *testing.T) {
	cfg := validConfig()
	cfg.Payload.OverrideRaw = []PayloadRule{{
		Models: []PayloadModelRule{{Name: "p1_a1"}},
		Params: map[string]interface{}{"response_format": "{not json}"},
	}}
	if !hasCode(Validate(cfg), "invalid_raw_json") {
		t.Error("expected invalid_raw_json")
	}
}

func TestPayloadModelFieldProtected(t *testing.T) {
	cfg := validConfig()
	cfg.Payload.Override = []PayloadRule{{
		Models: []PayloadModelRule{{Name: "p1_a1"}},
		Params: map[string]interface{}{"model": "x"},
	}}
	if !hasCode(Validate(cfg), "model_field_protected") {
		t.Error("expected model_field_protected")
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("FOO", "bar")
	t.Setenv("EMPTY", "")
	cases := map[string]string{
		"${FOO}":         "bar",
		"${MISSING}":     "",
		"${EMPTY:-def}":  "def",
		"${FOO:-def}":    "bar",
		"pre-${FOO}-post": "pre-bar-post",
	}
	for in, want := range cases {
		if got := ExpandEnv(in); got != want {
			t.Errorf("ExpandEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseInternalModelID(t *testing.T) {
	cases := []struct {
		in               string
		prov, alias      string
		ok               bool
	}{
		{"p/a", "p", "a", true},
		{"p/a/b", "p", "a/b", true}, // split on first slash only
		{"pa", "", "", false},
		{"/a", "", "", false},
		{"p/", "", "", false},
	}
	for _, c := range cases {
		prov, alias, ok := ParseInternalModelID(c.in)
		if ok != c.ok || prov != c.prov || alias != c.alias {
			t.Errorf("ParseInternalModelID(%q) = (%q,%q,%v) want (%q,%q,%v)", c.in, prov, alias, ok, c.prov, c.alias, c.ok)
		}
	}
}
