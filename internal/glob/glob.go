// Package glob provides trivial '*' wildcard matching for aliasB patterns and
// payload rule predicates. '*' matches any (possibly empty) character sequence;
// patterns may carry the wildcard at the start, middle, end, or be a lone '*'.
package glob

import "strings"

// Match reports whether s matches pattern, where '*' in pattern matches any
// (possibly empty) run of characters. An empty pattern matches nothing.
func Match(pattern, s string) bool {
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		idx := strings.Index(s, p)
		if idx < 0 {
			return false
		}
		s = s[idx+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}
