package config

import "encoding/json"

// Redact returns a deep copy of cfg with all secret material blanked:
// providers[].key, api_keys[].key, and management.admin_key. The returned copy
// is safe to serialize for GET endpoints; the live cfg is never mutated.
func Redact(cfg *Config) *Config {
	// Deep copy via JSON round-trip. Our structs carry json tags, and the result
	// is only used for display, so float64 widening of numeric Params values is
	// acceptable.
	data, err := json.Marshal(cfg)
	if err != nil {
		// Should never happen for our plain structs; fall back to a shallow nil.
		return &Config{}
	}
	out := &Config{}
	if err := json.Unmarshal(data, out); err != nil {
		return &Config{}
	}
	for i := range out.Providers {
		out.Providers[i].Key = ""
		for k := range out.Providers[i].Headers {
			// Headers may also carry secrets; blank all to be safe.
			out.Providers[i].Headers[k] = ""
		}
	}
	for i := range out.APIKeys {
		out.APIKeys[i].Key = ""
	}
	out.Management.AdminKey = ""
	return out
}
