package translate

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// parseOpenAIRequest converts an OpenAI Chat Completions request body to norm.
func parseOpenAIRequest(body []byte) normRequest {
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
	if v := gjson.GetBytes(body, "stop"); v.Exists() {
		if v.IsArray() {
			nr.Stop = toStringSlice(v)
		} else {
			nr.Stop = []string{v.String()}
		}
	}
	gjson.GetBytes(body, "messages").ForEach(func(_, m gjson.Result) bool {
		role := m.Get("role").String()
		switch role {
		case "tool":
			// tool result → normalized as a user message with tool_result content
			nr.Messages = append(nr.Messages, normMessage{
				Role: "user",
				Content: []normContent{{
					Type:       "tool_result",
					ToolID:     m.Get("tool_call_id").String(),
					ToolResult: m.Get("content").String(),
				}},
			})
			return true
		case "system":
			nr.System = append(nr.System, parseOpenAIContent(m.Get("content"))...)
			// keep going to append system as a message too? No: store in System.
			return true
		}
		nm := normMessage{Role: role, Content: parseOpenAIContent(m.Get("content"))}
		m.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			nm.Content = append(nm.Content, normContent{
				Type:      "tool_use",
				ToolID:    tc.Get("id").String(),
				ToolName:  tc.Get("function.name").String(),
				ToolInput: tc.Get("function.arguments").String(),
			})
			return true
		})
		nr.Messages = append(nr.Messages, nm)
		return true
	})
	gjson.GetBytes(body, "tools").ForEach(func(_, t gjson.Result) bool {
		nr.Tools = append(nr.Tools, normTool{
			Name:        t.Get("function.name").String(),
			Description: t.Get("function.description").String(),
			InputSchema: []byte(t.Get("function.parameters").Raw),
		})
		return true
	})
	return nr
}

// parseOpenAIContent reads an OpenAI content field (string or block array).
func parseOpenAIContent(res gjson.Result) []normContent {
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
		case "image_url":
			url := b.Get("image_url.url").String()
			out = append(out, normContent{Type: "image", ImageData: dataURLToB64(url), ImageMediaType: dataURLMediaType(url)})
		}
		return true
	})
	return out
}

// buildOpenAIRequest converts norm to an OpenAI Chat Completions request body.
func buildOpenAIRequest(nr normRequest) []byte {
	m := map[string]any{"model": nr.Model}
	if nr.MaxTokens > 0 {
		m["max_tokens"] = nr.MaxTokens
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
	if len(nr.Stop) == 1 {
		m["stop"] = nr.Stop[0]
	} else if len(nr.Stop) > 1 {
		m["stop"] = nr.Stop
	}

	msgs := make([]any, 0)
	if len(nr.System) > 0 {
		msgs = append(msgs, map[string]any{"role": "system", "content": joinText(nr.System)})
	}
	for _, msg := range nr.Messages {
		// tool results become separate "tool" role messages
		textBlocks, toolResults, toolUses := splitContent(msg.Content)
		if len(toolResults) > 0 {
			for _, tr := range toolResults {
				msgs = append(msgs, map[string]any{
					"role":         "tool",
					"tool_call_id": tr.ToolID,
					"content":      tr.ToolResult,
				})
			}
		}
		entry := map[string]any{"role": msg.Role}
		if msg.Role == "assistant" {
			if len(textBlocks) > 0 {
				entry["content"] = joinText(textBlocks)
			} else {
				entry["content"] = nil
			}
			if len(toolUses) > 0 {
				calls := make([]any, 0, len(toolUses))
				for _, tu := range toolUses {
					calls = append(calls, map[string]any{
						"id":   tu.ToolID,
						"type": "function",
						"function": map[string]any{
							"name":      tu.ToolName,
							"arguments": ensureJSONObject(tu.ToolInput),
						},
					})
				}
				entry["tool_calls"] = calls
			}
		} else {
			entry["content"] = buildOpenAIUserContent(textBlocks)
		}
		msgs = append(msgs, entry)
	}
	m["messages"] = msgs
	if len(nr.Tools) > 0 {
		tools := make([]any, 0, len(nr.Tools))
		for _, t := range nr.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  jsonRaw(t.InputSchema),
				},
			})
		}
		m["tools"] = tools
	}
	b, _ := json.Marshal(m)
	return b
}

// buildOpenAIUserContent builds the content field for a non-assistant message.
func buildOpenAIUserContent(cs []normContent) any {
	if len(cs) == 0 {
		return nil
	}
	if len(cs) == 1 && cs[0].Type == "text" {
		return cs[0].Text
	}
	out := make([]any, 0, len(cs))
	for _, c := range cs {
		switch c.Type {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": c.Text})
		case "image":
			url := "data:" + c.ImageMediaType + ";base64," + c.ImageData
			out = append(out, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		}
	}
	return out
}

// parseOpenAIResponse converts an OpenAI Chat Completions non-stream response to norm.
func parseOpenAIResponse(body []byte) normResponse {
	nr := normResponse{Model: gjson.GetBytes(body, "model").String()}
	choice := gjson.GetBytes(body, "choices.0")
	nr.StopReason = mapOpenAIFinishReason(choice.Get("finish_reason").String())
	msg := choice.Get("message")
	if t := msg.Get("content").String(); t != "" {
		nr.Content = append(nr.Content, normContent{Type: "text", Text: t})
	}
	msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
		nr.Content = append(nr.Content, normContent{
			Type:      "tool_use",
			ToolID:    tc.Get("id").String(),
			ToolName:  tc.Get("function.name").String(),
			ToolInput: tc.Get("function.arguments").String(),
		})
		return true
	})
	if u := gjson.GetBytes(body, "usage"); u.Exists() {
		nr.Usage = normUsage{
			InputTokens:  int(u.Get("prompt_tokens").Int()),
			OutputTokens: int(u.Get("completion_tokens").Int()),
		}
	}
	return nr
}

// buildOpenAIResponse converts norm to an OpenAI Chat Completions non-stream body.
func buildOpenAIResponse(nr normResponse) []byte {
	msg := map[string]any{"role": "assistant"}
	var textParts []string
	var toolUses []normContent
	for _, c := range nr.Content {
		if c.Type == "text" {
			textParts = append(textParts, c.Text)
		} else if c.Type == "tool_use" {
			toolUses = append(toolUses, c)
		}
	}
	if len(textParts) > 0 {
		msg["content"] = strings.Join(textParts, "")
	} else {
		msg["content"] = nil
	}
	if len(toolUses) > 0 {
		calls := make([]any, 0, len(toolUses))
		for _, tu := range toolUses {
			calls = append(calls, map[string]any{
				"id":   tu.ToolID,
				"type": "function",
				"function": map[string]any{"name": tu.ToolName, "arguments": ensureJSONObject(tu.ToolInput)},
			})
		}
		msg["tool_calls"] = calls
	}
	m := map[string]any{
		"id":      "chatcmpl_simpleapi",
		"object":  "chat.completion",
		"model":   nr.Model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": unmapOpenAIFinishReason(nr.StopReason)}},
		"usage": map[string]any{
			"prompt_tokens":     nr.Usage.InputTokens,
			"completion_tokens": nr.Usage.OutputTokens,
			"total_tokens":      nr.Usage.InputTokens + nr.Usage.OutputTokens,
		},
	}
	b, _ := json.Marshal(m)
	return b
}

func mapOpenAIFinishReason(s string) string {
	switch s {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	}
	return "end_turn"
}

func unmapOpenAIFinishReason(s string) string {
	switch s {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	}
	return "stop"
}

// splitContent partitions content blocks into text/image, tool_result, and tool_use.
func splitContent(cs []normContent) (text []normContent, results []normContent, uses []normContent) {
	for _, c := range cs {
		switch c.Type {
		case "tool_result":
			results = append(results, c)
		case "tool_use":
			uses = append(uses, c)
		default:
			text = append(text, c)
		}
	}
	return
}

func joinText(cs []normContent) string {
	var sb strings.Builder
	for _, c := range cs {
		if c.Type == "text" || c.Type == "thinking" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// ensureJSONObject returns the string if it parses as a JSON object/array,
// otherwise "{}". Used for openai function arguments (must be a JSON string).
func ensureJSONObject(s string) string {
	t := strings.TrimSpace(s)
	if t != "" && (t[0] == '{' || t[0] == '[') && gjson.Valid(t) {
		return t
	}
	return "{}"
}

// dataURLToB64 extracts the base64 payload from a data: URL.
func dataURLToB64(url string) string {
	if i := strings.Index(url, ","); i >= 0 {
		return url[i+1:]
	}
	return url
}

func dataURLMediaType(url string) string {
	if !strings.HasPrefix(url, "data:") {
		return ""
	}
	rest := url[len("data:"):]
	if i := strings.Index(rest, ";base64,"); i >= 0 {
		return rest[:i]
	}
	if i := strings.Index(rest, ","); i >= 0 {
		return rest[:i]
	}
	return ""
}
