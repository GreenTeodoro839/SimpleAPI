package config

import (
	"fmt"

	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
)

// ValidationError describes one config problem at a JSON-shaped path.
type ValidationError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationResult is returned by the management API validate/PUT endpoints.
type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationError `json:"errors,omitempty"`
}

// Validate runs the full §5 rule set against cfg and returns every problem
// found. An empty (or nil) slice means the config is valid. Callers treat any
// non-empty result as fatal at startup (log.Fatal + exit) and as HTTP 422 from
// the management API.
func Validate(cfg *Config) []ValidationError {
	if cfg == nil {
		return []ValidationError{{Path: "", Code: "invalid_request", Message: "config is nil"}}
	}
	var errs []ValidationError

	internalIDs := map[string]struct{}{}
	providerSeen := map[string]int{}

	// --- providers ---
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		base := fmt.Sprintf("providers[%d]", i)

		if p.Name == "" {
			errs = append(errs, ve(base+".name", "provider_name_required", "provider name is required"))
		} else {
			if containsSlash(p.Name) {
				errs = append(errs, ve(base+".name", "provider_name_slash",
					fmt.Sprintf("provider name %q must not contain '/'", p.Name)))
			}
			if _, dup := providerSeen[p.Name]; dup {
				errs = append(errs, ve(base+".name", "duplicate_provider",
					fmt.Sprintf("duplicate provider name %q", p.Name)))
			} else {
				providerSeen[p.Name] = i
			}
		}

		if _, u := p.UpstreamFor(p.Type); u == "" {
			errs = append(errs, ve(base+".url", "provider_url_required",
				"default upstream requires url or urls[type]"))
		}
		for pr := range p.URLs {
			if !protocol.IsValid(pr) {
				errs = append(errs, ve(fmt.Sprintf("%s.urls[%q]", base, pr), "invalid_protocol",
					fmt.Sprintf("invalid upstream protocol %q (want anthropic|openai_completion|codex)", pr)))
			}
		}
		if !protocol.IsValid(p.Type) {
			errs = append(errs, ve(base+".type", "invalid_provider_type",
				fmt.Sprintf("invalid provider type %q (want anthropic|openai_completion|codex)", p.Type)))
		}
		if p.MaxConcurrency != nil && *p.MaxConcurrency < 0 {
			errs = append(errs, ve(base+".max_concurrency", "invalid_max_concurrency",
				fmt.Sprintf("max_concurrency %d must be >= 0 (0 = unlimited)", *p.MaxConcurrency)))
		}

		aliasSeen := map[string]int{}
		for j := range p.Models {
			m := &p.Models[j]
			mbase := fmt.Sprintf("%s.models[%d]", base, j)
			if m.Model == "" {
				errs = append(errs, ve(mbase+".model", "model_required", "provider model name is required"))
			}
			eff := EffectiveAliasA(*m)
			if _, dup := aliasSeen[eff]; dup {
				errs = append(errs, ve(mbase+".aliasA", "duplicate_aliasA",
					fmt.Sprintf("duplicate aliasA %q under provider %q", eff, p.Name)))
			} else {
				aliasSeen[eff] = j
			}
			if p.Name != "" && m.Model != "" {
				id := ModelInternalID(p.Name, *m)
				if _, dup := internalIDs[id]; dup {
					errs = append(errs, ve(mbase+".aliasA", "duplicate_internal_model",
						fmt.Sprintf("duplicate internal model id %q", id)))
				} else {
					internalIDs[id] = struct{}{}
				}
			}
		}
	}

	// --- api_keys ---
	keyNameSeen := map[string]int{}
	keyValueSeen := map[string]int{}
	for i := range cfg.APIKeys {
		k := &cfg.APIKeys[i]
		base := fmt.Sprintf("api_keys[%d]", i)

		if k.Name == "" {
			errs = append(errs, ve(base+".name", "api_key_name_required", "api key name is required"))
		} else if _, dup := keyNameSeen[k.Name]; dup {
			errs = append(errs, ve(base+".name", "duplicate_api_key_name",
				fmt.Sprintf("duplicate api key name %q", k.Name)))
		} else {
			keyNameSeen[k.Name] = i
		}

		if k.Key != "" {
			if _, dup := keyValueSeen[k.Key]; dup {
				errs = append(errs, ve(base+".key", "duplicate_api_key", "duplicate api key value"))
			} else {
				keyValueSeen[k.Key] = i
			}
		}

		for j, pr := range k.AllowedProtocols {
			if !protocol.IsValid(pr) {
				errs = append(errs, ve(fmt.Sprintf("%s.allowed_protocols[%d]", base, j), "invalid_protocol",
					fmt.Sprintf("invalid protocol %q", pr)))
			}
		}

		for j := range k.Models {
			cm := &k.Models[j]
			cmbase := fmt.Sprintf("%s.models[%d]", base, j)
			if cm.Model == "" {
				errs = append(errs, ve(cmbase+".model", "model_required", "client model id is required"))
				continue
			}
			if _, exists := internalIDs[cm.Model]; !exists {
				errs = append(errs, ve(cmbase+".model", "model_not_found",
					fmt.Sprintf("model %q does not resolve to a known internal model id", cm.Model)))
			}
		}
	}

	// --- payload rules ---
	errs = append(errs, validatePayload(cfg.Payload)...)

	// --- server / management basics ---
	if cfg.Server.Listen == nil || *cfg.Server.Listen == "" {
		errs = append(errs, ve("server.listen", "server_listen_required", "server.listen is required"))
	}
	if cfg.Management.IsEnabled() && cfg.Management.AdminKey == "" {
		errs = append(errs, ve("management.admin_key", "admin_key_required",
			"management.admin_key is required when management is enabled"))
	}

	return errs
}

// ResultFromErrrors builds a ValidationResult (nil/empty → valid).
func ResultFromErrors(errs []ValidationError) ValidationResult {
	return ValidationResult{Valid: len(errs) == 0, Errors: errs}
}

func ve(path, code, msg string) ValidationError {
	return ValidationError{Path: path, Code: code, Message: msg}
}

func containsSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}
