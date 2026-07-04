package management

import (
	"net/http"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/web"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

func (h *Handler) listProviders(c *gin.Context) {
	snap := h.rt.Snapshot()
	r := config.DeepCopy(snap.Raw)
	c.JSON(http.StatusOK, gin.H{"providers": r.Providers})
}

func (h *Handler) createProvider(c *gin.Context) {
	body, err := readBody(c)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	var p config.Provider
	if err := yaml.Unmarshal(body, &p); err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	h.mutateRaw(c, http.StatusCreated, func(raw *config.Config) {
		raw.Providers = append(raw.Providers, p)
	})
}

func (h *Handler) getProvider(c *gin.Context) {
	name := c.Param("name")
	snap := h.rt.Snapshot()
	for _, p := range snap.Raw.Providers {
		if p.Name == name {
			c.JSON(http.StatusOK, p)
			return
		}
	}
	web.WriteError(c, http.StatusNotFound, "not_found", "provider "+name+" not found", nil)
}

func (h *Handler) putProvider(c *gin.Context) {
	name := c.Param("name")
	body, err := readBody(c)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	var p config.Provider
	if err := yaml.Unmarshal(body, &p); err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	snap := h.rt.Snapshot()
	idx := providerIndex(snap.Raw, name)
	if idx < 0 {
		web.WriteError(c, http.StatusNotFound, "not_found", "provider "+name+" not found", nil)
		return
	}
	h.mutateRaw(c, http.StatusOK, func(raw *config.Config) {
		raw.Providers[idx] = p
	})
}

func (h *Handler) deleteProvider(c *gin.Context) {
	name := c.Param("name")
	snap := h.rt.Snapshot()
	if idx := providerIndex(snap.Raw, name); idx < 0 {
		web.WriteError(c, http.StatusNotFound, "not_found", "provider "+name+" not found", nil)
		return
	}
	// Reject if any API key references a model belonging to this provider.
	for _, k := range snap.Raw.APIKeys {
		for _, m := range k.Models {
			if prov, _, ok := config.ParseInternalModelID(m.Model); ok && prov == name {
				web.WriteError(c, http.StatusUnprocessableEntity, "provider_in_use",
					"provider "+name+" is referenced by api key "+k.Name, nil)
				return
			}
		}
	}
	h.mutateRaw(c, http.StatusNoContent, func(raw *config.Config) {
		idx := providerIndex(raw, name)
		raw.Providers = append(raw.Providers[:idx], raw.Providers[idx+1:]...)
	})
}

func providerIndex(cfg *config.Config, name string) int {
	for i, p := range cfg.Providers {
		if p.Name == name {
			return i
		}
	}
	return -1
}
