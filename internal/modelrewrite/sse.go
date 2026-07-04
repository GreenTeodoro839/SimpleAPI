package modelrewrite

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// RewriteSSEFragment rewrites model fields in every "data: <json>" line within
// an SSE fragment. A fragment may be a single line or multiple lines (e.g. an
// "event:" line followed by a "data:" line and a blank separator). Non-data
// lines, "[DONE]", and invalid-JSON payloads are returned verbatim. Framing is
// preserved by splitting and re-joining on '\n'.
func RewriteSSEFragment(frag []byte, aliasB string) []byte {
	if !bytes.Contains(frag, []byte("data:")) {
		return frag
	}
	lines := bytes.Split(frag, []byte("\n"))
	for i, ln := range lines {
		if !bytes.HasPrefix(ln, []byte("data:")) {
			continue
		}
		rest := ln[len("data:"):]
		hadSpace := len(rest) > 0 && rest[0] == ' '
		if hadSpace {
			rest = rest[1:]
		}
		rest = bytes.TrimRight(rest, " \t")
		if len(rest) == 0 || bytes.Equal(rest, []byte("[DONE]")) || !gjson.ValidBytes(rest) {
			continue
		}
		if !gjson.GetBytes(rest, "model").Exists() &&
			!gjson.GetBytes(rest, "message.model").Exists() &&
			!gjson.GetBytes(rest, "response.model").Exists() {
			continue
		}
		out := rewriteModelFields(rest, aliasB)
		prefix := []byte("data:")
		if hadSpace {
			prefix = append(prefix, ' ')
		}
		lines[i] = append(prefix, out...)
	}
	return bytes.Join(lines, []byte("\n"))
}
