package routepath

import "testing"

func TestIsProviderLookup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		basePath string
		want     bool
	}{
		{name: "root", path: ProviderLookupPath, want: true},
		{name: "base path", path: "/api" + ProviderLookupPath, basePath: "/api", want: true},
		{name: "trailing slash", path: ProviderLookupPath + "/", want: true},
		{name: "alias", path: "/provider/lookup", want: false},
		{name: "prefix collision", path: ProviderLookupPath + "/extra", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProviderLookup(tc.path, tc.basePath); got != tc.want {
				t.Fatalf("IsProviderLookup(%q, %q) = %t, want %t", tc.path, tc.basePath, got, tc.want)
			}
		})
	}
}
