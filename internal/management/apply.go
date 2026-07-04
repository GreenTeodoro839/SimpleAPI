package management

import (
	"net/http"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/web"
	"github.com/gin-gonic/gin"
)

// applyAndCommit validates candidateRaw (a placeholder config), and on success
// writes it atomically to config.yaml, rebuilds indexes from an expanded copy,
// and swaps the runtime. On validation failure it responds 422 and changes
// nothing. ok reports whether the commit succeeded.
func (h *Handler) applyAndCommit(c *gin.Context, candidateRaw *config.Config) bool {
	expanded := config.DeepCopy(candidateRaw)
	config.Expand(expanded)
	if errs := config.Validate(expanded); len(errs) > 0 {
		c.JSON(http.StatusUnprocessableEntity, config.ResultFromErrors(errs))
		return false
	}
	data, err := config.Marshal(candidateRaw)
	if err != nil {
		web.WriteError(c, http.StatusInternalServerError, "internal_error", "could not marshal config", nil)
		return false
	}
	if err := config.WriteFileAtomic(h.rt.ConfigPath(), data); err != nil {
		web.WriteError(c, http.StatusInternalServerError, "internal_error", "could not write config: "+err.Error(), nil)
		return false
	}
	idx, err := indexes.Build(expanded)
	if err != nil {
		web.WriteError(c, http.StatusInternalServerError, "internal_error", "could not build indexes: "+err.Error(), nil)
		return false
	}
	h.rt.Replace(candidateRaw, expanded, idx)
	return true
}

// mutateRaw clones the current raw config, applies a mutation, and commits. On
// success it responds with the given HTTP status and a ValidationResult.
func (h *Handler) mutateRaw(c *gin.Context, status int, mutate func(raw *config.Config)) {
	snap := h.rt.Snapshot()
	candidate := config.DeepCopy(snap.Raw)
	mutate(candidate)
	if h.applyAndCommit(c, candidate) {
		c.JSON(status, config.ValidationResult{Valid: true})
	}
}
