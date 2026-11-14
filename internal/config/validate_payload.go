package config

import (
	"encoding/json"
	"fmt"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
)

// validatePayload validates the five payload rule lists (§5, §11): raw-fragment
// values must be valid JSON, no rule may target the top-level model field, and
// protocol/from-protocol values must be in the enum.
func validatePayload(pc PayloadConfig) []ValidationError {
	var errs []ValidationError

	checkRules := func(phase string, rules []PayloadRule, raw bool) {
		for i := range rules {
			r := &rules[i]
			rbase := fmt.Sprintf("payload.%s[%d]", phase, i)
			errs = append(errs, validateModelRules(rbase, r.Models)...)
			for path, val := range r.Params {
				pbase := fmt.Sprintf("%s.params.%s", rbase, path)
				if firstPathSegment(path) == "model" {
					errs = append(errs, ve(pbase, "model_field_protected",
						"payload rules must not modify or delete the top-level model field"))
					continue
				}
				if raw {
					s, ok := val.(string)
					if !ok {
						errs = append(errs, ve(pbase, "invalid_raw_json",
							"raw payload value must be a JSON fragment string"))
					} else if !json.Valid([]byte(s)) {
						errs = append(errs, ve(pbase, "invalid_raw_json",
							"raw payload value is not a valid JSON fragment"))
					}
				}
			}
		}
	}

	checkRules("default", pc.Default, false)
	checkRules("default-raw", pc.DefaultRaw, true)
	checkRules("override", pc.Override, false)
	checkRules("override-raw", pc.OverrideRaw, true)

	// filter
	for i := range pc.Filter {
		r := &pc.Filter[i]
		rbase := fmt.Sprintf("payload.filter[%d]", i)
		errs = append(errs, validateModelRules(rbase, r.Models)...)
		for _, path := range r.Params {
			if firstPathSegment(path) == "model" {
				errs = append(errs, ve(fmt.Sprintf("%s.params", rbase), "model_field_protected",
					"payload filter must not delete the top-level model field"))
			}
		}
	}

	return errs
}

func validateModelRules(rbase string, models []PayloadModelRule) []ValidationError {
	var errs []ValidationError
	for j := range models {
		mr := &models[j]
		mrbase := fmt.Sprintf("%s.models[%d]", rbase, j)
		if mr.Name == "" {
			errs = append(errs, ve(mrbase+".name", "payload_model_name_required",
				"payload rule model name is required"))
		}
		if mr.Protocol != "" && !protocol.IsValid(mr.Protocol) {
			errs = append(errs, ve(mrbase+".protocol", "invalid_protocol",
				fmt.Sprintf("invalid protocol %q", mr.Protocol)))
		}
		if mr.FromProtocol != "" && !protocol.IsValid(mr.FromProtocol) {
			errs = append(errs, ve(mrbase+".from-protocol", "invalid_protocol",
				fmt.Sprintf("invalid from-protocol %q", mr.FromProtocol)))
		}
	}
	return errs
}

// firstPathSegment returns the leading segment of a JSON path, i.e. everything
// before the first '.' or '['. Used to guard the top-level "model" field.
func firstPathSegment(path string) string {
	for i := 0; i < len(path); i++ {
		if path[i] == '.' || path[i] == '[' {
			return path[:i]
		}
	}
	return path
}
