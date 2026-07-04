package websearch

import (
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
)

// ResolveForward returns the target model to reroute to when the current model
// has web-search forwarding enabled and the request carries a web_search tool.
// It returns nil when forwarding does not apply (disabled, no target, no
// web_search tool, self-loop, or unknown target).
func ResolveForward(rm *indexes.ResolvedModel, body []byte, models map[string]*indexes.ResolvedModel) *indexes.ResolvedModel {
	if rm == nil || rm.WebSearch == nil || !rm.WebSearch.Enabled {
		return nil
	}
	if !HasWebSearchTool(body) {
		return nil
	}
	target := rm.WebSearch.TargetModel
	if target == "" || target == rm.InternalID {
		return nil
	}
	return models[target]
}
