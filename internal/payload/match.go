// Package payload applies the configured outbound-payload rules to the final
// JSON body sent upstream (DEVELOPMENT.md §11). The five phases run in a fixed
// order: default, default-raw, override, override-raw, filter.
package payload

import (
	"net/http"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/glob"
	"github.com/tidwall/gjson"
)

// MatchContext is what a rule is matched against.
type MatchContext struct {
	InternalID    string         // target model after web-search forwarding
	AliasA        string
	UpstreamModel string
	Protocol      string // outbound provider type
	FromProtocol  string // client source protocol
	Headers       http.Header
}

// anyModelMatches reports whether any of a rule's model clauses matches mc.
func anyModelMatches(models []config.PayloadModelRule, mc MatchContext, body []byte) bool {
	for _, m := range models {
		if ruleMatches(m, mc, body) {
			return true
		}
	}
	return false
}

// ruleMatches evaluates all predicates of one model clause; ALL must pass.
func ruleMatches(r config.PayloadModelRule, mc MatchContext, body []byte) bool {
	// 1. model name glob (try internal id, aliasA, upstream model)
	if r.Name != "" {
		if !glob.Match(r.Name, mc.InternalID) &&
			!glob.Match(r.Name, mc.AliasA) &&
			!glob.Match(r.Name, mc.UpstreamModel) {
			return false
		}
	}
	// 2. protocol (outbound)
	if r.Protocol != "" && r.Protocol != mc.Protocol {
		return false
	}
	// 3. from-protocol (client)
	if r.FromProtocol != "" && r.FromProtocol != mc.FromProtocol {
		return false
	}
	// 4. headers (case-insensitive keys, glob values)
	for k, pat := range r.Headers {
		if !glob.Match(pat, mc.Headers.Get(k)) {
			return false
		}
	}
	// 5. match: each path must equal value
	for _, entry := range r.Match {
		for path, val := range entry {
			if !gjsonEquals(gjson.GetBytes(body, path), val) {
				return false
			}
		}
	}
	// 6. not-match: each path must NOT equal value
	for _, entry := range r.NotMatch {
		for path, val := range entry {
			if gjsonEquals(gjson.GetBytes(body, path), val) {
				return false
			}
		}
	}
	// 7. exist: each path must exist and not be null
	for _, path := range r.Exist {
		res := gjson.GetBytes(body, path)
		if !res.Exists() || res.Type == gjson.Null {
			return false
		}
	}
	// 8. not-exist: each path must be absent or null
	for _, path := range r.NotExist {
		res := gjson.GetBytes(body, path)
		if res.Exists() && res.Type != gjson.Null {
			return false
		}
	}
	return true
}

// gjsonEquals compares a gjson result to a YAML-decoded value (string/bool/number).
func gjsonEquals(res gjson.Result, val interface{}) bool {
	switch v := val.(type) {
	case string:
		return res.Type == gjson.String && res.String() == v
	case bool:
		return res.Type == gjson.True && v || res.Type == gjson.False && !v
	case int:
		return res.Type == gjson.Number && res.Int() == int64(v)
	case int64:
		return res.Type == gjson.Number && res.Int() == v
	case float64:
		return res.Type == gjson.Number && res.Float() == v
	}
	return false
}

// glob wildcard handling lives in internal/glob (shared with aliasB routing).

