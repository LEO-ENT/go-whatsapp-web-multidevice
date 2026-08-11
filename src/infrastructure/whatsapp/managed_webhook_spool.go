package whatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainSpool "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/webhookspool"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	defaultManagedWebhookMaxPayload  = 1 << 20
	defaultManagedWebhookMaxAttempts = 12
)

type managedWebhookAdmissionOptions struct {
	SecretVersion string
	MaxPayload    int
	MaxAttempts   int
	MaxAge        time.Duration
}

func (o managedWebhookAdmissionOptions) normalized() managedWebhookAdmissionOptions {
	if o.MaxPayload <= 0 {
		o.MaxPayload = defaultManagedWebhookMaxPayload
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = defaultManagedWebhookMaxAttempts
	}
	if o.MaxAge <= 0 {
		o.MaxAge = 24 * time.Hour
	}
	o.SecretVersion = strings.TrimSpace(o.SecretVersion)
	return o
}

// enqueueManagedWebhookPayload is the source admission boundary. It performs
// no HTTP and starts no goroutine. A successful return means the protected
// delivery was committed (or already existed idempotently) in SQLite.
func enqueueManagedWebhookPayload(
	ctx context.Context,
	repo domainSpool.Repository,
	codec domainSpool.Codec,
	payload map[string]any,
	eventName string,
	webhookConfig *domainChatStorage.DeviceWebhookConfig,
	options managedWebhookAdmissionOptions,
) error {
	options = options.normalized()
	if repo == nil || codec == nil {
		return domainSpool.ErrCodecUnavailable
	}
	if payload == nil || webhookConfig == nil || webhookConfig.WebhookURL == nil {
		return domainSpool.ErrInvalidEnvelope
	}
	targetURL := strings.TrimSpace(*webhookConfig.WebhookURL)
	if _, err := validatedDeviceWebhookURL(targetURL); err != nil {
		return domainSpool.ErrInvalidEnvelope
	}
	deviceID, _ := payload["device_id"].(string)
	sessionID, _ := payload["session_id"].(string)
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(eventName) == "" {
		return domainSpool.ErrInvalidEnvelope
	}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("serialize managed webhook envelope: %w", err)
	}
	if len(rawBody) == 0 || len(rawBody) > options.MaxPayload {
		return domainSpool.ErrPayloadTooLarge
	}
	messageID := managedWebhookSourceMessageID(payload, rawBody)
	protected, err := codec.Protect(ctx, &domainSpool.ProtectRequest{
		DeviceID:                  deviceID,
		SourceSessionID:           sessionID,
		EventName:                 eventName,
		MessageID:                 messageID,
		RawBody:                   append([]byte(nil), rawBody...),
		TargetURL:                 targetURL,
		WebhookSecretVersion:      options.SecretVersion,
		WebhookInsecureSkipVerify: webhookConfig.WebhookInsecureSkipVerify,
	})
	if err != nil {
		return fmt.Errorf("protect managed webhook delivery: %w", err)
	}
	if err := validateProtectedManagedWebhook(protected, eventName, options.MaxPayload); err != nil {
		return err
	}
	_, _, err = repo.EnqueueManagedWebhookDelivery(ctx, &domainSpool.EnqueueRequest{
		DeliveryID:          protected.DeliveryID,
		DeviceDigest:        protected.DeviceDigest,
		SourceSessionDigest: protected.SourceSessionDigest,
		EventName:           protected.EventName,
		MessageIDDigest:     protected.MessageIDDigest,
		BodyHash:            protected.BodyHash,
		PayloadCiphertext:   append([]byte(nil), protected.PayloadCiphertext...),
		PayloadKeyVersion:   protected.PayloadKeyVersion,
		SecretVersion:       protected.SecretVersion,
		MaxAttempts:         options.MaxAttempts,
		MaxAge:              options.MaxAge,
	})
	if err != nil {
		return fmt.Errorf("commit managed webhook delivery: %w", err)
	}
	return nil
}

func managedWebhookSourceMessageID(payload map[string]any, rawBody []byte) string {
	if nested, ok := payload["payload"].(map[string]any); ok {
		for _, key := range []string{"id", "message_id", "deleted_message_id", "original_message_id", "call_id", "reacted_message_id"} {
			if value := strings.TrimSpace(fmt.Sprint(nested[key])); value != "" && value != "<nil>" {
				return value
			}
		}
	}
	for _, key := range []string{"id", "message_id"} {
		if value := strings.TrimSpace(fmt.Sprint(payload[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	sum := sha256.Sum256(rawBody)
	return "body:" + hex.EncodeToString(sum[:])
}

func validateProtectedManagedWebhook(value *domainSpool.ProtectedDelivery, eventName string, maxPayload int) error {
	if value == nil || strings.TrimSpace(value.DeliveryID) == "" || strings.TrimSpace(value.DeviceDigest) == "" ||
		strings.TrimSpace(value.SourceSessionDigest) == "" || value.EventName != eventName ||
		strings.TrimSpace(value.MessageIDDigest) == "" || strings.TrimSpace(value.BodyHash) == "" ||
		len(value.PayloadCiphertext) == 0 || len(value.PayloadCiphertext) > maxPayload+64*1024 ||
		strings.TrimSpace(value.PayloadKeyVersion) == "" || strings.TrimSpace(value.SecretVersion) == "" {
		return domainSpool.ErrInvalidEnvelope
	}
	return nil
}

type managedWebhookWorkerOptions struct {
	Owner        string
	Lease        time.Duration
	PollInterval time.Duration
	HTTPTimeout  time.Duration
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	MaxPayload   int
}

func (o managedWebhookWorkerOptions) normalized() managedWebhookWorkerOptions {
	if strings.TrimSpace(o.Owner) == "" {
		o.Owner = "gowa-" + uuid.NewString()
	}
	if o.Lease <= 0 {
		o.Lease = 30 * time.Second
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.HTTPTimeout <= 0 {
		o.HTTPTimeout = 8 * time.Second
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 5 * time.Minute
	}
	if o.MaxPayload <= 0 {
		o.MaxPayload = defaultManagedWebhookMaxPayload
	}
	return o
}

type managedWebhookSpoolWorker struct {
	repo             domainSpool.Repository
	codec            domainSpool.Codec
	options          managedWebhookWorkerOptions
	onStorageFailure func()
	wake             chan struct{}
	cancel           context.CancelFunc
	done             chan struct{}
	mu               sync.Mutex
}

var managedWebhookRuntime struct {
	sync.RWMutex
	codec   domainSpool.Codec
	repo    domainSpool.Repository
	worker  *managedWebhookSpoolWorker
	healthy bool
}

// InstallManagedWebhookCodec installs the external per-device keyring/codec.
// GOWA intentionally ships no plaintext or global-secret fallback. The caller
// must install this before enabling WHATSAPP_WEBHOOK_DEVICE_FAIL_CLOSED.
func InstallManagedWebhookCodec(codec domainSpool.Codec) error {
	if codec == nil {
		return domainSpool.ErrCodecUnavailable
	}
	managedWebhookRuntime.Lock()
	defer managedWebhookRuntime.Unlock()
	if managedWebhookRuntime.worker != nil {
		return fmt.Errorf("managed webhook codec cannot be replaced while worker is running")
	}
	managedWebhookRuntime.codec = codec
	return nil
}

// StartManagedWebhookSpoolWorker wires the repository and starts the durable
// delivery worker. With the managed gate disabled it is a no-op. With the gate
// enabled, a missing codec/repository is a readiness failure, never a fallback
// to the legacy direct goroutine.
func StartManagedWebhookSpoolWorker(repo domainChatStorage.IChatStorageRepository) error {
	if !config.WhatsappWebhookDeviceFailClosed {
		return nil
	}
	spoolRepo, ok := repo.(domainSpool.Repository)
	if !ok || spoolRepo == nil {
		return fmt.Errorf("managed webhook spool repository unavailable")
	}
	managedWebhookRuntime.Lock()
	defer managedWebhookRuntime.Unlock()
	if managedWebhookRuntime.codec == nil {
		return domainSpool.ErrCodecUnavailable
	}
	if managedWebhookRuntime.worker != nil {
		return nil
	}
	worker := newManagedWebhookSpoolWorker(spoolRepo, managedWebhookRuntime.codec, managedWebhookWorkerOptions{})
	worker.onStorageFailure = markManagedWebhookSpoolUnready
	if err := worker.Start(context.Background()); err != nil {
		return err
	}
	managedWebhookRuntime.repo = spoolRepo
	managedWebhookRuntime.worker = worker
	managedWebhookRuntime.healthy = true
	return nil
}

func StopManagedWebhookSpoolWorker(ctx context.Context) error {
	managedWebhookRuntime.RLock()
	worker := managedWebhookRuntime.worker
	managedWebhookRuntime.RUnlock()
	if worker == nil {
		return nil
	}
	err := worker.Stop(ctx)
	managedWebhookRuntime.Lock()
	if managedWebhookRuntime.worker == worker {
		managedWebhookRuntime.worker = nil
		managedWebhookRuntime.repo = nil
		managedWebhookRuntime.healthy = false
	}
	managedWebhookRuntime.Unlock()
	return err
}

// ManagedWebhookSpoolReady participates in HTTP readiness. A configured
// managed path is never considered ready while encryption/keyring or durable
// worker wiring is absent.
func ManagedWebhookSpoolReady() bool {
	if !config.WhatsappWebhookDeviceFailClosed {
		return true
	}
	managedWebhookRuntime.RLock()
	defer managedWebhookRuntime.RUnlock()
	return managedWebhookRuntime.codec != nil && managedWebhookRuntime.repo != nil && managedWebhookRuntime.worker != nil && managedWebhookRuntime.healthy
}

func markManagedWebhookSpoolUnready() {
	managedWebhookRuntime.Lock()
	managedWebhookRuntime.healthy = false
	managedWebhookRuntime.Unlock()
}

func enqueueManagedWebhookWithRuntime(ctx context.Context, payload map[string]any, eventName string, webhookConfig *domainChatStorage.DeviceWebhookConfig) error {
	managedWebhookRuntime.RLock()
	repo := managedWebhookRuntime.repo
	codec := managedWebhookRuntime.codec
	worker := managedWebhookRuntime.worker
	managedWebhookRuntime.RUnlock()
	if repo == nil || codec == nil || worker == nil {
		markManagedWebhookSpoolUnready()
		return domainSpool.ErrCodecUnavailable
	}
	if err := enqueueManagedWebhookPayload(ctx, repo, codec, payload, eventName, webhookConfig, managedWebhookAdmissionOptions{}); err != nil {
		markManagedWebhookSpoolUnready()
		return err
	}
	worker.Wake()
	return nil
}

// dispatchWebhookForward preserves the legacy asynchronous path while making
// managed admission synchronous. Therefore no managed delivery goroutine is
// created before the SQLite commit boundary.
func dispatchWebhookForward(ctx context.Context, task func(context.Context) error) {
	if task == nil {
		return
	}
	if config.WhatsappWebhookDeviceFailClosed {
		admissionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := task(admissionCtx); err != nil {
			markManagedWebhookSpoolUnready()
			logrus.Error("CRITICAL managed webhook was not durably admitted; zero-loss guarantee did not begin")
		}
		return
	}
	go func() {
		legacyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := task(legacyCtx); err != nil {
			logrus.Warn("Legacy webhook forwarding failed")
		}
	}()
}

func newManagedWebhookSpoolWorker(repo domainSpool.Repository, codec domainSpool.Codec, options managedWebhookWorkerOptions) *managedWebhookSpoolWorker {
	return &managedWebhookSpoolWorker{
		repo: repo, codec: codec, options: options.normalized(),
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
}

func (w *managedWebhookSpoolWorker) Start(parent context.Context) error {
	if w == nil || w.repo == nil || w.codec == nil {
		return domainSpool.ErrCodecUnavailable
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	go w.run(ctx)
	return nil
}

func (w *managedWebhookSpoolWorker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	cancel := w.cancel
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *managedWebhookSpoolWorker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *managedWebhookSpoolWorker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		for {
			processed, err := w.processOne(ctx)
			if err != nil {
				if w.onStorageFailure != nil {
					w.onStorageFailure()
				}
				logrus.Error("CRITICAL managed webhook spool worker storage transition failed")
				break
			}
			if !processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

func (w *managedWebhookSpoolWorker) processOne(ctx context.Context) (bool, error) {
	delivery, err := w.repo.ClaimManagedWebhookDelivery(ctx, w.options.Owner, w.options.Lease)
	if err != nil || delivery == nil {
		return false, err
	}
	envelope, err := w.codec.Open(ctx, delivery)
	if errors.Is(err, domainSpool.ErrCodecUnavailable) {
		result, updated, retryErr := w.repo.RetryManagedWebhookDelivery(ctx, delivery.ID, delivery.Owner, delivery.FenceToken, "key_unavailable", w.backoff(delivery.AttemptCount))
		if retryErr == nil && updated && result == domainSpool.StatusDead {
			logrus.Warn("Managed webhook delivery exhausted retry budget while key material was unavailable")
		}
		return true, retryErr
	}
	if err != nil || validateManagedWebhookEnvelope(delivery, envelope, w.options.MaxPayload) != nil {
		updated, markErr := w.repo.DeadLetterManagedWebhookDelivery(ctx, delivery.ID, delivery.Owner, delivery.FenceToken, "ciphertext_invalid")
		if markErr != nil {
			return true, markErr
		}
		if updated {
			logrus.Error("CRITICAL managed webhook delivery moved to dead-letter: protected payload unavailable")
		}
		return true, nil
	}

	status, retryAfter := w.deliver(ctx, envelope)
	switch {
	case status == "success":
		_, err = w.repo.CompleteManagedWebhookDelivery(ctx, delivery.ID, delivery.Owner, delivery.FenceToken)
		return true, err
	case status == "http_redirect" || status == "http_client_error":
		updated, markErr := w.repo.DeadLetterManagedWebhookDelivery(ctx, delivery.ID, delivery.Owner, delivery.FenceToken, status)
		if markErr == nil && updated {
			logrus.Warn("Managed webhook delivery moved to dead-letter after terminal response")
		}
		return true, markErr
	default:
		delay := w.backoff(delivery.AttemptCount)
		if retryAfter > delay {
			delay = retryAfter
		}
		if delay > w.options.MaxBackoff {
			delay = w.options.MaxBackoff
		}
		result, updated, retryErr := w.repo.RetryManagedWebhookDelivery(ctx, delivery.ID, delivery.Owner, delivery.FenceToken, status, delay)
		if retryErr == nil && updated && result == domainSpool.StatusDead {
			logrus.Warn("Managed webhook delivery exhausted retry budget and moved to dead-letter")
		}
		return true, retryErr
	}
}

func validateManagedWebhookEnvelope(delivery *domainSpool.Delivery, envelope *domainSpool.Envelope, maxPayload int) error {
	if delivery == nil || envelope == nil || len(envelope.RawBody) == 0 || len(envelope.RawBody) > maxPayload ||
		envelope.DeliveryID != delivery.DeliveryID || envelope.SecretVersion != delivery.SecretVersion ||
		strings.TrimSpace(envelope.SignatureHeader) == "" || strings.TrimSpace(envelope.Signature) == "" {
		return domainSpool.ErrInvalidEnvelope
	}
	parsed, err := url.ParseRequestURI(strings.TrimSpace(envelope.TargetURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return domainSpool.ErrInvalidEnvelope
	}
	return nil
}

func (w *managedWebhookSpoolWorker) deliver(ctx context.Context, envelope *domainSpool.Envelope) (string, time.Duration) {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || baseTransport == nil {
		return "http_client_error", 0
	}
	transport := baseTransport.Clone()
	defer transport.CloseIdleConnections()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: envelope.WebhookInsecureSkipVerify}
	client := &http.Client{
		Timeout:   w.options.HTTPTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, envelope.TargetURL, bytes.NewReader(envelope.RawBody))
	if err != nil {
		return "http_client_error", 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(envelope.SignatureHeader, envelope.Signature)
	req.Header.Set("X-Leo-Webhook-Secret-Version", envelope.SecretVersion)
	req.Header.Set("X-Leo-Delivery-ID", envelope.DeliveryID)
	resp, err := client.Do(req)
	if err != nil {
		return "network_error", 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "success", 0
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return "http_redirect", 0
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests:
		return "http_retryable", boundedRetryAfter(resp.Header.Get("Retry-After"), time.Now(), w.options.MaxBackoff)
	case resp.StatusCode >= 500:
		return "http_retryable", boundedRetryAfter(resp.Header.Get("Retry-After"), time.Now(), w.options.MaxBackoff)
	default:
		return "http_client_error", 0
	}
}

func (w *managedWebhookSpoolWorker) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := w.options.BaseBackoff
	for i := 1; i < attempt && delay < w.options.MaxBackoff/2; i++ {
		delay *= 2
	}
	if delay > w.options.MaxBackoff {
		return w.options.MaxBackoff
	}
	return delay
}

func boundedRetryAfter(value string, now time.Time, maximum time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		result := time.Duration(seconds) * time.Second
		if result > maximum {
			return maximum
		}
		return result
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	result := when.Sub(now)
	if result < 0 {
		return 0
	}
	if result > maximum {
		return maximum
	}
	return result
}
