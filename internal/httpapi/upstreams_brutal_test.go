package httpapi

// Independent brutal / adversarial tests for multi-protocol provider upstreams.
// These tests are self-contained: they never reference the helpers in
// upstreams_test.go so the two files can evolve independently. Everything runs
// against in-process httptest doubles; no real upstream is contacted.
//
// Coverage:
//   - full type × client-protocol × urls-shape selection matrix (native first,
//     urls[type] default, legacy url fallback, empty-string = unconfigured)
//   - native passthrough field fidelity (request + response, stream + non-stream)
//   - translated fallback request shape + usage parsed from the RAW upstream body
//   - native errors never silently re-route to translation, cross-provider
//     failover still works
//   - provider concurrency quota shared across protocols
//   - config reload: new requests pick up the new snapshot, in-flight requests
//     keep the old one
//   - management CRUD lifecycle for urls, persistence, invalid mutations
//   - legacy type/url-only config compatibility (urls omitted on disk)
//   - payload rule protocol/from-protocol match the ACTUAL upstream protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ---------------------------------------------------------------------------
// Brutal helpers (names prefixed "brutal" to stay independent of
// upstreams_test.go).
// ---------------------------------------------------------------------------

const (
	brutalUpstreamKey = "brutal-upstream-key"
	brutalClientKey   = "brutal-client-key"
	brutalAdminKey    = "brutal-admin-key"
	brutalAliasB      = "brutal-client"
	brutalUpModel     = "real-model"
)

// brutalPaths maps full upstream request path -> protocol. Native slots are
// prefixed; legacy slots are the bare BuildURL suffixes of the server root.
var brutalPaths = map[string]string{
	"/nat-anthropic/v1/messages":      protocol.Anthropic,
	"/nat-openai/v1/chat/completions": protocol.OpenAICompletion,
	"/nat-codex/v1/responses":         protocol.Codex,
	"/v1/messages":                    protocol.Anthropic,
	"/chat/completions":               protocol.OpenAICompletion,
	"/responses":                      protocol.Codex,
}

func brutalNativeBase(pr string) string {
	switch pr {
	case protocol.Anthropic:
		return "/nat-anthropic"
	case protocol.OpenAICompletion:
		return "/nat-openai/v1"
	case protocol.Codex:
		return "/nat-codex/v1"
	}
	panic("bad protocol " + pr)
}

// brutalTokens are distinct per protocol so a wrong-protocol usage parse shows
// up as a mismatch, not a coincidence.
func brutalTokens(pr string) (int64, int64) {
	switch pr {
	case protocol.Anthropic:
		return 11, 7
	case protocol.OpenAICompletion:
		return 13, 5
	case protocol.Codex:
		return 17, 3
	}
	return 0, 0
}

// brutalCall is one observed upstream request.
type brutalCall struct {
	Path      string
	XAPIKey   string
	Bearer    string
	SharedHdr string
	Body      []byte
}

// brutalMock is a controllable upstream doubling all three protocols. It can
// block one in-flight request on a gate (for concurrency/reload tests) and
// fail a specific path with a fixed status (for error tests).
type brutalMock struct {
	mu    sync.Mutex
	calls []brutalCall

	gatePath  string
	enter     chan struct{}
	enterOnce sync.Once
	entered   chan struct{} // one token per gated arrival (cap-bounded)
	release   chan struct{}
	releaseMu sync.Once

	failPath   string
	failStatus int
}

func newBrutalMock() *brutalMock {
	return &brutalMock{enter: make(chan struct{}), entered: make(chan struct{}, 16), release: make(chan struct{})}
}

// unblock releases every handler waiting on the gate. Tests defer it so a
// t.Fatal before the normal release never deadlocks httptest.Server.Close.
func (m *brutalMock) unblock() {
	m.releaseMu.Do(func() { close(m.release) })
}

func (m *brutalMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	call := brutalCall{
		Path:      r.URL.Path,
		XAPIKey:   r.Header.Get("X-Api-Key"),
		Bearer:    r.Header.Get("Authorization"),
		SharedHdr: r.Header.Get("X-Brutal-Shared"),
		Body:      body,
	}
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()

	if m.gatePath != "" && r.URL.Path == m.gatePath {
		m.enterOnce.Do(func() { close(m.enter) })
		select {
		case m.entered <- struct{}{}:
		default:
		}
		<-m.release
	}
	if m.failPath == r.URL.Path && m.failStatus != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.failStatus)
		fmt.Fprint(w, `{"error":{"type":"upstream_error","message":"brutal mock failure"}}`)
		return
	}

	pr, ok := brutalPaths[r.URL.Path]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"message":"unknown mock path `+"`"+`"}}`)
		return
	}
	if gjson.GetBytes(body, "stream").Bool() {
		brutalStreamReply(w, pr)
	} else {
		brutalNonStreamReply(w, pr)
	}
}

func (m *brutalMock) snapshot() []brutalCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]brutalCall(nil), m.calls...)
}

func (m *brutalMock) nonStreamReply(pr string) []byte {
	in, out := brutalTokens(pr)
	switch pr {
	case protocol.Anthropic:
		return []byte(fmt.Sprintf(`{"type":"message","role":"assistant","model":%q,
			"content":[{"type":"text","text":"ok-anthropic"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":%d,"output_tokens":%d},
			"brutal_native":{"proto":"anthropic","depth":{"keeps":["a",2,true]}}}`,
			brutalUpModel, in, out))
	case protocol.OpenAICompletion:
		return []byte(fmt.Sprintf(`{"object":"chat.completion","model":%q,
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok-openai_completion"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d},
			"brutal_native":{"proto":"openai_completion","depth":{"keeps":["a",2,true]}}}`,
			brutalUpModel, in, out, in+out))
	default: // codex
		return []byte(fmt.Sprintf(`{"object":"response","model":%q,"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok-codex"}]}],
			"usage":{"input_tokens":%d,"output_tokens":%d},
			"brutal_native":{"proto":"codex","depth":{"keeps":["a",2,true]}}}`,
			brutalUpModel, in, out))
	}
}

func (m *brutalMock) streamReply(pr string) []byte {
	in, out := brutalTokens(pr)
	switch pr {
	case protocol.Anthropic:
		return []byte(fmt.Sprintf("event: message_start\n"+
			"data: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"usage\":{\"input_tokens\":%d,\"output_tokens\":1}}}\n\n"+
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok-anthropic\"}}\n\n"+
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":%d}}\n\n"+
			"data: {\"type\":\"message_stop\"}\n\n", brutalUpModel, in, out))
	case protocol.OpenAICompletion:
		return []byte(fmt.Sprintf("data: {\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok-openai_completion\"}}]}\n\n"+
			"data: {\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n"+
			"data: [DONE]\n\n", brutalUpModel, brutalUpModel, in, out, in+out))
	default: // codex
		return []byte(fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"model\":%q,\"status\":\"in_progress\"}}\n\n"+
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok-codex\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"model\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok-codex\"}]}],\"usage\":{\"input_tokens\":%d,\"output_tokens\":%d}}}\n\n"+
			"data: [DONE]\n\n", brutalUpModel, brutalUpModel, in, out))
	}
}

func brutalNonStreamReply(w http.ResponseWriter, pr string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(mustReply(pr))
}

func brutalStreamReply(w http.ResponseWriter, pr string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Write(mustReplyStream(pr))
}

// reply caches are written by the owning mock; these package-level helpers just
// dispatch on protocol for a fresh mock each time.
var replyMu sync.Mutex

func mustReply(pr string) []byte {
	replyMu.Lock()
	defer replyMu.Unlock()
	m := &brutalMock{}
	return m.nonStreamReply(pr)
}

func mustReplyStream(pr string) []byte {
	replyMu.Lock()
	defer replyMu.Unlock()
	m := &brutalMock{}
	return m.streamReply(pr)
}

func brutalProvider(name, ptype string, urls map[string]string, legacy string) config.Provider {
	return config.Provider{
		Name: name, Type: ptype, URL: legacy, URLs: urls,
		Key:     brutalUpstreamKey,
		Headers: map[string]string{"X-Brutal-Shared": "yes"},
		Models:  []config.ProviderModel{{Model: brutalUpModel, AliasA: "shared"}},
	}
}

// brutalFixture builds a full server + runtime with one client API key owning
// the given aliasB bindings.
func brutalFixture(t *testing.T, providers []config.Provider, keyModels []config.ClientModel, mutate func(*config.Config)) (http.Handler, *runtime.Runtime, string) {
	t.Helper()
	listen := "127.0.0.1:0"
	cfg := &config.Config{
		Version:    1,
		Server:     config.ServerConfig{Listen: &listen},
		Management: config.ManagementConfig{AdminKey: brutalAdminKey},
		Providers:  providers,
		APIKeys: []config.ClientApiKey{{
			Name: "brutal-key", Key: brutalClientKey,
			AllowedProtocols: protocol.All, Models: keyModels,
		}},
	}
	if mutate != nil {
		mutate(cfg)
	}
	if errs := config.Validate(cfg); len(errs) > 0 {
		t.Fatalf("invalid brutal fixture: %+v", errs)
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

var brutalClientRoute = map[string]string{
	protocol.Anthropic:        "/v1/messages",
	protocol.OpenAICompletion: "/v1/chat/completions",
	protocol.Codex:            "/v1/responses",
}

func brutalClientCall(server http.Handler, incoming string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, brutalClientRoute[incoming], bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+brutalClientKey)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	return w
}

func brutalAdmin(server http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Key", brutalAdminKey)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	return w
}

// brutalClientBody builds a minimal valid client request for the protocol,
// with a native unknown field that must survive passthrough untouched.
func brutalClientBody(incoming string, stream bool) []byte {
	fields := map[string]any{
		"model":          brutalAliasB,
		"stream":         stream,
		"brutal_unknown": map[string]any{"n": 1, "deep": []any{"x", 2}},
	}
	switch incoming {
	case protocol.Anthropic:
		fields["max_tokens"] = 64
		fields["messages"] = []any{map[string]any{"role": "user", "content": "hi"}}
	case protocol.OpenAICompletion:
		fields["max_tokens"] = 64
		fields["messages"] = []any{map[string]any{"role": "user", "content": "hi"}}
	default: // codex
		fields["max_output_tokens"] = 64
		fields["input"] = []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "hi"}},
		}}
	}
	b, _ := json.Marshal(fields)
	return b
}

func assertAuth(t *testing.T, call brutalCall, pr string) {
	t.Helper()
	if pr == protocol.Anthropic {
		if call.XAPIKey != brutalUpstreamKey || call.Bearer != "" {
			t.Errorf("anthropic auth wrong: x-api-key=%q bearer=%q", call.XAPIKey, call.Bearer)
		}
	} else {
		if call.Bearer != "Bearer "+brutalUpstreamKey || call.XAPIKey != "" {
			t.Errorf("%s auth wrong: bearer=%q x-api-key=%q", pr, call.Bearer, call.XAPIKey)
		}
	}
	if call.SharedHdr != "yes" {
		t.Errorf("shared provider header lost")
	}
}

func assertUsageRow(t *testing.T, rt *runtime.Runtime, incoming, selected string) {
	t.Helper()
	rows := rt.Usage().Snapshot()
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	in, out := brutalTokens(selected)
	if row.ProviderType != selected || row.TargetProviderType != selected || row.SourceProtocol != incoming {
		t.Fatalf("usage row protocol wrong: got type=%s target=%s source=%s, want type/target=%s source=%s",
			row.ProviderType, row.TargetProviderType, row.SourceProtocol, selected, incoming)
	}
	if row.InputTokens != in || row.OutputTokens != out {
		t.Fatalf("usage counts = %d/%d, want %d/%d (parsed from the %s upstream body)",
			row.InputTokens, row.OutputTokens, in, out, selected)
	}
	if row.Requests != 1 || row.Failures != 0 {
		t.Fatalf("usage req/fail = %d/%d, want 1/0", row.Requests, row.Failures)
	}
}

// ---------------------------------------------------------------------------
// 1. Full selection matrix.
// ---------------------------------------------------------------------------

func TestBrutalUpstreamSelectionMatrix(t *testing.T) {
	legacyUnused := "http://127.0.0.1:1/brutal-legacy-must-not-be-called"
	type urlShape struct {
		name   string
		urls   func(up, t, i string) map[string]string
		legacy string
		want   func(t, i string) (string, string) // (protocol, origin)
	}
	shapes := []urlShape{
		{"all-native", func(up, t, i string) map[string]string {
			m := map[string]string{}
			for _, pr := range protocol.All {
				m[pr] = up + brutalNativeBase(pr)
			}
			return m
		}, legacyUnused,
			func(t, i string) (string, string) { return i, "native" }},
		{"legacy-only", func(up, t, i string) map[string]string { return nil }, "",
			func(t, i string) (string, string) { return t, "legacy" }},
		{"urls-type-only", func(up, t, i string) map[string]string {
			return map[string]string{t: up + brutalNativeBase(t)}
		}, legacyUnused,
			func(t, i string) (string, string) { return t, "native" }},
		{"urls-type-and-legacy", func(up, t, i string) map[string]string {
			return map[string]string{t: up + brutalNativeBase(t)}
		}, "",
			func(t, i string) (string, string) { return t, "native" }},
		{"incoming-only-and-legacy", func(up, t, i string) map[string]string {
			return map[string]string{i: up + brutalNativeBase(i)}
		}, "",
			// The only request this subtest sends is the incoming protocol,
			// whose native url is configured — always native.
			func(t, i string) (string, string) { return i, "native" }},
		{"empty-urls-and-legacy", func(up, t, i string) map[string]string {
			return map[string]string{protocol.Anthropic: "", protocol.OpenAICompletion: "", protocol.Codex: ""}
		}, "",
			func(t, i string) (string, string) { return t, "legacy" }},
	}

	for _, def := range protocol.All {
		for _, incoming := range protocol.All {
			for _, shape := range shapes {
				t.Run(fmt.Sprintf("%s/%s/%s", def, incoming, shape.name), func(t *testing.T) {
					mock := newBrutalMock()
					up := httptest.NewServer(mock)
					defer up.Close()

					var urls map[string]string
					if shape.urls != nil {
						urls = shape.urls(up.URL, def, incoming)
					}
					legacy := shape.legacy
					if legacy == "" {
						legacy = up.URL // bare server root = legacy slots
					}
					sel, origin := shape.want(def, incoming)

					server, rt, _ := brutalFixture(t,
						[]config.Provider{brutalProvider("multi", def, urls, legacy)},
						[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

					w := brutalClientCall(server, incoming, brutalClientBody(incoming, false))
					if w.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
					}
					calls := mock.snapshot()
					if len(calls) != 1 {
						t.Fatalf("upstream calls = %d, want 1: %+v", len(calls), calls)
					}
					var wantPath string
					if origin == "native" {
						wantPath = brutalNativeBase(sel) + map[string]string{
							protocol.Anthropic: "/v1/messages", protocol.OpenAICompletion: "/chat/completions", protocol.Codex: "/responses",
						}[sel]
					} else {
						wantPath = map[string]string{
							protocol.Anthropic: "/v1/messages", protocol.OpenAICompletion: "/chat/completions", protocol.Codex: "/responses",
						}[sel]
					}
					if calls[0].Path != wantPath {
						t.Fatalf("hit %s, want %s (shape=%s def=%s incoming=%s)", calls[0].Path, wantPath, shape.name, def, incoming)
					}
					if sel != incoming && calls[0].Body == nil {
						t.Fatal("no body")
					}
					if gjson.GetBytes(calls[0].Body, "model").String() != brutalUpModel {
						t.Fatalf("upstream model not rewritten: %s", calls[0].Body)
					}
					assertAuth(t, calls[0], sel)

					// Response: native marker survives passthrough; translated
					// bodies carry the selected protocol's text.
					if !strings.Contains(w.Body.String(), "ok-"+sel) {
						t.Fatalf("response missing native text ok-%s: %s", sel, w.Body.String())
					}
					if sel == incoming && !strings.Contains(w.Body.String(), "brutal_native") {
						t.Fatalf("native passthrough lost unknown response fields: %s", w.Body.String())
					}
					if !strings.Contains(w.Body.String(), `"model":"`+brutalAliasB+`"`) {
						t.Fatalf("response model not rewritten to aliasB: %s", w.Body.String())
					}

					// Usage and call log must describe the ACTUAL upstream protocol.
					assertUsageRow(t, rt, incoming, sel)
					entries := rt.CallLog().Recent(1)
					if len(entries) != 1 {
						t.Fatalf("call log entries = %d", len(entries))
					}
					if entries[0].ProviderType != sel || entries[0].SourceProtocol != incoming || entries[0].HTTPStatus != 200 {
						t.Fatalf("call log wrong: %+v", entries[0])
					}
					if entries[0].Alias != brutalAliasB || entries[0].Model != brutalUpModel {
						t.Fatalf("call log alias/model wrong: %+v", entries[0])
					}
				})
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Native passthrough fidelity: request body byte-shape preserved (model
// rewritten), response preserved (model rewritten), SSE framing preserved.
// ---------------------------------------------------------------------------

func TestBrutalNativePassthroughFidelity(t *testing.T) {
	for _, pr := range protocol.All {
		for _, stream := range []bool{false, true} {
			t.Run(pr+fmt.Sprintf("/stream=%v", stream), func(t *testing.T) {
				mock := newBrutalMock()
				up := httptest.NewServer(mock)
				defer up.Close()

				// Rich native request with tools / system / nested unknown fields.
				var body []byte
				switch pr {
				case protocol.Anthropic:
					body = []byte(`{"model":"` + brutalAliasB + `","max_tokens":512,"stream":` + fmt.Sprint(stream) + `,
						"system":[{"type":"text","text":"sys"}],
						"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
						"tools":[{"name":"f","description":"d","input_schema":{"type":"object","properties":{"x":{"type":"number"}}}}],
						"tool_choice":{"type":"auto"},
						"metadata":{"user_id":"u-1"},
						"brutal_unknown":{"n":[1,2,{"k":null}],"s":"keep"}}`)
				case protocol.OpenAICompletion:
					body = []byte(`{"model":"` + brutalAliasB + `","stream":` + fmt.Sprint(stream) + `,
						"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}],
						"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],
						"tool_choice":"auto",
						"response_format":{"type":"json_object"},
						"logprobs":true,"top_logprobs":3,
						"brutal_unknown":{"n":[1,2,{"k":null}],"s":"keep"}}`)
				default: // codex
					body = []byte(`{"model":"` + brutalAliasB + `","stream":` + fmt.Sprint(stream) + `,
						"instructions":"sys",
						"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
						"tools":[{"type":"function","name":"f","description":"d","parameters":{"type":"object"}}],
						"tool_choice":"auto",
						"parallel_tool_calls":false,
						"brutal_unknown":{"n":[1,2,{"k":null}],"s":"keep"}}`)
				}

				server, _, _ := brutalFixture(t,
					[]config.Provider{brutalProvider("multi", pr, map[string]string{pr: up.URL + brutalNativeBase(pr)}, "http://127.0.0.1:1/unused")},
					[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

				w := brutalClientCall(server, pr, body)
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				calls := mock.snapshot()
				if len(calls) != 1 {
					t.Fatalf("calls=%d", len(calls))
				}
				if stream {
					// SSE: exact framing equality after the only permitted change
					// (model rewritten to aliasB inside data lines).
					raw := mustReplyStream(pr)
					want := strings.ReplaceAll(string(raw), brutalUpModel, brutalAliasB)
					if w.Body.String() != want {
						t.Fatalf("SSE framing mutated.\nwant:\n%s\ngot:\n%s", want, w.Body.String())
					}
				} else {
					// Request: identical JSON to the client body with only the
					// model field swapped to the upstream model.
					wantReq, err := sjson.SetBytes(body, "model", brutalUpModel)
					if err != nil {
						t.Fatal(err)
					}
					var a, b interface{}
					if err := json.Unmarshal(wantReq, &a); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(calls[0].Body, &b); err != nil {
						t.Fatalf("upstream body not JSON: %s", calls[0].Body)
					}
					if !reflect.DeepEqual(a, b) {
						t.Fatalf("native request mutated.\nwant: %s\ngot:  %s", wantReq, calls[0].Body)
					}
					// Response: identical to the mock body with only model swapped.
					wantResp := strings.ReplaceAll(string(mustReply(pr)),
						`"model":"`+brutalUpModel+`"`, `"model":"`+brutalAliasB+`"`)
					var c, d interface{}
					_ = json.Unmarshal([]byte(wantResp), &c)
					_ = json.Unmarshal(w.Body.Bytes(), &d)
					if !reflect.DeepEqual(c, d) {
						t.Fatalf("native response mutated.\nwant: %s\ngot:  %s", wantResp, w.Body.String())
					}
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// 3. Translated fallback: request must be re-shaped, unknown client fields
// dropped, and usage parsed from the RAW upstream body (actual upstream
// protocol), not from the translated response.
// ---------------------------------------------------------------------------

func TestBrutalTranslatedFallbackShapeAndUsage(t *testing.T) {
	cases := []struct {
		incoming, def string
		checkRequest  func(t *testing.T, body []byte)
	}{
		{protocol.Codex, protocol.Anthropic, func(t *testing.T, body []byte) {
			if gjson.GetBytes(body, "model").String() != brutalUpModel {
				t.Errorf("model: %s", body)
			}
			if !gjson.GetBytes(body, "messages").Exists() || !gjson.GetBytes(body, "max_tokens").Exists() {
				t.Errorf("anthropic shape missing: %s", body)
			}
			if gjson.GetBytes(body, "max_tokens").Int() != 64 {
				t.Errorf("max_tokens lost: %s", body)
			}
			for _, bad := range []string{"input", "instructions", "max_output_tokens", "brutal_unknown"} {
				if gjson.GetBytes(body, bad).Exists() {
					t.Errorf("codex field %s leaked into anthropic request: %s", bad, body)
				}
			}
		}},
		{protocol.OpenAICompletion, protocol.Codex, func(t *testing.T, body []byte) {
			if !gjson.GetBytes(body, "input").Exists() || !gjson.GetBytes(body, "max_output_tokens").Exists() {
				t.Errorf("codex shape missing: %s", body)
			}
			if gjson.GetBytes(body, "max_output_tokens").Int() != 64 {
				t.Errorf("max_output_tokens lost: %s", body)
			}
			for _, bad := range []string{"messages", "max_tokens", "brutal_unknown"} {
				if gjson.GetBytes(body, bad).Exists() {
					t.Errorf("openai field %s leaked into codex request: %s", bad, body)
				}
			}
		}},
		{protocol.Anthropic, protocol.OpenAICompletion, func(t *testing.T, body []byte) {
			if !gjson.GetBytes(body, "messages").Exists() || !gjson.GetBytes(body, "max_tokens").Exists() {
				t.Errorf("openai shape missing: %s", body)
			}
			for _, bad := range []string{"system", "brutal_unknown"} {
				if gjson.GetBytes(body, bad).Exists() {
					t.Errorf("anthropic field %s leaked into openai request: %s", bad, body)
				}
			}
			// system must be converted, not dropped silently: first message is system.
			sys := gjson.GetBytes(body, "messages.0")
			if sys.Get("role").String() != "system" || !strings.Contains(sys.Get("content").String(), "be nice") {
				t.Errorf("system message not carried: %s", body)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.incoming+"->"+tc.def, func(t *testing.T) {
			mock := newBrutalMock()
			up := httptest.NewServer(mock)
			defer up.Close()

			in := brutalClientBody(tc.incoming, false)
			if tc.incoming == protocol.Anthropic {
				in, _ = sjson.SetBytes(in, "system", "be nice")
			} else if tc.incoming == protocol.Codex {
				in, _ = sjson.SetBytes(in, "instructions", "be nice")
			} else {
				in, _ = sjson.SetBytes(in, "messages.0", map[string]any{"role": "system", "content": "be nice"})
			}

			server, rt, _ := brutalFixture(t,
				[]config.Provider{brutalProvider("multi", tc.def,
					map[string]string{tc.def: up.URL + brutalNativeBase(tc.def)}, "http://127.0.0.1:1/unused")},
				[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

			w := brutalClientCall(server, tc.incoming, in)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			calls := mock.snapshot()
			if len(calls) != 1 || calls[0].Path != brutalNativeBase(tc.def)+map[string]string{
				protocol.Anthropic: "/v1/messages", protocol.OpenAICompletion: "/chat/completions", protocol.Codex: "/responses",
			}[tc.def] {
				t.Fatalf("wrong upstream call: %+v", calls)
			}
			tc.checkRequest(t, calls[0].Body)
			assertAuth(t, calls[0], tc.def)

			if !strings.Contains(w.Body.String(), `"model":"`+brutalAliasB+`"`) {
				t.Fatalf("translated response model not rewritten: %s", w.Body.String())
			}
			// Usage MUST come from the raw upstream body: the per-protocol
			// numbers differ, so parsing the translated body (or the wrong
			// protocol's parser) yields different or zero counts.
			assertUsageRow(t, rt, tc.incoming, tc.def)
		})
	}
}

// ---------------------------------------------------------------------------
// 4. Native errors never re-route to translation; cross-provider failover
// still works with mixed per-provider url sets.
// ---------------------------------------------------------------------------

func TestBrutalNativeErrorNoTranslateCrossProviderFailover(t *testing.T) {
	newUp := func(failOpenAI bool) (*brutalMock, *httptest.Server) {
		mock := newBrutalMock()
		if failOpenAI {
			mock.failPath = brutalNativeBase(protocol.OpenAICompletion) + "/chat/completions"
			mock.failStatus = 500 // default retry code → failover
		}
		return mock, httptest.NewServer(mock)
	}

	t.Run("cross-provider-failover-to-native", func(t *testing.T) {
		mockA, upA := newUp(true)
		defer upA.Close()
		mockB, upB := newUp(false)
		defer upB.Close()

		pA := brutalProvider("multi", protocol.Anthropic,
			map[string]string{protocol.OpenAICompletion: upA.URL + brutalNativeBase(protocol.OpenAICompletion)},
			"http://127.0.0.1:1/unused-a")
		pB := brutalProvider("backup", protocol.OpenAICompletion, nil, upB.URL)
		server, _, _ := brutalFixture(t, []config.Provider{pA, pB},
			[]config.ClientModel{
				// Higher Priority value wins first: multi is the primary.
				{Model: "multi/shared", AliasB: brutalAliasB, Priority: 1},
				{Model: "backup/shared", AliasB: brutalAliasB, Priority: 0},
			}, nil)

		w := brutalClientCall(server, protocol.OpenAICompletion, brutalClientBody(protocol.OpenAICompletion, false))
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if got := len(mockA.snapshot()); got != 1 {
			t.Fatalf("primary calls=%d, want exactly 1 (no same-provider translation retry)", got)
		}
		if p := mockA.snapshot()[0].Path; p != brutalNativeBase(protocol.OpenAICompletion)+"/chat/completions" {
			t.Fatalf("primary hit %s, want native openai", p)
		}
		callsB := mockB.snapshot()
		if len(callsB) != 1 || callsB[0].Path != "/chat/completions" {
			t.Fatalf("backup calls=%+v, want one legacy openai call", callsB)
		}
		assertAuth(t, callsB[0], protocol.OpenAICompletion)
	})

	t.Run("single-provider-native-error-relayed-no-translation", func(t *testing.T) {
		mock, up := newUp(true)
		mock.failStatus = 400 // non-retryable → relayed verbatim, no failover
		defer up.Close()
		server, rt, _ := brutalFixture(t,
			[]config.Provider{brutalProvider("multi", protocol.Anthropic,
				map[string]string{protocol.OpenAICompletion: up.URL + brutalNativeBase(protocol.OpenAICompletion)},
				"http://127.0.0.1:1/unused")},
			[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

		w := brutalClientCall(server, protocol.OpenAICompletion, brutalClientBody(protocol.OpenAICompletion, false))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d, want relayed 400: %s", w.Code, w.Body.String())
		}
		calls := mock.snapshot()
		if len(calls) != 1 || !strings.HasPrefix(calls[0].Path, "/nat-openai") {
			t.Fatalf("native error incorrectly changed protocol: %+v", calls)
		}
		// Failed attempt still lands in usage with the native protocol. A
		// relayed 400 is a client error, not an infrastructure failure, so
		// Failures stays 0 — but the row must carry the native protocol.
		rows := rt.Usage().Snapshot()
		if len(rows) != 1 || rows[0].ProviderType != protocol.OpenAICompletion ||
			rows[0].TargetProviderType != protocol.OpenAICompletion || rows[0].HTTPStatus != 400 {
			t.Fatalf("failed usage row wrong: %+v", rows)
		}
	})
}

// ---------------------------------------------------------------------------
// 5. Provider concurrency quota is shared across protocols of one provider.
// ---------------------------------------------------------------------------

func TestBrutalSharedConcurrencyAcrossProtocols(t *testing.T) {
	run := func(t *testing.T, limit int, expectSecond int) {
		t.Helper()
		mock := newBrutalMock()
		mock.gatePath = "/v1/messages" // legacy anthropic slot (provider url = server root)
		up := httptest.NewServer(mock)
		defer up.Close()
		defer mock.unblock()

		prov := brutalProvider("multi", protocol.Anthropic, nil, up.URL)
		lim := limit
		prov.MaxConcurrency = &lim
		server, _, _ := brutalFixture(t, []config.Provider{prov},
			[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

		// First request (anthropic native) takes the single slot and blocks in
		// the mock.
		doneA := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			doneA <- brutalClientCall(server, protocol.Anthropic, brutalClientBody(protocol.Anthropic, false))
		}()
		select {
		case <-mock.enter:
		case <-time.After(5 * time.Second):
			t.Fatal("first request never reached the mock")
		}

		// Second request, DIFFERENT protocol of the SAME provider, must see the
		// shared quota as exhausted.
		doneB := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			doneB <- brutalClientCall(server, protocol.OpenAICompletion, brutalClientBody(protocol.OpenAICompletion, false))
		}()
		var wB *httptest.ResponseRecorder
		select {
		case wB = <-doneB:
		case <-time.After(5 * time.Second):
			t.Fatal("second request hung")
		}
		if wB.Code != expectSecond {
			t.Fatalf("second request status=%d, want %d body=%s", wB.Code, expectSecond, wB.Body.String())
		}
		if expectSecond == 529 && !strings.Contains(wB.Body.String(), "server_busy") {
			t.Fatalf("expected server_busy body: %s", wB.Body.String())
		}

		// Release the first; it must complete fine against the mock.
		mock.unblock()
		var wA *httptest.ResponseRecorder
		select {
		case wA = <-doneA:
		case <-time.After(5 * time.Second):
			t.Fatal("first request never completed")
		}
		if wA.Code != http.StatusOK {
			t.Fatalf("first request status=%d body=%s", wA.Code, wA.Body.String())
		}
		if len(mock.snapshot()) != 1 {
			t.Fatalf("upstream saw %d calls, want 1 (busy request must not hit upstream)", len(mock.snapshot()))
		}
	}

	t.Run("limit-1-blocks-other-protocol", func(t *testing.T) {
		run(t, 1, 529)
	})
}

func TestBrutalConcurrencyAllowsMixedProtocolsUnderLimit(t *testing.T) {
	mock := newBrutalMock()
	mock.gatePath = "/v1/messages" // legacy anthropic slot (provider url = server root)
	up := httptest.NewServer(mock)
	defer up.Close()
	defer mock.unblock()

	lim := 2
	prov := brutalProvider("multi", protocol.Anthropic, nil, up.URL)
	prov.MaxConcurrency = &lim
	server, _, _ := brutalFixture(t, []config.Provider{prov},
		[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

	// Both client protocols translate to the SAME provider default (anthropic
	// legacy URL), so both hit the gated endpoint: the gate blocks the mock
	// handler, keeping both requests in flight at once. Two gated arrivals
	// prove two concurrent upstream slots were granted to mixed-protocol
	// clients of one provider.
	doneA := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		doneA <- brutalClientCall(server, protocol.Anthropic, brutalClientBody(protocol.Anthropic, false))
	}()
	doneB := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		doneB <- brutalClientCall(server, protocol.OpenAICompletion, brutalClientBody(protocol.OpenAICompletion, false))
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-mock.entered:
		case <-time.After(5 * time.Second):
			mock.unblock() // unblock the mock so the test can terminate
			t.Fatalf("only %d of 2 requests reached the mock concurrently", i)
		}
	}
	mock.unblock()

	for _, ch := range []chan *httptest.ResponseRecorder{doneA, doneB} {
		select {
		case w := <-ch:
			if w.Code != http.StatusOK {
				t.Fatalf("concurrent request status=%d body=%s", w.Code, w.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("gated request never completed")
		}
	}
	if len(mock.snapshot()) != 2 {
		t.Fatalf("mock saw %d calls, want 2", len(mock.snapshot()))
	}
}

// ---------------------------------------------------------------------------
// 6. Reload: in-flight request keeps its snapshot; new requests switch.
// ---------------------------------------------------------------------------

func TestBrutalReloadSwitchesNewRequestsKeepsInFlight(t *testing.T) {
	mockOld := newBrutalMock()
	mockOld.gatePath = brutalNativeBase(protocol.OpenAICompletion) + "/chat/completions"
	upOld := httptest.NewServer(mockOld)
	defer upOld.Close()
	defer mockOld.unblock()
	mockNew := newBrutalMock()
	upNew := httptest.NewServer(mockNew)
	defer upNew.Close()

	server, _, _ := brutalFixture(t,
		[]config.Provider{brutalProvider("multi", protocol.OpenAICompletion,
			map[string]string{protocol.OpenAICompletion: upOld.URL + brutalNativeBase(protocol.OpenAICompletion)},
			"http://127.0.0.1:1/unused")},
		[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

	// In-flight request against the old snapshot.
	doneA := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		doneA <- brutalClientCall(server, protocol.OpenAICompletion, brutalClientBody(protocol.OpenAICompletion, false))
	}()
	select {
	case <-mockOld.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached old mock")
	}

	// Mutate: point the provider's native openai url at the NEW mock, persist + reload.
	p := brutalProvider("multi", protocol.OpenAICompletion,
		map[string]string{protocol.OpenAICompletion: upNew.URL + brutalNativeBase(protocol.OpenAICompletion)},
		"http://127.0.0.1:1/unused")
	body, _ := json.Marshal(p)
	if w := brutalAdmin(server, http.MethodPut, "/v0/management/providers/multi", body); w.Code != http.StatusOK {
		t.Fatalf("PUT provider: %d %s", w.Code, w.Body.String())
	}
	if w := brutalAdmin(server, http.MethodPost, "/v0/management/reload", nil); w.Code != http.StatusOK {
		t.Fatalf("reload: %d %s", w.Code, w.Body.String())
	}

	// New request must hit the new URL.
	w2 := brutalClientCall(server, protocol.OpenAICompletion, brutalClientBody(protocol.OpenAICompletion, false))
	if w2.Code != http.StatusOK {
		t.Fatalf("post-reload request: %d %s", w2.Code, w2.Body.String())
	}
	if got := len(mockNew.snapshot()); got != 1 {
		t.Fatalf("new mock calls=%d, want 1", got)
	}
	if got := len(mockOld.snapshot()); got != 1 {
		t.Fatalf("old mock calls=%d, want exactly the one in-flight request", got)
	}

	// The in-flight request finishes against the OLD mock and still succeeds.
	mockOld.unblock()
	var w1 *httptest.ResponseRecorder
	select {
	case w1 = <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request never completed")
	}
	if w1.Code != http.StatusOK {
		t.Fatalf("in-flight request status=%d body=%s", w1.Code, w1.Body.String())
	}
	if got := len(mockOld.snapshot()); got != 1 {
		t.Fatalf("old mock calls after release=%d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// 7. Management CRUD lifecycle for urls.
// ---------------------------------------------------------------------------

func TestBrutalManagementUrlsLifecycle(t *testing.T) {
	mock := newBrutalMock()
	up := httptest.NewServer(mock)
	defer up.Close()

	p1 := brutalProvider("p1", protocol.Anthropic, nil, up.URL)
	server, rt, path := brutalFixture(t, []config.Provider{p1},
		[]config.ClientModel{{Model: "p1/shared", AliasB: "m1", Priority: 0}}, nil)

	// GET legacy provider: urls absent.
	if w := brutalAdmin(server, http.MethodGet, "/v0/management/providers/p1", nil); w.Code != 200 {
		t.Fatalf("GET p1: %d", w.Code)
	} else if gjson.Get(w.Body.String(), "urls").Exists() {
		t.Fatalf("legacy provider must not expose urls: %s", w.Body.String())
	}

	// CREATE provider with native openai + empty codex, no legacy url.
	p2 := config.Provider{
		Name: "p2", Type: protocol.OpenAICompletion,
		URLs:   map[string]string{protocol.OpenAICompletion: up.URL + brutalNativeBase(protocol.OpenAICompletion), protocol.Codex: ""},
		Key:    brutalUpstreamKey,
		Models: []config.ProviderModel{{Model: brutalUpModel, AliasA: "shared"}},
	}
	body, _ := json.Marshal(p2)
	if w := brutalAdmin(server, http.MethodPost, "/v0/management/providers", body); w.Code != http.StatusCreated {
		t.Fatalf("POST p2: %d %s", w.Code, w.Body.String())
	}
	if w := brutalAdmin(server, http.MethodGet, "/v0/management/providers/p2", nil); w.Code != 200 {
		t.Fatalf("GET p2: %d", w.Code)
	} else {
		if gjson.Get(w.Body.String(), "urls.openai_completion").String() != p2.URLs[protocol.OpenAICompletion] {
			t.Fatalf("GET p2 urls wrong: %s", w.Body.String())
		}
		if gjson.Get(w.Body.String(), "urls.codex").String() != "" {
			t.Fatalf("empty codex url must stay empty: %s", w.Body.String())
		}
	}

	// Bind a client alias to p2 and verify native routing, then clear the
	// native url via PUT and verify fallback to the default (legacy) url.
	k := config.ClientApiKey{
		Name: "brutal-k2", Key: "brutal-k2-key",
		AllowedProtocols: protocol.All,
		Models:           []config.ClientModel{{Model: "p2/shared", AliasB: "m2", Priority: 0}},
	}
	kbody, _ := json.Marshal(k)
	if w := brutalAdmin(server, http.MethodPost, "/v0/management/api-keys", kbody); w.Code != http.StatusCreated {
		t.Fatalf("POST key: %d %s", w.Code, w.Body.String())
	}

	callAs := func(key, incoming string) *httptest.ResponseRecorder {
		body, _ := sjson.SetBytes(brutalClientBody(incoming, false), "model", "m2")
		req := httptest.NewRequest(http.MethodPost, brutalClientRoute[incoming], bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		return w
	}

	if w := callAs("brutal-k2-key", protocol.OpenAICompletion); w.Code != 200 {
		t.Fatalf("native openai call: %d %s", w.Code, w.Body.String())
	}
	natPath := brutalNativeBase(protocol.OpenAICompletion) + "/chat/completions"
	if calls := mock.snapshot(); len(calls) != 1 || calls[0].Path != natPath {
		t.Fatalf("expected native call, got %+v", calls)
	}

	// PUT p2: drop native openai, add legacy url → openai must now go legacy.
	p2b := p2
	p2b.URL = up.URL
	p2b.URLs = map[string]string{protocol.OpenAICompletion: "", protocol.Codex: up.URL + brutalNativeBase(protocol.Codex)}
	body, _ = json.Marshal(p2b)
	if w := brutalAdmin(server, http.MethodPut, "/v0/management/providers/p2", body); w.Code != 200 {
		t.Fatalf("PUT p2b: %d %s", w.Code, w.Body.String())
	}
	if w := brutalAdmin(server, http.MethodPost, "/v0/management/reload", nil); w.Code != 200 {
		t.Fatalf("reload: %d", w.Code)
	}
	if w := callAs("brutal-k2-key", protocol.OpenAICompletion); w.Code != 200 {
		t.Fatalf("legacy fallback call: %d %s", w.Code, w.Body.String())
	}
	if w := callAs("brutal-k2-key", protocol.Codex); w.Code != 200 {
		t.Fatalf("native codex call: %d %s", w.Code, w.Body.String())
	}
	calls := mock.snapshot()
	// calls: [nat-openai, legacy-openai(/chat/completions), nat-codex(/nat-codex/v1/responses)]
	if len(calls) != 3 {
		t.Fatalf("expected 3 upstream calls, got %+v", calls)
	}
	if calls[1].Path != "/chat/completions" {
		t.Fatalf("cleared native url must fall back to legacy: %+v", calls)
	}
	if calls[2].Path != brutalNativeBase(protocol.Codex)+"/responses" {
		t.Fatalf("codex native expected: %+v", calls)
	}

	// Invalid urls key → 422, disk unchanged, runtime unchanged.
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pBad := p2b
	pBad.URLs = map[string]string{"bogus": "https://x"}
	body, _ = json.Marshal(pBad)
	if w := brutalAdmin(server, http.MethodPut, "/v0/management/providers/p2", body); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid PUT: %d %s", w.Code, w.Body.String())
	}
	diskAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(diskBefore, diskAfter) {
		t.Fatal("invalid mutation changed the config file")
	}
	if _, ok := rt.Snapshot().Indexes.Providers["p2"].URLs["bogus"]; ok {
		t.Fatal("invalid mutation changed runtime indexes")
	}

	// DELETE protection: p1 is referenced → 422.
	if w := brutalAdmin(server, http.MethodDelete, "/v0/management/providers/p1", nil); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("DELETE p1: %d, want 422 provider_in_use", w.Code)
	}
	// DELETE p2 after removing its references → 204, then 404.
	if w := brutalAdmin(server, http.MethodDelete, "/v0/management/api-keys/brutal-k2", nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE key: %d", w.Code)
	}
	if w := brutalAdmin(server, http.MethodDelete, "/v0/management/providers/p2", nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE p2: %d", w.Code)
	}
	if w := brutalAdmin(server, http.MethodGet, "/v0/management/providers/p2", nil); w.Code != http.StatusNotFound {
		t.Fatalf("GET deleted p2: %d", w.Code)
	}
	if w := brutalAdmin(server, http.MethodPost, "/v0/management/reload", nil); w.Code != 200 {
		t.Fatalf("reload after delete: %d", w.Code)
	}
	if _, ok := rt.Snapshot().Indexes.Providers["p2"]; ok {
		t.Fatal("deleted provider still in runtime indexes")
	}
}

// ---------------------------------------------------------------------------
// 8. Legacy type/url-only compatibility: no urls key on disk, all three client
// protocols still work through the single default.
// ---------------------------------------------------------------------------

func TestBrutalLegacyConfigCompat(t *testing.T) {
	for _, def := range protocol.All {
		for _, incoming := range protocol.All {
			t.Run(def+"/"+incoming, func(t *testing.T) {
				mock := newBrutalMock()
				up := httptest.NewServer(mock)
				defer up.Close()

				p := brutalProvider("multi", def, nil, up.URL)
				server, _, _ := brutalFixture(t, []config.Provider{p},
					[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

				// Marshaled YAML must NOT contain a urls key (omitempty).
				data, err := config.Marshal(mustReload(t, server))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "urls") {
					t.Fatalf("legacy config marshaled a urls key: %s", data)
				}

				w := brutalClientCall(server, incoming, brutalClientBody(incoming, false))
				if w.Code != 200 {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				calls := mock.snapshot()
				wantPath := map[string]string{
					protocol.Anthropic: "/v1/messages", protocol.OpenAICompletion: "/chat/completions", protocol.Codex: "/responses",
				}[def]
				if len(calls) != 1 || calls[0].Path != wantPath {
					t.Fatalf("legacy routing wrong: %+v", calls)
				}
				if incoming == def && !strings.Contains(w.Body.String(), "brutal_native") {
					t.Fatalf("legacy native passthrough lost fields: %s", w.Body.String())
				}
			})
		}
	}
}

func mustReload(t *testing.T, server http.Handler) *config.Config {
	t.Helper()
	w := brutalAdmin(server, http.MethodGet, "/v0/management/config", nil)
	if w.Code != 200 {
		t.Fatalf("GET config: %d", w.Code)
	}
	cfg, err := config.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// 9. ResolveUpstream never mutates the shared snapshot entry.
// ---------------------------------------------------------------------------

func TestBrutalResolveUpstreamIsolation(t *testing.T) {
	entry := &indexes.ProviderEntry{
		Name: "multi", Type: protocol.Anthropic,
		URL:     "https://legacy",
		URLs:    map[string]string{protocol.OpenAICompletion: "https://nat-openai/v1"},
		Key:     brutalUpstreamKey,
		Headers: map[string]string{"X-Shared": "yes"},
	}

	a := entry.ResolveUpstream(protocol.OpenAICompletion)
	if a.Type != protocol.OpenAICompletion || a.URL != "https://nat-openai/v1" {
		t.Fatalf("native resolve wrong: %+v", a)
	}
	b := entry.ResolveUpstream(protocol.Codex)
	if b.Type != protocol.Anthropic || b.URL != "https://legacy" {
		t.Fatalf("fallback resolve wrong: %+v", b)
	}
	// Original untouched.
	if entry.Type != protocol.Anthropic || entry.URL != "https://legacy" {
		t.Fatalf("ResolveUpstream mutated the shared entry: %+v", entry)
	}
	// Nil URLs map must not panic.
	plain := &indexes.ProviderEntry{Name: "p", Type: protocol.Codex, URL: "https://legacy"}
	c := plain.ResolveUpstream(protocol.Anthropic)
	if c.Type != protocol.Codex || c.URL != "https://legacy" {
		t.Fatalf("nil-URLs resolve wrong: %+v", c)
	}
	// Concurrent resolves with different protocols stay independent.
	var wg sync.WaitGroup
	for i, pr := range protocol.All {
		wg.Add(1)
		go func(i int, pr string) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r := entry.ResolveUpstream(pr)
				wantType, wantURL := protocol.Anthropic, "https://legacy"
				if pr == protocol.OpenAICompletion {
					wantType, wantURL = pr, "https://nat-openai/v1"
				}
				if r.Type != wantType || r.URL != wantURL {
					t.Errorf("concurrent resolve %d/%d: got %s/%s want %s/%s", i, j, r.Type, r.URL, wantType, wantURL)
					return
				}
			}
		}(i, pr)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// 10. Streaming through translation: client protocol framing restored, usage
// mined from the actual upstream protocol.
// ---------------------------------------------------------------------------

func TestBrutalStreamTranslatedFallback(t *testing.T) {
	cases := []struct {
		incoming, def    string
		wantText         string
		wantClientMarker string
	}{
		{protocol.Anthropic, protocol.OpenAICompletion, "ok-openai_completion", "message_start"},
		{protocol.OpenAICompletion, protocol.Anthropic, "ok-anthropic", "chat.completion.chunk"},
		{protocol.Codex, protocol.OpenAICompletion, "ok-openai_completion", "response.completed"},
	}
	for _, tc := range cases {
		t.Run(tc.incoming+"->"+tc.def, func(t *testing.T) {
			mock := newBrutalMock()
			up := httptest.NewServer(mock)
			defer up.Close()

			server, rt, _ := brutalFixture(t,
				[]config.Provider{brutalProvider("multi", tc.def,
					map[string]string{tc.def: up.URL + brutalNativeBase(tc.def)}, "http://127.0.0.1:1/unused")},
				[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

			w := brutalClientCall(server, tc.incoming, brutalClientBody(tc.incoming, true))
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, tc.wantClientMarker) {
				t.Fatalf("client protocol %s marker %q missing: %s", tc.incoming, tc.wantClientMarker, body)
			}
			if !strings.Contains(body, tc.wantText) {
				t.Fatalf("text %q missing from translated stream: %s", tc.wantText, body)
			}
			if strings.Contains(body, `"model":"`+brutalUpModel+`"`) {
				t.Fatalf("stream still carries upstream model: %s", body)
			}
			in, out := brutalTokens(tc.def)
			rows := rt.Usage().Snapshot()
			if len(rows) != 1 || rows[0].ProviderType != tc.def || rows[0].InputTokens != in || rows[0].OutputTokens != out {
				t.Fatalf("streamed translated usage wrong: %+v, want type=%s %d/%d", rows, tc.def, in, out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 11. Payload rules match the ACTUAL upstream protocol and the client
// from-protocol, per request, even as routing switches.
// ---------------------------------------------------------------------------

func TestBrutalPayloadProtocolMeta(t *testing.T) {
	mock := newBrutalMock()
	up := httptest.NewServer(mock)
	defer up.Close()

	provider := brutalProvider("multi", protocol.Anthropic, nil, up.URL)
	server, _, path := brutalFixture(t, []config.Provider{provider},
		[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}},
		func(cfg *config.Config) {
			cfg.Payload.Override = []config.PayloadRule{
				{Models: []config.PayloadModelRule{{Name: "*", Protocol: protocol.Anthropic, FromProtocol: protocol.OpenAICompletion}},
					Params: map[string]interface{}{"brutal_hit": "anthropic-from-openai"}},
				{Models: []config.PayloadModelRule{{Name: "*", Protocol: protocol.OpenAICompletion}},
					Params: map[string]interface{}{"brutal_hit": "native-openai"}},
			}
		})

	// Step 1: translated (openai client → anthropic default) → rule 1 matches
	// on actual upstream protocol anthropic + from-protocol openai.
	w := brutalClientCall(server, protocol.OpenAICompletion, brutalClientBody(protocol.OpenAICompletion, false))
	if w.Code != 200 {
		t.Fatalf("translated call: %d %s", w.Code, w.Body.String())
	}
	calls := mock.snapshot()
	if len(calls) != 1 || gjson.GetBytes(calls[0].Body, "brutal_hit").String() != "anthropic-from-openai" {
		t.Fatalf("override rule did not match actual protocol/from-protocol: %+v", calls)
	}

	// Step 2: give the provider a native openai url → same client request now
	// passthrough → rule 2 matches (protocol=openai_completion, no from).
	p := brutalProvider("multi", protocol.Anthropic,
		map[string]string{protocol.OpenAICompletion: up.URL + brutalNativeBase(protocol.OpenAICompletion)}, up.URL)
	body, _ := json.Marshal(p)
	if w := brutalAdmin(server, http.MethodPut, "/v0/management/providers/multi", body); w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if w := brutalAdmin(server, http.MethodPost, "/v0/management/reload", nil); w.Code != 200 {
		t.Fatalf("reload: %d", w.Code)
	}
	if w := brutalClientCall(server, protocol.OpenAICompletion, brutalClientBody(protocol.OpenAICompletion, false)); w.Code != 200 {
		t.Fatalf("native call: %d %s", w.Code, w.Body.String())
	}
	calls = mock.snapshot()[1:]
	if len(calls) != 1 || calls[0].Path != brutalNativeBase(protocol.OpenAICompletion)+"/chat/completions" {
		t.Fatalf("native routing wrong: %+v", calls)
	}
	if gjson.GetBytes(calls[0].Body, "brutal_hit").String() != "native-openai" {
		t.Fatalf("native payload rule mismatch: %s", calls[0].Body)
	}
	// Rule 1 (anthropic-only) must NOT have fired on the openai native body.
	if gjson.GetBytes(calls[0].Body, "brutal_hit").String() == "anthropic-from-openai" {
		t.Fatalf("wrong rule fired: %s", calls[0].Body)
	}
	_ = path
}

// ---------------------------------------------------------------------------
// 12. Successful archive records preserve actual selected upstream protocol
// and RAW upstream body (before translation and response-model rewriting).

func TestBrutalArchiveActualUpstreamProtocolAndRawResponse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			mock := newBrutalMock()
			up := httptest.NewServer(mock)
			defer up.Close()
			archiveDir := filepath.Join(t.TempDir(), "archive")
			server, rt, _ := brutalFixture(t,
				[]config.Provider{brutalProvider("multi", protocol.Codex,
					map[string]string{protocol.Codex: up.URL + brutalNativeBase(protocol.Codex)}, "http://127.0.0.1:1/unused")},
				[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}},
				func(cfg *config.Config) { cfg.RequestArchive.Dir = archiveDir })

			w := brutalClientCall(server, protocol.OpenAICompletion, brutalClientBody(protocol.OpenAICompletion, stream))
			rt.Archive().Close() // Drain the writer before inspecting files, not just file creation.
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			files, err := os.ReadDir(archiveDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				t.Fatalf("archive files=%d, want 1", len(files))
			}
			data, err := os.ReadFile(filepath.Join(archiveDir, files[0].Name()))
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record["provider_type"] != protocol.Codex {
				t.Fatalf("archive protocol=%v, want codex", record["provider_type"])
			}
			if stream {
				raw, ok := record["response_stream"].(string)
				if !ok || !strings.Contains(raw, `"type":"response.completed"`) || !strings.Contains(raw, `"model":"real-model"`) {
					t.Fatalf("archive did not store raw codex SSE: %#v", record["response_stream"])
				}
			} else {
				raw, ok := record["response"].(map[string]any)
				native, _ := raw["brutal_native"].(map[string]any)
				if !ok || native["proto"] != "codex" || raw["model"] != brutalUpModel {
					t.Fatalf("archive did not store raw codex response: %#v", record["response"])
				}
			}
		})
	}
}

// 13. Mixed high-churn protocol traffic: no cross-request bleed, no race on
// shared provider state. (Run under -race for full effect.)
// ---------------------------------------------------------------------------

func TestBrutalMixedProtocolConcurrentNoBleed(t *testing.T) {
	mock := newBrutalMock()
	up := httptest.NewServer(mock)
	defer up.Close()

	provider := brutalProvider("multi", protocol.OpenAICompletion, nil, up.URL)
	server, rt, _ := brutalFixture(t, []config.Provider{provider},
		[]config.ClientModel{{Model: "multi/shared", AliasB: brutalAliasB, Priority: 0}}, nil)

	const workers = 24
	const rounds = 8 // 192 requests total, bounded
	protos := protocol.All
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				in := protos[(i+j)%len(protos)]
				w := brutalClientCall(server, in, brutalClientBody(in, false))
				if w.Code != 200 {
					errs <- fmt.Errorf("worker %d round %d proto %s: status %d %s", i, j, in, w.Code, w.Body.String())
					continue
				}
				// The response text must belong to the expected upstream
				// (default protocol = openai_completion for every incoming).
				if !strings.Contains(w.Body.String(), "ok-openai_completion") {
					errs <- fmt.Errorf("worker %d round %d proto %s: wrong upstream text: %s", i, j, in, w.Body.String())
				}
				if !strings.Contains(w.Body.String(), `"model":"`+brutalAliasB+`"`) {
					errs <- fmt.Errorf("worker %d round %d: model not rewritten: %s", i, j, w.Body.String())
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := len(mock.snapshot()); got != workers*rounds {
		t.Fatalf("mock saw %d calls, want %d", got, workers*rounds)
	}
	// All three source protocols must appear in usage, always targeting
	// openai_completion.
	rows := rt.Usage().Snapshot()
	if len(rows) != len(protos) {
		t.Fatalf("usage rows=%d, want %d (one per source protocol)", len(rows), len(protos))
	}
	for _, row := range rows {
		if row.TargetProviderType != protocol.OpenAICompletion || row.ProviderType != protocol.OpenAICompletion {
			t.Fatalf("usage row cross-threaded: %+v", row)
		}
		if row.SourceProtocol != protocol.Anthropic && row.SourceProtocol != protocol.OpenAICompletion && row.SourceProtocol != protocol.Codex {
			t.Fatalf("bad source protocol: %+v", row)
		}
	}
	total := int64(0)
	for _, row := range rows {
		total += row.Requests
	}
	if total != workers*rounds {
		t.Fatalf("total usage requests=%d, want %d", total, workers*rounds)
	}
}
