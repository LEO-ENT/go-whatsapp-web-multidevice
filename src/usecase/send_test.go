package usecase

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainSend "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

type replyMessageRepo struct {
	domainChatStorage.IChatStorageRepository
	message     *domainChatStorage.Message
	err         error
	gotID       string
	gotDeviceID string
}

func (r *replyMessageRepo) GetChat(string) (*domainChatStorage.Chat, error) {
	return nil, nil
}

func (r *replyMessageRepo) StoreSentMessageWithContext(_ context.Context, messageID, _, _, _ string, _ time.Time, _ *waE2E.Message) error {
	return r.err
}

func (r *replyMessageRepo) RecordProviderMessagePresent(_ context.Context, _, _ string, _ time.Time) error {
	return r.err
}

func (r *replyMessageRepo) GetMessageByIDAndDevice(deviceID, id string) (*domainChatStorage.Message, error) {
	r.gotDeviceID = deviceID
	r.gotID = id
	return r.message, r.err
}

func TestWithoutCancelPreservesDeviceContext(t *testing.T) {
	deviceID := "6289605618749@s.whatsapp.net"
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance(deviceID, nil, nil))

	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	storeCtx := context.WithoutCancel(cancelledCtx)
	inst, ok := whatsapp.DeviceFromContext(storeCtx)
	if !ok || inst == nil {
		t.Fatal("expected device instance to remain in detached context")
	}
	if got := inst.ID(); got != deviceID {
		t.Fatalf("expected device id %q, got %q", deviceID, got)
	}
}

func TestResolveDocumentMIME(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantMIME string
	}{
		{
			name:     "Docx",
			filename: "document.docx",
			wantMIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		},
		{
			name:     "Xlsx",
			filename: "spreadsheet.xlsx",
			wantMIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		},
		{
			name:     "Pptx",
			filename: "presentation.pptx",
			wantMIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		},
		{
			name:     "Zip",
			filename: "archive.zip",
			wantMIME: "application/zip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveDocumentMIME(tt.filename, []byte("dummy"))
			if got != tt.wantMIME {
				t.Fatalf("resolveDocumentMIME() = %q, want %q", got, tt.wantMIME)
			}
		})
	}
}

func TestBuildLinkMessageText(t *testing.T) {
	tests := []struct {
		name    string
		caption string
		link    string
		want    string
	}{
		{
			name: "returns link when caption is empty",
			link: "https://example.com",
			want: "https://example.com",
		},
		{
			name:    "joins caption and link with newline",
			caption: "Check this out",
			link:    "https://example.com",
			want:    "Check this out\nhttps://example.com",
		},
		{
			name:    "ignores blank caption",
			caption: "   ",
			link:    "https://example.com",
			want:    "https://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildLinkMessageText(tt.caption, tt.link)
			if got != tt.want {
				t.Fatalf("buildLinkMessageText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeSendErrorMapsReachoutTimelock(t *testing.T) {
	err := normalizeSendError(errors.Join(whatsmeow.ErrServerReturnedError, errors.New("server returned error 463")))

	genericErr, ok := err.(pkgError.GenericError)
	if !ok {
		t.Fatalf("expected generic error, got %T", err)
	}
	if got := genericErr.ErrCode(); got != "WA_REACHOUT_TIMELOCK" {
		t.Fatalf("expected WA_REACHOUT_TIMELOCK code, got %q", got)
	}
	if got := genericErr.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("expected status %d, got %d", http.StatusTooManyRequests, got)
	}
	if got := genericErr.Error(); got != string(pkgError.ErrWaReachoutTimelock) {
		t.Fatalf("unexpected error message: %q", got)
	}
}

func TestSendTextDeterministicProviderOptionsReachWhatsmeowExactly(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	timeoutMS := 20_000
	messageID := providerID
	var gotExtras []whatsmeow.SendRequestExtra

	service := serviceSend{
		chatStorageRepo: &replyMessageRepo{},
		resolveRecipient: func(*whatsmeow.Client, string) (types.JID, error) {
			return types.NewJID("12345", types.GroupServer), nil
		},
		sendMessage: func(_ context.Context, _ *whatsmeow.Client, _ types.JID, _ *waE2E.Message, extras ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
			gotExtras = append(gotExtras, extras...)
			return whatsmeow.SendResponse{ID: providerID, Timestamp: time.Unix(1, 0)}, nil
		},
	}
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device", &whatsmeow.Client{}, nil))
	response, err := service.SendText(ctx, domainSend.MessageRequest{
		BaseRequest:       domainSend.BaseRequest{Phone: "12345@g.us"},
		Message:           "sensitive body",
		ProviderMessageID: &messageID,
		ProviderTimeoutMS: &timeoutMS,
	})
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if len(gotExtras) != 1 {
		t.Fatalf("extras = %#v, want exactly one", gotExtras)
	}
	if gotExtras[0].ID != types.MessageID(providerID) {
		t.Fatalf("ID = %q, want exact deterministic ID", gotExtras[0].ID)
	}
	if gotExtras[0].Timeout != 20*time.Second {
		t.Fatalf("Timeout = %s, want 20s", gotExtras[0].Timeout)
	}
	if response.MessageID != providerID {
		t.Fatalf("receipt = %q, want exact provider ID", response.MessageID)
	}
	if strings.Contains(response.Status, providerID) || strings.Contains(response.Status, "12345") || strings.Contains(response.Status, "sensitive body") {
		t.Fatalf("status exposed provider data: %q", response.Status)
	}
}

type providerEvidenceSendRepo struct {
	domainChatStorage.IChatStorageRepository
	recordErr error
	calls     int
	deviceID  string
	id        string
}

func (r *providerEvidenceSendRepo) GetChat(string) (*domainChatStorage.Chat, error) {
	return nil, nil
}

func (r *providerEvidenceSendRepo) RecordProviderMessagePresent(_ context.Context, deviceID, providerMessageID string, _ time.Time) error {
	r.calls++
	r.deviceID = deviceID
	r.id = providerMessageID
	return r.recordErr
}

func (r *providerEvidenceSendRepo) StoreSentMessageWithContext(_ context.Context, _, _, _, _ string, _ time.Time, _ *waE2E.Message) error {
	return nil
}

func TestSendTextAcknowledgementRecordsProviderPresenceBeforeReturning(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	repo := &providerEvidenceSendRepo{}
	service := serviceSend{
		chatStorageRepo: repo,
		resolveRecipient: func(*whatsmeow.Client, string) (types.JID, error) {
			return types.NewJID("12345", types.GroupServer), nil
		},
		sendMessage: func(_ context.Context, _ *whatsmeow.Client, _ types.JID, _ *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
			return whatsmeow.SendResponse{ID: providerID, Timestamp: time.Unix(1, 0)}, nil
		},
	}
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device-a", &whatsmeow.Client{}, nil))
	id := providerID
	result, err := service.SendText(ctx, domainSend.MessageRequest{
		BaseRequest:       domainSend.BaseRequest{Phone: "12345@g.us"},
		Message:           "body",
		ProviderMessageID: &id,
	})
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if result.MessageID != providerID || repo.calls != 1 || repo.deviceID != "device-a" || repo.id != providerID {
		t.Fatalf("result/record = %#v %d/%q/%q", result, repo.calls, repo.deviceID, repo.id)
	}
}

func TestSendTextEvidenceStorageFailureDoesNotCreateBlindRetrySignal(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	repo := &providerEvidenceSendRepo{recordErr: errors.New("storage failed for " + providerID)}
	service := serviceSend{
		chatStorageRepo: repo,
		resolveRecipient: func(*whatsmeow.Client, string) (types.JID, error) {
			return types.NewJID("12345", types.GroupServer), nil
		},
		sendMessage: func(_ context.Context, _ *whatsmeow.Client, _ types.JID, _ *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
			return whatsmeow.SendResponse{ID: providerID, Timestamp: time.Unix(1, 0)}, nil
		},
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

	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device-a", &whatsmeow.Client{}, nil))
	id := providerID
	result, err := service.SendText(ctx, domainSend.MessageRequest{
		BaseRequest:       domainSend.BaseRequest{Phone: "12345@g.us"},
		Message:           "body",
		ProviderMessageID: &id,
	})
	if err != nil || result.MessageID != providerID {
		t.Fatalf("acknowledged send became retryable failure: %#v %v", result, err)
	}
	got := logs.String()
	if !strings.Contains(got, "provider_message_receipt.storage_unavailable") || strings.Contains(got, providerID) || strings.Contains(got, "storage failed") {
		t.Fatalf("unsafe evidence log: %q", got)
	}
}

func TestSendTextLegacyRequestPassesNoWhatsmeowExtra(t *testing.T) {
	var extraCount int
	service := serviceSend{
		chatStorageRepo: &replyMessageRepo{},
		resolveRecipient: func(*whatsmeow.Client, string) (types.JID, error) {
			return types.NewJID("12345", types.GroupServer), nil
		},
		sendMessage: func(_ context.Context, _ *whatsmeow.Client, _ types.JID, _ *waE2E.Message, extras ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
			extraCount = len(extras)
			return whatsmeow.SendResponse{ID: "legacy-random", Timestamp: time.Unix(1, 0)}, nil
		},
	}
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device", &whatsmeow.Client{}, nil))
	_, err := service.SendText(ctx, domainSend.MessageRequest{
		BaseRequest: domainSend.BaseRequest{Phone: "12345@g.us"},
		Message:     "body",
	})
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if extraCount != 0 {
		t.Fatalf("legacy request passed %d extras, want none", extraCount)
	}
}

func TestProviderSendExtrasUseSafeDefaultAndKeepDistinctEffectIDs(t *testing.T) {
	firstID := "3EB0A1B2C3D4E5F6071829"
	secondID := "3EB0FFEEDDCCBBAA998877"

	first := providerSendExtras(domainSend.MessageRequest{ProviderMessageID: &firstID})
	second := providerSendExtras(domainSend.MessageRequest{ProviderMessageID: &secondID})
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("deterministic requests must each produce one extra: %#v %#v", first, second)
	}
	if first[0].ID != firstID || second[0].ID != secondID {
		t.Fatalf("effect IDs were not propagated exactly: %#v %#v", first, second)
	}
	if first[0].ID == second[0].ID {
		t.Fatal("distinct effects collapsed to the same provider ID")
	}
	wantDefault := time.Duration(domainSend.ProviderTimeoutDefaultMS) * time.Millisecond
	if first[0].Timeout != wantDefault || second[0].Timeout != wantDefault {
		t.Fatalf("default timeout mismatch: %#v %#v", first, second)
	}
}

func TestProviderSendExtrasTimeoutOnlyLeavesIDForLegacyGeneration(t *testing.T) {
	timeoutMS := 12_345
	extras := providerSendExtras(domainSend.MessageRequest{ProviderTimeoutMS: &timeoutMS})
	if len(extras) != 1 {
		t.Fatalf("extras = %#v, want one bounded timeout option", extras)
	}
	if extras[0].ID != "" {
		t.Fatalf("ID = %q, want empty so pinned Whatsmeow generates the legacy random ID", extras[0].ID)
	}
	if extras[0].Timeout != 12_345*time.Millisecond {
		t.Fatalf("Timeout = %s, want 12.345s", extras[0].Timeout)
	}
}

func TestSendTextRejectsInvalidProviderIDBeforeSendWithoutEcho(t *testing.T) {
	blankID := ""
	malformedID := "person@example.test?secret=1"
	oversizeID := "3EB0A1B2C3D4E5F6071829AA"
	invalidTimeout := domainSend.ProviderTimeoutMaxMS + 1
	sendCalls := 0
	service := serviceSend{
		sendMessage: func(_ context.Context, _ *whatsmeow.Client, _ types.JID, _ *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
			sendCalls++
			return whatsmeow.SendResponse{}, nil
		},
	}
	requests := []domainSend.MessageRequest{
		{BaseRequest: domainSend.BaseRequest{Phone: "12345@g.us"}, Message: "body", ProviderMessageID: &blankID},
		{BaseRequest: domainSend.BaseRequest{Phone: "12345@g.us"}, Message: "body", ProviderMessageID: &malformedID},
		{BaseRequest: domainSend.BaseRequest{Phone: "12345@g.us"}, Message: "body", ProviderMessageID: &oversizeID},
		{BaseRequest: domainSend.BaseRequest{Phone: "12345@g.us"}, Message: "body", ProviderTimeoutMS: &invalidTimeout},
	}
	for _, request := range requests {
		_, err := service.SendText(context.Background(), request)
		if err == nil {
			t.Fatal("expected validation error")
		}
		for _, sensitive := range []string{malformedID, oversizeID} {
			if strings.Contains(err.Error(), sensitive) {
				t.Fatal("validation error exposed provider ID")
			}
		}
	}
	if sendCalls != 0 {
		t.Fatalf("send calls = %d, want zero", sendCalls)
	}
}

func TestSendTextRecipientResolutionErrorIsRedactedAndDoesNotSend(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	messageID := providerID
	sendCalls := 0
	service := serviceSend{
		resolveRecipient: func(*whatsmeow.Client, string) (types.JID, error) {
			return types.JID{}, errors.New("recipient 12345@g.us body secret " + providerID)
		},
		sendMessage: func(_ context.Context, _ *whatsmeow.Client, _ types.JID, _ *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
			sendCalls++
			return whatsmeow.SendResponse{}, nil
		},
	}
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device", &whatsmeow.Client{}, nil))
	_, err := service.SendText(ctx, domainSend.MessageRequest{
		BaseRequest:       domainSend.BaseRequest{Phone: "12345@g.us"},
		Message:           "body secret",
		ProviderMessageID: &messageID,
	})
	if err == nil {
		t.Fatal("expected recipient resolution error")
	}
	if sendCalls != 0 {
		t.Fatalf("send calls = %d, want zero", sendCalls)
	}
	for _, sensitive := range []string{providerID, "12345@g.us", "body secret"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("recipient error exposed %q", sensitive)
		}
	}
}

func TestSendTextCrashAfterSendRetryReusesExactProviderID(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	messageID := providerID
	timeoutMS := 20_000
	var effectIDs []types.MessageID
	service := serviceSend{
		chatStorageRepo: &replyMessageRepo{},
		resolveRecipient: func(*whatsmeow.Client, string) (types.JID, error) {
			return types.NewJID("12345", types.GroupServer), nil
		},
		sendMessage: func(_ context.Context, _ *whatsmeow.Client, _ types.JID, _ *waE2E.Message, extras ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
			if len(extras) != 1 {
				t.Fatalf("extras = %d, want one", len(extras))
			}
			effectIDs = append(effectIDs, extras[0].ID)
			if len(effectIDs) == 1 {
				return whatsmeow.SendResponse{}, errors.New("transport failed after effect " + providerID)
			}
			return whatsmeow.SendResponse{ID: extras[0].ID, Timestamp: time.Unix(2, 0)}, nil
		},
	}
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device", &whatsmeow.Client{}, nil))
	request := domainSend.MessageRequest{
		BaseRequest:       domainSend.BaseRequest{Phone: "12345@g.us"},
		Message:           "body",
		ProviderMessageID: &messageID,
		ProviderTimeoutMS: &timeoutMS,
	}

	_, firstErr := service.SendText(ctx, request)
	if firstErr == nil {
		t.Fatal("expected ambiguous first failure")
	}
	if strings.Contains(firstErr.Error(), providerID) {
		t.Fatal("send error exposed provider ID")
	}
	second, secondErr := service.SendText(ctx, request)
	if secondErr != nil {
		t.Fatalf("retry: %v", secondErr)
	}
	if len(effectIDs) != 2 || effectIDs[0] != providerID || effectIDs[1] != providerID {
		t.Fatalf("effect IDs = %#v, want exact reuse", effectIDs)
	}
	if second.MessageID != providerID {
		t.Fatalf("retry receipt = %q, want %q", second.MessageID, providerID)
	}
}

func TestWrapSendMessageStorageFailureLogIsProviderDataFree(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	const recipient = "12345@g.us"
	stored := make(chan struct{}, 1)
	repo := &loggingFailureRepo{stored: stored, err: errors.New("failed " + providerID + " for " + recipient)}
	service := serviceSend{
		chatStorageRepo: repo,
		sendMessage: func(_ context.Context, _ *whatsmeow.Client, _ types.JID, _ *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
			return whatsmeow.SendResponse{ID: providerID, Timestamp: time.Unix(1, 0)}, nil
		},
	}

	var logs bytes.Buffer
	oldOutput := logrus.StandardLogger().Out
	oldLevel := logrus.GetLevel()
	logrus.SetOutput(&logs)
	logrus.SetLevel(logrus.WarnLevel)
	t.Cleanup(func() {
		logrus.SetOutput(oldOutput)
		logrus.SetLevel(oldLevel)
	})

	_, err := service.wrapSendMessage(context.Background(), &whatsmeow.Client{}, types.NewJID("12345", types.GroupServer), &waE2E.Message{}, "sensitive body")
	if err != nil {
		t.Fatalf("wrapSendMessage: %v", err)
	}
	select {
	case <-stored:
	case <-time.After(time.Second):
		t.Fatal("storage seam was not called")
	}
	deadline := time.Now().Add(time.Second)
	for logs.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	logText := logs.String()
	if strings.Contains(logText, providerID) || strings.Contains(logText, recipient) || strings.Contains(logText, "sensitive body") {
		t.Fatalf("storage warning exposed provider data: %q", logText)
	}
}

type loggingFailureRepo struct {
	domainChatStorage.IChatStorageRepository
	stored chan struct{}
	err    error
}

func (r *loggingFailureRepo) StoreSentMessageWithContext(_ context.Context, _, _, _, _ string, _ time.Time, _ *waE2E.Message) error {
	r.stored <- struct{}{}
	return r.err
}

func (r *loggingFailureRepo) RecordProviderMessagePresent(_ context.Context, _, _ string, _ time.Time) error {
	return nil
}

func TestMergeReplyContextAddsQuoteFields(t *testing.T) {
	replyID := "3EB089B9D6ADD58153C561"
	repo := &replyMessageRepo{
		message: &domainChatStorage.Message{
			Sender:  "628123456789@s.whatsapp.net",
			Content: "quoted message body",
		},
	}
	service := serviceSend{chatStorageRepo: repo}
	contextInfo := &waE2E.ContextInfo{}

	deviceID := "6289605618749@s.whatsapp.net"
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance(deviceID, nil, nil))
	got := service.mergeReplyContext(ctx, contextInfo, &replyID)

	if got != contextInfo {
		t.Fatal("expected existing context info to be reused")
	}
	if repo.gotID != replyID {
		t.Fatalf("expected lookup for reply ID %q, got %q", replyID, repo.gotID)
	}
	if repo.gotDeviceID != deviceID {
		t.Fatalf("expected device-scoped lookup for %q, got %q", deviceID, repo.gotDeviceID)
	}
	if got.GetStanzaID() != replyID {
		t.Fatalf("expected stanza ID %q, got %q", replyID, got.GetStanzaID())
	}
	if got.GetParticipant() != "628123456789@s.whatsapp.net" {
		t.Fatalf("unexpected participant: %q", got.GetParticipant())
	}
	if got.GetQuotedMessage().GetConversation() != "quoted message body" {
		t.Fatalf("unexpected quoted body: %q", got.GetQuotedMessage().GetConversation())
	}
}

func TestMergeReplyContextPreservesExistingContext(t *testing.T) {
	replyID := "3EB089B9D6ADD58153C561"
	repo := &replyMessageRepo{
		message: &domainChatStorage.Message{
			Sender:  "628123456789@s.whatsapp.net",
			Content: "quoted message body",
		},
	}
	service := serviceSend{chatStorageRepo: repo}
	contextInfo := &waE2E.ContextInfo{
		IsForwarded:     proto.Bool(true),
		ForwardingScore: proto.Uint32(100),
		Expiration:      proto.Uint32(3600),
		MentionedJID:    []string{"628999999999@s.whatsapp.net"},
	}

	got := service.mergeReplyContext(context.Background(), contextInfo, &replyID)

	if !got.GetIsForwarded() {
		t.Fatal("expected forwarded flag to be preserved")
	}
	if got.GetForwardingScore() != 100 {
		t.Fatalf("expected forwarding score 100, got %d", got.GetForwardingScore())
	}
	if got.GetExpiration() != 3600 {
		t.Fatalf("expected expiration 3600, got %d", got.GetExpiration())
	}
	if len(got.GetMentionedJID()) != 1 || got.GetMentionedJID()[0] != "628999999999@s.whatsapp.net" {
		t.Fatalf("expected mentioned JIDs to be preserved, got %#v", got.GetMentionedJID())
	}
	if got.GetQuotedMessage().GetConversation() != "quoted message body" {
		t.Fatalf("unexpected quoted body: %q", got.GetQuotedMessage().GetConversation())
	}
}

func TestMergeReplyContextLeavesExistingContextWhenReplyUnavailable(t *testing.T) {
	replyID := "3EB089B9D6ADD58153C561"

	tests := []struct {
		name    string
		replyID *string
		message *domainChatStorage.Message
		err     error
	}{
		{
			name: "nil reply ID",
		},
		{
			name:    "empty reply ID",
			replyID: proto.String(""),
		},
		{
			name:    "message not found",
			replyID: &replyID,
		},
		{
			name:    "lookup error",
			replyID: &replyID,
			err:     errors.New("storage unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contextInfo := &waE2E.ContextInfo{Expiration: proto.Uint32(3600)}
			service := serviceSend{chatStorageRepo: &replyMessageRepo{
				message: tt.message,
				err:     tt.err,
			}}

			got := service.mergeReplyContext(context.Background(), contextInfo, tt.replyID)

			if got != contextInfo {
				t.Fatal("expected existing context info to be reused")
			}
			if got.GetExpiration() != 3600 {
				t.Fatalf("expected expiration to remain 3600, got %d", got.GetExpiration())
			}
			if got.GetStanzaID() != "" {
				t.Fatalf("expected no stanza ID, got %q", got.GetStanzaID())
			}
			if got.GetQuotedMessage() != nil {
				t.Fatalf("expected no quoted message, got %#v", got.GetQuotedMessage())
			}
		})
	}
}

func TestSendForwardMessageNotFound(t *testing.T) {
	repo := &replyMessageRepo{}
	service := serviceSend{chatStorageRepo: repo}
	deviceID := "6289605618749@s.whatsapp.net"
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance(deviceID, nil, nil))

	_, err := service.SendForward(ctx, domainSend.ForwardRequest{
		MessageID: "missing-id",
		Phone:     "628123456789@s.whatsapp.net",
	})
	if err == nil {
		t.Fatal("expected error for missing message")
	}
	if repo.gotID != "missing-id" {
		t.Fatalf("expected lookup for missing-id, got %q", repo.gotID)
	}
	if repo.gotDeviceID != deviceID {
		t.Fatalf("expected device-scoped lookup for %q, got %q", deviceID, repo.gotDeviceID)
	}
}

func TestForwardDurationOptionExplicitZero(t *testing.T) {
	zero := 0
	got := forwardDurationOption(serviceSend{}, domainSend.ForwardRequest{
		Phone:    "628123456789@s.whatsapp.net",
		Duration: &zero,
	})
	if got == nil || *got != 0 {
		t.Fatalf("expected explicit duration 0 to be honored, got %v", got)
	}
}

func TestSendForwardUnsupportedType(t *testing.T) {
	repo := &replyMessageRepo{
		message: &domainChatStorage.Message{
			MediaType: "call",
			Content:   "incoming call",
		},
	}
	service := serviceSend{chatStorageRepo: repo}
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("6289605618749@s.whatsapp.net", nil, nil))

	_, err := service.SendForward(ctx, domainSend.ForwardRequest{
		MessageID: "call-msg-id",
		Phone:     "628123456789@s.whatsapp.net",
	})
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
	genericErr, ok := err.(pkgError.GenericError)
	if !ok {
		t.Fatalf("expected validation error, got %T: %v", err, err)
	}
	if genericErr.Error() != utils.ErrUnsupportedForwardType {
		t.Fatalf("error = %q, want %q", genericErr.Error(), utils.ErrUnsupportedForwardType)
	}
}
