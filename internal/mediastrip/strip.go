// Package mediastrip replaces image content blocks in a source-protocol
// request body with a fixed text placeholder (DEVELOPMENT.md §12.1), for
// models configured image_input: false. Replacement is 1:1 in place: content
// arrays never shrink, so an Anthropic content array can never become empty,
// and every replacement preserves untouched bytes exactly (key order,
// whitespace, unknown fields). The input slice is never written to; a
// modified body is always a new slice.
package mediastrip

import (
	"fmt"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Placeholder is the fixed text substituted for every image block.
const Placeholder = "[Unsupported Image]"

// Apply replaces image content blocks in a source-protocol request body with
// Placeholder text blocks. sourceProto is the client-facing protocol
// (internal/protocol); an unknown protocol leaves the body untouched. It
// returns the body to forward and how many image blocks were replaced. When
// nothing matched, the input slice is returned unchanged.
func Apply(body []byte, sourceProto string) (out []byte, replaced int) {
	switch sourceProto {
	case protocol.Anthropic:
		return applyAnthropic(body)
	case protocol.OpenAICompletion:
		return applyOpenAI(body)
	case protocol.Codex:
		return applyCodex(body)
	}
	return body, 0
}

// placeholderBlock returns the raw JSON for a text block carrying the
// placeholder. blockType is "text" or "input_text"; cacheControl, when
// non-empty, is appended verbatim so an Anthropic prompt-cache breakpoint
// placed on the image block survives on the replacement.
func placeholderBlock(blockType, cacheControl string) []byte {
	if cacheControl != "" {
		return []byte(`{"type":"` + blockType + `","text":"` + Placeholder + `","cache_control":` + cacheControl + `}`)
	}
	return []byte(`{"type":"` + blockType + `","text":"` + Placeholder + `"}`)
}

// set replaces body[path] with repl and counts the replacement on success.
func set(body []byte, path string, repl []byte, replaced *int) []byte {
	nb, err := sjson.SetRawBytes(body, path, repl)
	if err != nil {
		return body // unreachable: path is a valid indexed path
	}
	*replaced++
	return nb
}

// applyAnthropic replaces messages[*].content[*] image blocks, including
// images nested inside tool_result content arrays. system blocks are not
// walked: the Anthropic spec does not allow images there.
func applyAnthropic(body []byte) ([]byte, int) {
	replaced := 0
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, 0
	}
	i := -1
	messages.ForEach(func(_, m gjson.Result) bool {
		i++
		content := m.Get("content")
		if !content.IsArray() {
			return true // string content has no images
		}
		j := -1
		content.ForEach(func(_, b gjson.Result) bool {
			j++
			switch b.Get("type").String() {
			case "image":
				body = set(body, fmt.Sprintf("messages.%d.content.%d", i, j),
					placeholderBlock("text", b.Get("cache_control").Raw), &replaced)
			case "tool_result":
				inner := b.Get("content")
				if !inner.IsArray() {
					return true
				}
				k := -1
				inner.ForEach(func(_, ib gjson.Result) bool {
					k++
					if ib.Get("type").String() == "image" {
						body = set(body, fmt.Sprintf("messages.%d.content.%d.content.%d", i, j, k),
							placeholderBlock("text", ib.Get("cache_control").Raw), &replaced)
					}
					return true
				})
			}
			return true
		})
		return true
	})
	return body, replaced
}

// applyOpenAI replaces messages[*].content[*].image_url blocks with text
// blocks, keeping the content array shape (never collapsing to a plain
// string).
func applyOpenAI(body []byte) ([]byte, int) {
	replaced := 0
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, 0
	}
	i := -1
	messages.ForEach(func(_, m gjson.Result) bool {
		i++
		content := m.Get("content")
		if !content.IsArray() {
			return true // string content has no images
		}
		j := -1
		content.ForEach(func(_, b gjson.Result) bool {
			j++
			if b.Get("type").String() == "image_url" {
				body = set(body, fmt.Sprintf("messages.%d.content.%d", i, j),
					placeholderBlock("text", ""), &replaced)
			}
			return true
		})
		return true
	})
	return body, replaced
}

// applyCodex replaces Responses input image content: top-level
// input[*].type=="input_image" items (replaced by a wrapped user message
// item, since a bare text block is not a valid Responses input item) and
// input_image/"image" blocks inside any input[*].content array.
func applyCodex(body []byte) ([]byte, int) {
	replaced := 0
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, 0 // string input has no images
	}
	itemRepl := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + Placeholder + `"}]}`)
	i := -1
	input.ForEach(func(_, item gjson.Result) bool {
		i++
		if item.Get("type").String() == "input_image" {
			body = set(body, fmt.Sprintf("input.%d", i), itemRepl, &replaced)
			return true
		}
		content := item.Get("content")
		if !content.IsArray() {
			return true
		}
		j := -1
		content.ForEach(func(_, b gjson.Result) bool {
			j++
			switch b.Get("type").String() {
			case "input_image", "image": // "image" mirrors translate/codex.go's dual-name parse
				body = set(body, fmt.Sprintf("input.%d.content.%d", i, j),
					placeholderBlock("input_text", ""), &replaced)
			}
			return true
		})
		return true
	})
	return body, replaced
}
