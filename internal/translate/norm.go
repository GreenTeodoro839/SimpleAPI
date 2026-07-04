package translate

// normRequest is a protocol-neutral view of a chat request, used to convert
// between anthropic / openai_completion / codex. It intentionally covers text,
// images, and tool use; exotic fields (reasoning, cache_control, metadata) are
// carried in Raw for best-effort passthrough rather than modeled explicitly.
type normRequest struct {
	Model       string
	Stream      bool
	System      []normContent // system prompt content (text)
	Messages    []normMessage
	Tools       []normTool
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	Stop        []string
	Raw         map[string]any // unmodeled top-level fields for best-effort carry-over
}

type normMessage struct {
	Role    string // system/user/assistant/tool
	Content []normContent
	// assistant tool calls (openai shape) — also representable as tool_use content
	ToolCalls []normToolCall
	// tool result (openai "tool" role)
	ToolCallID string
}

type normContent struct {
	Type string // text/image/tool_use/tool_result/thinking
	Text string

	// image
	ImageMediaType string
	ImageData      string // base64 (without data: prefix)

	// tool_use
	ToolID   string
	ToolName string
	ToolInput string // raw json string

	// tool_result
	ToolResult string // text content of the result
	IsError   bool
}

type normTool struct {
	Name        string
	Description string
	InputSchema []byte // raw json schema
}

type normToolCall struct {
	ID   string
	Name string
	Args string // raw json arguments string
}

// normResponse is a protocol-neutral view of a non-stream completion response.
type normResponse struct {
	Model       string
	StopReason  string // normalized: end_turn|max_tokens|stop_sequence|tool_use|stop
	Content     []normContent // assistant output (text/tool_use)
	Usage       normUsage
}

type normUsage struct {
	InputTokens  int
	OutputTokens int
}
