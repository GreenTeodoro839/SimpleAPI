// Package web holds HTTP-layer helpers shared by middleware, handlers, and the
// pipeline: the error envelope and the path-to-protocol mapping. It sits below
// httpapi/auth/pipeline in the import graph so they can all use it without
// cycles.
package web

import (
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/gin-gonic/gin"
)

// WriteError emits the standard proxy error envelope (DEVELOPMENT.md §16):
//
//	{"error":{"code":..,"message":..,"details":{}}}
func WriteError(c *gin.Context, status int, code, message string, details map[string]interface{}) {
	if details == nil {
		details = map[string]interface{}{}
	}
	c.JSON(status, gin.H{"error": gin.H{
		"code":    code,
		"message": message,
		"details": details,
	}})
}

// PathProtocol identifies the client-facing protocol implied by a request path.
// isModels reports the /v1/models list endpoint (which has no protocol). ok is
// false for unrecognized paths.
func PathProtocol(path string) (p string, isModels bool, ok bool) {
	switch path {
	case "/v1/messages":
		return protocol.Anthropic, false, true
	case "/v1/chat/completions":
		return protocol.OpenAICompletion, false, true
	case "/v1/responses":
		return protocol.Codex, false, true
	case "/v1/models":
		return "", true, true
	}
	return "", false, false
}
