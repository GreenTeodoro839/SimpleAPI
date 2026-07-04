// Package management implements the management API (DEVELOPMENT.md §14). Health
// is wired first; the remaining endpoints and admin-key middleware land in
// later phases.
package management

import "github.com/gin-gonic/gin"

// Health is the unauthenticated liveness probe (GET /-/health).
func Health(c *gin.Context) {
	c.JSON(200, gin.H{"status": "ok"})
}
