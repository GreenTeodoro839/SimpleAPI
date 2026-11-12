// Package provider executes upstream HTTP calls: it builds per-type auth
// headers, constructs the protocol-specific URL, classifies retryable failures,
// and issues the request.
package provider

import (
	"net/http"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
)

// BuildHeaders returns the outbound headers for a provider of the given type:
//   - anthropic: x-api-key + provider headers (e.g. anthropic-version).
//   - openai_completion / codex: Authorization: Bearer + provider headers.
//
// Client end-to-end headers are forwarded first, then providerHeaders overlay
// them (operator configuration wins), then auth is replaced with the provider
// key. Forwarding matters for upstreams that route or attribute requests by
// client headers: Claude Code sends its conversation id in
// X-Claude-Code-Session-Id, and OpenCode Go rejects requests without a session
// header. It also preserves user-agent, anthropic-beta, x-stainless-* and
// other client/protocol metadata.
//
// The following are deliberately not forwarded:
//   - inbound credentials (Authorization / x-api-key / X-Admin-Key) and cookies;
//   - hop-by-hop and request-framing headers (Connection, Content-Length,
//     Transfer-Encoding, ...) because net/http recomputes them for the new body;
//   - Accept-Encoding, so net/http negotiates gzip and transparently decodes it
//     (the JSON/SSE readers cannot parse br/zstd);
//   - proxy/client network metadata (Forwarded, X-Forwarded-*, X-Real-IP) and
//     browser provenance (Origin, Referer).
//
// Provider header keys are set as-is (case handled by Go's canonicalization).
func BuildHeaders(ptype, key string, providerHeaders map[string]string, inbound http.Header) http.Header {
	h := make(http.Header, len(inbound)+len(providerHeaders)+2)
	for k, values := range inbound {
		if !forwardableHeader(k) {
			continue
		}
		for _, v := range values {
			h.Add(k, v)
		}
	}
	for k, v := range providerHeaders {
		h.Set(k, v)
	}
	// Inbound credentials never reach this point (forwardableHeader excludes
	// them), so setting the provider credential is enough. A configured
	// x-api-key/Authorization for the opposite protocol is left untouched.
	switch ptype {
	case protocol.Anthropic:
		if key != "" {
			h.Set("x-api-key", key)
		}
	default: // openai_completion, codex
		if key != "" {
			h.Set("Authorization", "Bearer "+key)
		}
	}
	return h
}

// forwardableHeader reports whether an inbound request header may be sent to
// the upstream. Keys are compared in canonical form, so callers may pass either
// canonical or lowercase names.
func forwardableHeader(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Authorization", "Proxy-Authorization", "X-Api-Key", "X-Admin-Key",
		"Cookie", "Set-Cookie",
		"Connection", "Proxy-Connection", "Keep-Alive", "Te", "Trailer",
		"Transfer-Encoding", "Upgrade", "Content-Length", "Expect",
		"Accept-Encoding",
		"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
		"X-Real-Ip", "Origin", "Referer":
		return false
	}
	return true
}
