package whatsapp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/chatwoot"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type receiptPIITestRepo struct {
	domainChatStorage.IChatStorageRepository
	links       map[string]*domainChatStorage.ChatwootMessageLink
	lookupID    string
	lookupError error
	upsertID    string
	upsertError error
}

func (r *receiptPIITestRepo) GetChatwootMessageLinkByWhatsAppID(_ string, messageID string) (*domainChatStorage.ChatwootMessageLink, error) {
	if messageID == r.lookupID {
		return nil, r.lookupError
	}
	return r.links[messageID], nil
}

func (r *receiptPIITestRepo) UpsertChatwootMessageLink(link *domainChatStorage.ChatwootMessageLink) error {
	if link != nil && link.WhatsAppMessageID == r.upsertID {
		return r.upsertError
	}
	return nil
}

type receiptPIIRoundTripper struct {
	failure error
}

func (rt receiptPIIRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/conversations/3/") {
		return nil, rt.failure
	}
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

type receiptPIIErrorStringSentinel struct {
	errorCalls  *int
	stringCalls *int
}

func (e receiptPIIErrorStringSentinel) Error() string {
	(*e.errorCalls)++
	return "receipt error secret=https://user:pass@example.test/path?token=raw"
}

func (e receiptPIIErrorStringSentinel) String() string {
	(*e.stringCalls)++
	return "receipt string secret=https://user:pass@example.test/path?token=raw"
}

func TestProxyConfigurationLoggingNeverRendersURLDeviceOrError(t *testing.T) {
	sink := newRecordingWhatsmeowLogger()
	client := whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	const sensitiveURL = "://628123456789:secret@example.test/private?token=raw"

	configureOutboundProxy(client, sensitiveURL, sink)
	sentinelCalls := 0
	logProxyConfigurationResult(sink, secretErrorSentinel{calls: &sentinelCalls})
	logProxyConfigurationResult(sink, nil)

	if sentinelCalls != 0 {
		t.Fatalf("proxy error rendered %d times before redaction", sentinelCalls)
	}
	if got := len(*sink.events); got != 3 {
		t.Fatalf("proxy log count = %d, want 3", got)
	}
	for _, event := range *sink.events {
		if event.args != 0 {
			t.Fatalf("proxy logger forwarded unrendered arguments: %#v", event)
		}
		combined := strings.ToLower(event.module + " " + event.text)
		for _, sensitive := range []string{"628123456789", "secret", "example.test", "private", "token", "raw", "device"} {
			if strings.Contains(combined, sensitive) {
				t.Fatalf("proxy PII %q crossed log boundary: %#v", sensitive, event)
			}
		}
		if event.text != proxyConfiguredEvent && event.text != proxyConfigurationFailed {
			t.Fatalf("proxy log is not a safe category: %#v", event)
		}
	}
}

func TestReceiptLoggingEmitsOnlySafeStatusAndCount(t *testing.T) {
	oldLog := log
	sink := newRecordingWhatsmeowLogger()
	log = sink
	t.Cleanup(func() { log = oldLog })

	const (
		providerID = "3EB0A1B2C3D4E5F6071829"
		jidUser    = "628123456789"
	)

	// This test owns the process-global waLog sink, so exercise the synchronous
	// nominal facade directly. handleReceipt deliberately detaches webhook work;
	// using it here allowed a goroutine to outlive cleanup and race the next test.
	logReceiptRead(1)
	logReceiptDelivered(1)

	if got := len(*sink.events); got != 2 {
		t.Fatalf("receipt log count = %d, want 2", got)
	}
	for _, event := range *sink.events {
		combined := strings.ToLower(event.module + " " + event.text)
		for _, sensitive := range []string{providerID, jidUser, types.DefaultUserServer, "2026-08-12", "messageids", "sourcestring"} {
			if strings.Contains(combined, strings.ToLower(sensitive)) {
				t.Fatalf("receipt PII %q crossed log boundary: %#v", sensitive, event)
			}
		}
		if !strings.HasPrefix(event.text, "whatsapp_receipt.") {
			t.Fatalf("receipt log is not a safe category: %#v", event)
		}
	}
}

func TestLinkedDeviceReceiptSkipUsesFixedCategory(t *testing.T) {
	oldOutput := logrus.StandardLogger().Out
	oldLevel := logrus.GetLevel()
	oldFormatter := logrus.StandardLogger().Formatter
	var logs bytes.Buffer
	logrus.SetOutput(&logs)
	logrus.SetLevel(logrus.DebugLevel)
	logrus.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})
	t.Cleanup(func() {
		logrus.SetOutput(oldOutput)
		logrus.SetLevel(oldLevel)
		logrus.SetFormatter(oldFormatter)
	})

	const jidUser = "628123456789"
	evt := &events.Receipt{
		MessageSource: types.MessageSource{
			Sender: types.JID{User: jidUser, Device: 37, Server: types.DefaultUserServer},
		},
	}
	if err := forwardReceiptToWebhook(context.Background(), evt, jidUser+"@"+types.DefaultUserServer, nil); err != nil {
		t.Fatalf("forward linked-device receipt: %v", err)
	}

	got := strings.ToLower(logs.String())
	if strings.Count(got, "whatsapp_receipt.linked_device_skipped") != 1 {
		t.Fatalf("safe linked-device category count != 1 in %s", got)
	}
	for _, sensitive := range []string{jidUser, "37", types.DefaultUserServer} {
		if strings.Contains(got, strings.ToLower(sensitive)) {
			t.Fatalf("linked-device receipt PII %q crossed log boundary: %s", sensitive, got)
		}
	}
}

func TestChatwootReceiptDownstreamNeverLogsMessageIDJIDOrError(t *testing.T) {
	const (
		lookupID = "3EB0000000000000000001"
		sourceID = "3EB0000000000000000002"
		updateID = "3EB0000000000000000003"
		upsertID = "3EB0000000000000000004"
		deviceID = "628123456789:37@s.whatsapp.net"
	)

	errorCalls := 0
	stringCalls := 0
	explosive := receiptPIIErrorStringSentinel{errorCalls: &errorCalls, stringCalls: &stringCalls}
	repo := &receiptPIITestRepo{
		lookupID: lookupID, lookupError: explosive,
		upsertID: upsertID, upsertError: explosive,
		links: map[string]*domainChatStorage.ChatwootMessageLink{
			sourceID: {WhatsAppMessageID: sourceID, ChatwootConversationID: 2},
			updateID: {WhatsAppMessageID: updateID, ChatwootConversationID: 3, ChatwootContactInboxSourceID: deviceID, ChatwootAccountID: 7},
			upsertID: {WhatsAppMessageID: upsertID, ChatwootConversationID: 4, ChatwootContactInboxSourceID: "opaque-source", ChatwootAccountID: 7},
		},
	}
	cw := &chatwoot.Client{
		BaseURL: "https://chatwoot.invalid", APIToken: "opaque-token", AccountID: 7, InboxID: 9,
		InboxIdentifier: "opaque-inbox",
		HTTPClient:      &http.Client{Transport: receiptPIIRoundTripper{failure: explosive}},
	}

	oldResolve := getChatwootClientFn
	getChatwootClientFn = func(string) (*chatwoot.ResolvedConfig, error) {
		return &chatwoot.ResolvedConfig{Client: cw}, nil
	}
	t.Cleanup(func() { getChatwootClientFn = oldResolve })

	oldOutput := logrus.StandardLogger().Out
	oldLevel := logrus.GetLevel()
	oldFormatter := logrus.StandardLogger().Formatter
	var logs bytes.Buffer
	logrus.SetOutput(&logs)
	logrus.SetLevel(logrus.DebugLevel)
	logrus.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})
	t.Cleanup(func() {
		logrus.SetOutput(oldOutput)
		logrus.SetLevel(oldLevel)
		logrus.SetFormatter(oldFormatter)
	})

	ctx := ContextWithDevice(context.Background(), NewDeviceInstance(deviceID, nil, repo))
	payload := map[string]any{
		"device_id": deviceID,
		"payload": map[string]any{
			"ids":          []string{lookupID, sourceID, updateID, upsertID},
			"receipt_type": string(types.ReceiptTypeRead),
		},
	}
	forwardToChatwoot(ctx, payload, "message.ack")

	if errorCalls != 0 || stringCalls != 0 {
		t.Fatalf("downstream receipt sentinel rendered: Error=%d String=%d", errorCalls, stringCalls)
	}
	got := strings.ToLower(logs.String())
	for _, sensitive := range []string{lookupID, sourceID, updateID, upsertID, deviceID, "example.test", "token=raw", "secret="} {
		if strings.Contains(got, strings.ToLower(sensitive)) {
			t.Fatalf("downstream receipt PII %q crossed log boundary: %s", sensitive, got)
		}
	}
	for _, category := range []string{
		"chatwoot_receipt.lookup_failed",
		"chatwoot_receipt.missing_source",
		"chatwoot_receipt.update_last_seen_failed",
		"chatwoot_receipt.mark_read_failed",
	} {
		if strings.Count(got, category) != 1 {
			t.Errorf("safe downstream category %q count != 1 in %s", category, got)
		}
	}
}
