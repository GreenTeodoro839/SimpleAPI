package management

import (
	"crypto/subtle"
	"net/http"

	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/GreenTeodoro839/SimpleAPI/internal/web"
	"github.com/gin-gonic/gin"
)

// AdminKeyAuth guards management endpoints with a constant-time X-Admin-Key
// comparison against the expanded management.admin_key. When management is
// disabled, every management endpoint (except /-/health) returns 401.
func AdminKeyAuth(rt *runtime.Runtime) gin.HandlerFunc {
	return func(c *gin.Context) {
		snap := rt.Snapshot()
		if !snap.Config.Management.IsEnabled() {
			web.WriteError(c, http.StatusUnauthorized, "unauthorized", "management API is disabled", nil)
			c.Abort()
			return
		}
		provided := c.GetHeader("X-Admin-Key")
		want := snap.Config.Management.AdminKey
		if len(provided) == 0 || len(want) == 0 || subtle.ConstantTimeCompare([]byte(provided), []byte(want)) != 1 {
			web.WriteError(c, http.StatusUnauthorized, "unauthorized", "invalid or missing admin key", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}
