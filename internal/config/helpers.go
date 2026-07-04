package config

import "strings"

// EffectiveAliasA returns the internal alias for a provider model: aliasA if
// set, otherwise the upstream model name (DEVELOPMENT.md §3).
func EffectiveAliasA(pm ProviderModel) string {
	if pm.AliasA != "" {
		return pm.AliasA
	}
	return pm.Model
}

// ModelInternalID returns the internal model id "providerName/effectiveAliasA".
// Provider names are validated to contain no '/', so the aliasA is preserved
// even when it itself contains '/'.
func ModelInternalID(providerName string, pm ProviderModel) string {
	return providerName + "/" + EffectiveAliasA(pm)
}

// ParseInternalModelID splits an internal model id on the FIRST '/'. ok is false
// when the id has no '/', or when either side is empty.
func ParseInternalModelID(id string) (provider, aliasA string, ok bool) {
	i := strings.IndexByte(id, '/')
	if i <= 0 || i == len(id)-1 {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}
