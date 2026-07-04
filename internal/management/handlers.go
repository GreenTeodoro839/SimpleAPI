package management

import (
	"io"
	"net/http"
	"strconv"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/GreenTeodoro839/SimpleAPI/internal/web"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// Handler holds the dependencies shared by all management endpoints.
type Handler struct {
	rt     *runtime.Runtime
	logger *logrus.Logger
}

func NewHandler(rt *runtime.Runtime, logger *logrus.Logger) *Handler {
	return &Handler{rt: rt, logger: logger}
}

// Register wires every management endpoint (except /-/health) on rg.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.GET("/config", h.getConfig)
	rg.PUT("/config", h.putConfig)
	rg.POST("/validate", h.validate)
	rg.GET("/payload", h.getPayload)
	rg.PUT("/payload", h.putPayload)
	rg.POST("/reload", h.reload)
	rg.GET("/providers", h.listProviders)
	rg.POST("/providers", h.createProvider)
	rg.GET("/providers/:name", h.getProvider)
	rg.PUT("/providers/:name", h.putProvider)
	rg.DELETE("/providers/:name", h.deleteProvider)
	rg.GET("/api-keys", h.listAPIKeys)
	rg.POST("/api-keys", h.createAPIKey)
	rg.GET("/api-keys/:keyName", h.getAPIKey)
	rg.PUT("/api-keys/:keyName", h.putAPIKey)
	rg.DELETE("/api-keys/:keyName", h.deleteAPIKey)
	rg.GET("/models", h.listModels)
	rg.GET("/usage", h.getUsage)
	rg.GET("/call-log", h.getCallLog)
}

// ---- config ----

func (h *Handler) getConfig(c *gin.Context) {
	snap := h.rt.Snapshot()
	c.JSON(http.StatusOK, config.DeepCopy(snap.Raw))
}

func (h *Handler) putConfig(c *gin.Context) {
	body, err := readBody(c)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	cfg, err := config.Decode(body)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if h.applyAndCommit(c, cfg) {
		c.JSON(http.StatusOK, config.ValidationResult{Valid: true})
	}
}

func (h *Handler) validate(c *gin.Context) {
	body, err := readBody(c)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	cfg, err := config.Decode(body)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	expanded := config.DeepCopy(cfg)
	config.Expand(expanded)
	c.JSON(http.StatusOK, config.ResultFromErrors(config.Validate(expanded)))
}

// ---- payload ----

func (h *Handler) getPayload(c *gin.Context) {
	snap := h.rt.Snapshot()
	c.JSON(http.StatusOK, snap.Raw.Payload)
}

func (h *Handler) putPayload(c *gin.Context) {
	body, err := readBody(c)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	var pc config.PayloadConfig
	if err := yaml.Unmarshal(body, &pc); err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	h.mutateRaw(c, http.StatusOK, func(raw *config.Config) { raw.Payload = pc })
}

// ---- reload ----

func (h *Handler) reload(c *gin.Context) {
	raw, err := config.Load(h.rt.ConfigPath())
	if err != nil {
		web.WriteError(c, http.StatusInternalServerError, "internal_error", "could not read config: "+err.Error(), nil)
		return
	}
	expanded := config.DeepCopy(raw)
	config.Expand(expanded)
	if errs := config.Validate(expanded); len(errs) > 0 {
		c.JSON(http.StatusUnprocessableEntity, config.ResultFromErrors(errs))
		return
	}
	idx, err := indexes.Build(expanded)
	if err != nil {
		web.WriteError(c, http.StatusInternalServerError, "internal_error", err.Error(), nil)
		return
	}
	h.rt.Replace(raw, expanded, idx)
	c.JSON(http.StatusOK, config.ValidationResult{Valid: true})
}

// ---- models ----

func (h *Handler) listModels(c *gin.Context) {
	snap := h.rt.Snapshot()
	models := make([]gin.H, 0, len(snap.Indexes.InternalIDs))
	for _, id := range snap.Indexes.InternalIDs {
		rm := snap.Indexes.Models[id]
		models = append(models, gin.H{
			"id":             rm.InternalID,
			"provider":       rm.ProviderName,
			"provider_type":  rm.ProviderType,
			"aliasA":         rm.AliasA,
			"upstream_model": rm.UpstreamModel,
		})
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}

// ---- usage ----

func (h *Handler) getUsage(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"items": h.rt.Usage().Snapshot()})
}

// ---- call log ----

// getCallLog returns the most recent upstream-attempt records (newest first),
// up to ?limit=N (default 100). Unlike CLIProxyAPI's usage-queue this is a
// non-destructive read of an in-memory ring buffer: records are not removed.
func (h *Handler) getCallLog(c *gin.Context) {
	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": h.rt.CallLog().Recent(limit)})
}

func readBody(c *gin.Context) ([]byte, error) {
	return io.ReadAll(c.Request.Body)
}
