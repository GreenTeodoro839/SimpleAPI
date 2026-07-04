package translate

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// parseAnthropicRequest converts an Anthropic Messages request body to norm.
func parseAnthropicRequest(body []byte) normRequest {
	nr := normRequest{
		Model:  gjson.GetBytes(body, "model").String(),
		Stream: gjson.GetBytes(body, "stream").Bool(),
	}
	if v := gjson.GetBytes(body, "max_tokens"); v.Exists() {
		nr.MaxTokens = int(v.Int())
	}
	if v := gjson.GetBytes(body, "temperature"); v.Exists() {
		f := v.Float()
		nr.Temperature = &f
	}
	if v := gjson.GetBytes(body, "top_p"); v.Exists() {
		f := v.Float()
		nr.TopP = &f
	}
	nr.Stop = toStringSlice(gjson.GetBytes(body, "stop_sequences"))
	nr.System = parseAnthropicContent(gjson.GetBytes(body, "system"))
	gjson.GetBytes(body, "messages").ForEach(func(_, m gjson.Result) bool {
		nr.Messages = append(nr.Messages, normMessage{
			Role:    m.Get("role").String(),
			Content: parseAnthropicContent(m.Get("content")),
		})
		return true
	})
	gjson.GetBytes(body, "tools").ForEach(func(_, t gjson.Result) bool {
		nr.Tools = append(nr.Tools, normTool{
			Name:        t.Get("name").String(),
			Description: t.Get("description").String(),
			InputSchema: []byte(t.Get("input_schema").Raw),
		})
		return true
	})
	return nr
}

// parseAnthropicContent reads an Anthropic content field (string or block array).
func parseAnthropicContent(res gjson.Result) []normContent {
	if !res.Exists() {
		return nil
	}
	if res.Type == gjson.String {
		return []normContent{{Type: "text", Text: res.String()}}
	}
	var out []normContent
	res.ForEach(func(_, b gjson.Result) bool {
		switch b.Get("type").String() {
		case "text":
			out = append(out, normContent{Type: "text", Text: b.Get("text").String()})
		case "image":
			out = append(out, normContent{
				Type:          "image",
				ImageMediaType: b.Get("source.media_type").String(),
				ImageData:      b.Get("source.data").String(),
			})
		case "tool_use":
			out = append(out, normContent{
				Type:      "tool_use",
				ToolID:    b.Get("id").String(),
				ToolName:  b.Get("name").String(),
				ToolInput: b.Get("input").Raw,
			})
		case "tool_result":
			out = append(out, normContent{
				Type:       "tool_result",
				ToolID:     b.Get("tool_use_id").String(),
				ToolResult: contentText(b.Get("content")),
				IsError:    b.Get("is_error").Bool(),
			})
		case "thinking":
			out = append(out, normContent{Type: "thinking", Text: b.Get("thinking").String()})
		}
		return true
	})
	return out
}

// buildAnthropicRequest converts norm to an Anthropic Messages request body.
func buildAnthropicRequest(nr normRequest) []byte {
	m := map[string]any{"model": nr.Model}
	if nr.MaxTokens > 0 {
		m["max_tokens"] = nr.MaxTokens
	} else {
		m["max_tokens"] = 1024
	}
	if nr.Stream {
		m["stream"] = true
	}
	if nr.Temperature != nil {
		m["temperature"] = *nr.Temperature
	}
	if nr.TopP != nil {
		m["top_p"] = *nr.TopP
	}
	if len(nr.Stop) > 0 {
		m["stop_sequences"] = nr.Stop
	}
	if len(nr.System) > 0 {
		m["system"] = buildAnthropicContent(nr.System)
	}
	msgs := make([]any, 0, len(nr.Messages))
	for _, msg := range nr.Messages {
		msgs = append(msgs, map[string]any{
			"role":    msg.Role,
			"content": buildAnthropicContent(msg.Content),
		})
	}
	m["messages"] = msgs
	if len(nr.Tools) > 0 {
		tools := make([]any, 0, len(nr.Tools))
		for _, t := range nr.Tools {
			tools = append(tools, map[string]any{
				"name":          t.Name,
				"description":   t.Description,
				"input_schema":  jsonRaw(t.InputSchema),
			})
		}
		m["tools"] = tools
	}
	b, _ := json.Marshal(m)
	return b
}

// buildAnthropicContent converts norm content blocks to the Anthropic content
// field as an array of blocks (Anthropic responses always use arrays, and
// requests accept arrays too).
func buildAnthropicContent(cs []normContent) any {
	out := make([]any, 0, len(cs))
	for _, c := range cs {
		switch c.Type {
		case "text", "thinking":
			out = append(out, map[string]any{"type": "text", "text": c.Text})
		case "image":
			out = append(out, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type": "base64",
					"media_type": c.ImageMediaType,
					"data":       c.ImageData,
				},
			})
		case "tool_use":
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    c.ToolID,
				"name":  c.ToolName,
				"input": jsonRaw([]byte(c.ToolInput)),
			})
		case "tool_result":
			out = append(out, map[string]any{
				"type":        "tool_result",
				"tool_use_id": c.ToolID,
				"content":     c.ToolResult,
				"is_error":    c.IsError,
			})
		}
	}
	return out
}

// parseAnthropicResponse converts an Anthropic non-stream Message response to norm.
func parseAnthropicResponse(body []byte) normResponse {
	nr := normResponse{Model: gjson.GetBytes(body, "model").String()}
	nr.StopReason = mapAnthropicStopReason(gjson.GetBytes(body, "stop_reason").String())
	nr.Content = parseAnthropicContent(gjson.GetBytes(body, "content"))
	if u := gjson.GetBytes(body, "usage"); u.Exists() {
		nr.Usage = normUsage{
			InputTokens:  int(u.Get("input_tokens").Int()),
			OutputTokens: int(u.Get("output_tokens").Int()),
		}
	}
	return nr
}

// buildAnthropicResponse converts norm to an Anthropic non-stream Message body.
func buildAnthropicResponse(nr normResponse) []byte {
	m := map[string]any{
		"id":          "msg_simpleapi",
		"type":        "message",
		"role":        "assistant",
		"model":       nr.Model,
		"content":     buildAnthropicContent(nr.Content),
		"stop_reason": unmapAnthropicStopReason(nr.StopReason),
		"stop_sequence": nil,
	}
	m["usage"] = map[string]any{
		"input_tokens":  nr.Usage.InputTokens,
		"output_tokens": nr.Usage.OutputTokens,
	}
	b, _ := json.Marshal(m)
	return b
}

func mapAnthropicStopReason(s string) string {
	switch s {
	case "end_turn":
		return "end_turn"
	case "max_tokens":
		return "max_tokens"
	case "stop_sequence":
		return "stop_sequence"
	case "tool_use":
		return "tool_use"
	}
	return "end_turn"
}

func unmapAnthropicStopReason(s string) string {
	switch s {
	case "end_turn", "max_tokens", "stop_sequence", "tool_use":
		return s
	}
	return "end_turn"
}
