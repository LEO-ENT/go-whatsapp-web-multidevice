package whatsapp

import (
	"strings"
	"testing"
)

func TestResolveDeviceUnknownErrorIsOpaque(t *testing.T) {
	const suppliedDevice = "628199999999:77@s.whatsapp.net?token=private-device"
	_, resolvedID, err := NewDeviceManager(nil, nil, nil).ResolveDevice(suppliedDevice)
	if err == nil || resolvedID != suppliedDevice {
		t.Fatalf("resolved/error = %q/%v", resolvedID, err)
	}
	for _, sensitive := range []string{suppliedDevice, "628199999999", "private-device"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error exposed %q: %v", sensitive, err)
		}
	}
}
