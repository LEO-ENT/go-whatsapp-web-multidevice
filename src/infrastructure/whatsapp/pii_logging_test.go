package whatsapp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
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
	oldDebug := config.AppDebug
	sink := newRecordingWhatsmeowLogger()
	log = sink
	config.AppDebug = true
	t.Cleanup(func() {
		log = oldLog
		config.AppDebug = oldDebug
	})

	const (
		providerID = "3EB0A1B2C3D4E5F6071829"
		jidUser    = "628123456789"
	)
	receipt := &events.Receipt{
		MessageSource: types.MessageSource{
			Chat:   types.NewJID(jidUser, types.DefaultUserServer),
			Sender: types.JID{User: jidUser, Device: 37, Server: types.DefaultUserServer},
		},
		MessageIDs: []types.MessageID{types.MessageID(providerID)},
		Timestamp:  time.Date(2026, 8, 12, 12, 34, 56, 0, time.UTC),
		Type:       types.ReceiptTypeRead,
	}

	handleReceipt(context.Background(), receipt, jidUser+"@"+types.DefaultUserServer, nil)
	receipt.Type = types.ReceiptTypeDelivered
	handleReceipt(context.Background(), receipt, jidUser+"@"+types.DefaultUserServer, nil)

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

func TestProductionPIILoggingCensusProtectsProxyAndReceiptCallSites(t *testing.T) {
	targets := map[string][]string{
		"init.go": {
			"redactProxyURL(proxyURL)",
			"WHATSAPP_PROXY=%q",
			"SetProxyAddress(",
		},
		"device_manager.go": {
			"redactProxyURL(proxyURL)",
			"WHATSAPP_PROXY=%q",
			"applied outbound proxy from WHATSAPP_PROXY for device %s",
			"SetProxyAddress(",
		},
		"proxy.go": {
			"url.Redacted",
			"logger.Errorf(proxyConfigurationFailed,",
			"logger.Infof(proxyConfiguredEvent,",
		},
		"event_handler.go": {
			"was read by %s",
			"was delivered to %s",
			"SourceString()",
		},
		"event_receipt.go": {
			"MessageIDs[0]",
			"SourceString()",
		},
		"webhook_forward.go": {
			"Failed to lookup read receipt link for %s",
			"Skipping read receipt %s",
			"Failed to update last seen for message %s",
			"Failed to mark link read for %s",
		},
	}

	for file, forbidden := range targets {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, fragment := range forbidden {
			if strings.Contains(string(source), fragment) {
				t.Errorf("%s contains sensitive logging fragment %q", file, fragment)
			}
		}
	}

	for _, file := range []string{"init.go", "device_manager.go"} {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if got := strings.Count(string(source), "configureOutboundProxy(client, config.WhatsappProxy, baseLogger)"); got != 1 {
			t.Errorf("%s safe proxy boundary calls = %d, want 1", file, got)
		}
	}
}
