package whatsapp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

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
