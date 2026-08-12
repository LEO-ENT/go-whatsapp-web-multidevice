package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
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
