package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainProvider "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/provider"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type providerReceiptRecorder struct {
	domainChatStorage.IChatStorageRepository
	err      error
	calls    int
	deviceID string
	id       string
}

func (r *providerReceiptRecorder) RecordProviderMessagePresent(_ context.Context, deviceID, providerMessageID string, _ time.Time) error {
	r.calls++
	r.deviceID = deviceID
	r.id = providerMessageID
	return r.err
}

func (r *providerReceiptRecorder) LookupProviderMessage(_ context.Context, deviceID, providerMessageID string) (domainProvider.MessageLookup, error) {
	r.calls++
	r.deviceID = deviceID
	r.id = providerMessageID
	return domainProvider.MessageLookup{Status: domainProvider.MessageUnknown}, r.err
}

func TestDeviceChatStorageDefaultsProviderLookupToOwnedDevice(t *testing.T) {
	base := &providerReceiptRecorder{}
	wrapped := newDeviceChatStorage("device-a@s.whatsapp.net", base)
	_, err := wrapped.LookupProviderMessage(context.Background(), "", "3EB0A1B2C3D4E5F6071829")
	if err != nil {
		t.Fatalf("LookupProviderMessage: %v", err)
	}
	if base.calls != 1 || base.deviceID != "device-a@s.whatsapp.net" {
		t.Fatalf("wrapper call = %d/%q", base.calls, base.deviceID)
	}
}

func TestOutgoingPrimaryReceiptPersistsProviderPresenceEvidence(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	repo := &providerReceiptRecorder{}
	evt := &events.Receipt{
		MessageSource: types.MessageSource{IsFromMe: true, Sender: types.NewJID("6281", types.DefaultUserServer)},
		MessageIDs:    []types.MessageID{providerID},
		Timestamp:     time.Now(),
		Type:          types.ReceiptTypeDelivered,
	}

	recordProviderReceiptEvidence(context.Background(), evt, "device-a@s.whatsapp.net", repo)
	if repo.calls != 1 || repo.deviceID != "device-a@s.whatsapp.net" || repo.id != providerID {
		t.Fatalf("record call = %d/%q/%q", repo.calls, repo.deviceID, repo.id)
	}
}

func TestReceiptEvidenceIgnoresIncomingAndLinkedDeviceReceipts(t *testing.T) {
	for _, tc := range []struct {
		name string
		evt  *events.Receipt
	}{
		{
			name: "incoming message read on another own device",
			evt: &events.Receipt{
				MessageSource: types.MessageSource{IsFromMe: false},
				MessageIDs:    []types.MessageID{"3EB0A1B2C3D4E5F6071829"},
				Type:          types.ReceiptTypeReadSelf,
			},
		},
		{
			name: "linked recipient device duplicate",
			evt: &events.Receipt{
				MessageSource: types.MessageSource{IsFromMe: true, Sender: types.JID{User: "6281", Server: types.DefaultUserServer, Device: 2}},
				MessageIDs:    []types.MessageID{"3EB0A1B2C3D4E5F6071829"},
				Type:          types.ReceiptTypeDelivered,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &providerReceiptRecorder{}
			recordProviderReceiptEvidence(context.Background(), tc.evt, "device-a@s.whatsapp.net", repo)
			if repo.calls != 0 {
				t.Fatalf("recorded %d non-authoritative receipts", repo.calls)
			}
		})
	}
}

func TestReceiptEvidenceStorageFailureUsesFixedPrivacySafeLog(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	repo := &providerReceiptRecorder{err: errors.New("disk corrupt for " + providerID + " device-a@s.whatsapp.net")}
	evt := &events.Receipt{
		MessageSource: types.MessageSource{IsFromMe: true, Sender: types.NewJID("6281", types.DefaultUserServer)},
		MessageIDs:    []types.MessageID{providerID},
		Timestamp:     time.Now(),
		Type:          types.ReceiptTypeRead,
	}
	var logs bytes.Buffer
	oldOutput := logrus.StandardLogger().Out
	oldFormatter := logrus.StandardLogger().Formatter
	logrus.SetOutput(&logs)
	logrus.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true})
	defer func() {
		logrus.SetOutput(oldOutput)
		logrus.SetFormatter(oldFormatter)
	}()

	recordProviderReceiptEvidence(context.Background(), evt, "device-a@s.whatsapp.net", repo)
	got := logs.String()
	if !strings.Contains(got, "provider_message_receipt.storage_unavailable") {
		t.Fatalf("missing fixed category: %q", got)
	}
	for _, sensitive := range []string{providerID, "device-a@s.whatsapp.net", "disk corrupt"} {
		if strings.Contains(got, sensitive) {
			t.Fatalf("log exposed %q: %q", sensitive, got)
		}
	}
}
