package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

var clientPaths = map[string]string{
	protocol.Anthropic:        "/v1/messages",
	protocol.OpenAICompletion: "/v1/chat/completions",
	protocol.Codex:            "/v1/responses",
}
var upstreamPaths = map[string]string{
	protocol.Anthropic:        "/native/anthropic/v1/messages",
	protocol.OpenAICompletion: "/native/openai_completion/chat/completions",
	protocol.Codex:            "/native/codex/responses",
}

func multiRequest(pr string, stream bool) []byte {
	fields := map[string]any{"model": "client-model", "stream": stream, "native_extension": map[string]any{"keep": "unchanged"}}
	if pr == protocol.Codex {
		fields["input"] = "hello"
	} else {
		fields["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
		fields["max_tokens"] = 16
	}
	body, _ := json.Marshal(fields)
	return body
}

func nativeReply(w http.ResponseWriter, pr string, stream bool) {
	body := map[string]any{"id": "r", "model": "real-model", "native_response": "preserved", "usage": map[string]int{"input_tokens": 3, "output_tokens": 2}}
	switch pr {
	case protocol.Anthropic:
		body["type"] = "message"
		body["role"] = "assistant"
		body["content"] = []any{map[string]any{"type": "text", "text": "hello"}}
	case protocol.OpenAICompletion:
		body["object"] = "chat.completion"
		body["choices"] = []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "hello"}, "finish_reason": "stop"}}
		body["usage"] = map[string]int{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
	case protocol.Codex:
		body["object"] = "response"
		body["status"] = "completed"
		body["output"] = []any{}
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	switch pr {
	case protocol.Anthropic:
		body = map[string]any{"type": "message_start", "message": body}
	case protocol.Codex:
		body = map[string]any{"type": "response.completed", "response": body}
	}
	data, _ := json.Marshal(body)
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
}

func multiServer(t *testing.T, p config.Provider) (http.Handler, *runtime.Runtime, string) {
	t.Helper()
	listen := "127.0.0.1:0"
	cfg := &config.Config{Version: 1, Server: config.ServerConfig{Listen: &listen}, Management: config.ManagementConfig{AdminKey: "admin"}, Providers: []config.Provider{p}, APIKeys: []config.ClientApiKey{{Name: "test", Key: "client-key", AllowedProtocols: protocol.All, Models: []config.ClientModel{{Model: p.Name + "/real-model", AliasB: "client-model"}}}}}
	if errs := config.Validate(cfg); len(errs) > 0 {
		t.Fatalf("invalid fixture: %+v", errs)
	}
	idx, err := indexes.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	rt := runtime.New(cfg, cfg, idx, path, logger)
	return NewServer(rt, logger), rt, path
}

func multiCall(server http.Handler, method, path string, body []byte, admin bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if admin {
		req.Header.Set("X-Admin-Key", "admin")
	} else {
		req.Header.Set("Authorization", "Bearer client-key")
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	return w
}

func TestMultiProtocolNativeRoutes(t *testing.T) {
	for _, def := range protocol.All {
		for _, incoming := range protocol.All {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", def, incoming, stream), func(t *testing.T) {
					var mu sync.Mutex
					calls := 0
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						calls++
						mu.Unlock()
						if r.URL.Path != upstreamPaths[incoming] {
							t.Errorf("wrong endpoint: %s", r.URL.Path)
						}
						data, _ := io.ReadAll(r.Body)
						if gjson.GetBytes(data, "model").String() != "real-model" {
							t.Errorf("upstream model: %s", data)
						}
						if gjson.GetBytes(data, "native_extension.keep").String() != "unchanged" {
							t.Errorf("native field lost: %s", data)
						}
						if incoming == protocol.Anthropic {
							if r.Header.Get("X-Api-Key") != "shared" || r.Header.Get("Authorization") != "" {
								t.Errorf("wrong anthropic auth: %v", r.Header)
							}
						} else if r.Header.Get("Authorization") != "Bearer shared" || r.Header.Get("X-Api-Key") != "" {
							t.Errorf("wrong bearer auth: %v", r.Header)
						}
						if r.Header.Get("X-Upstream") != "shared-header" {
							t.Error("shared headers missing")
						}
						nativeReply(w, incoming, stream)
					}))
					defer up.Close()
					urls := map[string]string{}
					for _, pr := range protocol.All {
						urls[pr] = up.URL + "/native/" + pr
					}
					p := config.Provider{Name: "multi", Type: def, URL: "http://127.0.0.1:1/legacy-must-not-be-used", URLs: urls, Key: "shared", Headers: map[string]string{"X-Upstream": "shared-header"}, Models: []config.ProviderModel{{Model: "real-model"}}}
					server, rt, _ := multiServer(t, p)
					w := multiCall(server, http.MethodPost, clientPaths[incoming], multiRequest(incoming, stream), false)
					if w.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", w.Code, w.Body)
					}
					if !strings.Contains(w.Body.String(), `"client-model"`) || !strings.Contains(w.Body.String(), `"native_response":"preserved"`) {
						t.Fatalf("response lost native fields or model: %s", w.Body)
					}
					rows := rt.Usage().Snapshot()
					if len(rows) != 1 || rows[0].ProviderType != incoming || rows[0].InputTokens != 3 || rows[0].OutputTokens != 2 {
						t.Fatalf("native usage uses wrong protocol: %+v", rows)
					}
					entries := rt.CallLog().Recent(1)
					if len(entries) != 1 || entries[0].ProviderType != incoming {
						t.Fatalf("native call log uses wrong protocol: %+v", entries)
					}
					mu.Lock()
					defer mu.Unlock()
					if calls != 1 {
						t.Errorf("calls=%d", calls)
					}
				})
			}
		}
	}
}

func TestProviderURLsManagementRoundTrip(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nativeReply(w, protocol.OpenAICompletion, false) }))
	defer up.Close()
	p := config.Provider{Name: "multi", Type: protocol.OpenAICompletion, URL: up.URL, Models: []config.ProviderModel{{Model: "real-model"}}}
	server, rt, path := multiServer(t, p)
	p.URL = ""
	p.URLs = map[string]string{protocol.OpenAICompletion: up.URL + "/native/openai_completion", protocol.Codex: up.URL + "/native/codex"}
	body, _ := json.Marshal(p)
	w := multiCall(server, http.MethodPut, "/v0/management/providers/multi", body, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	w = multiCall(server, http.MethodGet, "/v0/management/providers/multi", nil, true)
	if gjson.Get(w.Body.String(), "urls.codex").String() != p.URLs[protocol.Codex] {
		t.Fatalf("GET: %s", w.Body)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Providers[0].URLs[protocol.Codex] != p.URLs[protocol.Codex] {
		t.Fatalf("disk lost URLs: %+v", loaded.Providers)
	}
	w = multiCall(server, http.MethodPost, "/v0/management/reload", nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("reload: %d %s", w.Code, w.Body)
	}
	selected := rt.Snapshot().Indexes.Providers["multi"].ResolveUpstream(protocol.Codex)
	if selected.Type != protocol.Codex || selected.URL != p.URLs[protocol.Codex] {
		t.Fatalf("runtime: %+v", selected)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p.URLs["typo"] = "https://bad"
	body, _ = json.Marshal(p)
	w = multiCall(server, http.MethodPut, "/v0/management/providers/multi", body, true)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid PUT: %d %s", w.Code, w.Body)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid mutation changed disk")
	}
	if _, ok := rt.Snapshot().Indexes.Providers["multi"].URLs["typo"]; ok {
		t.Fatal("invalid mutation changed runtime")
	}
}

func TestMissingNativeProtocolUsesDefault(t *testing.T) {
	for _, def := range protocol.All {
		for _, incoming := range protocol.All {
			if def == incoming {
				continue
			}
			for _, legacy := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/legacy=%v", def, incoming, legacy), func(t *testing.T) {
					var mu sync.Mutex
					calls := 0
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						calls++
						mu.Unlock()
						if r.URL.Path != upstreamPaths[def] {
							t.Errorf("wrong fallback endpoint: %s", r.URL.Path)
						}
						data, _ := io.ReadAll(r.Body)
						if gjson.GetBytes(data, "model").String() != "real-model" {
							t.Errorf("model: %s", data)
						}
						if gjson.GetBytes(data, "native_extension").Exists() {
							t.Errorf("request was not translated: %s", data)
						}
						if def == protocol.Codex {
							if !gjson.GetBytes(data, "input").Exists() {
								t.Errorf("missing codex input: %s", data)
							}
						} else if !gjson.GetBytes(data, "messages").Exists() {
							t.Errorf("missing messages: %s", data)
						}
						nativeReply(w, def, false)
					}))
					defer up.Close()
					p := config.Provider{Name: "multi", Type: def, URLs: map[string]string{incoming: ""}, Key: "shared", Models: []config.ProviderModel{{Model: "real-model"}}}
					if legacy {
						p.URL = up.URL + "/native/" + def
					} else {
						p.URLs[def] = up.URL + "/native/" + def
						p.URL = "http://127.0.0.1:1/unused-legacy"
					}
					server, rt, _ := multiServer(t, p)
					w := multiCall(server, http.MethodPost, clientPaths[incoming], multiRequest(incoming, false), false)
					if w.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", w.Code, w.Body)
					}
					if gjson.Get(w.Body.String(), "model").String() != "client-model" {
						t.Fatalf("response model: %s", w.Body)
					}
					rows := rt.Usage().Snapshot()
					if len(rows) != 1 || rows[0].ProviderType != def || rows[0].InputTokens != 3 || rows[0].OutputTokens != 2 {
						t.Fatalf("usage must describe raw upstream: %+v", rows)
					}
					mu.Lock()
					defer mu.Unlock()
					if calls != 1 {
						t.Errorf("calls=%d", calls)
					}
				})
			}
		}
	}
}

func TestNativeFailureDoesNotTranslateToDefault(t *testing.T) {
	for _, status := range []int{400, 503} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/stream=%v", status, stream), func(t *testing.T) {
				var mu sync.Mutex
				paths := []string{}
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					paths = append(paths, r.URL.Path)
					mu.Unlock()
					if r.URL.Path == upstreamPaths[protocol.OpenAICompletion] {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(status)
						fmt.Fprint(w, `{"error":{"message":"native failed"}}`)
						return
					}
					nativeReply(w, protocol.Anthropic, stream)
				}))
				defer up.Close()
				p := config.Provider{Name: "multi", Type: protocol.Anthropic, URL: up.URL + "/native/anthropic", URLs: map[string]string{protocol.OpenAICompletion: up.URL + "/native/openai_completion"}, Models: []config.ProviderModel{{Model: "real-model"}}}
				server, _, _ := multiServer(t, p)
				w := multiCall(server, http.MethodPost, clientPaths[protocol.OpenAICompletion], multiRequest(protocol.OpenAICompletion, stream), false)
				want := status
				if status == 503 {
					want = http.StatusBadGateway
				}
				if w.Code != want {
					t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(paths) != 1 || paths[0] != upstreamPaths[protocol.OpenAICompletion] {
					t.Fatalf("failure incorrectly changed protocol: %v", paths)
				}
			})
		}
	}
}
