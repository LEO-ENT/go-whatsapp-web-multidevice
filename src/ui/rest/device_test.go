package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainDevice "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

// addDeviceStubUsecase implements domainDevice.IDeviceUsecase by embedding the
// interface while recording the arguments actually received by AddDevice.
type addDeviceStubUsecase struct {
	domainDevice.IDeviceUsecase
	receivedDeviceID string
	receivedWebhook  *chatstorage.DeviceWebhookConfig
}

func (s *addDeviceStubUsecase) AddDevice(_ context.Context, deviceID string, webhook *chatstorage.DeviceWebhookConfig) (*domainDevice.Device, error) {
	s.receivedDeviceID = deviceID
	s.receivedWebhook = webhook
	return &domainDevice.Device{ID: deviceID}, nil
}

func newAddDeviceTestApp(stub *addDeviceStubUsecase) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	controller := Device{Service: stub}
	app.Post("/devices", controller.AddDevice)
	return app
}

// TestAddDevice_ForwardsFullWebhookConfig verifies that POST /devices accepts the
// complete webhook configuration (url, secret, events, insecure_skip_verify) that the
// device manager UI sends, instead of silently dropping everything but webhook_url.
func TestAddDevice_ForwardsFullWebhookConfig(t *testing.T) {
	stub := &addDeviceStubUsecase{}
	app := newAddDeviceTestApp(stub)

	body := `{
		"device_id": "dev1",
		"webhook_url": "https://hook.example.com",
		"webhook_secret": "s3cret",
		"webhook_events": "message,message.ack",
		"webhook_insecure_skip_verify": true
	}`
	req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	if stub.receivedDeviceID != "dev1" {
		t.Fatalf("expected device_id dev1, got %q", stub.receivedDeviceID)
	}
	cfg := stub.receivedWebhook
	if cfg == nil {
		t.Fatal("expected webhook config to be forwarded to the usecase, got nil")
	}
	if cfg.WebhookURL == nil || *cfg.WebhookURL != "https://hook.example.com" {
		t.Fatalf("expected webhook_url to be forwarded, got %v", cfg.WebhookURL)
	}
	if cfg.WebhookSecret != "s3cret" {
		t.Fatalf("expected webhook_secret to be forwarded, got %q", cfg.WebhookSecret)
	}
	if cfg.WebhookEvents != "message,message.ack" {
		t.Fatalf("expected webhook_events to be forwarded, got %q", cfg.WebhookEvents)
	}
	if !cfg.WebhookInsecureSkipVerify {
		t.Fatal("expected webhook_insecure_skip_verify to be forwarded as true")
	}

	var parsed struct {
		Results map[string]any `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if _, exposed := parsed.Results["webhook_secret"]; exposed {
		t.Fatal("POST /devices must not expose webhook_secret")
	}
	if configured, ok := parsed.Results["webhook_secret_configured"].(bool); !ok || !configured {
		t.Fatalf("expected webhook_secret_configured=true, got %v", parsed.Results["webhook_secret_configured"])
	}
}

type webhookDeviceStubUsecase struct {
	domainDevice.IDeviceUsecase
	config         *chatstorage.DeviceWebhookConfig
	receivedConfig *chatstorage.DeviceWebhookConfig
}

func (s *webhookDeviceStubUsecase) GetDeviceWebhookConfig(context.Context, string) (*chatstorage.DeviceWebhookConfig, error) {
	return s.config, nil
}

func (s *webhookDeviceStubUsecase) SetDeviceWebhookConfig(_ context.Context, _ string, config *chatstorage.DeviceWebhookConfig) error {
	s.receivedConfig = config
	return nil
}

func TestGetDeviceWebhook_DoesNotExposeSecret(t *testing.T) {
	webhookURL := "https://hook.example.com"
	secret := "never-return-this-secret"
	stub := &webhookDeviceStubUsecase{config: &chatstorage.DeviceWebhookConfig{
		WebhookURL:    &webhookURL,
		WebhookSecret: secret,
		WebhookEvents: "message",
	}}
	app := fiber.New()
	controller := Device{Service: stub}
	app.Get("/devices/:device_id/webhook", controller.GetDeviceWebhook)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/devices/device-a/webhook", nil))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	var parsed struct {
		Results map[string]any `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if _, exposed := parsed.Results["webhook_secret"]; exposed {
		t.Fatal("GET device webhook must not expose webhook_secret")
	}
	if configured, ok := parsed.Results["webhook_secret_configured"].(bool); !ok || !configured {
		t.Fatalf("expected webhook_secret_configured=true, got %v", parsed.Results["webhook_secret_configured"])
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("encode parsed response: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("GET response leaked webhook secret: %s", encoded)
	}
}

func TestUpdateDeviceWebhook_DoesNotEchoSecret(t *testing.T) {
	secret := "write-only-webhook-secret"
	stub := &webhookDeviceStubUsecase{}
	app := fiber.New()
	controller := Device{Service: stub}
	app.Patch("/devices/:device_id/webhook", controller.UpdateDeviceWebhook)

	body := `{"webhook_url":"https://hook.example.com","webhook_secret":"` + secret + `","webhook_events":"message"}`
	req := httptest.NewRequest(http.MethodPatch, "/devices/device-a/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if stub.receivedConfig == nil || stub.receivedConfig.WebhookSecret != secret {
		t.Fatal("PATCH must still pass the write-only secret to the usecase")
	}

	var parsed struct {
		Results map[string]any `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if _, exposed := parsed.Results["webhook_secret"]; exposed {
		t.Fatal("PATCH device webhook must not echo webhook_secret")
	}
	if configured, ok := parsed.Results["webhook_secret_configured"].(bool); !ok || !configured {
		t.Fatalf("expected webhook_secret_configured=true, got %v", parsed.Results["webhook_secret_configured"])
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("encode parsed response: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("PATCH response leaked webhook secret: %s", encoded)
	}
}

func TestDeviceWebhookConfigJSONOmitsSecret(t *testing.T) {
	secret := "domain-secret-marker"
	encoded, err := json.Marshal(chatstorage.DeviceWebhookConfig{WebhookSecret: secret})
	if err != nil {
		t.Fatalf("marshal webhook config: %v", err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "webhook_secret") {
		t.Fatalf("domain webhook config serialized a write-only secret: %s", encoded)
	}
}

// TestAddDevice_NoWebhookFields verifies that a plain device creation without any
// webhook fields passes a nil config to the usecase.
func TestAddDevice_NoWebhookFields(t *testing.T) {
	stub := &addDeviceStubUsecase{}
	app := newAddDeviceTestApp(stub)

	req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(`{"device_id":"dev2"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	if stub.receivedWebhook != nil {
		t.Fatalf("expected nil webhook config when no webhook fields sent, got %+v", stub.receivedWebhook)
	}

	var parsed struct {
		Results map[string]any `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if parsed.Results["id"] != "dev2" {
		t.Fatalf("expected result id dev2, got %v", parsed.Results["id"])
	}
}
