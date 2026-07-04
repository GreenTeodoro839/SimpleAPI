// Package auth authenticates inbound API keys and resolves the client protocol
// from the request path, storing both on the request context as gin middleware.
package auth

import (
	"net/http"
	"strings"

	"github.com/GreenTeodoro839/SimpleAPI/internal/reqctx"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/GreenTeodoro839/SimpleAPI/internal/web"
	"github.com/gin-gonic/gin"
)

// Middleware authenticates the inbound API key (Authorization: Bearer, falling
// back to x-api-key) and resolves the client protocol from the path. On success
// it stores the KeyContext and protocol on the request. On failure it aborts.
func Middleware(rt *runtime.Runtime) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, _, ok := web.PathProtocol(c.Request.URL.Path)
		if !ok {
			web.WriteError(c, http.StatusNotFound, "not_found", "unknown path", nil)
			c.Abort()
			return
		}
		reqctx.SetProtocol(c, p)

		token := extractToken(c)
		if token == "" {
			web.WriteError(c, http.StatusUnauthorized, "unauthorized", "missing API key", nil)
			c.Abort()
			return
		}
		snap := rt.Snapshot()
		kc := snap.Indexes.Keys[token]
		if kc == nil {
			web.WriteError(c, http.StatusUnauthorized, "unauthorized", "invalid API key", nil)
			c.Abort()
			return
		}
		reqctx.SetKeyContext(c, kc)
		c.Next()
	}
}

func extractToken(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if k := c.GetHeader("x-api-key"); k != "" {
		return k
	}
	return ""
}
