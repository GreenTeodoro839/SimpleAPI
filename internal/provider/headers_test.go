package provider

import (
	"net/http"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
)

func TestBuildHeadersForwardsClientRoutingHeaders(t *testing.T) {
	inbound := http.Header{
		"X-Claude-Code-Session-Id": {"07b08bc5-385f-4f57-8a89-ede77d63da8f"},
		"Anthropic-Beta":           {"claude-code-20250219,interleaved-thinking-2025-05-14"},
		"Anthropic-Version":        {"2023-06-01"},
		"User-Agent":               {"claude-cli/2.1.322 (external, sdk-cli)"},
		"X-Stainless-Retry-Count":  {"0"},
		"X-Multi":                  {"a", "b"},
		// Must never reach the upstream.
		"X-Api-Key":         {"inbound-secret"},
		"X-Admin-Key":       {"proxy-admin-secret"},
		"Authorization":     {"Bearer inbound-secret"},
		"Cookie":            {"session=secret"},
		"Content-Length":    {"4304"},
		"Accept-Encoding":   {"gzip, deflate, br, zstd"},
		"Connection":        {"keep-alive"},
		"Transfer-Encoding": {"chunked"},
		"X-Forwarded-For":   {"203.0.113.9"},
		"X-Real-Ip":         {"203.0.113.9"},
		"Origin":            {"https://example.com"},
		"Referer":           {"https://example.com/"},
	}

	h := BuildHeaders(protocol.Anthropic, "upstream-secret", map[string]string{
		"anthropic-version": "2024-01-01", // provider config wins over the client
		"X-Configured":      "yes",
	}, inbound)

	for k, want := range map[string]string{
		"X-Claude-Code-Session-Id": "07b08bc5-385f-4f57-8a89-ede77d63da8f",
		"Anthropic-Beta":           "claude-code-20250219,interleaved-thinking-2025-05-14",
		"Anthropic-Version":        "2024-01-01",
		"User-Agent":               "claude-cli/2.1.322 (external, sdk-cli)",
		"X-Stainless-Retry-Count":  "0",
		"X-Configured":             "yes",
		"X-Api-Key":                "upstream-secret",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if got := h.Values("X-Multi"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("X-Multi = %v, want [a b]", got)
	}
	for _, k := range []string{
		"Authorization", "Cookie", "Content-Length", "Accept-Encoding",
		"Connection", "Transfer-Encoding", "X-Forwarded-For", "X-Real-Ip",
		"Origin", "Referer", "X-Admin-Key",
	} {
		if got := h.Get(k); got != "" {
			t.Errorf("%s leaked upstream: %q", k, got)
		}
	}
}

func TestBuildHeadersReplacesAuthPerProtocol(t *testing.T) {
	inbound := http.Header{
		"x-api-key":                {"inbound-anthropic"},
		"authorization":            {"Bearer inbound-bearer"},
		"x-claude-code-session-id": {"session"},
	}

	anthropic := BuildHeaders(protocol.Anthropic, "anthropic-key", nil, inbound)
	if got := anthropic.Get("X-Api-Key"); got != "anthropic-key" {
		t.Errorf("anthropic x-api-key = %q", got)
	}
	if got := anthropic.Get("Authorization"); got != "" {
		t.Errorf("anthropic leaked Authorization: %q", got)
	}

	openai := BuildHeaders(protocol.OpenAICompletion, "openai-key", nil, inbound)
	if got := openai.Get("Authorization"); got != "Bearer openai-key" {
		t.Errorf("openai Authorization = %q", got)
	}
	if got := openai.Get("X-Api-Key"); got != "" {
		t.Errorf("openai leaked x-api-key: %q", got)
	}

	// Inbound credentials are filtered out even when no provider key is
	// configured, so the client's key can never become the upstream credential.
	noKey := BuildHeaders(protocol.Anthropic, "", nil, inbound)
	if got := noKey.Get("X-Api-Key"); got != "" {
		t.Errorf("empty provider key leaked x-api-key: %q", got)
	}
	if got := noKey.Get("Authorization"); got != "" {
		t.Errorf("empty provider key leaked Authorization: %q", got)
	}
}

func TestForwardableHeaderCaseInsensitive(t *testing.T) {
	for _, k := range []string{"authorization", "AUTHORIZATION", "x-api-key", "X-API-KEY", "x-admin-key", "content-length", "accept-encoding"} {
		if forwardableHeader(k) {
			t.Errorf("%q should not be forwardable", k)
		}
	}
	for _, k := range []string{"x-claude-code-session-id", "anthropic-beta", "user-agent", "x-opencode-session"} {
		if !forwardableHeader(k) {
			t.Errorf("%q should be forwardable", k)
		}
	}
}
