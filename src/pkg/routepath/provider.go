package routepath

import "strings"

const (
	ProviderLookupPath               = "/provider/messages/lookup"
	ProviderLookupAuthenticatedLocal = "gowa.provider.lookup.authenticated"
)

// IsProviderLookup reports whether a request path is the canonical provider
// reconciliation boundary under the configured application base path.
func IsProviderLookup(path string, basePath string) bool {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	basePath = strings.TrimRight(strings.TrimSpace(basePath), "/")
	return path == basePath+ProviderLookupPath
}
