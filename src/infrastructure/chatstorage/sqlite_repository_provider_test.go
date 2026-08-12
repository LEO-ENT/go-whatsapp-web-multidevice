package chatstorage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainProvider "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/provider"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
)

func TestProviderMessageLookupIsExactDeviceScopedAndNeverInfersAbsence(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	const deviceA = "device-a@s.whatsapp.net"
	const deviceB = "device-b@s.whatsapp.net"
	const providerID = "3EB0A1B2C3D4E5F6071829"

	if err := repo.RecordProviderMessagePresent(context.Background(), deviceB, providerID, time.Now()); err != nil {
		t.Fatalf("record provider evidence: %v", err)
	}

	present, err := repo.LookupProviderMessage(context.Background(), deviceB, providerID)
	if err != nil {
		t.Fatalf("lookup owning device: %v", err)
	}
	if present.Status != domainProvider.MessagePresent || present.Receipt == nil || present.Receipt.ProviderMessageID != providerID {
		t.Fatalf("owning lookup = %#v, want PRESENT", present)
	}

	for _, tc := range []struct {
		name     string
		deviceID string
		id       string
	}{
		{name: "cross-device", deviceID: deviceA, id: providerID},
		{name: "unknown", deviceID: deviceB, id: "3EB0FFEEDDCCBBAA998877"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := repo.LookupProviderMessage(context.Background(), tc.deviceID, tc.id)
			if err != nil {
				t.Fatalf("LookupProviderMessage: %v", err)
			}
			if result.Status != domainProvider.MessageUnknown || result.Receipt != nil {
				t.Fatalf("result = %#v, want UNKNOWN without receipt", result)
			}
			if result.Status == domainProvider.MessageProvablyAbsent {
				t.Fatal("empty local authority was treated as proof of absence")
			}
		})
	}
}

func TestProviderMessageLookupAcceptsDurableSentMessageButNotIncomingHistory(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	const deviceID = "device-a@s.whatsapp.net"
	const chatJID = "628123456789@s.whatsapp.net"
	const sentID = "3EB0A1B2C3D4E5F6071829"
	const incomingID = "3EB0FFEEDDCCBBAA998877"
	now := time.Now()

	for _, message := range []*domainChatStorage.Message{
		{ID: sentID, ChatJID: chatJID, DeviceID: deviceID, Sender: deviceID, Content: "sent", Timestamp: now, IsFromMe: true},
		{ID: incomingID, ChatJID: chatJID, DeviceID: deviceID, Sender: chatJID, Content: "incoming", Timestamp: now, IsFromMe: false},
	} {
		if err := repo.StoreMessage(message); err != nil {
			t.Fatalf("StoreMessage: %v", err)
		}
	}

	sent, err := repo.LookupProviderMessage(context.Background(), deviceID, sentID)
	if err != nil || sent.Status != domainProvider.MessagePresent {
		t.Fatalf("sent lookup = %#v, %v, want PRESENT", sent, err)
	}
	incoming, err := repo.LookupProviderMessage(context.Background(), deviceID, incomingID)
	if err != nil {
		t.Fatalf("incoming lookup: %v", err)
	}
	if incoming.Status != domainProvider.MessageUnknown || incoming.Receipt != nil {
		t.Fatalf("incoming history = %#v, want UNKNOWN", incoming)
	}
}

func TestProviderMessageEvidenceSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider-receipts.db")
	open := func() (*sql.DB, *SQLiteRepository) {
		db, err := sql.Open(sqlite.DriverName, path)
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		repo := &SQLiteRepository{db: db}
		if err := repo.InitializeSchema(); err != nil {
			_ = db.Close()
			t.Fatalf("initialize schema: %v", err)
		}
		return db, repo
	}

	const deviceID = "device-a@s.whatsapp.net"
	const providerID = "3EB0A1B2C3D4E5F6071829"
	db, repo := open()
	if err := repo.RecordProviderMessagePresent(context.Background(), deviceID, providerID, time.Now()); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close first db: %v", err)
	}

	db, repo = open()
	defer db.Close()
	result, err := repo.LookupProviderMessage(context.Background(), deviceID, providerID)
	if err != nil || result.Status != domainProvider.MessagePresent {
		t.Fatalf("lookup after restart = %#v, %v", result, err)
	}
}

func TestProviderMessageLookupPropagatesUnavailableCorruptAndTimedOutStorage(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		repo := newTestSQLiteRepository(t)
		if err := repo.db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		_, err := repo.LookupProviderMessage(context.Background(), "device", "3EB0A1B2C3D4E5F6071829")
		if err == nil {
			t.Fatal("expected closed storage error")
		}
	})

	t.Run("corrupt schema", func(t *testing.T) {
		db, err := sql.Open(sqlite.DriverName, ":memory:")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer db.Close()
		repo := &SQLiteRepository{db: db}
		_, err = repo.LookupProviderMessage(context.Background(), "device", "3EB0A1B2C3D4E5F6071829")
		if err == nil {
			t.Fatal("expected missing/corrupt schema error")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		repo := newTestSQLiteRepository(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := repo.LookupProviderMessage(ctx, "device", "3EB0A1B2C3D4E5F6071829")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})
}
