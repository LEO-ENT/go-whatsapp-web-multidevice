package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainProvider "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/provider"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

type scopedProviderLookupStub struct {
	called   bool
	deviceID string
}

func (s *scopedProviderLookupStub) LookupMessage(ctx context.Context, _ string) (domainProvider.MessageLookup, error) {
	s.called = true
	if instance, ok := whatsapp.DeviceFromContext(ctx); ok && instance != nil {
		s.deviceID = instance.ID()
	}
	return domainProvider.MessageLookup{Status: domainProvider.MessageUnknown}, nil
}

func TestProviderLookupRouteRequiresAuthenticationAndDeviceOwnership(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	dm.AddDevice(whatsapp.NewDeviceInstance("device-a", nil, nil))
	stub := &scopedProviderLookupStub{}
	app := fiber.New()
	app.Use(newBasicAuthMiddleware(map[string]string{"user": "secret"}))
	scoped := app.Group("", middleware.DeviceMiddleware(dm))
	rest.InitRestProvider(scoped, stub)

	newRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/provider/messages/lookup", strings.NewReader(`{"provider_message_id":"`+providerID+`"}`))
		request.Header.Set("Content-Type", "application/json")
		return request
	}

	unauthenticated := newRequest()
	resp, err := app.Test(unauthenticated)
	if err != nil {
		t.Fatalf("unauthenticated app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized || stub.called {
		t.Fatalf("unauthenticated status/called = %d/%t", resp.StatusCode, stub.called)
	}

	unknownDevice := newRequest()
	unknownDevice.SetBasicAuth("user", "secret")
	unknownDevice.Header.Set(middleware.DeviceIDHeader, "device-b")
	resp, err = app.Test(unknownDevice)
	if err != nil {
		t.Fatalf("unknown-device app.Test: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read unknown-device response: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound || stub.called {
		t.Fatalf("unknown-device status/called = %d/%t", resp.StatusCode, stub.called)
	}
	if strings.Contains(string(body), providerID) {
		t.Fatalf("unknown-device response exposed provider ID: %s", body)
	}

	authorized := newRequest()
	authorized.SetBasicAuth("user", "secret")
	authorized.Header.Set(middleware.DeviceIDHeader, "device-a")
	resp, err = app.Test(authorized)
	if err != nil {
		t.Fatalf("authorized app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !stub.called || stub.deviceID != "device-a" {
		t.Fatalf("authorized status/called/device = %d/%t/%q", resp.StatusCode, stub.called, stub.deviceID)
	}
}
