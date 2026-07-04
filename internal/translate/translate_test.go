package translate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// anthropicReq is a minimal Anthropic Messages request used across cases.
const anthropicReq = `{"model":"m","max_tokens":32,"system":"be brief","messages":[` +
	`{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"text","text":"hi"}]},` +
	`{"role":"user","content":[{"type":"text","text":"pic?"}]}],"tools":[{"name":"t","description":"d","input_schema":{"type":"object","properties":{"x":{"type":"string"}}}}]}`

func TestRequestTranslationsPreserveText(t *testing.T) {
	// anthropic -> openai -> anthropic should preserve the user text "hello".
	pAO, _ := Get(Anthropic, OpenAICompletion)
	if pAO == nil {
		t.Fatal("missing anthropic->openai pair")
	}
	openaiBody, err := pAO.Request([]byte(anthropicReq))
	if err != nil {
		t.Fatalf("anthropic->openai request: %v", err)
	}
	if gjson.GetBytes(openaiBody, "messages.2.content").String() != "hi" {
		t.Errorf("openai assistant content (messages.2): %s", openaiBody)
	}
	if gjson.GetBytes(openaiBody, "tools.0.function.name").String() != "t" {
		t.Errorf("openai tool name missing: %s", openaiBody)
	}

	pOA, _ := Get(OpenAICompletion, Anthropic)
	roundTrip, err := pOA.Request(openaiBody)
	if err != nil {
		t.Fatalf("openai->anthropic request: %v", err)
	}
	if gjson.GetBytes(roundTrip, "messages.0.content.0.text").String() != "hello" {
		t.Errorf("round-trip user text lost: %s", roundTrip)
	}
}

func TestCodexRoundTrip(t *testing.T) {
	// anthropic -> codex -> anthropic should preserve text and tool name.
	pAC, _ := Get(Anthropic, Codex)
	codexBody, err := pAC.Request([]byte(anthropicReq))
	if err != nil {
		t.Fatalf("anthropic->codex request: %v", err)
	}
	if gjson.GetBytes(codexBody, "input.0.content.0.text").String() != "hello" {
		t.Errorf("codex input text lost: %s", codexBody)
	}
	if gjson.GetBytes(codexBody, "tools.0.name").String() != "t" {
		t.Errorf("codex tool name lost: %s", codexBody)
	}
	// codex -> anthropic
	pCA, _ := Get(Codex, Anthropic)
	back, err := pCA.Request(codexBody)
	if err != nil {
		t.Fatalf("codex->anthropic request: %v", err)
	}
	if gjson.GetBytes(back, "messages.0.content.0.text").String() != "hello" {
		t.Errorf("codex round-trip user text lost: %s", back)
	}
}

func TestCodexOpenAIBridge(t *testing.T) {
	// openai -> codex -> openai preserves text.
	pOC, _ := Get(OpenAICompletion, Codex)
	openaiReq := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`
	codexBody, err := pOC.Request([]byte(openaiReq))
	if err != nil {
		t.Fatalf("openai->codex: %v", err)
	}
	if gjson.GetBytes(codexBody, "input.0.content.0.text").String() != "ping" {
		t.Errorf("codex input text: %s", codexBody)
	}
	pCO, _ := Get(Codex, OpenAICompletion)
	back, err := pCO.Request(codexBody)
	if err != nil {
		t.Fatalf("codex->openai: %v", err)
	}
	if gjson.GetBytes(back, "messages.0.content").String() != "ping" {
		t.Errorf("openai round-trip text: %s", back)
	}
}

func TestNonStreamResponseTranslation(t *testing.T) {
	// openai chat.completion -> anthropic message: client=anthropic, upstream=openai.
	openaiResp := `{"model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	p, _ := Get(Anthropic, OpenAICompletion)
	out, err := p.Response([]byte(openaiResp))
	if err != nil {
		t.Fatalf("response: %v", err)
	}
	if gjson.GetBytes(out, "type").String() != "message" {
		t.Errorf("not anthropic message: %s", out)
	}
	if gjson.GetBytes(out, "content.0.text").String() != "hello" {
		t.Errorf("content lost: %s", out)
	}
}

func TestStreamOpenAIToAnthropic(t *testing.T) {
	// Feed two openai content deltas + a finish through the transform.
	p, _ := Get(Anthropic, OpenAICompletion) // upstream openai -> client anthropic
	st := &StreamState{OpenToolBlocks: map[int]bool{}}
	var out bytes.Buffer
	for _, chunk := range []string{
		`{"model":"x","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"model":"x","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
		`{"model":"x","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
		`{"model":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	} {
		frag, err := p.ResponseStream([]byte(chunk), st)
		if err != nil {
			t.Fatalf("stream chunk: %v", err)
		}
		out.Write(frag)
	}
	s := out.String()
	if !strings.Contains(s, "message_start") || !strings.Contains(s, `"text_delta"`) || !strings.Contains(s, "message_stop") {
		t.Errorf("missing anthropic lifecycle events: %s", s)
	}
	if !strings.Contains(s, `"text":"Hel"`) || !strings.Contains(s, `"text":"lo"`) {
		t.Errorf("text deltas lost: %s", s)
	}
}
