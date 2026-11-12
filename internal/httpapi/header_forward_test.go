package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
)

// TestClientHeadersForwardedUpstream guards the proxy's most important
// passthrough property: the client's end-to-end headers reach the upstream,
// while inbound credentials/cookies/proxy metadata do not. Claude Code's
// native session header is the concrete regression: OpenCode Go rejects every
// request without it.
func TestClientHeadersForwardedUpstream(t *testing.T) {
	cases := []struct {
		name         string
		providerType string
		wantPath     string
		translated   bool
	}{
		{"same-protocol", protocol.Anthropic, "/v1/messages", false},
		{"cross-protocol", protocol.OpenAICompletion, "/chat/completions", true},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != tc.wantPath {
						t.Errorf("upstream path = %q, want %q", r.URL.Path, tc.wantPath)
					}
					wantQuery := "beta=true"
					if tc.translated {
						wantQuery = ""
					}
					if got := r.URL.RawQuery; got != wantQuery {
						t.Errorf("upstream query = %q, want %q", got, wantQuery)
					}
					for k, want := range map[string]string{
						"X-Claude-Code-Session-Id": "07b08bc5-385f-4f57-8a89-ede77d63da8f",
						"User-Agent":               "claude-cli/2.1.322 (external, sdk-cli)",
						"Anthropic-Beta":           "claude-code-20250219,interleaved-thinking-2025-05-14",
						"X-Configured":             "yes",
					} {
						if got := r.Header.Get(k); got != want {
							t.Errorf("%s = %q, want %q", k, got, want)
						}
					}
					// The provider key always replaces the inbound credential.
					if tc.providerType == protocol.Anthropic {
						if got := r.Header.Get("X-Api-Key"); got != "shared" {
							t.Errorf("x-api-key = %q, want provider key", got)
						}
						if got := r.Header.Get("Authorization"); got != "" {
							t.Errorf("inbound Authorization leaked: %q", got)
						}
					} else {
						if got := r.Header.Get("Authorization"); got != "Bearer shared" {
							t.Errorf("Authorization = %q, want provider bearer", got)
						}
						if got := r.Header.Get("X-Api-Key"); got != "" {
							t.Errorf("inbound x-api-key leaked: %q", got)
						}
					}
					for _, k := range []string{"Cookie", "X-Forwarded-For", "X-Real-Ip", "Origin", "Referer"} {
						if got := r.Header.Get(k); got != "" {
							t.Errorf("%s leaked upstream: %q", k, got)
						}
					}
					nativeReply(w, tc.providerType, stream)
				}))
				defer up.Close()

				p := config.Provider{
					Name:    "multi",
					Type:    tc.providerType,
					URL:     up.URL,
					Key:     "shared",
					Headers: map[string]string{"X-Configured": "yes"},
					Models:  []config.ProviderModel{{Model: "real-model"}},
				}
				server, _, _ := multiServer(t, p)

				req := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", bytes.NewReader(multiRequest(protocol.Anthropic, stream)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer client-key")
				req.Header.Set("x-api-key", "inbound-must-not-leak")
				req.Header.Set("X-Claude-Code-Session-Id", "07b08bc5-385f-4f57-8a89-ede77d63da8f")
				req.Header.Set("User-Agent", "claude-cli/2.1.322 (external, sdk-cli)")
				req.Header.Set("anthropic-beta", "claude-code-20250219,interleaved-thinking-2025-05-14")
				req.Header.Set("Cookie", "session=secret")
				req.Header.Set("X-Forwarded-For", "203.0.113.9")
				req.Header.Set("X-Real-Ip", "203.0.113.9")
				req.Header.Set("Origin", "https://example.com")
				req.Header.Set("Referer", "https://example.com/")

				w := httptest.NewRecorder()
				server.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", w.Code, w.Body)
				}
			})
		}
	}
}
