package pipeline

// Corner-case additions for the request-archive pipeline hooks: degenerate
// bodies, cross-protocol stream accumulation, failover residue, saturation,
// web-search forwarding, reload snapshot consistency, and large payloads.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/reqctx"
	"github.com/gin-gonic/gin"
)

func ptrBool(v bool) *bool { return &v }

// serveChatProto drives ServeProxy with an explicit client protocol and path
// (serveChat is the openai_completion specialization).
func serveChatProto(t *testing.T, h *Handler, idx *indexes.Indexes, body []byte, proto, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	reqctx.SetProtocol(c, proto)
	reqctx.SetKeyContext(c, idx.Keys["kk"])
	h.ServeProxy(c)
	return w
}

// A 2xx response with an empty body is a success and must be archived with
// "response": null. Regression: an empty non-nil body failed MarshalIndent
// ("unexpected end of JSON input") and silently dropped the whole record.
func TestArchiveNonStreamEmpty2xxBody(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"200-empty-body", 200},
		{"204-no-content", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status) // success, zero-byte body
			}))
			defer up.Close()

			dir := t.TempDir()
			rt, h, idx := newArchiveRT(t, up.URL, dir)
			w := serveChat(t, h, idx, []byte(`{"model":"alias","messages":[{"role":"user","content":"x"}]}`))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			rt.Archive().Close()

			if got := archiveCount(t, dir); got != 1 {
				t.Fatalf("archive files = %d, want 1 (empty 2xx body must still archive)", got)
			}
			rec := readOneArchive(t, dir)
			if string(rec.Response) != "null" {
				t.Errorf("response = %s, want null", rec.Response)
			}
			if rec.ResponseStream != nil {
				t.Errorf("response_stream = %q, want null", *rec.ResponseStream)
			}
		})
	}
}

// A client body gjson tolerates but encoding/json rejects (trailing garbage,
// invalid UTF-8) still proxies against a lenient upstream, but cannot be
// embedded as raw JSON: the record must drop whole, cleanly, without breaking
// the proxy for later requests.
func TestArchiveInvalidJSONRequestBodyDroppedCleanly(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer up.Close()

	valid := []byte(`{"model":"alias","messages":[{"role":"user","content":"ok"}]}`)
	invalidUTF8 := append(append([]byte(`{"model":"alias","x":"`), 0xff, 0xfe), '"', '}')

	for _, tc := range []struct {
		name     string
		body     []byte
		archives bool // whether the degenerate request still produces a file
	}{
		// Structurally invalid JSON cannot be embedded: the record drops whole.
		{"trailing-garbage", []byte(`{"model":"alias","messages":[]}garbage`), false},
		// Invalid UTF-8 inside a string literal embeds fine (the marshaler's
		// scanner checks structure, not encoding): the record survives with
		// its raw bytes verbatim.
		{"invalid-utf8", invalidUTF8, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rt, h, idx := newArchiveRT(t, up.URL, dir)
			w1 := serveChat(t, h, idx, tc.body)
			if w1.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (the lenient upstream accepted it)", w1.Code)
			}
			// The proxy keeps serving afterwards; the valid follow-up archives.
			w2 := serveChat(t, h, idx, valid)
			if w2.Code != http.StatusOK {
				t.Fatalf("follow-up status = %d, want 200", w2.Code)
			}
			rt.Archive().Close()

			want := 1
			if tc.archives {
				want = 2
			}
			if got := archiveCount(t, dir); got != want {
				t.Fatalf("archive files = %d, want %d", got, want)
			}
			if tc.archives {
				// The degenerate record survives with its raw bytes verbatim
				// and the written file still parses.
				found := false
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range entries {
					data, err := os.ReadFile(filepath.Join(dir, e.Name()))
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Contains(data, []byte{0xff, 0xfe}) {
						continue
					}
					found = true
					var rec map[string]json.RawMessage
					if err := json.Unmarshal(data, &rec); err != nil {
						t.Fatalf("archived file with raw invalid-UTF-8 body does not parse: %v", err)
					}
				}
				if !found {
					t.Error("no archived record carries the raw invalid-UTF-8 request bytes")
				}
			} else {
				rec := readOneArchive(t, dir)
				jsonSemanticEqual(t, rec.Request, valid, "request")
			}
		})
	}
}

// Cross-protocol translated stream (anthropic client over an openai_completion
// upstream): the archive must capture the REMOTE's original openai SSE
// verbatim — never the translated anthropic events the client received — and
// the request must stay the pre-translation anthropic body.
func TestArchiveStreamTranslatedCapturesUpstreamSSE(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir, func(c *config.Config) {
		c.APIKeys[0].AllowedProtocols = []string{"anthropic"}
	})
	clientBody := []byte(`{"model":"alias","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	w := serveChatProto(t, h, idx, clientBody, protocol.Anthropic, "/v1/messages")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	rec := readOneArchive(t, dir)
	if rec.ResponseStream == nil {
		t.Fatal("response_stream is null for a streamed request")
	}
	upstreamSSE := "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n" +
		"data: [DONE]\n\n"
	if *rec.ResponseStream != upstreamSSE {
		t.Errorf("response_stream is not the raw upstream stream:\narchived: %q\nupstream: %q", *rec.ResponseStream, upstreamSSE)
	}
	for _, leaked := range []string{"message_start", "content_block_delta", "message_stop"} {
		if strings.Contains(*rec.ResponseStream, leaked) {
			t.Errorf("response_stream leaked translated client-protocol event %q:\n%s", leaked, *rec.ResponseStream)
		}
	}
	jsonSemanticEqual(t, rec.Request, clientBody, "request (pre-translation anthropic body)")
	if rec.Model != "alias" || rec.InternalModel != "p/m" || rec.ProviderType != "openai_completion" {
		t.Errorf("metadata: %+v", rec)
	}
	if rec.Tokens.InputTokens != 4 || rec.Tokens.OutputTokens != 2 || rec.Tokens.TotalTokens != 6 {
		t.Errorf("tokens: %+v (mined from the upstream openai usage event)", rec.Tokens)
	}
}

// rewrite_response_model: false — a passthrough stream relays the upstream
// lines unmodified, so the raw-upstream archive and the client bytes coincide
// (the model stays the upstream name either way).
func TestArchiveStreamRewriteDisabled(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir, func(c *config.Config) {
		c.Proxy.RewriteResponseModel = ptrBool(false)
	})
	w := serveChat(t, h, idx, []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	rec := readOneArchive(t, dir)
	if rec.ResponseStream == nil {
		t.Fatal("response_stream is null for a streamed request")
	}
	if *rec.ResponseStream != w.Body.String() {
		t.Errorf("response_stream = %q, want client-visible %q", *rec.ResponseStream, w.Body.String())
	}
	if !strings.Contains(*rec.ResponseStream, `"model":"m"`) {
		t.Errorf("expected the unrewritten upstream model in response_stream:\n%s", *rec.ResponseStream)
	}
	if strings.Contains(*rec.ResponseStream, `"model":"alias"`) {
		t.Errorf("model was rewritten although rewrite_response_model=false:\n%s", *rec.ResponseStream)
	}
}

// A retryable stream failure (500) fails over to the next candidate: exactly
// one file, holding only the second attempt's bytes and counts — the shared
// sink must carry no residue from the aborted first attempt.
func TestArchiveStreamFailoverRetryableThenSuccess(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"down"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"from-p2\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":6,\"total_tokens\":10}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer good.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, "", dir, func(c *config.Config) {
		c.Providers = []config.Provider{
			{Name: "p", Type: "openai_completion", URL: bad.URL,
				Models: []config.ProviderModel{{Model: "m"}}},
			{Name: "p2", Type: "openai_completion", URL: good.URL,
				Models: []config.ProviderModel{{Model: "m"}}},
		}
		c.APIKeys[0].Models = []config.ClientModel{
			{Model: "p/m", AliasB: "alias", Priority: 10},
			{Model: "p2/m", AliasB: "alias", Priority: 5},
		}
	})
	w := serveChat(t, h, idx, []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	if got := archiveCount(t, dir); got != 1 {
		t.Fatalf("archive files = %d, want exactly 1", got)
	}
	rec := readOneArchive(t, dir)
	if rec.ResponseStream == nil || !strings.Contains(*rec.ResponseStream, "from-p2") {
		t.Fatalf("response_stream should hold only the second attempt: %+v", rec)
	}
	goodSSE := "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"from-p2\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":6,\"total_tokens\":10}}\n\n" +
		"data: [DONE]\n\n"
	if *rec.ResponseStream != goodSSE {
		t.Errorf("response_stream = %q, want only the second attempt's raw upstream stream %q", *rec.ResponseStream, goodSSE)
	}
	if rec.InternalModel != "p2/m" {
		t.Errorf("internal_model = %q, want p2/m", rec.InternalModel)
	}
	if rec.Tokens.TotalTokens != 10 {
		t.Errorf("tokens: %+v (must come from the successful attempt only)", rec.Tokens)
	}
}

// An empty 2xx stream body (upstream 200, EOF before any line) commits
// nothing, is forced retryable, exhausts the candidates and archives nothing.
func TestArchiveStreamEmptyBodyRetriesAndArchivesNothing(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // no SSE lines at all
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir)
	w := serveChat(t, h, idx, []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (empty stream is retryable; single candidate exhausted)", w.Code)
	}
	rt.Archive().Close()
	if got := archiveCount(t, dir); got != 0 {
		t.Errorf("archive files = %d, want 0 (nothing was committed)", got)
	}
}

// Passthrough relays every SSE line verbatim — comments, event: lines, CRLF
// endings — and the archive must be byte-identical to what the upstream sent.
func TestArchiveStreamPassthroughCommentsAndCRLF(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keepalive\r\n")
		fmt.Fprint(w, "event: ping\r\n")
		fmt.Fprint(w, "data: {\"model\":\"m\",\"choices\":[]}\r\n\r\n")
		fmt.Fprint(w, "data: [DONE]\r\n\r\n")
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir)
	w := serveChat(t, h, idx, []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	rec := readOneArchive(t, dir)
	if rec.ResponseStream == nil {
		t.Fatal("response_stream is null for a streamed request")
	}
	upstreamSSE := ": keepalive\r\nevent: ping\r\n" +
		"data: {\"model\":\"m\",\"choices\":[]}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"
	if *rec.ResponseStream != upstreamSSE {
		t.Errorf("response_stream = %q, want the verbatim upstream bytes %q", *rec.ResponseStream, upstreamSSE)
	}
	for _, want := range []string{": keepalive", "event: ping", "data: [DONE]"} {
		if !strings.Contains(*rec.ResponseStream, want) {
			t.Errorf("response_stream missing %q:\n%s", want, *rec.ResponseStream)
		}
	}
}

// A request skipped because its provider is at the concurrency limit (529)
// never reaches an upstream: no archive file for it, while the request
// holding the slot archives normally.
func TestArchiveBusy529WritesNothing(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","model":"m","usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir, func(c *config.Config) {
		c.Providers[0].MaxConcurrency = ptrInt(1)
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- serveChat(t, h, idx, []byte(`{"model":"alias","messages":[{"role":"user","content":"first"}]}`))
	}()
	<-started // the first request now holds the only slot, blocked upstream

	w2 := serveChat(t, h, idx, []byte(`{"model":"alias","messages":[{"role":"user","content":"second"}]}`))
	if w2.Code != 529 {
		t.Fatalf("status = %d, want 529 (provider busy)", w2.Code)
	}
	close(release)
	w1 := <-done
	if w1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", w1.Code)
	}
	rt.Archive().Close()

	if got := archiveCount(t, dir); got != 1 {
		t.Fatalf("archive files = %d, want 1 (only the request that reached an upstream)", got)
	}
}

// anthropic web_search forward: the archived internal_model must be the
// forward TARGET that actually served, not the originally routed model.
func TestArchiveWebSearchForwardRecordsTarget(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m2","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"output_tokens":3}}`)
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir, func(c *config.Config) {
		c.Providers = []config.Provider{
			{Name: "p", Type: "anthropic", URL: up.URL,
				Models: []config.ProviderModel{{Model: "m",
					AnthropicWebSearchForward: &config.WebSearchForward{Enabled: true, TargetModel: "p2/m2"}}}},
			{Name: "p2", Type: "anthropic", URL: up.URL,
				Models: []config.ProviderModel{{Model: "m2"}}},
		}
		c.APIKeys[0] = config.ClientApiKey{Name: "k", Key: "kk",
			AllowedProtocols: []string{"anthropic"},
			Models:           []config.ClientModel{{Model: "p/m", AliasB: "alias"}}}
	})
	body := []byte(`{"model":"alias","max_tokens":8,"messages":[{"role":"user","content":"search"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	w := serveChatProto(t, h, idx, body, protocol.Anthropic, "/v1/messages")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	rec := readOneArchive(t, dir)
	if rec.InternalModel != "p2/m2" {
		t.Errorf("internal_model = %q, want p2/m2 (the web_search forward target)", rec.InternalModel)
	}
	if rec.Model != "alias" {
		t.Errorf("model = %q, want alias", rec.Model)
	}
	if rec.ProviderType != "anthropic" {
		t.Errorf("provider_type = %q, want anthropic", rec.ProviderType)
	}
	if rec.Tokens.InputTokens != 5 || rec.Tokens.OutputTokens != 3 || rec.Tokens.TotalTokens != 8 {
		t.Errorf("tokens: %+v", rec.Tokens)
	}
	jsonSemanticEqual(t, rec.Request, body, "request")
	// The remote's original body: the model stays the forward target's
	// upstream name ("m2"), not the aliasB the client saw.
	jsonSemanticEqual(t, rec.Response, []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m2","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"output_tokens":3}}`), "response")
}

// Reload semantics (rt.Replace mirrors a management PUT): each request
// archives under the snapshot it started with; after the swap, new requests
// go to the new dir. No cross-dir mixing.
func TestArchiveReloadSwitchesDirPerSnapshot(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","model":"m","usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer up.Close()

	dirA, dirB := t.TempDir(), t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dirA)
	if w := serveChat(t, h, idx, []byte(`{"model":"alias","messages":[{"role":"user","content":"a"}]}`)); w.Code != http.StatusOK {
		t.Fatalf("first status = %d", w.Code)
	}

	raw2 := &config.Config{
		Providers: []config.Provider{{Name: "p", Type: "openai_completion", URL: up.URL,
			Models: []config.ProviderModel{{Model: "m"}}}},
		APIKeys: []config.ClientApiKey{{Name: "k", Key: "kk",
			AllowedProtocols: []string{"openai_completion"},
			Models:           []config.ClientModel{{Model: "p/m", AliasB: "alias"}}}},
		RequestArchive: config.RequestArchiveConfig{Dir: dirB},
	}
	expanded2 := config.DeepCopy(raw2)
	idx2, err := indexes.Build(expanded2)
	if err != nil {
		t.Fatalf("indexes.Build: %v", err)
	}
	rt.Replace(raw2, expanded2, idx2)

	if w := serveChat(t, h, idx, []byte(`{"model":"alias","messages":[{"role":"user","content":"b"}]}`)); w.Code != http.StatusOK {
		t.Fatalf("second status = %d", w.Code)
	}
	rt.Archive().Close()

	if got := archiveCount(t, dirA); got != 1 {
		t.Errorf("dirA files = %d, want 1 (pre-reload snapshot)", got)
	}
	if got := archiveCount(t, dirB); got != 1 {
		t.Errorf("dirB files = %d, want 1 (post-reload snapshot)", got)
	}
}

// Big (>=1MB) unicode bodies round-trip whole: the archive never truncates.
func TestArchiveLargeUnicodeBodies(t *testing.T) {
	content := strings.Repeat("你好🌍 ", 150000) // ~1.65 MB
	reqBody, err := json.Marshal(map[string]any{
		"model": "alias", "messages": []any{map[string]any{"role": "user", "content": content}}})
	if err != nil {
		t.Fatal(err)
	}
	respBody, err := json.Marshal(map[string]any{"id": "x", "model": "m", "content": content})
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBody)
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir)
	w := serveChat(t, h, idx, reqBody)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	rt.Archive().Close()

	rec := readOneArchive(t, dir)
	jsonSemanticEqual(t, rec.Request, reqBody, "request")
	jsonSemanticEqual(t, rec.Response, respBody, "response") // the remote's original body
	if len(rec.Response) < 1000000 {
		t.Errorf("archived response is only %d bytes; the archive must not truncate", len(rec.Response))
	}
}
