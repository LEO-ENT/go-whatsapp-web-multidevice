package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/chatwoot"
	"github.com/sirupsen/logrus"
)

type chatwootForwardQueueTestRepo struct {
	chatstorage.IChatStorageRepository
	events []*chatstorage.ChatwootForwardEvent
}

func (r *chatwootForwardQueueTestRepo) EnqueueChatwootForwardEvent(event *chatstorage.ChatwootForwardEvent) error {
	cloned := *event
	r.events = append(r.events, &cloned)
	return nil
}

func TestForwardPayloadToConfiguredWebhooks_NoWebhooksConfigured(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"foo": "bar"}

	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = nil
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalSubmit := submitWebhookFn
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		t.Fatal("submitWebhookFn should not be invoked when no webhooks are configured")
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "test"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestForwardPayloadToConfiguredWebhooks_PartialFailure(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"foo": "bar"}

	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://success", "https://fail", "https://success2"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalSubmit := submitWebhookFn
	var attempts []string
	submitWebhookFn = func(_ context.Context, _ map[string]any, url string, _ *chatstorage.DeviceWebhookConfig) error {
		attempts = append(attempts, url)
		if strings.Contains(url, "fail") {
			return errors.New("boom")
		}
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "test"); err != nil {
		t.Fatalf("expected partial failure to return nil, got %v", err)
	}

	if len(attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(attempts))
	}
}

func TestForwardPayloadToConfiguredWebhooks_AllFail(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"foo": "bar"}

	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://fail1", "https://fail2"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalSubmit := submitWebhookFn
	submitWebhookFn = func(_ context.Context, _ map[string]any, url string, _ *chatstorage.DeviceWebhookConfig) error {
		return errors.New("failure for " + url)
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "test"); err == nil {
		t.Fatalf("expected error when all webhooks fail")
	}
}

func TestForwardPayloadToConfiguredWebhooks_EventWhitelist_FilteredOut(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"foo": "bar"}

	originalWebhooks := config.WhatsappWebhook
	originalEvents := config.WhatsappWebhookEvents
	config.WhatsappWebhook = []string{"https://test.com"}
	config.WhatsappWebhookEvents = []string{"message"}
	defer func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookEvents = originalEvents
	}()

	called := false
	originalSubmit := submitWebhookFn
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		called = true
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "message.ack"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if called {
		t.Fatal("message.ack should be filtered by whitelist when only 'message' is allowed")
	}
}

func TestForwardPayloadToConfiguredWebhooks_EventWhitelist_Allowed(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"foo": "bar"}

	originalWebhooks := config.WhatsappWebhook
	originalEvents := config.WhatsappWebhookEvents
	config.WhatsappWebhook = []string{"https://test.com"}
	config.WhatsappWebhookEvents = []string{"message", "message.ack"}
	defer func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookEvents = originalEvents
	}()

	called := false
	originalSubmit := submitWebhookFn
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		called = true
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "message"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Fatal("message should be forwarded when in whitelist")
	}
}

func TestForwardPayloadToConfiguredWebhooks_EmptyWhitelist_AllowsAll(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"foo": "bar"}

	originalWebhooks := config.WhatsappWebhook
	originalEvents := config.WhatsappWebhookEvents
	config.WhatsappWebhook = []string{"https://test.com"}
	config.WhatsappWebhookEvents = []string{}
	defer func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookEvents = originalEvents
	}()

	called := false
	originalSubmit := submitWebhookFn
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		called = true
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "any.event"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Fatal("any event should be forwarded when whitelist is empty")
	}
}

func TestForwardPayloadToConfiguredWebhooks_WhitelistCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"foo": "bar"}

	originalWebhooks := config.WhatsappWebhook
	originalEvents := config.WhatsappWebhookEvents
	config.WhatsappWebhook = []string{"https://test.com"}
	config.WhatsappWebhookEvents = []string{"MESSAGE", "Message.Ack"}
	defer func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookEvents = originalEvents
	}()

	called := 0
	originalSubmit := submitWebhookFn
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		called++
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "message"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "message.ack"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if called != 2 {
		t.Fatalf("expected 2 calls (case-insensitive match), got %d", called)
	}
}

func TestEnqueueChatwootForwardRetryStoresTransientMessageFailure(t *testing.T) {
	repo := &chatwootForwardQueueTestRepo{}
	payload := map[string]any{
		"payload": map[string]any{
			"id":      "wa-queue-1",
			"chat_id": "628123456789@s.whatsapp.net",
		},
	}

	queued := enqueueChatwootForwardRetry(repo, "device-a@s.whatsapp.net", "message", payload, &chatwoot.HTTPStatusError{
		StatusCode: http.StatusInternalServerError,
		Op:         "create message",
		Body:       "unavailable",
	})
	if !queued {
		t.Fatal("expected transient Chatwoot failure to be queued")
	}
	if len(repo.events) != 1 {
		t.Fatalf("queued events = %d, want 1", len(repo.events))
	}
	event := repo.events[0]
	if event.DeviceID != "device-a@s.whatsapp.net" || event.EventName != "message" || event.WhatsAppMessageID != "wa-queue-1" {
		t.Fatalf("unexpected queued event: %+v", event)
	}
	if !strings.Contains(event.PayloadJSON, "wa-queue-1") || event.NextAttemptAt.IsZero() {
		t.Fatalf("queued event missing payload/next attempt: %+v", event)
	}
}

func TestEnqueueChatwootForwardRetrySkipsPermanentFailure(t *testing.T) {
	repo := &chatwootForwardQueueTestRepo{}
	payload := map[string]any{
		"payload": map[string]any{"id": "wa-permanent"},
	}

	queued := enqueueChatwootForwardRetry(repo, "device-a@s.whatsapp.net", "message", payload, &chatwoot.HTTPStatusError{
		StatusCode: http.StatusBadRequest,
		Op:         "create message",
		Body:       "bad payload",
	})
	if queued {
		t.Fatal("permanent Chatwoot failure should not be queued")
	}
	if len(repo.events) != 0 {
		t.Fatalf("queued events = %d, want 0", len(repo.events))
	}
}

// retryWorkerTestRepo records which terminal action the retry worker takes for a
// due event so a test can distinguish "rescheduled" from "marked done/deleted".
type retryWorkerTestRepo struct {
	chatstorage.IChatStorageRepository
	due       []*chatstorage.ChatwootForwardEvent
	failedIDs []int64
	doneIDs   []int64
}

func (r *retryWorkerTestRepo) ListDueChatwootForwardEvents(_ time.Time, _ int) ([]*chatstorage.ChatwootForwardEvent, error) {
	return r.due, nil
}

func (r *retryWorkerTestRepo) MarkChatwootForwardEventFailed(id int64, _ string, _ time.Time) error {
	r.failedIDs = append(r.failedIDs, id)
	return nil
}

func (r *retryWorkerTestRepo) MarkChatwootForwardEventDone(id int64) error {
	r.doneIDs = append(r.doneIDs, id)
	return nil
}

func TestProcessDueChatwootForwardRetriesReschedulesOnRegistryUnavailable(t *testing.T) {
	// Reproduces the P1 data-loss bug: when the registry is uninitialized, a due
	// retry must be rescheduled, NOT marked done (which deletes it without ever
	// delivering the message).
	orig := getChatwootClientFn
	t.Cleanup(func() { getChatwootClientFn = orig })
	getChatwootClientFn = func(string) (*chatwoot.ResolvedConfig, error) {
		return nil, chatwoot.ErrClientRegistryUnavailable
	}

	repo := &retryWorkerTestRepo{
		due: []*chatstorage.ChatwootForwardEvent{
			{ID: 7, DeviceID: "dev", EventName: "message", WhatsAppMessageID: "wa-1", PayloadJSON: `{"payload":{"id":"wa-1"}}`},
		},
	}

	processDueChatwootForwardRetries(repo)

	if len(repo.doneIDs) != 0 {
		t.Fatalf("retry job must not be marked done on nil registry, got done=%v", repo.doneIDs)
	}
	if len(repo.failedIDs) != 1 || repo.failedIDs[0] != 7 {
		t.Fatalf("retry job should be rescheduled, got failed=%v", repo.failedIDs)
	}
}

func TestEnqueueChatwootForwardRetrySkipsRegistryUnavailable(t *testing.T) {
	// An uninitialized registry is a wiring/startup condition, not a transient
	// network failure. The live path must NOT enqueue a retry that would only
	// fail the same way (and, in MCP-only deployments, accumulate forever).
	repo := &chatwootForwardQueueTestRepo{}
	payload := map[string]any{
		"payload": map[string]any{"id": "wa-no-registry"},
	}

	queued := enqueueChatwootForwardRetry(repo, "device-a@s.whatsapp.net", "message", payload, chatwoot.ErrClientRegistryUnavailable)
	if queued {
		t.Fatal("registry-unavailable failure should not be queued")
	}
	if len(repo.events) != 0 {
		t.Fatalf("queued events = %d, want 0", len(repo.events))
	}
}

func TestExtractStructuredMessageContentWithContactPayload(t *testing.T) {
	payload := map[string]any{
		"contact": webhookContactPayload{
			DisplayName: "Alice",
			PhoneNumber: "+62 812 3456 7890",
		},
	}

	got := extractStructuredMessageContent(payload)
	want := "Contact: Alice (+62 812 3456 7890)"
	if got != want {
		t.Fatalf("extractStructuredMessageContent() = %q, want %q", got, want)
	}
}

func TestExtractStructuredMessageContentWithContactsArrayPayload(t *testing.T) {
	payload := map[string]any{
		"contacts_array": []webhookContactPayload{
			{
				DisplayName: "Alice",
				PhoneNumber: "+62 812 3456 7890",
			},
			{
				DisplayName: "Bob",
				PhoneNumber: "+62 813 9876 5432",
			},
		},
	}

	got := extractStructuredMessageContent(payload)
	want := "Contacts: Alice (+62 812 3456 7890)"
	if got != want {
		t.Fatalf("extractStructuredMessageContent() = %q, want %q", got, want)
	}
}

func TestGetWebhookConfigForDevice_NoDeviceID(t *testing.T) {
	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://global-webhook.com"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	config, err := getWebhookConfigForDevice("")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if config != nil {
		t.Fatalf("expected nil config for empty deviceID, got %v", config)
	}
}

func TestGetWebhookConfigForDevice_DeviceNotFound(t *testing.T) {
	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://global-webhook.com"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalStorageForTest := webhookStorageForTest
	webhookStorageForTest = func(deviceJID string) (*chatstorage.DeviceRecord, error) {
		return nil, nil // not found, no error
	}
	defer func() { webhookStorageForTest = originalStorageForTest }()

	config, err := getWebhookConfigForDevice("unknown-device-jid@s.whatsapp.net")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if config != nil {
		t.Fatalf("expected nil config when device not found, got %v", config)
	}
}

func TestGetWebhookConfigForDevice_FallbackToGlobal(t *testing.T) {
	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://global-webhook.com"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalStorageForTest := webhookStorageForTest
	emptyURL := ""
	webhookStorageForTest = func(deviceJID string) (*chatstorage.DeviceRecord, error) {
		return &chatstorage.DeviceRecord{
			DeviceID:   deviceJID,
			WebhookURL: &emptyURL,
		}, nil
	}
	defer func() { webhookStorageForTest = originalStorageForTest }()

	config, err := getWebhookConfigForDevice("6289600000000@s.whatsapp.net")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if config != nil {
		t.Fatalf("expected nil config when device has no webhook, got %v", config)
	}
}

func TestGetWebhookConfigForDevice_DeviceSpecificOverride(t *testing.T) {
	deviceWebhookURL := "https://device-specific-webhook.com"
	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://global-webhook.com"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalStorageForTest := webhookStorageForTest
	webhookStorageForTest = func(deviceJID string) (*chatstorage.DeviceRecord, error) {
		return &chatstorage.DeviceRecord{
			DeviceID:   deviceJID,
			WebhookURL: &deviceWebhookURL,
		}, nil
	}
	defer func() { webhookStorageForTest = originalStorageForTest }()

	config, err := getWebhookConfigForDevice("6289600000000@s.whatsapp.net")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if config == nil {
		t.Fatal("expected non-nil config when device has webhook")
	}
	if config.WebhookURL == nil || *config.WebhookURL != deviceWebhookURL {
		t.Fatalf("expected device-specific webhook %s, got %v", deviceWebhookURL, config.WebhookURL)
	}
}

func TestForwardPayloadToConfiguredWebhooks_WithDeviceSpecificWebhook(t *testing.T) {
	ctx := context.Background()
	deviceWebhookURL := "https://device-specific-webhook.com"
	payload := map[string]any{
		"foo":       "bar",
		"device_id": "6289600000000@s.whatsapp.net",
	}

	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://global-webhook.com"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalStorageForTest := webhookStorageForTest
	webhookStorageForTest = func(deviceJID string) (*chatstorage.DeviceRecord, error) {
		return &chatstorage.DeviceRecord{
			DeviceID:   deviceJID,
			WebhookURL: &deviceWebhookURL,
		}, nil
	}
	defer func() { webhookStorageForTest = originalStorageForTest }()

	var calledURLs []string
	originalSubmit := submitWebhookFn
	submitWebhookFn = func(_ context.Context, _ map[string]any, url string, _ *chatstorage.DeviceWebhookConfig) error {
		calledURLs = append(calledURLs, url)
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "test"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(calledURLs) != 1 {
		t.Fatalf("expected 1 webhook call (device-specific override), got %d", len(calledURLs))
	}
	if calledURLs[0] != deviceWebhookURL {
		t.Fatalf("expected device-specific webhook %s, got %s", deviceWebhookURL, calledURLs[0])
	}
}

func TestForwardPayloadToConfiguredWebhooks_DeviceWebhookCleared_FallsBackToGlobal(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{
		"foo":       "bar",
		"device_id": "6289600000000@s.whatsapp.net",
	}

	originalWebhooks := config.WhatsappWebhook
	originalFailClosed := config.WhatsappWebhookDeviceFailClosed
	config.WhatsappWebhook = []string{"https://global-webhook.com"}
	config.WhatsappWebhookDeviceFailClosed = false
	defer func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookDeviceFailClosed = originalFailClosed
	}()

	originalStorageForTest := webhookStorageForTest
	emptyURL := ""
	webhookStorageForTest = func(deviceJID string) (*chatstorage.DeviceRecord, error) {
		return &chatstorage.DeviceRecord{
			DeviceID:   deviceJID,
			WebhookURL: &emptyURL,
		}, nil
	}
	defer func() { webhookStorageForTest = originalStorageForTest }()

	var calledURLs []string
	originalSubmit := submitWebhookFn
	submitWebhookFn = func(_ context.Context, _ map[string]any, url string, _ *chatstorage.DeviceWebhookConfig) error {
		calledURLs = append(calledURLs, url)
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "test"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(calledURLs) != 1 {
		t.Fatalf("expected 1 webhook call (global fallback), got %d", len(calledURLs))
	}
	if calledURLs[0] != "https://global-webhook.com" {
		t.Fatalf("expected global webhook, got %s", calledURLs[0])
	}
}

// TestForwardPayloadToConfiguredWebhooks_DeviceLookupErrorFailsClosed proves that
// a backend lookup error can never redirect a managed device's event to the global
// webhook. The operator-visible log must also avoid the device JID and backend
// error text because either may contain tenant-sensitive identifiers or secrets.
func TestForwardPayloadToConfiguredWebhooks_DeviceLookupErrorFailsClosed(t *testing.T) {
	ctx := context.Background()
	deviceJID := "6289600000000@s.whatsapp.net"
	secretMarker := "storage-secret-marker"
	payload := map[string]any{
		"foo":       "bar",
		"device_id": deviceJID,
	}

	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://global-webhook.com"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalStorageForTest := webhookStorageForTest
	webhookStorageForTest = func(deviceJID string) (*chatstorage.DeviceRecord, error) {
		return nil, errors.New("database is locked: " + secretMarker)
	}
	defer func() { webhookStorageForTest = originalStorageForTest }()

	originalLogOutput := logrus.StandardLogger().Out
	var logs bytes.Buffer
	logrus.SetOutput(&logs)
	defer logrus.SetOutput(originalLogOutput)

	var calledURLs []string
	originalSubmit := submitWebhookFn
	submitWebhookFn = func(_ context.Context, _ map[string]any, url string, _ *chatstorage.DeviceWebhookConfig) error {
		calledURLs = append(calledURLs, url)
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "message"); err == nil {
		t.Fatal("device config lookup failure must be surfaced")
	}

	if len(calledURLs) != 0 {
		t.Fatalf("lookup failure must suppress all generic webhook delivery, got %v", calledURLs)
	}
	for _, sensitive := range []string{deviceJID, secretMarker} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("fail-closed log leaked sensitive value %q: %s", sensitive, logs.String())
		}
	}
}

func TestForwardPayloadToConfiguredWebhooks_ManagedMissingConfigFailsClosedWhenEnabled(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"device_id": "managed-device@s.whatsapp.net"}

	originalWebhooks := config.WhatsappWebhook
	originalFailClosed := config.WhatsappWebhookDeviceFailClosed
	config.WhatsappWebhook = []string{"https://global-webhook.example.com"}
	config.WhatsappWebhookDeviceFailClosed = true
	defer func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookDeviceFailClosed = originalFailClosed
	}()

	originalStorageForTest := webhookStorageForTest
	webhookStorageForTest = func(string) (*chatstorage.DeviceRecord, error) {
		return &chatstorage.DeviceRecord{DeviceID: "managed-device"}, nil
	}
	defer func() { webhookStorageForTest = originalStorageForTest }()

	originalSubmit := submitWebhookFn
	called := false
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		called = true
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "message"); err != nil {
		t.Fatalf("missing config is an intentional drop, not a delivery error: %v", err)
	}
	if called {
		t.Fatal("fail-closed gate must suppress global fallback for a managed device without config")
	}
}

func TestForwardPayloadToConfiguredWebhooks_InvalidDeviceConfigDoesNotFallbackOrLeak(t *testing.T) {
	ctx := context.Background()
	deviceJID := "invalid-device@s.whatsapp.net"
	secretMarker := "url-secret-marker"
	invalidURL := "not-a-webhook/" + secretMarker
	payload := map[string]any{"device_id": deviceJID}

	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://global-webhook.example.com"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalStorageForTest := webhookStorageForTest
	webhookStorageForTest = func(string) (*chatstorage.DeviceRecord, error) {
		return &chatstorage.DeviceRecord{DeviceID: "invalid-device", WebhookURL: &invalidURL, WebhookSecret: secretMarker}, nil
	}
	defer func() { webhookStorageForTest = originalStorageForTest }()

	originalLogOutput := logrus.StandardLogger().Out
	var logs bytes.Buffer
	logrus.SetOutput(&logs)
	defer logrus.SetOutput(originalLogOutput)

	originalSubmit := submitWebhookFn
	called := false
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		called = true
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "message"); err == nil {
		t.Fatal("invalid device webhook config must be surfaced")
	}
	if called {
		t.Fatal("invalid device webhook config must not be submitted or fall back globally")
	}
	for _, sensitive := range []string{deviceJID, secretMarker, invalidURL} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("invalid-config log leaked sensitive value %q: %s", sensitive, logs.String())
		}
	}
}

func TestForwardPayloadToConfiguredWebhooks_GenericConfigFailureStillInvokesChatwootOnce(t *testing.T) {
	deviceJID := "6289600000000@s.whatsapp.net"
	globalURL := "https://global-webhook.example.com/?token=global-secret-marker"
	backendMarker := "backend-secret-marker"
	invalidURL := "not-a-webhook/url-secret-marker"
	deviceSecret := "device-secret-marker"

	tests := []struct {
		name      string
		storage   func(string) (*chatstorage.DeviceRecord, error)
		sensitive []string
	}{
		{
			name: "backend lookup error",
			storage: func(string) (*chatstorage.DeviceRecord, error) {
				return nil, errors.New("database unavailable: " + backendMarker)
			},
			sensitive: []string{deviceJID, globalURL, backendMarker},
		},
		{
			name: "invalid per-device URL",
			storage: func(string) (*chatstorage.DeviceRecord, error) {
				return &chatstorage.DeviceRecord{
					DeviceID:      "managed-device",
					WebhookURL:    &invalidURL,
					WebhookSecret: deviceSecret,
				}, nil
			},
			sensitive: []string{deviceJID, globalURL, invalidURL, deviceSecret},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalWebhooks := config.WhatsappWebhook
			originalEvents := config.WhatsappWebhookEvents
			originalChatwootEnabled := config.ChatwootEnabled
			config.WhatsappWebhook = []string{globalURL}
			config.WhatsappWebhookEvents = []string{"message"}
			config.ChatwootEnabled = true
			defer func() {
				config.WhatsappWebhook = originalWebhooks
				config.WhatsappWebhookEvents = originalEvents
				config.ChatwootEnabled = originalChatwootEnabled
			}()

			originalStorageForTest := webhookStorageForTest
			webhookStorageForTest = tt.storage
			defer func() { webhookStorageForTest = originalStorageForTest }()

			genericSubmits := 0
			originalSubmit := submitWebhookFn
			submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
				genericSubmits++
				return nil
			}
			defer func() { submitWebhookFn = originalSubmit }()

			chatwootInvoked := make(chan struct{}, 2)
			originalChatwootClient := getChatwootClientFn
			getChatwootClientFn = func(string) (*chatwoot.ResolvedConfig, error) {
				chatwootInvoked <- struct{}{}
				return nil, nil
			}
			defer func() { getChatwootClientFn = originalChatwootClient }()

			originalLogOutput := logrus.StandardLogger().Out
			originalLogLevel := logrus.GetLevel()
			var logs bytes.Buffer
			logrus.SetOutput(&logs)
			logrus.SetLevel(logrus.InfoLevel)
			defer func() {
				logrus.SetOutput(originalLogOutput)
				logrus.SetLevel(originalLogLevel)
			}()

			payload := map[string]any{
				"device_id": deviceJID,
				"payload":   map[string]any{"id": "message-1"},
			}
			err := forwardPayloadToConfiguredWebhooks(context.Background(), payload, "message")
			if err == nil {
				t.Fatal("generic config failure must be surfaced")
			}
			if genericSubmits != 0 {
				t.Fatalf("generic webhook submit/fallback must remain suppressed, got %d call(s)", genericSubmits)
			}

			select {
			case <-chatwootInvoked:
			case <-time.After(time.Second):
				t.Fatal("Chatwoot path was not invoked after generic webhook config failure")
			}
			select {
			case <-chatwootInvoked:
				t.Fatal("Chatwoot path was invoked more than once")
			case <-time.After(50 * time.Millisecond):
			}

			for _, sensitive := range tt.sensitive {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("returned error leaked sensitive value %q: %v", sensitive, err)
				}
				if strings.Contains(logs.String(), sensitive) {
					t.Fatalf("logs leaked sensitive value %q: %s", sensitive, logs.String())
				}
			}
		})
}

func TestForwardToWebhooks_FailureLogRedactsURLSecretAndJID(t *testing.T) {
	secretMarker := "query-secret-marker"
	deviceJID := "6289600000000@s.whatsapp.net"
	webhookURL := "https://hooks.example.com/device/" + deviceJID + "?token=" + secretMarker

	originalLogOutput := logrus.StandardLogger().Out
	var logs bytes.Buffer
	logrus.SetOutput(&logs)
	defer logrus.SetOutput(originalLogOutput)

	originalSubmit := submitWebhookFn
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		return errors.New("delivery failed with " + secretMarker)
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardToWebhooks(context.Background(), map[string]any{}, "message", []string{webhookURL}, nil); err == nil {
		t.Fatal("all-failed delivery must return an error")
	}
	for _, sensitive := range []string{webhookURL, deviceJID, secretMarker} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("delivery log leaked sensitive value %q: %s", sensitive, logs.String())
		}
	}
}

// TestForwardPayloadToConfiguredWebhooks_DeviceWebhookOnly_NoGlobal verifies that when
// no global webhook is configured but a device-specific webhook is set, events are
// forwarded to the device-specific webhook. This is the primary path aldinokemal asked
// to verify: "the feature should work when only a device webhook is set."
func TestForwardPayloadToConfiguredWebhooks_DeviceWebhookOnly_NoGlobal(t *testing.T) {
	ctx := context.Background()
	deviceWebhookURL := "https://device-only-webhook.example.com"
	payload := map[string]any{
		"foo":       "bar",
		"device_id": "6289600000000@s.whatsapp.net",
	}

	// Ensure no global webhook is configured
	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = nil
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	originalStorageForTest := webhookStorageForTest
	webhookStorageForTest = func(deviceJID string) (*chatstorage.DeviceRecord, error) {
		return &chatstorage.DeviceRecord{
			DeviceID:   deviceJID,
			WebhookURL: &deviceWebhookURL,
		}, nil
	}
	defer func() { webhookStorageForTest = originalStorageForTest }()

	var calledURLs []string
	originalSubmit := submitWebhookFn
	submitWebhookFn = func(_ context.Context, _ map[string]any, url string, _ *chatstorage.DeviceWebhookConfig) error {
		calledURLs = append(calledURLs, url)
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "message"); err != nil {
		t.Fatalf("expected no error when only device webhook is set, got %v", err)
	}

	if len(calledURLs) != 1 {
		t.Fatalf("expected 1 webhook call (device-specific only), got %d calls: %v", len(calledURLs), calledURLs)
	}
	if calledURLs[0] != deviceWebhookURL {
		t.Fatalf("expected device-specific webhook %s, got %s", deviceWebhookURL, calledURLs[0])
	}
}

// TestAddWebhookSessionID pins the issue #578 enrichment: webhook payloads gain a
// session_id derived from their device_id (JID) so multi-tenant consumers can map
// an event back to the session they registered, while device_id stays the JID.
func TestAddWebhookSessionID(t *testing.T) {
	orig := sessionIDForJIDFn
	defer func() { sessionIDForJIDFn = orig }()

	t.Run("injects session_id resolved from device_id", func(t *testing.T) {
		sessionIDForJIDFn = func(jid string) string {
			if jid == "556283088170@s.whatsapp.net" {
				return "org_2"
			}
			return ""
		}
		payload := map[string]any{"device_id": "556283088170@s.whatsapp.net"}
		addWebhookSessionID(payload)
		if payload["session_id"] != "org_2" {
			t.Fatalf("expected session_id=org_2, got %v", payload["session_id"])
		}
		// device_id must remain the JID (backward-compatible).
		if payload["device_id"] != "556283088170@s.whatsapp.net" {
			t.Fatalf("device_id must be unchanged, got %v", payload["device_id"])
		}
	})

	t.Run("no session_id when JID is unmapped", func(t *testing.T) {
		sessionIDForJIDFn = func(string) string { return "" }
		payload := map[string]any{"device_id": "unknown@s.whatsapp.net"}
		addWebhookSessionID(payload)
		if _, ok := payload["session_id"]; ok {
			t.Fatalf("expected no session_id for unmapped JID, got %v", payload["session_id"])
		}
	})

	t.Run("does not overwrite an existing session_id", func(t *testing.T) {
		sessionIDForJIDFn = func(string) string { return "resolved" }
		payload := map[string]any{"device_id": "x@s.whatsapp.net", "session_id": "preset"}
		addWebhookSessionID(payload)
		if payload["session_id"] != "preset" {
			t.Fatalf("expected existing session_id to be preserved, got %v", payload["session_id"])
		}
	})
}

// TestSessionIDForJIDEmpty pins the empty-JID guard: it must short-circuit to ""
// before touching the device manager, regardless of global manager state.
func TestSessionIDForJIDEmpty(t *testing.T) {
	if got := sessionIDForJID(""); got != "" {
		t.Fatalf("expected empty session id for empty jid, got %q", got)
	}
}

// TestForwardPayloadInjectsSessionID verifies the session id is enriched in the
// real forward path before reaching the webhook submitter.
func TestForwardPayloadInjectsSessionID(t *testing.T) {
	ctx := context.Background()

	originalWebhooks := config.WhatsappWebhook
	config.WhatsappWebhook = []string{"https://test.com"}
	defer func() { config.WhatsappWebhook = originalWebhooks }()

	origResolve := sessionIDForJIDFn
	sessionIDForJIDFn = func(jid string) string {
		if jid == "556283088170@s.whatsapp.net" {
			return "org_2"
		}
		return ""
	}
	defer func() { sessionIDForJIDFn = origResolve }()

	var captured map[string]any
	originalSubmit := submitWebhookFn
	submitWebhookFn = func(_ context.Context, payload map[string]any, _ string, _ *chatstorage.DeviceWebhookConfig) error {
		captured = payload
		return nil
	}
	defer func() { submitWebhookFn = originalSubmit }()

	payload := map[string]any{"event": "message", "device_id": "556283088170@s.whatsapp.net"}
	if err := forwardPayloadToConfiguredWebhooks(ctx, payload, "message"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected submitWebhookFn to be invoked")
	}
	if captured["session_id"] != "org_2" {
		t.Fatalf("expected forwarded payload session_id=org_2, got %v", captured["session_id"])
	}
}
