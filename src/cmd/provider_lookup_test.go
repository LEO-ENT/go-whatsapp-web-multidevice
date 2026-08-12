package cmd

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/sirupsen/logrus"
)

type scopedProviderLookupStub struct {
	called   bool
	deviceID string
}

func TestProviderLookupRouteFailsClosedWithoutConfiguredAccounts(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	dm.AddDevice(whatsapp.NewDeviceInstance("device-a", nil, nil))
	stub := &scopedProviderLookupStub{}
	app := fiber.New()
	registerProviderLookupRoutes(app, nil, dm, stub)

	newRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/provider/messages/lookup", strings.NewReader(`{"provider_message_id":"`+providerID+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(middleware.DeviceIDHeader, "device-a")
		return request
	}

	for _, tc := range []struct {
		name      string
		authorize func(*http.Request)
	}{
		{name: "missing authorization"},
		{name: "unconfigured credential has no fallback", authorize: func(request *http.Request) {
			request.SetBasicAuth("attacker", "guessed-secret")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := newRequest()
			if tc.authorize != nil {
				tc.authorize(request)
			}
			resp, err := app.Test(request)
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != http.StatusUnauthorized || stub.called {
				t.Fatalf("status/called = %d/%t, want 401/false", resp.StatusCode, stub.called)
			}
		})
	}
}

func TestProviderLookupUnknownDeviceResponseAndLogsAreOpaque(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	const suppliedDevice = "628199999999:77@s.whatsapp.net?token=private-device"
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	stub := &scopedProviderLookupStub{}
	app := fiber.New()
	registerProviderLookupRoutes(app, map[string]string{"user": "secret"}, dm, stub)

	var logs bytes.Buffer
	oldOutput := logrus.StandardLogger().Out
	oldFormatter := logrus.StandardLogger().Formatter
	logrus.SetOutput(&logs)
	logrus.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true})
	defer func() {
		logrus.SetOutput(oldOutput)
		logrus.SetFormatter(oldFormatter)
	}()

	request := httptest.NewRequest(http.MethodPost, "/provider/messages/lookup", strings.NewReader(`{"provider_message_id":"`+providerID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(middleware.DeviceIDHeader, suppliedDevice)
	request.SetBasicAuth("user", "secret")
	resp, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound || stub.called {
		t.Fatalf("status/called = %d/%t, want 404/false", resp.StatusCode, stub.called)
	}
	for surface, value := range map[string]string{"body": string(body), "logs": logs.String()} {
		for _, sensitive := range []string{suppliedDevice, "628199999999", "private-device", providerID} {
			if strings.Contains(value, sensitive) {
				t.Fatalf("%s exposed %q: %s", surface, sensitive, value)
			}
		}
	}
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
	registerProviderLookupRoutes(app, map[string]string{"user": "secret"}, dm, stub)

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

func TestProviderLookupProductionComposition(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	const suppliedDevice = "628199999999:77@s.whatsapp.net?token=private-device"

	newApp := func(accounts map[string]string) (*fiber.App, *scopedProviderLookupStub) {
		dm := whatsapp.NewDeviceManager(nil, nil, nil)
		dm.AddDevice(whatsapp.NewDeviceInstance("device-a", nil, nil))
		stub := &scopedProviderLookupStub{}
		app := fiber.New()
		if len(accounts) > 0 {
			app.Use(newBasicAuthMiddleware(accounts))
		}
		registerProviderAndDeviceScopedRoutes(app, accounts, dm, stub, func(router fiber.Router) {
			router.Get("/device-scoped", func(c fiber.Ctx) error {
				return c.SendStatus(http.StatusOK)
			})
		})
		// This reproduces the route helper used by restServer; it does not execute
		// the Cobra/configuration/DB composition root itself. The dashboard is
		// registered after that helper and remains public without global auth.
		app.Get("/", func(c fiber.Ctx) error { return c.SendStatus(http.StatusOK) })
		return app, stub
	}

	providerRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, rest.ProviderLookupPath, strings.NewReader(`{"provider_message_id":"`+providerID+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(middleware.DeviceIDHeader, suppliedDevice)
		return request
	}

	t.Run("anonymous lookup cannot probe a device and dashboard stays public", func(t *testing.T) {
		app, stub := newApp(nil)
		request := providerRequest()
		resp, err := app.Test(request)
		if err != nil {
			t.Fatalf("provider app.Test: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read provider response: %v", err)
		}
		if resp.StatusCode != http.StatusUnauthorized || stub.called {
			t.Fatalf("provider status/called = %d/%t, want 401/false", resp.StatusCode, stub.called)
		}
		if strings.Contains(string(body), suppliedDevice) || strings.Contains(string(body), "628199999999") {
			t.Fatalf("anonymous provider response exposed selected device: %s", body)
		}

		dashboard, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
		if err != nil {
			t.Fatalf("dashboard app.Test: %v", err)
		}
		if dashboard.StatusCode != http.StatusOK {
			t.Fatalf("dashboard status = %d, want 200", dashboard.StatusCode)
		}
	})

	t.Run("authenticated unknown device reaches the opaque boundary first", func(t *testing.T) {
		app, stub := newApp(map[string]string{"user": "secret"})
		request := providerRequest()
		request.SetBasicAuth("user", "secret")
		resp, err := app.Test(request)
		if err != nil {
			t.Fatalf("provider app.Test: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read provider response: %v", err)
		}
		if resp.StatusCode != http.StatusNotFound || stub.called {
			t.Fatalf("provider status/called = %d/%t, want 404/false", resp.StatusCode, stub.called)
		}
		if strings.Contains(string(body), suppliedDevice) || strings.Contains(string(body), "628199999999") {
			t.Fatalf("opaque provider response exposed selected device: %s", body)
		}

		dashboard, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
		if err != nil {
			t.Fatalf("dashboard app.Test: %v", err)
		}
		if dashboard.StatusCode != http.StatusUnauthorized {
			t.Fatalf("authenticated-mode dashboard status = %d, want 401", dashboard.StatusCode)
		}
	})
}

func TestProviderLookupResolvedPathVariantsRemainOpaque(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	const suppliedDevice = "628199999999:77@s.whatsapp.net?token=private-device"

	for _, basePath := range []string{"", "/api"} {
		t.Run(fmt.Sprintf("base=%q", basePath), func(t *testing.T) {
			matched := 0
			matchedPaths := make(map[string]bool)
			for _, path := range providerLookupPathVariants(basePath) {
				probe := fiber.New()
				var probeRouter fiber.Router = probe
				if basePath != "" {
					probeRouter = probe.Group(basePath)
				}
				probeRouter.Post(rest.ProviderLookupPath, func(c fiber.Ctx) error {
					return c.SendStatus(http.StatusNoContent)
				})
				probeResponse, err := probe.Test(httptest.NewRequest(http.MethodPost, path, nil))
				if err != nil {
					t.Fatalf("probe %q: %v", path, err)
				}
				if probeResponse.StatusCode != http.StatusNoContent {
					continue
				}
				matched++
				matchedPaths[path] = true

				for _, authenticated := range []bool{false, true} {
					t.Run(fmt.Sprintf("path=%q/auth=%t", path, authenticated), func(t *testing.T) {
						dm := whatsapp.NewDeviceManager(nil, nil, nil)
						stub := &scopedProviderLookupStub{}
						app := fiber.New()
						var router fiber.Router = app
						if basePath != "" {
							router = app.Group(basePath)
						}
						registerProviderAndDeviceScopedRoutes(
							router,
							map[string]string{"user": "secret"},
							dm,
							stub,
							func(deviceRouter fiber.Router) {
								deviceRouter.Post("/reflective-compatibility", func(c fiber.Ctx) error {
									return c.SendStatus(http.StatusOK)
								})
							},
						)

						request := httptest.NewRequest(
							http.MethodPost,
							path,
							strings.NewReader(`{"provider_message_id":"`+providerID+`"}`),
						)
						request.Header.Set("Content-Type", "application/json")
						request.Header.Set(middleware.DeviceIDHeader, suppliedDevice)
						wantStatus := http.StatusUnauthorized
						if authenticated {
							request.SetBasicAuth("user", "secret")
							wantStatus = http.StatusNotFound
						}
						response, err := app.Test(request)
						if err != nil {
							t.Fatalf("app.Test: %v", err)
						}
						body, err := io.ReadAll(response.Body)
						if err != nil {
							t.Fatalf("read response: %v", err)
						}
						if response.StatusCode != wantStatus || stub.called {
							t.Fatalf("status/called = %d/%t, want %d/false", response.StatusCode, stub.called, wantStatus)
						}
						for _, sensitive := range []string{suppliedDevice, "628199999999", "private-device", providerID} {
							if strings.Contains(string(body), sensitive) {
								t.Fatalf("resolved route exposed %q: %s", sensitive, body)
							}
						}
					})
				}
			}
			if matched < 4 {
				t.Fatalf("Fiber matched only %d adversarial variants", matched)
			}
			canonical := basePath + rest.ProviderLookupPath
			for _, required := range []string{strings.ToUpper(canonical), canonical + "/"} {
				if !matchedPaths[required] {
					t.Fatalf("Fiber did not exercise required M4/trailing variant %q", required)
				}
			}
		})
	}
}

func providerLookupPathVariants(basePath string) []string {
	canonical := basePath + rest.ProviderLookupPath
	seen := map[string]struct{}{}
	add := func(path string) {
		seen[path] = struct{}{}
	}
	add(canonical)
	add(strings.ToUpper(canonical))
	add(canonical + "/")
	add(strings.ToUpper(canonical) + "/")
	add(canonical + "//")
	add(strings.Replace(canonical, "/messages/", "/messages%2F", 1))
	add(strings.Replace(canonical, "provider", "%70rovider", 1))
	add(strings.Replace(canonical, "lookup", "look%75p", 1))
	add(strings.Replace(canonical, "/messages/", "/messages/%20/", 1))

	bytesPath := []byte(canonical)
	for index, character := range bytesPath {
		if character >= 'a' && character <= 'z' {
			variant := append([]byte(nil), bytesPath...)
			variant[index] = character - ('a' - 'A')
			add(string(variant))
		}
	}
	variants := make([]string, 0, len(seen))
	for path := range seen {
		variants = append(variants, path)
	}
	return variants
}
