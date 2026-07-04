package translate

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// ---- chunk builders ----

func openAIChunk(model string, delta map[string]any, finish *string) []byte {
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != nil {
		choice["finish_reason"] = *finish
	}
	obj := map[string]any{
		"id":      "chatcmpl_simpleapi",
		"object":  "chat.completion.chunk",
		"model":   model,
		"choices": []any{choice},
	}
	b, _ := json.Marshal(obj)
	return []byte("data: " + string(b) + "\n\n")
}

func anthropicEvent(eventType string, data map[string]any) []byte {
	data["type"] = eventType
	b, _ := json.Marshal(data)
	return []byte("event: " + eventType + "\ndata: " + string(b) + "\n\n")
}

func codexEvent(eventType string, data map[string]any) []byte {
	data["type"] = eventType
	b, _ := json.Marshal(data)
	return []byte("event: " + eventType + "\ndata: " + string(b) + "\n\n")
}

// ---- anthropic -> openai_completion ----

func streamAnthropicToOpenAI(chunk []byte, st *StreamState) ([]byte, error) {
	t := gjson.GetBytes(chunk, "type").String()
	var sb strings.Builder
	switch t {
	case "message_start":
		st.MessageStarted = true
		st.Model = gjson.GetBytes(chunk, "message.model").String()
		sb.Write(openAIChunk(st.Model, map[string]any{"role": "assistant"}, nil))
	case "content_block_start":
		cb := gjson.GetBytes(chunk, "content_block")
		if cb.Get("type").String() == "tool_use" {
			if st.ToolMeta == nil {
				st.ToolMeta = map[int]toolBlockMeta{}
			}
			st.ToolMeta[int(chunkIndex(chunk))] = toolBlockMeta{
				ID: cb.Get("id").String(), Name: cb.Get("name").String(),
			}
		}
	case "content_block_delta":
		dt := gjson.GetBytes(chunk, "delta.type").String()
		if dt == "text_delta" {
			sb.Write(openAIChunk(st.Model, map[string]any{"content": gjson.GetBytes(chunk, "delta.text").String()}, nil))
		} else if dt == "input_json_delta" {
			idx := int(chunkIndex(chunk))
			if st.ToolArgBuilders == nil {
				st.ToolArgBuilders = map[int]*string{}
			}
			p, ok := st.ToolArgBuilders[idx]
			if !ok {
				s := ""
				p = &s
				st.ToolArgBuilders[idx] = p
			}
			*p += gjson.GetBytes(chunk, "delta.partial_json").String()
		}
	case "content_block_stop":
		idx := int(chunkIndex(chunk))
		if meta, ok := st.ToolMeta[idx]; ok {
			args := ""
			if p := st.ToolArgBuilders[idx]; p != nil {
				args = *p
			}
			sb.Write(openAIChunk(st.Model, map[string]any{
				"tool_calls": []any{map[string]any{
					"index": st.ToolIndexSeq,
					"id":    meta.ID,
					"type":  "function",
					"function": map[string]any{
						"name":      meta.Name,
						"arguments": ensureJSONObject(args),
					},
				}},
			}, nil))
			st.ToolIndexSeq++
		}
	case "message_delta":
		sr := gjson.GetBytes(chunk, "delta.stop_reason").String()
		fr := unmapOpenAIFinishReason(sr)
		sb.Write(openAIChunk(st.Model, map[string]any{}, &fr))
	case "message_stop":
		sb.WriteString("data: [DONE]\n\n")
	}
	return []byte(sb.String()), nil
}

// chunkIndex reads the "index" field of an anthropic content_block event.
func chunkIndex(chunk []byte) int {
	return int(gjson.GetBytes(chunk, "index").Int())
}

// ---- openai_completion -> anthropic ----

func streamOpenAIToAnthropic(chunk []byte, st *StreamState) ([]byte, error) {
	delta := gjson.GetBytes(chunk, "choices.0.delta")
	finish := gjson.GetBytes(chunk, "choices.0.finish_reason")
	model := gjson.GetBytes(chunk, "model").String()
	if model != "" {
		st.Model = model
	}
	var sb strings.Builder

	if !st.MessageStarted {
		sb.Write(anthropicEvent("message_start", map[string]any{
			"message": map[string]any{
				"id": "msg_simpleapi", "type": "message", "role": "assistant",
				"content": []any{}, "model": st.Model, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		}))
		st.MessageStarted = true
	}

	if delta.Exists() {
		if c := delta.Get("content").String(); c != "" {
			if !st.OpenTextBlock {
				sb.Write(anthropicEvent("content_block_start", map[string]any{
					"index": st.NextBlockIndex, "content_block": map[string]any{"type": "text", "text": ""},
				}))
				st.OpenTextBlock = true
			}
			sb.Write(anthropicEvent("content_block_delta", map[string]any{
				"index": st.NextBlockIndex,
				"delta": map[string]any{"type": "text_delta", "text": c},
			}))
		}
	}

	if finish.Exists() && finish.Type != gjson.Null {
		if st.OpenTextBlock {
			sb.Write(anthropicEvent("content_block_stop", map[string]any{"index": st.NextBlockIndex}))
			st.OpenTextBlock = false
			st.NextBlockIndex++
		}
		sb.Write(anthropicEvent("message_delta", map[string]any{
			"delta":  map[string]any{"stop_reason": mapOpenAIFinishReason(finish.String()), "stop_sequence": nil},
			"usage":  map[string]any{"output_tokens": 0},
		}))
		sb.Write(anthropicEvent("message_stop", map[string]any{}))
	}
	return []byte(sb.String()), nil
}

// ---- anthropic -> codex ----

func streamAnthropicToCodex(chunk []byte, st *StreamState) ([]byte, error) {
	t := gjson.GetBytes(chunk, "type").String()
	var sb strings.Builder
	switch t {
	case "message_start":
		st.Model = gjson.GetBytes(chunk, "message.model").String()
		sb.Write(codexEvent("response.created", map[string]any{"response": codexResponseShell(st.Model, "in_progress")}))
	case "content_block_delta":
		if gjson.GetBytes(chunk, "delta.type").String() == "text_delta" {
			sb.Write(codexEvent("response.output_text.delta", map[string]any{
				"output_index": 0, "content_index": 0,
				"delta": gjson.GetBytes(chunk, "delta.text").String(),
			}))
		}
	case "message_delta":
		sb.Write(codexEvent("response.completed", map[string]any{"response": codexResponseShell(st.Model, "completed")}))
	}
	return []byte(sb.String()), nil
}

// ---- codex -> anthropic ----

func streamCodexToAnthropic(chunk []byte, st *StreamState) ([]byte, error) {
	t := gjson.GetBytes(chunk, "type").String()
	var sb strings.Builder
	switch t {
	case "response.created", "response.in_progress":
		st.Model = gjson.GetBytes(chunk, "response.model").String()
		sb.Write(anthropicEvent("message_start", map[string]any{
			"message": map[string]any{
				"id": "msg_simpleapi", "type": "message", "role": "assistant",
				"content": []any{}, "model": st.Model, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		}))
		st.MessageStarted = true
	case "response.output_text.delta":
		if !st.OpenTextBlock {
			sb.Write(anthropicEvent("content_block_start", map[string]any{
				"index": st.NextBlockIndex, "content_block": map[string]any{"type": "text", "text": ""},
			}))
			st.OpenTextBlock = true
		}
		sb.Write(anthropicEvent("content_block_delta", map[string]any{
			"index": st.NextBlockIndex,
			"delta": map[string]any{"type": "text_delta", "text": gjson.GetBytes(chunk, "delta").String()},
		}))
	case "response.completed":
		if st.OpenTextBlock {
			sb.Write(anthropicEvent("content_block_stop", map[string]any{"index": st.NextBlockIndex}))
			st.OpenTextBlock = false
		}
		sb.Write(anthropicEvent("message_delta", map[string]any{
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 0},
		}))
		sb.Write(anthropicEvent("message_stop", map[string]any{}))
	}
	return []byte(sb.String()), nil
}

// ---- openai_completion -> codex ----

func streamOpenAIToCodex(chunk []byte, st *StreamState) ([]byte, error) {
	delta := gjson.GetBytes(chunk, "choices.0.delta")
	finish := gjson.GetBytes(chunk, "choices.0.finish_reason")
	model := gjson.GetBytes(chunk, "model").String()
	if model != "" {
		st.Model = model
	}
	var sb strings.Builder
	if !st.MessageStarted {
		sb.Write(codexEvent("response.created", map[string]any{"response": codexResponseShell(st.Model, "in_progress")}))
		st.MessageStarted = true
	}
	if delta.Exists() {
		if c := delta.Get("content").String(); c != "" {
			sb.Write(codexEvent("response.output_text.delta", map[string]any{
				"output_index": 0, "content_index": 0, "delta": c,
			}))
		}
	}
	if finish.Exists() && finish.Type != gjson.Null {
		sb.Write(codexEvent("response.completed", map[string]any{"response": codexResponseShell(st.Model, "completed")}))
	}
	return []byte(sb.String()), nil
}

// ---- codex -> openai_completion ----

func streamCodexToOpenAI(chunk []byte, st *StreamState) ([]byte, error) {
	t := gjson.GetBytes(chunk, "type").String()
	var sb strings.Builder
	switch t {
	case "response.created":
		st.Model = gjson.GetBytes(chunk, "response.model").String()
		sb.Write(openAIChunk(st.Model, map[string]any{"role": "assistant"}, nil))
	case "response.output_text.delta":
		sb.Write(openAIChunk(st.Model, map[string]any{"content": gjson.GetBytes(chunk, "delta").String()}, nil))
	case "response.completed":
		fr := "stop"
		sb.Write(openAIChunk(st.Model, map[string]any{}, &fr))
		sb.WriteString("data: [DONE]\n\n")
	}
	return []byte(sb.String()), nil
}

// codexResponseShell builds a minimal Responses "response" object for events.
func codexResponseShell(model, status string) map[string]any {
	return map[string]any{
		"id": "resp_simpleapi", "object": "response", "status": status,
		"model": model, "output": []any{},
	}
}
