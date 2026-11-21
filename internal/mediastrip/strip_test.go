package mediastrip

import (
	"bytes"
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/tidwall/gjson"
)

func TestApplyAnthropicTopLevelImage(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"hi"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}},
		{"type":"tool_use","id":"t1","name":"ls","input":{}}
	]}]}`)
	out, n := Apply(in, protocol.Anthropic)
	if n != 1 {
		t.Fatalf("replaced = %d, want 1", n)
	}
	img := gjson.GetBytes(out, "messages.0.content.1")
	if img.Get("type").String() != "text" || img.Get("text").String() != Placeholder {
		t.Fatalf("image block not replaced: %s", img.Raw)
	}
	if img.Get("cache_control").Exists() {
		t.Fatalf("replacement must not invent cache_control: %s", img.Raw)
	}
	// Sibling blocks untouched, byte-identical.
	if got := gjson.GetBytes(out, "messages.0.content.0").Raw; got != `{"type":"text","text":"hi"}` {
		t.Fatalf("preceding text block changed: %s", got)
	}
	if got := gjson.GetBytes(out, "messages.0.content.2").Raw; got != `{"type":"tool_use","id":"t1","name":"ls","input":{}}` {
		t.Fatalf("following tool_use block changed: %s", got)
	}
	if gjson.GetBytes(out, "messages.0.content.#").Int() != 3 {
		t.Fatalf("content array must keep 3 blocks")
	}
	// Input slice never mutated.
	if bytes.Contains(in, []byte(Placeholder)) {
		t.Fatalf("input body was modified in place")
	}
}

func TestApplyAnthropicToolResultNestedImage(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"t1","content":[
			{"type":"text","text":"screenshot below"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}
		]}
	]}]}`)
	out, n := Apply(in, protocol.Anthropic)
	if n != 1 {
		t.Fatalf("replaced = %d, want 1", n)
	}
	nested := gjson.GetBytes(out, "messages.0.content.0.content.1")
	if nested.Get("type").String() != "text" || nested.Get("text").String() != Placeholder {
		t.Fatalf("nested image not replaced: %s", nested.Raw)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.content.0").Raw; got != `{"type":"text","text":"screenshot below"}` {
		t.Fatalf("nested sibling text changed: %s", got)
	}
}

func TestApplyAnthropicCacheControlPreserved(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"},"cache_control":{"type":"ephemeral","ttl":"5m"}}
	]}]}`)
	out, n := Apply(in, protocol.Anthropic)
	if n != 1 {
		t.Fatalf("replaced = %d, want 1", n)
	}
	img := gjson.GetBytes(out, "messages.0.content.0")
	if img.Get("cache_control.type").String() != "ephemeral" || img.Get("cache_control.ttl").String() != "5m" {
		t.Fatalf("cache_control not preserved verbatim: %s", img.Raw)
	}
}

func TestApplyOpenAI(t *testing.T) {
	in := []byte(`{"model":"m","messages":[
		{"role":"user","content":[
			{"type":"text","text":"look"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}
		]},
		{"role":"assistant","content":"done"}
	]}`)
	out, n := Apply(in, protocol.OpenAICompletion)
	if n != 1 {
		t.Fatalf("replaced = %d, want 1", n)
	}
	blk := gjson.GetBytes(out, "messages.0.content.1")
	if blk.Get("type").String() != "text" || blk.Get("text").String() != Placeholder {
		t.Fatalf("image_url block not replaced: %s", blk.Raw)
	}
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != "done" {
		t.Fatalf("string-form content must be untouched, got %q", got)
	}
	if gjson.GetBytes(out, "messages.0.content.#").Int() != 2 {
		t.Fatalf("content array must keep 2 blocks")
	}
}

func TestApplyCodexTopLevelItem(t *testing.T) {
	in := []byte(`{"model":"m","input":[
		{"type":"input_text","text":"hi"},
		{"type":"input_image","image_url":"data:image/png;base64,AAA"}
	]}`)
	out, n := Apply(in, protocol.Codex)
	if n != 1 {
		t.Fatalf("replaced = %d, want 1", n)
	}
	if gjson.GetBytes(out, "input.#").Int() != 2 {
		t.Fatalf("input array length must be unchanged")
	}
	item := gjson.GetBytes(out, "input.1")
	if item.Get("type").String() != "message" || item.Get("role").String() != "user" {
		t.Fatalf("input_image item not replaced by wrapped message: %s", item.Raw)
	}
	if item.Get("content.0.type").String() != "input_text" || item.Get("content.0.text").String() != Placeholder {
		t.Fatalf("wrapped placeholder wrong: %s", item.Raw)
	}
	if got := gjson.GetBytes(out, "input.0").Raw; got != `{"type":"input_text","text":"hi"}` {
		t.Fatalf("sibling item changed: %s", got)
	}
}

func TestApplyCodexContentBlock(t *testing.T) {
	in := []byte(`{"model":"m","input":[
		{"type":"message","role":"user","content":[
			{"type":"input_text","text":"hi"},
			{"type":"input_image","image_url":"https://example.com/a.png"},
			{"type":"image","image_url":"https://example.com/b.png"}
		]}
	]}`)
	out, n := Apply(in, protocol.Codex)
	if n != 2 {
		t.Fatalf("replaced = %d, want 2 (input_image and image alias)", n)
	}
	for _, idx := range []string{"input.0.content.1", "input.0.content.2"} {
		blk := gjson.GetBytes(out, idx)
		if blk.Get("type").String() != "input_text" || blk.Get("text").String() != Placeholder {
			t.Fatalf("%s not replaced: %s", idx, blk.Raw)
		}
	}
	if gjson.GetBytes(out, "input.0.content.#").Int() != 3 {
		t.Fatalf("content array must keep 3 blocks")
	}
}

func TestApplyNoImagesNoop(t *testing.T) {
	cases := []struct {
		name  string
		proto string
		body  []byte
	}{
		{"anthropic", protocol.Anthropic,
			[]byte(`{"model":"m","messages":[{"role":"user","content":"text only"}]}`)},
		{"openai", protocol.OpenAICompletion,
			[]byte(`{"model":"m","messages":[{"role":"user","content":"text only"}]}`)},
		{"codex", protocol.Codex,
			[]byte(`{"model":"m","input":"text only"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, n := Apply(tc.body, tc.proto)
			if n != 0 {
				t.Fatalf("replaced = %d, want 0", n)
			}
			if !bytes.Equal(out, tc.body) {
				t.Fatalf("no-op must return the input unchanged:\n got: %s\nwant: %s", out, tc.body)
			}
			if &out[0] != &tc.body[0] {
				t.Fatalf("no-op must return the input slice itself, not a copy")
			}
		})
	}
}

func TestApplyEdgeShapes(t *testing.T) {
	// Two images across two messages.
	in := []byte(`{"model":"m","messages":[
		{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"A"}}]},
		{"role":"user","content":[{"type":"text","text":"and"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"B"}}]}
	]}`)
	if _, n := Apply(in, protocol.Anthropic); n != 2 {
		t.Fatalf("replaced = %d, want 2", n)
	}
	// Anthropic tool_result with string content is skipped without error.
	in = []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"t","content":"text"}
	]}]}`)
	if out, n := Apply(in, protocol.Anthropic); n != 0 || !bytes.Equal(out, in) {
		t.Fatalf("string tool_result content should be a no-op, n=%d", n)
	}
	// Codex string input skipped.
	in = []byte(`{"model":"m","input":"hello"}`)
	if out, n := Apply(in, protocol.Codex); n != 0 || !bytes.Equal(out, in) {
		t.Fatalf("string input should be a no-op, n=%d", n)
	}
	// Codex function_call_output content is a string, no-op.
	in = []byte(`{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":"ok"}]}`)
	if out, n := Apply(in, protocol.Codex); n != 0 || !bytes.Equal(out, in) {
		t.Fatalf("function_call_output should be a no-op, n=%d", n)
	}
	// Unknown protocol.
	in = []byte(`{"model":"m","contents":[{"parts":[{"inlineData":{"mimeType":"image/png"}}]}]}`)
	if out, n := Apply(in, "gemini"); n != 0 || !bytes.Equal(out, in) {
		t.Fatalf("unknown protocol should be a no-op, n=%d", n)
	}
	// Missing messages/input keys entirely.
	in = []byte(`{"model":"m"}`)
	for _, p := range []string{protocol.Anthropic, protocol.OpenAICompletion, protocol.Codex} {
		if out, n := Apply(in, p); n != 0 || !bytes.Equal(out, in) {
			t.Fatalf("%s with no content should be a no-op, n=%d", p, n)
		}
	}
}
