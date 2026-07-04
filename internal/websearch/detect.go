// Package websearch detects Anthropic server-side web_search tool requests
// (DEVELOPMENT.md §10) and resolves the configured forward target.
package websearch

import (
	"strings"

	"github.com/tidwall/gjson"
)

// HasWebSearchTool reports whether the request body declares a web_search tool
// (any tools[].type starting with "web_search", e.g. web_search_20250305).
func HasWebSearchTool(body []byte) bool {
	found := false
	gjson.GetBytes(body, "tools").ForEach(func(_, t gjson.Result) bool {
		ty := strings.ToLower(strings.TrimSpace(t.Get("type").String()))
		if strings.HasPrefix(ty, "web_search") {
			found = true
			return false // stop iterating
		}
		return true
	})
	return found
}
