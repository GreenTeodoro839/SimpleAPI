// Package config holds the YAML configuration model for the proxy, plus loading,
// environment-variable expansion, validation, and atomic file writes.
//
// The Go types in this file map one-to-one to config.yaml (DEVELOPMENT.md §4).
// Fields with non-zero defaults (or where zero is a meaningful explicit value,
// e.g. failure_reset_seconds) are pointers so "omitted" is distinguishable from
// "explicitly zero/false". ApplyDefaults resolves every pointer to non-nil.
package config

// Config is the root configuration object.
type Config struct {
	Version    int              `yaml:"version"     json:"version"`
	Server     ServerConfig     `yaml:"server"      json:"server"`
	Proxy      ProxyConfig      `yaml:"proxy"       json:"proxy"`
	Management ManagementConfig `yaml:"management"  json:"management"`
	Payload    PayloadConfig    `yaml:"payload"     json:"payload"`
	Providers  []Provider       `yaml:"providers"   json:"providers"`
	APIKeys    []ClientApiKey   `yaml:"api_keys"    json:"api_keys"`
}

// ServerConfig controls the HTTP listener.
type ServerConfig struct {
	Listen                   *string `yaml:"listen"                      json:"listen"`
	RequestTimeoutSeconds    *int    `yaml:"request_timeout_seconds"     json:"request_timeout_seconds"`
	StreamIdleTimeoutSeconds *int    `yaml:"stream_idle_timeout_seconds" json:"stream_idle_timeout_seconds"`
}

func (s ServerConfig) RequestTimeout() int {
	if s.RequestTimeoutSeconds != nil {
		return *s.RequestTimeoutSeconds
	}
	return 600
}

// StreamIdleTimeout returns the stream idle timeout in seconds. The default is 0
// (disabled): streams are bounded only by the client connection lifetime and
// never aborted on idle, so a slow or intermittently-stalled upstream is never
// cut. Set stream_idle_timeout_seconds > 0 to opt in to aborting a stream that
// receives no bytes for that long.
func (s ServerConfig) StreamIdleTimeout() int {
	if s.StreamIdleTimeoutSeconds != nil {
		return *s.StreamIdleTimeoutSeconds
	}
	return 0
}

// ProxyConfig controls routing, failover, and statistics behavior.
type ProxyConfig struct {
	MaxConsecutiveFailures   *int  `yaml:"max_consecutive_failures"     json:"max_consecutive_failures"`
	FailureResetSeconds      *int  `yaml:"failure_reset_seconds"        json:"failure_reset_seconds"`
	RewriteResponseModel     *bool `yaml:"rewrite_response_model"       json:"rewrite_response_model"`
	UsageStatisticsEnabled   *bool `yaml:"usage_statistics_enabled"     json:"usage_statistics_enabled"`
	UpstreamRetryStatusCodes []int `yaml:"upstream_retry_status_codes"  json:"upstream_retry_status_codes"`
	CallLogMaxEntries        *int  `yaml:"call_log_max_entries"          json:"call_log_max_entries"`
}

func (p ProxyConfig) MaxFailures() int {
	if p.MaxConsecutiveFailures != nil {
		return *p.MaxConsecutiveFailures
	}
	return 2
}
func (p ProxyConfig) FailureReset() int {
	if p.FailureResetSeconds != nil {
		return *p.FailureResetSeconds
	}
	return 300
}
func (p ProxyConfig) RewriteModel() bool {
	if p.RewriteResponseModel != nil {
		return *p.RewriteResponseModel
	}
	return true
}
func (p ProxyConfig) UsageEnabled() bool {
	if p.UsageStatisticsEnabled != nil {
		return *p.UsageStatisticsEnabled
	}
	return true
}
func (p ProxyConfig) RetryCodes() []int {
	if p.UpstreamRetryStatusCodes != nil {
		return p.UpstreamRetryStatusCodes
	}
	return []int{408, 429, 500, 502, 503, 504}
}

// CallLogMax returns the call-log ring buffer capacity. 0 disables call logging.
// The capacity is fixed at startup (restart to change it); reload does not
// resize an already-allocated buffer.
func (p ProxyConfig) CallLogMax() int {
	if p.CallLogMaxEntries != nil {
		return *p.CallLogMaxEntries
	}
	return 1000
}

// ManagementConfig controls the management API.
type ManagementConfig struct {
	Enabled  *bool  `yaml:"enabled"    json:"enabled"`
	BasePath string `yaml:"base_path"  json:"base_path"`
	AdminKey string `yaml:"admin_key"  json:"admin_key"` // expanded at load
}

func (m ManagementConfig) IsEnabled() bool {
	if m.Enabled != nil {
		return *m.Enabled
	}
	return true
}
func (m ManagementConfig) Base() string {
	if m.BasePath != "" {
		return m.BasePath
	}
	return "/v0/management"
}

// PayloadConfig holds the five ordered outbound-payload rule lists (§11).
// YAML keys for the raw phases use hyphens.
type PayloadConfig struct {
	Default     []PayloadRule       `yaml:"default"      json:"default"`
	DefaultRaw  []PayloadRule       `yaml:"default-raw"  json:"default-raw"`
	Override    []PayloadRule       `yaml:"override"     json:"override"`
	OverrideRaw []PayloadRule       `yaml:"override-raw" json:"override-raw"`
	Filter      []PayloadFilterRule `yaml:"filter"       json:"filter"`
}

// PayloadRule is a default/default-raw/override/override-raw rule. For the raw
// phases, Params values are JSON-fragment strings validated at load time.
type PayloadRule struct {
	Models []PayloadModelRule     `yaml:"models" json:"models"`
	Params map[string]interface{} `yaml:"params" json:"params"`
}

// PayloadFilterRule deletes JSON paths from the outbound payload.
type PayloadFilterRule struct {
	Models []PayloadModelRule `yaml:"models" json:"models"`
	Params []string           `yaml:"params" json:"params"`
}

// PayloadModelRule narrows a rule to matching requests. Match/NotMatch decode
// from YAML lists of single-key objects, e.g. `- "metadata.client": "codex"`.
type PayloadModelRule struct {
	Name         string                   `yaml:"name"          json:"name"`
	Protocol     string                   `yaml:"protocol"      json:"protocol"`
	FromProtocol string                   `yaml:"from-protocol" json:"from-protocol"`
	Headers      map[string]string        `yaml:"headers"       json:"headers"`
	Match        []map[string]interface{} `yaml:"match"         json:"match"`
	NotMatch     []map[string]interface{} `yaml:"not-match"     json:"not-match"`
	Exist        []string                 `yaml:"exist"         json:"exist"`
	NotExist     []string                 `yaml:"not-exist"     json:"not-exist"`
}

// Provider is one upstream provider. Name and URL are required; Name must not
// contain '/' (it forms the internal id "name/aliasA"). Each provider has exactly one key.
type Provider struct {
	Name    string            `yaml:"name"    json:"name"`
	Type    string            `yaml:"type"    json:"type"`
	URL     string            `yaml:"url"     json:"url"`
	Key     string            `yaml:"key"     json:"key"` // expanded at load
	Headers map[string]string `yaml:"headers" json:"headers"`
	Models  []ProviderModel   `yaml:"models"  json:"models"`
}

// ProviderModel is one upstream model under a provider.
type ProviderModel struct {
	Model                     string            `yaml:"model"                       json:"model"`
	AliasA                    string            `yaml:"aliasA"                      json:"aliasA"`
	AnthropicWebSearchForward *WebSearchForward `yaml:"anthropic_web_search_forward" json:"anthropic_web_search_forward"`
}

// WebSearchForward reroutes an Anthropic web_search request to a target model.
type WebSearchForward struct {
	Enabled     bool   `yaml:"enabled"      json:"enabled"`
	TargetModel string `yaml:"target_model" json:"target_model"` // internal model id providerName/aliasA
}

// ClientApiKey is one inbound API key with its protocol and model authorization.
type ClientApiKey struct {
	Name             string        `yaml:"name"              json:"name"`
	Key              string        `yaml:"key"               json:"key"` // expanded at load
	AllowedProtocols []string      `yaml:"allowed_protocols" json:"allowed_protocols"`
	Models           []ClientModel `yaml:"models"            json:"models"`
}

// ClientModel binds a client-visible aliasB to an internal model id.
type ClientModel struct {
	Model    string `yaml:"model"    json:"model"` // internal model id providerName/aliasA
	AliasB   string `yaml:"aliasB"   json:"aliasB"`
	Priority int    `yaml:"priority" json:"priority"`
}
