package management

import (
	"net/http"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/web"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

func (h *Handler) listAPIKeys(c *gin.Context) {
	snap := h.rt.Snapshot()
	r := config.DeepCopy(snap.Raw)
	c.JSON(http.StatusOK, gin.H{"api_keys": r.APIKeys})
}

func (h *Handler) createAPIKey(c *gin.Context) {
	body, err := readBody(c)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	var k config.ClientApiKey
	if err := yaml.Unmarshal(body, &k); err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	h.mutateRaw(c, http.StatusCreated, func(raw *config.Config) {
		raw.APIKeys = append(raw.APIKeys, k)
	})
}

func (h *Handler) getAPIKey(c *gin.Context) {
	name := c.Param("keyName")
	snap := h.rt.Snapshot()
	for _, k := range snap.Raw.APIKeys {
		if k.Name == name {
			c.JSON(http.StatusOK, k)
			return
		}
	}
	web.WriteError(c, http.StatusNotFound, "not_found", "api key "+name+" not found", nil)
}

func (h *Handler) putAPIKey(c *gin.Context) {
	name := c.Param("keyName")
	body, err := readBody(c)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	var k config.ClientApiKey
	if err := yaml.Unmarshal(body, &k); err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	snap := h.rt.Snapshot()
	idx := apiKeyIndex(snap.Raw, name)
	if idx < 0 {
		web.WriteError(c, http.StatusNotFound, "not_found", "api key "+name+" not found", nil)
		return
	}
	h.mutateRaw(c, http.StatusOK, func(raw *config.Config) {
		raw.APIKeys[idx] = k
	})
}

func (h *Handler) deleteAPIKey(c *gin.Context) {
	name := c.Param("keyName")
	snap := h.rt.Snapshot()
	if apiKeyIndex(snap.Raw, name) < 0 {
		web.WriteError(c, http.StatusNotFound, "not_found", "api key "+name+" not found", nil)
		return
	}
	h.mutateRaw(c, http.StatusNoContent, func(raw *config.Config) {
		idx := apiKeyIndex(raw, name)
		raw.APIKeys = append(raw.APIKeys[:idx], raw.APIKeys[idx+1:]...)
	})
}

func apiKeyIndex(cfg *config.Config, name string) int {
	for i, k := range cfg.APIKeys {
		if k.Name == name {
			return i
		}
	}
	return -1
}
