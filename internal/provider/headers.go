// Package provider executes upstream HTTP calls: it builds per-type auth
// headers, constructs the protocol-specific URL, classifies retryable failures,
// and issues the request.
package provider

import (
	"net/http"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
)

// BuildHeaders returns the outbound headers for a provider of the given type.
//   - anthropic: x-api-key + provider headers (e.g. anthropic-version).
//   - openai_completion / codex: Authorization: Bearer + provider headers.
//
// Provider header keys are set as-is (case handled by Go's canonicalization).
func BuildHeaders(ptype, key string, providerHeaders map[string]string) http.Header {
	h := http.Header{}
	for k, v := range providerHeaders {
		h.Set(k, v)
	}
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
