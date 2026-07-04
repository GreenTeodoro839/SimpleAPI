package config

import "gopkg.in/yaml.v3"

// DeepCopy returns an independent copy of cfg via YAML round-trip. Used to clone
// the raw (placeholder) config for management mutations and to derive an
// expanded copy without mutating the stored raw form.
func DeepCopy(cfg *Config) *Config {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return &Config{}
	}
	out := &Config{}
	if err := yaml.Unmarshal(data, out); err != nil {
		return &Config{}
	}
	return out
}
