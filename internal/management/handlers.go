package management

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

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
	cfg := &config.Config{}
	if err := decodeBody(c, cfg); err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if h.applyAndCommit(c, cfg) {
		c.JSON(http.StatusOK, config.ValidationResult{Valid: true})
	}
}

func (h *Handler) validate(c *gin.Context) {
	cfg := &config.Config{}
	if err := decodeBody(c, cfg); err != nil {
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
	var pc config.PayloadConfig
	if err := decodeBody(c, &pc); err != nil {
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

// decodeBody reads and parses the management request body into dst. It is the
// single entry point for every write endpoint.
//
// The management API is JSON; PUT /config and POST /validate also document
// application/yaml. JSON is decoded with encoding/json rather than yaml.v3:
// gopkg.in/yaml.v3 does not implement the "\/" escape, which is valid JSON and
// is emitted by some serializers (notably Android's org.json), so a YAML-only
// decode rejected such bodies with "found unknown escape character". YAML is
// used only when the request is explicitly YAML; otherwise JSON is tried first,
// with a YAML fallback for an unspecified content type so a plain YAML body
// still parses.
func decodeBody(c *gin.Context, dst any) error {
	body, err := readBody(c)
	if err != nil {
		return err
	}
	return decode(requestMIME(c.GetHeader("Content-Type")), body, dst)
}

// decode parses body into dst according to contentType (a bare MIME type with
// parameters stripped). Split out from decodeBody so the parsing rules can be
// exercised directly in tests.
func decode(contentType string, body []byte, dst any) error {
	switch contentType {
	case "application/yaml", "application/x-yaml", "text/yaml":
		if err := yaml.Unmarshal(body, dst); err != nil {
			return fmt.Errorf("invalid YAML request body: %w", err)
		}
		return nil
	}
	// JSON is the documented default, and encoding/json handles "\/" correctly.
	if err := json.Unmarshal(body, dst); err == nil {
		return nil
	} else if contentType == "application/json" || strings.HasSuffix(contentType, "+json") {
		// The client explicitly sent JSON, so surface the JSON error rather
		// than masking it by retrying as YAML.
		return fmt.Errorf("invalid JSON request body: %w", err)
	}
	// Unspecified (or unknown) content type: fall back to YAML, which is a
	// strict superset of JSON, so a plain YAML body still works.
	if err := yaml.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}

// requestMIME returns the bare, lowercased MIME type from a Content-Type header
// value, with any parameters (e.g. "; charset=utf-8") stripped. An empty header
// yields the empty string.
func requestMIME(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}
