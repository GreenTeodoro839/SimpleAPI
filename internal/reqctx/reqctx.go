// Package reqctx carries per-request resolved values across gin middleware and
// handlers (the authenticated API-key context and the client protocol).
package reqctx

import (
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/gin-gonic/gin"
)

const (
	ctxKeyContext  = "sapi.keyctx"
	ctxKeyProtocol = "sapi.protocol"
)

// SetKeyContext stores the resolved inbound API-key context on the request.
func SetKeyContext(c *gin.Context, kc *indexes.KeyContext) {
	c.Set(ctxKeyContext, kc)
}

// KeyContext returns the resolved API-key context, or nil if unset.
func KeyContext(c *gin.Context) *indexes.KeyContext {
	v, ok := c.Get(ctxKeyContext)
	if !ok {
		return nil
	}
	kc, _ := v.(*indexes.KeyContext)
	return kc
}

// SetProtocol stores the client-facing protocol for this request.
func SetProtocol(c *gin.Context, p string) {
	c.Set(ctxKeyProtocol, p)
}

// Protocol returns the client-facing protocol, or "" if unset.
func Protocol(c *gin.Context) string {
	v, ok := c.Get(ctxKeyProtocol)
	if !ok {
		return ""
	}
	p, _ := v.(string)
	return p
}
