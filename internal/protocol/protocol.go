// Package protocol defines the three wire protocols this proxy speaks, both as
// the client-facing interface and as the upstream provider type. The enum is the
// same set for both (DEVELOPMENT.md §3): anthropic, openai_completion, codex.
package protocol

const (
	Anthropic         = "anthropic"
	OpenAICompletion  = "openai_completion"
	Codex             = "codex"
)

// All is the full ordered enum.
var All = []string{Anthropic, OpenAICompletion, Codex}

// IsValid reports whether p is one of the three supported protocols.
func IsValid(p string) bool {
	switch p {
	case Anthropic, OpenAICompletion, Codex:
		return true
	}
	return false
}
