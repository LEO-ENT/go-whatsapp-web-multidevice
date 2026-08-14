package usecase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainProvider "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/provider"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/sirupsen/logrus"
)

const testProviderMessageID = "3EB0A1B2C3D4E5F6071829"

type providerLookupRepo struct {
	domainChatStorage.IChatStorageRepository
	lookup domainProvider.MessageLookup
	err    error
	calls  int
	device string
	id     string
}

func (r *providerLookupRepo) LookupProviderMessage(_ context.Context, deviceID, providerMessageID string) (domainProvider.MessageLookup, error) {
	r.calls++
	r.device = deviceID
	r.id = providerMessageID
	return r.lookup, r.err
}

func TestProviderLookupUsesExactDeviceScopedStoreResult(t *testing.T) {
	repo := &providerLookupRepo{lookup: domainProvider.MessageLookup{
		Status: domainProvider.MessagePresent,
		Receipt: &domainProvider.MessageReceipt{
			ProviderMessageID: testProviderMessageID,
		},
	}}
	service := NewProviderService(repo)
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device-a", nil, nil))

	result, err := service.LookupMessage(ctx, testProviderMessageID)
	if err != nil {
		t.Fatalf("LookupMessage: %v", err)
	}
	if repo.calls != 1 || repo.device != "device-a" || repo.id != testProviderMessageID {
		t.Fatalf("exact lookup call = calls:%d device:%q id:%q", repo.calls, repo.device, repo.id)
	}
	if result.Status != domainProvider.MessagePresent || result.Receipt == nil || result.Receipt.ProviderMessageID != testProviderMessageID {
		t.Fatalf("result = %#v, want PRESENT with opaque receipt", result)
	}
}

func TestProviderLookupRejectsNonCanonicalIDBeforeStoreWithoutEcho(t *testing.T) {
	for _, malformed := range []string{
		"",
		"3eb0a1b2c3d4e5f6071829",
		"3EB0A1B2C3D4E5F607182",
		"3EB0A1B2C3D4E5F60718290",
		"4EB0A1B2C3D4E5F6071829",
		"3EB0A1B2C3D4E5F607182G",
		"person@example.test?secret=raw",
	} {
		t.Run(malformed, func(t *testing.T) {
			repo := &providerLookupRepo{}
			service := NewProviderService(repo)
			ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device-a", nil, nil))

			_, err := service.LookupMessage(ctx, malformed)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if repo.calls != 0 {
				t.Fatalf("invalid ID reached store %d times", repo.calls)
			}
			generic, ok := err.(pkgError.GenericError)
			if !ok || generic.StatusCode() != 400 {
				t.Fatalf("error = %T %v, want HTTP 400 generic validation error", err, err)
			}
			if malformed != "" && strings.Contains(err.Error(), malformed) {
				t.Fatal("validation error echoed provider message ID")
			}
		})
	}
}

func TestProviderLookupNeverTreatsMissingContextOrCrossDeviceMissAsAbsent(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		repo *providerLookupRepo
	}{
		{
			name: "missing device context",
			ctx:  context.Background(),
			repo: &providerLookupRepo{},
		},
		{
			name: "cross-device miss",
			ctx:  whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device-a", nil, nil)),
			repo: &providerLookupRepo{lookup: domainProvider.MessageLookup{Status: domainProvider.MessageUnknown}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := NewProviderService(tt.repo).LookupMessage(tt.ctx, testProviderMessageID)
			if err != nil {
				t.Fatalf("LookupMessage: %v", err)
			}
			if result.Status != domainProvider.MessageUnknown || result.Receipt != nil {
				t.Fatalf("result = %#v, want UNKNOWN without receipt", result)
			}
			if result.Status == domainProvider.MessageProvablyAbsent {
				t.Fatal("non-authoritative miss became PROVABLY_ABSENT")
			}
		})
	}
}

func TestProviderLookupPreservesProvablyAbsentOnlyFromStoreAuthority(t *testing.T) {
	repo := &providerLookupRepo{lookup: domainProvider.MessageLookup{Status: domainProvider.MessageProvablyAbsent}}
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device-a", nil, nil))
	result, err := NewProviderService(repo).LookupMessage(ctx, testProviderMessageID)
	if err != nil {
		t.Fatalf("LookupMessage: %v", err)
	}
	if result.Status != domainProvider.MessageProvablyAbsent || result.Receipt != nil {
		t.Fatalf("result = %#v, want authoritative PROVABLY_ABSENT without receipt", result)
	}
}

type providerExplosiveError struct {
	errorCalls *int
}

func (e providerExplosiveError) Error() string {
	*e.errorCalls++
	return "storage secret=" + testProviderMessageID + " jid=device-a@s.whatsapp.net"
}

func TestProviderLookupStorageFailuresAreUnknownAndPrivacySafe(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "storage down", err: errors.New("database is closed")},
		{name: "storage corrupt", err: errors.New("database disk image is malformed")},
		{name: "storage timeout", err: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var errorCalls int
			repo := &providerLookupRepo{err: errors.Join(tt.err, providerExplosiveError{errorCalls: &errorCalls})}
			var logs bytes.Buffer
			oldOutput := logrus.StandardLogger().Out
			oldFormatter := logrus.StandardLogger().Formatter
			logrus.SetOutput(&logs)
			logrus.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true})
			defer func() {
				logrus.SetOutput(oldOutput)
				logrus.SetFormatter(oldFormatter)
			}()

			ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device-a", nil, nil))
			result, err := NewProviderService(repo).LookupMessage(ctx, testProviderMessageID)
			if err != nil {
				t.Fatalf("LookupMessage: %v", err)
			}
			if result.Status != domainProvider.MessageUnknown || result.Receipt != nil {
				t.Fatalf("result = %#v, want UNKNOWN without receipt", result)
			}
			if errorCalls != 0 {
				t.Fatalf("storage error was rendered %d times", errorCalls)
			}
			got := logs.String()
			if !strings.Contains(got, "provider_message_lookup.storage_unavailable") {
				t.Fatalf("missing fixed storage category: %q", got)
			}
			for _, sensitive := range []string{testProviderMessageID, "device-a@s.whatsapp.net", "database"} {
				if strings.Contains(got, sensitive) {
					t.Fatalf("log exposed %q: %q", sensitive, got)
				}
			}
		})
	}
}
