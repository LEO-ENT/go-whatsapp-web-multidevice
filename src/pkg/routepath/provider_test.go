package routepath

import "testing"

func TestProviderLookupRouteMetadata(t *testing.T) {
	if ProviderLookupPath != "/provider/messages/lookup" {
		t.Fatalf("ProviderLookupPath = %q", ProviderLookupPath)
	}
	if ProviderLookupBoundaryLocal == "" {
		t.Fatal("provider lookup route metadata must be non-empty")
	}
}
