package httpapi

import (
	"github.com/GreenTeodoro839/SimpleAPI/internal/auth"
	"github.com/GreenTeodoro839/SimpleAPI/internal/pipeline"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// registerClientRoutes wires the three client-compatible proxy routes (shared
// ServeProxy handler) and the /v1/models list, all behind inbound auth.
func registerClientRoutes(g *gin.Engine, rt *runtime.Runtime, logger *logrus.Logger) {
	mw := auth.Middleware(rt)
	h := pipeline.NewHandler(rt, logger)
	g.POST("/v1/messages", mw, h.ServeProxy)
	g.POST("/v1/chat/completions", mw, h.ServeProxy)
	g.POST("/v1/responses", mw, h.ServeProxy)
	g.GET("/v1/models", mw, h.ServeModels)
}
