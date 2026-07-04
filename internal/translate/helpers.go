package translate

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// toStringSlice collects the strings of a JSON array result.
func toStringSlice(res gjson.Result) []string {
	if !res.IsArray() {
		return nil
	}
	out := make([]string, 0, 1)
	res.ForEach(func(_, v gjson.Result) bool {
		out = append(out, v.String())
		return true
	})
	return out
}

// contentText extracts a best-effort plain-text rendering of a content field
// that may be a string or an array of content blocks with "text" fields.
func contentText(res gjson.Result) string {
	if !res.Exists() {
		return ""
	}
	if res.Type == gjson.String {
		return res.String()
	}
	var sb strings.Builder
	res.ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "text" || b.Get("text").Exists() {
			sb.WriteString(b.Get("text").String())
		}
		return true
	})
	return sb.String()
}

// jsonRaw returns the bytes as a json.RawMessage for inline embedding when
// building JSON via map[string]any; an empty input becomes "{}".
func jsonRaw(b []byte) json.RawMessage {
	if len(bytesTrim(b)) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\n' || b[0] == '\t' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\n' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
