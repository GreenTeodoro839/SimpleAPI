package config

import (
	"os"
	"regexp"
	"strings"
)

// varPattern matches ${VAR} or ${VAR:-default}. Default values must not contain '}'.
var varPattern = regexp.MustCompile(`\$\{([^}]*)\}`)

// ExpandEnv expands ${VAR} and ${VAR:-default} placeholders in s.
//
//   - ${VAR}              → the value of VAR (empty string when unset).
//   - ${VAR:-default}     → default when VAR is unset OR empty; otherwise the value.
//
// Missing variables without a default expand to an empty string; downstream
// validation may then flag the resulting empty key. Expansion is idempotent: the
// result contains no '${' sequences.
func ExpandEnv(s string) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		// match is "${...}"; strip the braces.
		inner := match[2 : len(match)-1]
		name := inner
		def := ""
		hasDefault := false
		if i := strings.Index(inner, ":-"); i >= 0 {
			name = inner[:i]
			def = inner[i+2:]
			hasDefault = true
		}
		val, ok := os.LookupEnv(name)
		if !ok || val == "" {
			if hasDefault {
				return def
			}
			return val // unset/empty and no default → ""
		}
		return val
	})
}
