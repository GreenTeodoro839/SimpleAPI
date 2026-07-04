// Package translate implements protocol translation between the three wire
// formats (anthropic, openai_completion, codex) via a registry keyed by
// (from, to). Transforms operate on raw JSON bytes using gjson/sjson; there is
// no intermediate IR. Streaming is driven by a protocol-agnostic loop that feeds
// each upstream SSE data event to the pair's chunk transform.
//
// Same-protocol requests do not use translation (they pass through the proxy).
package translate

// Protocol is one of the three wire protocols.
type Protocol string

const (
	Anthropic        Protocol = "anthropic"
	OpenAICompletion Protocol = "openai_completion"
	Codex            Protocol = "codex"
)

// RequestTransform converts an inbound request body in the pair's From protocol
// to the To protocol's request body.
type RequestTransform func(body []byte) ([]byte, error)

// ResponseTransform converts a complete non-stream upstream response body in the
// To protocol back to the From protocol.
type ResponseTransform func(body []byte) ([]byte, error)

// StreamChunkTransform converts ONE upstream SSE data event (the JSON after
// "data:") into zero or more complete client SSE events. Each returned event
// must be a full SSE fragment terminated by a blank line (i.e. "data: ...\n\n"
// or "event: ...\ndata: ...\n\n"). Return nil/"" to emit nothing. A nil chunk
// signals stream completion so the transform can flush any terminal events.
type StreamChunkTransform func(chunk []byte, st *StreamState) ([]byte, error)

// StreamState carries per-stream scratch used by chunk transforms to accumulate
// tool-call arguments and track message/block lifecycle across events.
type StreamState struct {
	MessageStarted bool // anthropic message_start emitted (openai/codex->anthropic)
	Model          string

	// anthropic content block tracking (used by openai/codex->anthropic)
	NextBlockIndex int
	OpenTextBlock  bool // a text content_block is currently open
	OpenToolBlocks map[int]bool

	// anthropic->openai/codex: tool_use block metadata and accumulated args
	ToolMeta       map[int]toolBlockMeta
	ToolArgBuilders map[int]*string
	ToolIndexSeq    int // openai tool_calls array index sequence
}

type toolBlockMeta struct {
	ID   string
	Name string
}

// Pair binds the four transforms for one (From, To) direction.
type Pair struct {
	From, To       Protocol
	Request        RequestTransform
	Response       ResponseTransform // non-stream
	ResponseStream StreamChunkTransform
}

var registry = map[[2]Protocol]*Pair{}

// Register adds a pair to the registry. Intended to be called from init().
func Register(p *Pair) {
	if p == nil {
		return
	}
	registry[[2]Protocol{p.From, p.To}] = p
}

// Get returns the pair translating from->to, or (nil,false) if none registered.
func Get(from, to Protocol) (*Pair, bool) {
	p, ok := registry[[2]Protocol{from, to}]
	return p, ok
}
