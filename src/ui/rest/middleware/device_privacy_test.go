package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestProviderLookupBoundaryMarkerIsRouteScoped(t *testing.T) {
	app := fiber.New()
	app.Post(routepath.ProviderLookupPath, ProviderLookupBoundary(), func(c fiber.Ctx) error {
		marked, _ := c.Locals(routepath.ProviderLookupBoundaryLocal).(bool)
		if !marked {
			return c.SendStatus(http.StatusInternalServerError)
		}
		return c.SendStatus(http.StatusNoContent)
	})
	app.Post("/reflective-compatibility", func(c fiber.Ctx) error {
		if marked, _ := c.Locals(routepath.ProviderLookupBoundaryLocal).(bool); marked {
			return c.SendStatus(http.StatusInternalServerError)
		}
		return c.SendStatus(http.StatusNoContent)
	})

	request := func(path string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		response, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test(%q): %v", path, err)
		}
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("status(%q) = %d, want 204", path, response.StatusCode)
		}
	}

	request(strings.ToUpper(routepath.ProviderLookupPath))
	request("/reflective-compatibility")
}
