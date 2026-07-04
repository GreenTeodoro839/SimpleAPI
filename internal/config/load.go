package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads and YAML-decodes the config at path WITHOUT expanding secret
// placeholders. Callers keep this "raw" form as the on-disk representation
// (so management edits write back ${VAR} placeholders, not plaintext) and pass
// an expanded copy to Validate/indexes.Build for runtime use.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Decode(data)
}

// Decode parses a config from raw bytes (YAML; JSON is a subset). Placeholders
// are left intact.
func Decode(data []byte) (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Marshal serializes cfg to YAML bytes (used by the management API to write
// config.yaml).
func Marshal(cfg *Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}

// Expand expands ${VAR}/${VAR:-default} placeholders in every secret-bearing
// string field in place: providers[].key, providers[].headers.*, api_keys[].key,
// and management.admin_key.
func Expand(cfg *Config) {
	for i := range cfg.Providers {
		cfg.Providers[i].Key = ExpandEnv(cfg.Providers[i].Key)
		for k, v := range cfg.Providers[i].Headers {
			cfg.Providers[i].Headers[k] = ExpandEnv(v)
		}
	}
	for i := range cfg.APIKeys {
		cfg.APIKeys[i].Key = ExpandEnv(cfg.APIKeys[i].Key)
	}
	cfg.Management.AdminKey = ExpandEnv(cfg.Management.AdminKey)
}
