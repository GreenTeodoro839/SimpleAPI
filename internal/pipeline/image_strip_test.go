package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/mediastrip"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/reqctx"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// serveProto drives ServeProxy for an arbitrary protocol/path, with the key
// context the auth middleware would have set.
func serveProto(t *testing.T, h *Handler, idx *indexes.Indexes, path, proto string, body []byte) *httptest.ResponseRecorder {
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

// captureUpstream returns a server whose handler records the request body and
// replies with reply (with the given content type).
func captureUpstream(reply, contentType string) (*httptest.Server, func() []byte) {
	var mu sync.Mutex
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = b
		mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, reply)
	}))
	return up, func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

const openAIReply = `{"id":"x","model":"m","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`

const openAIImageBody = `{"model":"alias","stream":false,"messages":[{"role":"user","content":[
	{"type":"text","text":"look"},
	{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}
]}]}`

func TestPipelineStripsImagesWhenFlagOn(t *testing.T) {
	up, getBody := captureUpstream(openAIReply, "application/json")
	defer up.Close()

	rt, h, idx := newArchiveRT(t, up.URL, "",
		func(c *config.Config) { c.Providers[0].Models[0].ImageInput = ptrBool(false) })
	w := serveChat(t, h, idx, []byte(openAIImageBody))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	upBody := getBody()
	blk := gjson.GetBytes(upBody, "messages.0.content.1")
	if blk.Get("type").String() != "text" || blk.Get("text").String() != mediastrip.Placeholder {
		t.Fatalf("upstream image block not replaced: %s", blk.Raw)
	}
	if strings.Contains(string(upBody), "image_url") {
		t.Fatalf("upstream body still contains image_url: %s", upBody)
	}
	if gjson.GetBytes(upBody, "model").String() != "m" {
		t.Fatalf("upstream model = %q, want m", gjson.GetBytes(upBody, "model").String())
	}
	if got := gjson.GetBytes(upBody, "messages.0.content.0.text").String(); got != "look" {
		t.Fatalf("sibling text block lost: %s", upBody)
	}
}

func TestPipelineStripsImagesStreamed(t *testing.T) {
	sse := "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
		"data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"y\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"
	up, getBody := captureUpstream(sse, "text/event-stream")
	defer up.Close()

	rt, h, idx := newArchiveRT(t, up.URL, "",
		func(c *config.Config) { c.Providers[0].Models[0].ImageInput = ptrBool(false) })
	body := strings.Replace(openAIImageBody, `"stream":false`, `"stream":true`, 1)
	w := serveChat(t, h, idx, []byte(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	upBody := getBody()
	if !strings.Contains(string(upBody), mediastrip.Placeholder) {
		t.Fatalf("streamed upstream body missing placeholder: %s", upBody)
	}
	if strings.Contains(string(upBody), "image_url") {
		t.Fatalf("streamed upstream body still contains image_url: %s", upBody)
	}
}

func TestPipelineKeepsImagesWhenFlagDefault(t *testing.T) {
	up, getBody := captureUpstream(openAIReply, "application/json")
	defer up.Close()

	rt, h, idx := newArchiveRT(t, up.URL, "")
	w := serveChat(t, h, idx, []byte(openAIImageBody))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	upBody := getBody()
	if !strings.Contains(string(upBody), `"type":"image_url"`) {
		t.Fatalf("default (nil) flag must forward images untouched: %s", upBody)
	}
}

func TestPipelineStripAnthropicPassthroughAndArchive(t *testing.T) {
	anthropicReply := `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":7}}`
	up, getBody := captureUpstream(anthropicReply, "application/json")
	defer up.Close()

	dir := t.TempDir()
	clientBody := []byte(`{"model":"alias","max_tokens":100,"messages":[{"role":"user","content":[
	{"type":"tool_result","tool_use_id":"t1","content":[
		{"type":"text","text":"shot"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"},"cache_control":{"type":"ephemeral"}}
	]}
	]}]}`)
	rt, h, idx := newArchiveRT(t, up.URL, dir,
		func(c *config.Config) {
			c.Providers[0].Type = "anthropic"
			c.APIKeys[0].AllowedProtocols = []string{"anthropic"}
			c.Providers[0].Models[0].ImageInput = ptrBool(false)
		})
	w := serveProto(t, h, idx, "/v1/messages", protocol.Anthropic, clientBody)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	// Passthrough path (anthropic client -> anthropic upstream): the strip
	// must still run, on the raw source-protocol body.
	upBody := getBody()
	nested := gjson.GetBytes(upBody, "messages.0.content.0.content.1")
	if nested.Get("type").String() != "text" || nested.Get("text").String() != mediastrip.Placeholder {
		t.Fatalf("nested image not replaced upstream: %s", upBody)
	}
	if nested.Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("cache_control not preserved on the replacement: %s", nested.Raw)
	}
	if got := gjson.GetBytes(upBody, "messages.0.content.0.content.0.text").String(); got != "shot" {
		t.Fatalf("nested sibling text lost: %s", upBody)
	}
	// The archive records the CLIENT's original request body, pre-strip.
	if got := archiveCount(t, dir); got != 1 {
		t.Fatalf("archive files = %d, want 1", got)
	}
	rec := readOneArchive(t, dir)
	if strings.Contains(string(rec.Request), mediastrip.Placeholder) {
		t.Fatalf("archived request must be the original body, not the stripped one: %s", rec.Request)
	}
	jsonSemanticEqual(t, rec.Request, clientBody, "request")
}

func TestPipelineStripCodexTranslatedToOpenAI(t *testing.T) {
	up, getBody := captureUpstream(openAIReply, "application/json")
	defer up.Close()

	rt, h, idx := newArchiveRT(t, up.URL, "",
		func(c *config.Config) {
			c.APIKeys[0].AllowedProtocols = []string{"codex"}
			c.Providers[0].Models[0].ImageInput = ptrBool(false)
		})
	body := []byte(`{"model":"alias","input":[{"type":"message","role":"user","content":[
	{"type":"input_text","text":"hi"},
	{"type":"input_image","image_url":"data:image/png;base64,AAA"}
]}]}`)
	w := serveProto(t, h, idx, "/v1/responses", protocol.Codex, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	rt.Archive().Close()

	// The strip ran on the SOURCE (codex) body before translation, so the
	// translated OpenAI upstream body carries a plain text block.
	upBody := getBody()
	if strings.Contains(string(upBody), "input_image") || strings.Contains(string(upBody), "image_url") {
		t.Fatalf("translated upstream body still contains an image: %s", upBody)
	}
	if !strings.Contains(string(upBody), mediastrip.Placeholder) {
		t.Fatalf("translated upstream body missing placeholder: %s", upBody)
	}
	if gjson.GetBytes(upBody, "messages.0.content.0.text").String() != "hi" {
		t.Fatalf("text content lost in translation: %s", upBody)
	}
}
