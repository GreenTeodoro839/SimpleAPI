package pipeline

import (
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/usage"
	"github.com/tidwall/gjson"
)

// GLM's anthropic stream reports placeholder zeros in message_start and the
// real counts (input + output + cache_read) only in the final message_delta.
func TestAccumulateUsage_GLMStreamShape(t *testing.T) {
	var acc usage.Counts
	accumulateUsage([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0}}}`), protocol.Anthropic, &acc)
	accumulateUsage([]byte(`{"type":"ping"}`), protocol.Anthropic, &acc)
	accumulateUsage([]byte(`{"type":"message_delta","usage":{"input_tokens":13,"output_tokens":3,"cache_read_input_tokens":0}}`), protocol.Anthropic, &acc)

	if acc.Input != 13 {
		t.Fatalf("input: got %d want 13", acc.Input)
	}
	if acc.Output != 3 {
		t.Fatalf("output: got %d want %d", acc.Output, 3)
	}
}

// Standard Anthropic reports input at message_start and the final output (and
// cache fields) at message_delta; the max must win over the small running counts.
func TestAccumulateUsage_StandardAnthropicWithCache(t *testing.T) {
	var acc usage.Counts
	accumulateUsage([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":42,"output_tokens":1,"cache_creation_input_tokens":100}}}`), protocol.Anthropic, &acc)
	accumulateUsage([]byte(`{"type":"message_delta","usage":{"output_tokens":99,"cache_read_input_tokens":40}}`), protocol.Anthropic, &acc)

	if acc.Input != 42 {
		t.Fatalf("input: got %d want 42", acc.Input)
	}
	if acc.Output != 99 {
		t.Fatalf("output: got %d want 99 (final > running)", acc.Output)
	}
	if acc.CacheCreation != 100 {
		t.Fatalf("cache_creation: got %d want 100", acc.CacheCreation)
	}
	if acc.CacheRead != 40 {
		t.Fatalf("cache_read: got %d want 40", acc.CacheRead)
	}
}

func TestParseUsageNode_OpenAICachedTokens(t *testing.T) {
	c := parseUsageNode(gjson.ParseBytes([]byte(`{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":50}}`)), protocol.OpenAICompletion)
	if c.Input != 120 || c.Output != 30 || c.Cached != 50 {
		t.Fatalf("got %+v want {Input:120 Output:30 Cached:50}", c)
	}
}

func TestParseUsageNode_CodexCachedTokens(t *testing.T) {
	c := parseUsageNode(gjson.ParseBytes([]byte(`{"input_tokens":7,"output_tokens":5,"input_tokens_details":{"cached_tokens":3}}`)), protocol.Codex)
	if c.Input != 7 || c.Output != 5 || c.Cached != 3 {
		t.Fatalf("got %+v want {Input:7 Output:5 Cached:3}", c)
	}
}

// pick prefers a non-zero field and accepts either naming.
func TestParseUsageNode_OpenAIEitherNaming(t *testing.T) {
	c := parseUsageNode(gjson.ParseBytes([]byte(`{"input_tokens":9,"output_tokens":2}`)), protocol.OpenAICompletion)
	if c.Input != 9 || c.Output != 2 {
		t.Fatalf("got %+v want {Input:9 Output:2}", c)
	}
}
