// Package httpapi wires the gin engine and registers routes. Phase 1 wires only
// the health probe; client routes and management endpoints are added later.
package httpapi

import (
	"time"

	"github.com/GreenTeodoro839/SimpleAPI/internal/management"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// NewServer builds the gin engine for the given runtime.
func NewServer(rt *runtime.Runtime, logger *logrus.Logger) *gin.Engine {
	g := gin.New()
	g.Use(gin.Recovery())
	g.Use(requestLogger(logger))

	// Management: health is unauthenticated.
	g.GET("/-/health", management.Health)

	// Client proxy routes and the rest of the management API are registered in
	// later phases.
	registerClientRoutes(g, rt, logger)
	registerManagementRoutes(g, rt, logger)

	return g
}

func requestLogger(logger *logrus.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.WithFields(logrus.Fields{
			"status":     c.Writer.Status(),
			"method":     c.Request.Method,
			"path":       c.Request.URL.Path,
			"latency_ms": time.Since(start).Milliseconds(),
		}).Info("request")
	}
}
