package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/gin-gonic/gin"
)

// buildRT builds a runtime with one provider/model and a single inbound key
// controlled by `enabled`. Used by the enable/disable middleware tests.
func buildRT(t *testing.T, enabled *bool, keyValue string) *runtime.Runtime {
	t.Helper()
	raw := &config.Config{
		Providers: []config.Provider{
			{Name: "p", Type: "openai_completion", URL: "u",
				Models: []config.ProviderModel{{Model: "m"}}}, // id: p/m
		},
		APIKeys: []config.ClientApiKey{
			{Name: "k", Key: keyValue, Enabled: enabled,
				Models: []config.ClientModel{{Model: "p/m"}}},
		},
	}
	expanded := config.DeepCopy(raw)
	idx, err := indexes.Build(expanded)
	if err != nil {
		t.Fatalf("indexes.Build: %v", err)
	}
	return runtime.New(raw, expanded, idx, "")
}

// do runs the auth middleware against GET /v1/chat/completions with the given
// bearer token and returns the recorded response.
func do(t *testing.T, rt *runtime.Runtime, token string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Middleware(rt))
	r.GET("/v1/chat/completions", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMiddlewareRejectsDisabledKey(t *testing.T) {
	falseVal := false
	rt := buildRT(t, &falseVal, "kk")

	w := do(t, rt, "kk")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (disabled key must be unusable)", w.Code, http.StatusUnauthorized)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v (body=%s)", err, w.Body.String())
	}
	if body.Error.Code != "api_key_disabled" {
		t.Errorf("error.code = %q, want api_key_disabled", body.Error.Code)
	}
}

func TestMiddlewareAcceptsEnabledAndDefaultKeys(t *testing.T) {
	// Explicit enabled: true is usable.
	trueVal := true
	rt := buildRT(t, &trueVal, "kk")
	if w := do(t, rt, "kk"); w.Code != http.StatusNoContent {
		t.Errorf("enabled:true key: status = %d, want %d", w.Code, http.StatusNoContent)
	}

	// Omitted enabled defaults to usable.
	rt2 := buildRT(t, nil, "kk2")
	if w := do(t, rt2, "kk2"); w.Code != http.StatusNoContent {
		t.Errorf("omitted-enabled key: status = %d, want %d", w.Code, http.StatusNoContent)
	}
}
