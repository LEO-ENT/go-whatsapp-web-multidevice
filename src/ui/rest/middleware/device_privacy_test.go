package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/routepath"
	"github.com/gofiber/fiber/v3"
	"github.com/sirupsen/logrus"
)

func TestOpaqueDeviceMiddlewareRedactsUnknownIdentifier(t *testing.T) {
	const suppliedDevice = "628199999999:77@s.whatsapp.net?token=private-device"
	var handlerCalls int
	app := fiber.New()
	app.Use(OpaqueDeviceMiddleware(whatsapp.NewDeviceManager(nil, nil, nil)))
	app.Post("/private", func(c fiber.Ctx) error {
		handlerCalls++
		return c.SendStatus(http.StatusOK)
	})

	var logs bytes.Buffer
	oldOutput := logrus.StandardLogger().Out
	oldFormatter := logrus.StandardLogger().Formatter
	logrus.SetOutput(&logs)
	logrus.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true})
	defer func() {
		logrus.SetOutput(oldOutput)
		logrus.SetFormatter(oldFormatter)
	}()

	request := httptest.NewRequest(http.MethodPost, "/private", nil)
	request.Header.Set(DeviceIDHeader, suppliedDevice)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.StatusCode != http.StatusNotFound || handlerCalls != 0 {
		t.Fatalf("status/calls = %d/%d, want 404/0", response.StatusCode, handlerCalls)
	}
	for surface, value := range map[string]string{"body": string(body), "logs": logs.String()} {
		for _, sensitive := range []string{suppliedDevice, "628199999999", "private-device"} {
			if strings.Contains(value, sensitive) {
				t.Fatalf("%s exposed %q: %s", surface, sensitive, value)
			}
		}
	}
}

func TestProviderPathOpacityIsRequestScoped(t *testing.T) {
	oldBasePath := config.AppBasePath
	config.AppBasePath = ""
	defer func() { config.AppBasePath = oldBasePath }()

	const suppliedDevice = "missing-private-device"
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals(routepath.ProviderLookupAuthenticatedLocal, true)
		return c.Next()
	})
	app.Use(DeviceMiddleware(whatsapp.NewDeviceManager(nil, nil, nil)))
	app.Post(routepath.ProviderLookupPath, func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})
	app.Post("/reflective-compatibility", func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	request := func(path string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set(DeviceIDHeader, suppliedDevice)
		response, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test(%q): %v", path, err)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read %q response: %v", path, err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("status(%q) = %d, want 404", path, response.StatusCode)
		}
		return string(body)
	}

	if body := request(routepath.ProviderLookupPath); strings.Contains(body, suppliedDevice) {
		t.Fatalf("provider response reflected identifier: %s", body)
	}
	if body := request("/reflective-compatibility"); !strings.Contains(body, suppliedDevice) {
		t.Fatalf("provider request leaked opacity into compatibility route: %s", body)
	}
}
