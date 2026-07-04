// Package modelrewrite rewrites the response "model" field back to the
// client-visible aliasB (DEVELOPMENT.md §8).
//
// The model field lives in different places across protocols:
//   - OpenAI Chat Completions and Codex Responses: top-level "model".
//   - Anthropic non-stream Message: top-level "model".
//   - Anthropic streaming message_start event: nested "message.model".
//
// rewriteModelFields covers both the top-level and the nested anthropic path.
package modelrewrite

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NonStream rewrites the model fields in a complete JSON response body to
// aliasB, only for fields that already exist.
func NonStream(body []byte, aliasB string) []byte {
	return rewriteModelFields(body, aliasB)
}

// rewriteModelFields sets "model", "message.model", and "response.model" to
// aliasB where present (openai/codex top-level, anthropic message_start nested,
// codex SSE response.* events nested under "response").
func rewriteModelFields(body []byte, aliasB string) []byte {
	if gjson.GetBytes(body, "model").Exists() {
		if out, err := sjson.SetBytes(body, "model", aliasB); err == nil {
			body = out
		}
	}
	if gjson.GetBytes(body, "message.model").Exists() {
		if out, err := sjson.SetBytes(body, "message.model", aliasB); err == nil {
			body = out
		}
	}
	if gjson.GetBytes(body, "response.model").Exists() {
		if out, err := sjson.SetBytes(body, "response.model", aliasB); err == nil {
			body = out
		}
	}
	return body
}
