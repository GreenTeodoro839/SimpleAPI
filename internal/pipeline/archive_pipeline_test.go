package pipeline

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
	"testing"
	"time"

	"github.com/GreenTeodoro839/SimpleAPI/internal/archive"
	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/reqctx"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// archivedRecord mirrors archive.Record's JSON shape for assertions.
type archivedRecord struct {
	Timestamp      string          `json:"timestamp"`
	RequestID      string          `json:"request_id"`
	APIKey         string          `json:"api_key"`
	Model          string          `json:"model"`
	InternalModel  string          `json:"internal_model"`
	ProviderType   string          `json:"provider_type"`
	Tokens         archive.Tokens  `json:"tokens"`
	Request        json.RawMessage `json:"request"`
	Response       json.RawMessage `json:"response"`
	ResponseStream *string         `json:"response_stream"`
}

func discardLogger() *logrus.Logger {
	lg := logrus.New()
	lg.SetOutput(io.Discard)
	return lg
}

func ptrInt(v int) *int { return &v }

// newArchiveRT builds a runtime with one openai_completion provider pointed at
// upstreamURL, one inbound key "k" (secret "kk") with aliasB "alias" -> p/m,
// and request_archive.dir = archiveDir ("" = feature off). mutate, if given,
// tweaks the config after the defaults are applied.
func newArchiveRT(t *testing.T, upstreamURL, archiveDir string, mutate ...func(*config.Config)) (*runtime.Runtime, *Handler, *indexes.Indexes) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	raw := &config.Config{
		Providers: []config.Provider{
			{Name: "p", Type: "openai_completion", URL: upstreamURL,
				Models: []config.ProviderModel{{Model: "m"}}}, // internal id p/m
		},
		APIKeys: []config.ClientApiKey{
			{Name: "k", Key: "kk", AllowedProtocols: []string{"openai_completion"},
				Models: []config.ClientModel{{Model: "p/m", AliasB: "alias"}}},
		},
	}
	if archiveDir != "" {
		raw.RequestArchive.Dir = archiveDir
	}
	for _, f := range mutate {
		f(raw)
	}
	expanded := config.DeepCopy(raw)
	idx, err := indexes.Build(expanded)
	if err != nil {
		t.Fatalf("indexes.Build: %v", err)
	}
	lg := discardLogger()
	rt := runtime.New(raw, expanded, idx, "", lg)
	return rt, NewHandler(rt, lg), idx
}

// serveChat drives ServeProxy with the key/protocol context the auth
// middleware would have set, and returns the recorded response.
func serveChat(t *testing.T, h *Handler, idx *indexes.Indexes, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	reqctx.SetProtocol(c, protocol.OpenAICompletion)
	reqctx.SetKeyContext(c, idx.Keys["kk"])
	h.ServeProxy(c)
	return w
}

// archiveCount counts .json files in dir; a missing dir counts as 0 (the
// writer creates the directory lazily, so a missing dir means nothing was
// ever enqueued).
func archiveCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// jsonSemanticEqual compares two JSON documents by value: MarshalIndent
// re-indents the embedded raw bodies when writing, so the archived bytes are
// not wire-identical, but the content must match exactly.
func jsonSemanticEqual(t *testing.T, got, want []byte, what string) {
	t.Helper()
	var g, w interface{}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: archived JSON does not parse: %v (%s)", what, err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: expected JSON does not parse: %v (%s)", what, err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s: got %s, want %s", what, got, want)
	}
}

// readOneArchive parses the single archive file expected in dir.
func readOneArchive(t *testing.T, dir string) archivedRecord {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	file := ""
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			if file != "" {
				t.Fatalf("expected exactly one archive file, got several in %v", entries)
			}
			file = e.Name()
		}
	}
	if file == "" {
		t.Fatalf("no archive file in %s", dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	var rec archivedRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", file, err, data)
	}
	return rec
}

func TestArchiveNonStreamSuccess(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","model":"m","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir)
	clientBody := []byte(`{"model":"alias","stream":false,"messages":[{"role":"user","content":"hello"}]}`)
	w := serveChat(t, h, idx, clientBody)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close() // drain before asserting

	if got := archiveCount(t, dir); got != 1 {
		t.Fatalf("archive files = %d, want 1", got)
	}
	rec := readOneArchive(t, dir)
	// Full-value equality with the client body also guards the sjson
	// in-place-mutation concern: the archived request must still say
	// model=alias, not an upstream model name.
	jsonSemanticEqual(t, rec.Request, clientBody, "request")
	// The archived response is the REMOTE's original body: pre-translation,
	// pre-model-rewrite (upstream model "m", not the aliasB the client saw).
	jsonSemanticEqual(t, rec.Response, []byte(`{"id":"x","model":"m","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`), "response")
	if rec.Model != "alias" || rec.InternalModel != "p/m" ||
		rec.ProviderType != "openai_completion" || rec.APIKey != "k" {
		t.Errorf("metadata: %+v", rec)
	}
	if rec.Tokens.InputTokens != 11 || rec.Tokens.OutputTokens != 7 || rec.Tokens.TotalTokens != 18 {
		t.Errorf("tokens: %+v", rec.Tokens)
	}
	if rec.ResponseStream != nil {
		t.Errorf("response_stream = %q, want null for non-stream", *rec.ResponseStream)
	}
	if rec.Timestamp == "" || rec.RequestID == "" {
		t.Errorf("timestamp/request_id missing: %+v", rec)
	}
}

func TestArchiveAllAttemptsFailWritesNothing(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir)
	w := serveChat(t, h, idx, []byte(`{"model":"alias","messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	rt.Archive().Close()
	if got := archiveCount(t, dir); got != 0 {
		t.Errorf("archive files = %d, want 0 (failed request must not be archived)", got)
	}
}

func TestArchiveNonStream4xxRelayedWritesNothing(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad input"}}`)
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir)
	w := serveChat(t, h, idx, []byte(`{"model":"alias","messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	rt.Archive().Close()
	if got := archiveCount(t, dir); got != 0 {
		t.Errorf("archive files = %d, want 0 (relayed 4xx is a failure)", got)
	}
}

func TestArchiveStream4xxRelayedWritesNothing(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"stream rejected"}}`)
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir)
	w := serveChat(t, h, idx, []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (relayed error body)", w.Code)
	}
	rt.Archive().Close()
	if got := archiveCount(t, dir); got != 0 {
		t.Errorf("archive files = %d, want 0 (committed >=400 stream must not be archived)", got)
	}
}

func TestArchiveStreamSuccess(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"y\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir)
	w := serveChat(t, h, idx, []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	if got := archiveCount(t, dir); got != 1 {
		t.Fatalf("archive files = %d, want 1", got)
	}
	rec := readOneArchive(t, dir)
	if rec.ResponseStream == nil {
		t.Fatal("response_stream is null for a streamed request")
	}
	// response_stream is the REMOTE's raw SSE: byte-identical to what the
	// upstream sent — upstream model name, no client-side rewrite.
	upstreamSSE := "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
		"data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"y\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"
	if *rec.ResponseStream != upstreamSSE {
		t.Errorf("response_stream = %q, want the raw upstream stream %q", *rec.ResponseStream, upstreamSSE)
	}
	if !strings.Contains(*rec.ResponseStream, `"model":"m"`) {
		t.Errorf("response_stream must carry the upstream model name:\n%s", *rec.ResponseStream)
	}
	if strings.Contains(*rec.ResponseStream, `"model":"alias"`) {
		t.Errorf("response_stream must not contain the client-side rewritten model:\n%s", *rec.ResponseStream)
	}
	if string(rec.Response) != "null" {
		t.Errorf("response = %s, want null for a stream", rec.Response)
	}
	if rec.Tokens.InputTokens != 3 || rec.Tokens.OutputTokens != 2 || rec.Tokens.TotalTokens != 5 {
		t.Errorf("tokens: %+v (mined from SSE usage event)", rec.Tokens)
	}
}

func TestArchiveDisabledWhenDirEmpty(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","model":"m","usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer up.Close()

	rt, h, idx := newArchiveRT(t, up.URL, "") // no request_archive.dir: off
	w := serveChat(t, h, idx, []byte(`{"model":"alias","messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	rt.Archive().Close()
	// No directory should ever have been created.
	if _, err := os.Stat("./request-archive"); err == nil {
		t.Error("an archive directory was created although archiving is off")
	}
}

func TestArchiveFailoverThenSuccessSingleFile(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"down"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"y","model":"m","choices":[{"message":{"role":"assistant","content":"from-p2"}}],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}`)
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
	w := serveChat(t, h, idx, []byte(`{"model":"alias","messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	if got := archiveCount(t, dir); got != 1 {
		t.Fatalf("archive files = %d, want exactly 1 (failover intermediate attempts are not archived)", got)
	}
	rec := readOneArchive(t, dir)
	if rec.InternalModel != "p2/m" {
		t.Errorf("internal_model = %q, want p2/m (the candidate that actually served)", rec.InternalModel)
	}
	if !strings.Contains(string(rec.Response), "from-p2") {
		t.Errorf("response should come from the second upstream: %s", rec.Response)
	}
	if rec.Tokens.TotalTokens != 10 {
		t.Errorf("tokens: %+v (from the successful attempt only)", rec.Tokens)
	}
}

func TestArchiveStreamIdleBreakArchivesPartial(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(1500 * time.Millisecond) // stall: longer than the 1s idle timeout
	}))
	defer up.Close()

	dir := t.TempDir()
	rt, h, idx := newArchiveRT(t, up.URL, dir, func(c *config.Config) {
		c.Server.StreamIdleTimeoutSeconds = ptrInt(1)
	})
	w := serveChat(t, h, idx, []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (headers were committed before the idle break)", w.Code)
	}
	rt.Archive().Close()

	if got := archiveCount(t, dir); got != 1 {
		t.Fatalf("archive files = %d, want 1 (broken-midway 2xx stream still archives)", got)
	}
	rec := readOneArchive(t, dir)
	if rec.ResponseStream == nil || !strings.Contains(*rec.ResponseStream, "partial") {
		t.Fatalf("response_stream should contain the partial content: %+v", rec)
	}
	rawEvent := "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	if *rec.ResponseStream != rawEvent {
		t.Errorf("response_stream = %q, want the raw upstream prefix %q", *rec.ResponseStream, rawEvent)
	}
}
