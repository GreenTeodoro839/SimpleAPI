package modelrewrite

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNonStreamTopLevel(t *testing.T) {
	out := NonStream([]byte(`{"model":"real","content":"hi"}`), "alias")
	if gjson.GetBytes(out, "model").String() != "alias" {
		t.Errorf("model=%s want alias", gjson.GetBytes(out, "model").String())
	}
}

func TestNonStreamNoModelField(t *testing.T) {
	in := []byte(`{"foo":"bar"}`)
	out := NonStream(in, "alias")
	if string(out) != string(in) {
		t.Error("should be unchanged when no model field")
	}
}

func TestSSEFragmentTopLevelModel(t *testing.T) {
	frag := []byte("data: {\"model\":\"real\",\"choices\":[]}\n\n")
	out := RewriteSSEFragment(frag, "alias")
	if gjson.Get(string(out), "model").String() != "alias" {
		t.Errorf("SSE top-level model not rewritten: %s", out)
	}
}

func TestSSEFragmentNestedMessageModel(t *testing.T) {
	// anthropic message_start: model nested under message.model
	frag := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"real\"}}\n\n")
	out := RewriteSSEFragment(frag, "alias")
	if gjson.Get(string(out), "message.model").String() != "alias" {
		t.Errorf("nested message.model not rewritten: %s", out)
	}
}

func TestSSEFragmentCodexResponseModel(t *testing.T) {
	// codex response.created/completed: model nested under response.model
	frag := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"object\":\"response\",\"model\":\"real\"}}\n\n")
	out := RewriteSSEFragment(frag, "alias")
	if gjson.Get(string(out), "response.model").String() != "alias" {
		t.Errorf("codex response.model not rewritten: %s", out)
	}
}

func TestSSEFragmentInvalidJSONPassthrough(t *testing.T) {
	frag := []byte("data: [DONE]\n\n")
	out := RewriteSSEFragment(frag, "alias")
	if string(out) != string(frag) {
		t.Errorf("[DONE] should pass through verbatim, got: %s", out)
	}
}

func TestSSEFragmentPreservesEventLine(t *testing.T) {
	frag := []byte("event: content_block_delta\ndata: {\"model\":\"x\"}\n\n")
	out := RewriteSSEFragment(frag, "alias")
	if !bytes.Contains(out, []byte("event: content_block_delta")) {
		t.Errorf("event line lost: %s", out)
	}
}
