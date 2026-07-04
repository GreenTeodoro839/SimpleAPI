package translate

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// parseCodexRequest converts an OpenAI Responses (codex) request body to norm.
func parseCodexRequest(body []byte) normRequest {
	nr := normRequest{
		Model:  gjson.GetBytes(body, "model").String(),
		Stream: gjson.GetBytes(body, "stream").Bool(),
	}
	if v := gjson.GetBytes(body, "max_output_tokens"); v.Exists() {
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
	if instr := gjson.GetBytes(body, "instructions").String(); instr != "" {
		nr.System = []normContent{{Type: "text", Text: instr}}
	}

	gjson.GetBytes(body, "input").ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "message":
			role := item.Get("role").String()
			if role == "" {
				role = "user"
			}
			nr.Messages = append(nr.Messages, normMessage{Role: role, Content: parseCodexContent(item.Get("content"), role)})
		case "function_call":
			// assistant tool call
			nr.Messages = append(nr.Messages, normMessage{
				Role: "assistant",
				Content: []normContent{{
					Type:      "tool_use",
					ToolID:    item.Get("call_id").String(),
					ToolName:  item.Get("name").String(),
					ToolInput: item.Get("arguments").String(),
				}},
			})
		case "function_call_output":
			nr.Messages = append(nr.Messages, normMessage{
				Role: "user",
				Content: []normContent{{
					Type:       "tool_result",
					ToolID:     item.Get("call_id").String(),
					ToolResult: item.Get("output").String(),
				}},
			})
		}
		return true
	})
	gjson.GetBytes(body, "tools").ForEach(func(_, t gjson.Result) bool {
		nr.Tools = append(nr.Tools, normTool{
			Name:        t.Get("name").String(),
			Description: t.Get("description").String(),
			InputSchema: []byte(t.Get("parameters").Raw),
		})
		return true
	})
	return nr
}

// parseCodexContent reads a codex message content array; textKey selects
// "input_text" (user) vs "output_text" (assistant).
func parseCodexContent(res gjson.Result, role string) []normContent {
	if !res.Exists() {
		return nil
	}
	var out []normContent
	res.ForEach(func(_, b gjson.Result) bool {
		switch b.Get("type").String() {
		case "input_text", "output_text", "text":
			out = append(out, normContent{Type: "text", Text: b.Get("text").String()})
		case "input_image", "image":
			url := b.Get("image_url").String()
			out = append(out, normContent{Type: "image", ImageData: dataURLToB64(url), ImageMediaType: dataURLMediaType(url)})
		}
		return true
	})
	_ = role
	return out
}

// buildCodexRequest converts norm to an OpenAI Responses (codex) request body.
func buildCodexRequest(nr normRequest) []byte {
	m := map[string]any{"model": nr.Model}
	if nr.MaxTokens > 0 {
		m["max_output_tokens"] = nr.MaxTokens
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
	if len(nr.System) > 0 {
		m["instructions"] = joinText(nr.System)
	}

	input := make([]any, 0)
	for _, msg := range nr.Messages {
		textBlocks, toolResults, toolUses := splitContent(msg.Content)
		// message item for text/image content
		hasMedia := false
		for _, c := range textBlocks {
			if c.Type == "image" {
				hasMedia = true
			}
		}
		if len(textBlocks) > 0 {
			content := make([]any, 0, len(textBlocks))
			textKind := "input_text"
			if msg.Role == "assistant" {
				textKind = "output_text"
			}
			for _, c := range textBlocks {
				switch c.Type {
				case "text", "thinking":
					content = append(content, map[string]any{"type": textKind, "text": c.Text})
				case "image":
					url := "data:" + c.ImageMediaType + ";base64," + c.ImageData
					content = append(content, map[string]any{"type": "input_image", "image_url": url})
				}
			}
			role := msg.Role
			if role == "" {
				role = "user"
			}
			input = append(input, map[string]any{"type": "message", "role": role, "content": content})
			_ = hasMedia
		}
		for _, tu := range toolUses {
			input = append(input, map[string]any{
				"type":      "function_call",
				"call_id":   tu.ToolID,
				"name":      tu.ToolName,
				"arguments": ensureJSONObject(tu.ToolInput),
			})
		}
		for _, tr := range toolResults {
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": tr.ToolID,
				"output":  tr.ToolResult,
			})
		}
	}
	m["input"] = input
	if len(nr.Tools) > 0 {
		tools := make([]any, 0, len(nr.Tools))
		for _, t := range nr.Tools {
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        t.Name,
				"description": t.Description,
				"parameters":  jsonRaw(t.InputSchema),
			})
		}
		m["tools"] = tools
	}
	b, _ := json.Marshal(m)
	return b
}

// parseCodexResponse converts an OpenAI Responses non-stream response to norm.
func parseCodexResponse(body []byte) normResponse {
	nr := normResponse{Model: gjson.GetBytes(body, "model").String()}
	nr.StopReason = mapCodexStatus(gjson.GetBytes(body, "status").String())
	gjson.GetBytes(body, "output").ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "message":
			item.Get("content").ForEach(func(_, c gjson.Result) bool {
				if c.Get("type").String() == "output_text" || c.Get("text").Exists() {
					nr.Content = append(nr.Content, normContent{Type: "text", Text: c.Get("text").String()})
				}
				return true
			})
		case "function_call":
			nr.Content = append(nr.Content, normContent{
				Type:      "tool_use",
				ToolID:    item.Get("call_id").String(),
				ToolName:  item.Get("name").String(),
				ToolInput: item.Get("arguments").String(),
			})
		}
		return true
	})
	if u := gjson.GetBytes(body, "usage"); u.Exists() {
		nr.Usage = normUsage{
			InputTokens:  int(u.Get("input_tokens").Int()),
			OutputTokens: int(u.Get("output_tokens").Int()),
		}
	}
	return nr
}

// buildCodexResponse converts norm to an OpenAI Responses non-stream body.
func buildCodexResponse(nr normResponse) []byte {
	output := make([]any, 0)
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
		output = append(output, map[string]any{
			"type": "message",
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "output_text",
				"text": strings.Join(textParts, ""),
			}},
		})
	}
	for _, tu := range toolUses {
		output = append(output, map[string]any{
			"type":      "function_call",
			"call_id":   tu.ToolID,
			"name":      tu.ToolName,
			"arguments": ensureJSONObject(tu.ToolInput),
		})
	}
	m := map[string]any{
		"id":     "resp_simpleapi",
		"object": "response",
		"model":  nr.Model,
		"status": unmapCodexStatus(nr.StopReason),
		"output": output,
		"usage": map[string]any{
			"input_tokens":  nr.Usage.InputTokens,
			"output_tokens": nr.Usage.OutputTokens,
		},
	}
	b, _ := json.Marshal(m)
	return b
}

func mapCodexStatus(status string) string {
	switch status {
	case "completed":
		return "end_turn"
	case "incomplete":
		return "max_tokens"
	}
	return "end_turn"
}

func unmapCodexStatus(s string) string {
	switch s {
	case "max_tokens":
		return "incomplete"
	}
	return "completed"
}
