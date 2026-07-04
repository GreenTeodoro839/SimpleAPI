package provider

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
)

// defaultClient has no overall timeout; per-request deadlines come from the
// context so long streams are not prematurely killed.
var defaultClient = &http.Client{Timeout: 0}

// Do issues a POST to the provider's protocol-specific URL with the given body.
// The caller owns response streaming; resp.Body must be closed by the caller.
func Do(ctx context.Context, pe *indexes.ProviderEntry, body []byte) (*http.Response, error) {
	url := BuildURL(pe.URL, pe.Type)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = BuildHeaders(pe.Type, pe.Key, pe.Headers)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return defaultClient.Do(req)
}

// BuildURL joins a provider base URL with the protocol-specific path.
func BuildURL(base, ptype string) string {
	b := strings.TrimRight(base, "/")
	switch ptype {
	case protocol.Anthropic:
		return b + "/v1/messages"
	case protocol.OpenAICompletion:
		return b + "/chat/completions"
	case protocol.Codex:
		return b + "/responses"
	}
	return b
}
