package payload

import (
	"sort"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Engine applies payload rules to outbound bodies.
type Engine struct {
	cfg *config.PayloadConfig
}

func NewEngine(cfg *config.PayloadConfig) *Engine {
	return &Engine{cfg: cfg}
}

// Apply runs the five phases in order against body and returns the result.
func (e *Engine) Apply(body []byte, mc MatchContext) []byte {
	if e.cfg == nil {
		return body
	}
	body = e.applyDefault(body, e.cfg.Default, mc, false)
	body = e.applyDefault(body, e.cfg.DefaultRaw, mc, true)
	body = e.applyOverride(body, e.cfg.Override, mc, false)
	body = e.applyOverride(body, e.cfg.OverrideRaw, mc, true)
	body = e.applyFilter(body, e.cfg.Filter, mc)
	return body
}

// applyDefault writes each param only when its path does not already exist;
// the first rule to write a path wins (later rules skip it).
func (e *Engine) applyDefault(body []byte, rules []config.PayloadRule, mc MatchContext, raw bool) []byte {
	for _, r := range rules {
		if !anyModelMatches(r.Models, mc, body) {
			continue
		}
		for path, val := range r.Params {
			if gjson.GetBytes(body, path).Exists() {
				continue
			}
			body = writeParam(body, path, val, raw)
		}
	}
	return body
}

// applyOverride always writes each param; the last rule to write a path wins.
func (e *Engine) applyOverride(body []byte, rules []config.PayloadRule, mc MatchContext, raw bool) []byte {
	for _, r := range rules {
		if !anyModelMatches(r.Models, mc, body) {
			continue
		}
		for path, val := range r.Params {
			body = writeParam(body, path, val, raw)
		}
	}
	return body
}

// applyFilter deletes matched paths; paths are reverse-sorted so array indices
// and nested deletions do not corrupt earlier ones.
func (e *Engine) applyFilter(body []byte, rules []config.PayloadFilterRule, mc MatchContext) []byte {
	var paths []string
	for _, r := range rules {
		if !anyModelMatches(r.Models, mc, body) {
			continue
		}
		paths = append(paths, r.Params...)
	}
	if len(paths) == 0 {
		return body
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, p := range paths {
		if out, err := sjson.DeleteBytes(body, p); err == nil {
			body = out
		}
	}
	return body
}

// writeParam sets a JSON path. For raw phases val is a JSON-fragment string.
func writeParam(body []byte, path string, val interface{}, raw bool) []byte {
	if raw {
		s, _ := val.(string)
		if out, err := sjson.SetRawBytes(body, path, []byte(s)); err == nil {
			return out
		}
		return body
	}
	if out, err := sjson.SetBytes(body, path, val); err == nil {
		return out
	}
	return body
}
