package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		s       string
		want    bool
	}{
		// suffix wildcard: "deepseek-*" = "deepseek-" + "*" + ""
		{"deepseek-*", "deepseek-chat", true},
		{"deepseek-*", "deepseek-", true}, // '*' matches empty
		{"deepseek-*", "deepseek", false}, // missing the literal '-'
		{"deepseek-*", "anthropic-opus", false},
		// prefix wildcard: "*-chat" = "*" + "-chat"
		{"*-chat", "deepseek-chat", true},
		{"*-chat", "claude-chat", true},
		{"*-chat", "deepseek-v4", false},
		// middle wildcard: two '*' with a literal between -> "deep*v4*flash"
		{"deep*v4*flash", "deepXv4Yflash", true},
		{"deep*v4*flash", "deepv4flash", true}, // both '*' match empty around v4
		{"deep*v4*flash", "deep-v99-pro", false},
		// lone '*' catch-all
		{"*", "", true},
		{"*", "anything-at-all", true},
		// exact (no wildcard)
		{"claude", "claude", true},
		{"claude", "claude-2", false},
		// empty pattern matches nothing
		{"", "anything", false},
		// two wildcards around a literal: "*-*" matches anything containing '-'
		{"*-*", "a-b", true},
		{"*-*", "ab", false},
	}
	for _, c := range cases {
		got := Match(c.pattern, c.s)
		if got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}
