package pipeline

import (
	"testing"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/usage"
	"github.com/tidwall/gjson"
)

// GLM's anthropic stream reports placeholder zeros in message_start and the
// real counts (input + output + cache_read) only in the final message_delta.
// finalizeCounts derives total from the accumulated counters.
func TestAccumulateUsage_GLMStreamShape(t *testing.T) {
	var acc usage.Counts
	accumulateUsage([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0}}}`), protocol.Anthropic, &acc)
	accumulateUsage([]byte(`{"type":"ping"}`), protocol.Anthropic, &acc)
	accumulateUsage([]byte(`{"type":"message_delta","usage":{"input_tokens":13,"output_tokens":3,"cache_read_input_tokens":0}}`), protocol.Anthropic, &acc)
	finalizeCounts(&acc, protocol.Anthropic)

	if acc.Input != 13 {
		t.Fatalf("input: got %d want 13", acc.Input)
	}
	if acc.Output != 3 {
		t.Fatalf("output: got %d want 3", acc.Output)
	}
	if acc.Total != 16 {
		t.Fatalf("total: got %d want 16", acc.Total)
	}
}

// Standard Anthropic reports input at message_start and the final output (and
// cache fields) at message_delta; the max must win, and total is the full sum.
func TestAccumulateUsage_StandardAnthropicWithCache(t *testing.T) {
	var acc usage.Counts
	accumulateUsage([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":42,"output_tokens":1,"cache_creation_input_tokens":100}}}`), protocol.Anthropic, &acc)
	accumulateUsage([]byte(`{"type":"message_delta","usage":{"output_tokens":99,"cache_read_input_tokens":40}}`), protocol.Anthropic, &acc)
	finalizeCounts(&acc, protocol.Anthropic)

	if acc.Input != 42 || acc.Output != 99 || acc.CacheCreation != 100 || acc.CacheRead != 40 {
		t.Fatalf("got %+v", acc)
	}
	if acc.Total != 281 { // 42+99+100+40
		t.Fatalf("total: got %d want 281", acc.Total)
	}
}

func TestParseUsageNode_OpenAICachedAndReasoning(t *testing.T) {
	c := parseUsageNode(gjson.ParseBytes([]byte(`{"prompt_tokens":120,"completion_tokens":30,"total_tokens":200,"prompt_tokens_details":{"cached_tokens":50},"completion_tokens_details":{"reasoning_tokens":12}}`)), protocol.OpenAICompletion)
	if c.Input != 120 || c.Output != 30 || c.Cached != 50 || c.Reasoning != 12 || c.Total != 200 {
		t.Fatalf("got %+v", c)
	}
}

// OpenAI without a reported total_tokens: finalizeCounts falls back to input+output.
func TestParseUsageNode_OpenAITotalFallback(t *testing.T) {
	c := parseUsageNode(gjson.ParseBytes([]byte(`{"prompt_tokens":9,"completion_tokens":2}`)), protocol.OpenAICompletion)
	finalizeCounts(&c, protocol.OpenAICompletion)
	if c.Total != 11 {
		t.Fatalf("total: got %d want 11", c.Total)
	}
}

func TestParseUsageNode_CodexCachedAndReasoning(t *testing.T) {
	c := parseUsageNode(gjson.ParseBytes([]byte(`{"input_tokens":7,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}`)), protocol.Codex)
	if c.Input != 7 || c.Output != 5 || c.Cached != 3 || c.Reasoning != 2 || c.Total != 15 {
		t.Fatalf("got %+v", c)
	}
}

// pick prefers a non-zero field and accepts either naming.
func TestParseUsageNode_OpenAIEitherNaming(t *testing.T) {
	c := parseUsageNode(gjson.ParseBytes([]byte(`{"input_tokens":9,"output_tokens":2}`)), protocol.OpenAICompletion)
	if c.Input != 9 || c.Output != 2 {
		t.Fatalf("got %+v want {Input:9 Output:2}", c)
	}
}
