package indexes

import "github.com/GreenTeodoro839/SimpleAPI/internal/config"

// ParseInternalModelID splits an internal model id on the FIRST '_'. ok is false
// when the id has no '_', or when either side is empty. Re-exported from config
// for callers that import indexes.
func ParseInternalModelID(id string) (provider, aliasA string, ok bool) {
	return config.ParseInternalModelID(id)
}
