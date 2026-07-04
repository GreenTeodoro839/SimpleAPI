package httpapi

import (
	"github.com/GreenTeodoro839/SimpleAPI/internal/management"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// registerManagementRoutes wires the management API under its configured base
// path (default /-/api), behind admin-key auth. Health stays unauthenticated
// and is registered separately in server.go. No-op when management is disabled.
func registerManagementRoutes(g *gin.Engine, rt *runtime.Runtime, logger *logrus.Logger) {
	snap := rt.Snapshot()
	if !snap.Config.Management.IsEnabled() {
		return
	}
	base := snap.Config.Management.Base()
	if base == "" {
		base = "/-/api"
	}
	h := management.NewHandler(rt, logger)
	rg := g.Group(base, management.AdminKeyAuth(rt))
	h.Register(rg)
}
