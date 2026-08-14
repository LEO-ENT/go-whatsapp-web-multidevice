package whatsapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainSpool "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/webhookspool"
)

type managedSpoolTestCodec struct {
	protectFn func(*domainSpool.ProtectRequest) (*domainSpool.ProtectedDelivery, error)
	openFn    func(*domainSpool.Delivery) (*domainSpool.Envelope, error)
}

func (c *managedSpoolTestCodec) Protect(_ context.Context, req *domainSpool.ProtectRequest) (*domainSpool.ProtectedDelivery, error) {
	return c.protectFn(req)
}

func (c *managedSpoolTestCodec) Open(_ context.Context, delivery *domainSpool.Delivery) (*domainSpool.Envelope, error) {
	return c.openFn(delivery)
}

type managedSpoolTestRepo struct {
	mu          sync.Mutex
	claim       *domainSpool.Delivery
	claimErr    error
	claimSeen   chan struct{}
	enqueued    *domainSpool.EnqueueRequest
	completed   int
	retried     int
	dead        int
	errorCode   string
	completeErr error
	retryStatus domainSpool.Status
}

func (r *managedSpoolTestRepo) EnqueueManagedWebhookDelivery(_ context.Context, req *domainSpool.EnqueueRequest) (*domainSpool.Delivery, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copyReq := *req
	copyReq.PayloadCiphertext = append([]byte(nil), req.PayloadCiphertext...)
	r.enqueued = &copyReq
	return &domainSpool.Delivery{ID: 1, DeliveryID: req.DeliveryID, Status: domainSpool.StatusPending}, true, nil
}

func (r *managedSpoolTestRepo) ClaimManagedWebhookDelivery(context.Context, string, time.Duration) (*domainSpool.Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimSeen != nil {
		select {
		case r.claimSeen <- struct{}{}:
		default:
		}
	}
	if r.claimErr != nil {
		err := r.claimErr
		r.claimErr = nil
		return nil, err
	}
	claim := r.claim
	r.claim = nil
	return claim, nil
}

func (r *managedSpoolTestRepo) CompleteManagedWebhookDelivery(context.Context, int64, string, int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.completeErr != nil {
		return false, r.completeErr
	}
	r.completed++
	return true, nil
}

func (r *managedSpoolTestRepo) RetryManagedWebhookDelivery(_ context.Context, _ int64, _ string, _ int64, code string, _ time.Duration) (domainSpool.Status, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retried++
	r.errorCode = code
	if r.retryStatus == "" {
		r.retryStatus = domainSpool.StatusRetry
	}
	return r.retryStatus, true, nil
}

func (r *managedSpoolTestRepo) DeadLetterManagedWebhookDelivery(_ context.Context, _ int64, _ string, _ int64, code string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dead++
	r.errorCode = code
	return true, nil
}

func (r *managedSpoolTestRepo) GetManagedWebhookDelivery(context.Context, int64) (*domainSpool.Delivery, error) {
	return nil, nil
}

func (r *managedSpoolTestRepo) ListManagedWebhookDeadLetters(context.Context, int) ([]*domainSpool.Delivery, error) {
	return nil, nil
}

func (r *managedSpoolTestRepo) PurgeTerminalManagedWebhookPayloads(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func managedClaim() *domainSpool.Delivery {
	return &domainSpool.Delivery{
		ID:                1,
		DeliveryID:        "opaque-delivery",
		PayloadCiphertext: []byte("ciphertext"),
		PayloadKeyVersion: "key-v2",
		SecretVersion:     "secret-v9",
		Status:            domainSpool.StatusProcessing,
		AttemptCount:      1,
		MaxAttempts:       5,
		Owner:             "worker-a",
		FenceToken:        7,
	}
}

func TestManagedWebhookAdmissionPersistsBeforeAnyHTTP(t *testing.T) {
	repo := &managedSpoolTestRepo{}
	var protected *domainSpool.ProtectRequest
	codec := &managedSpoolTestCodec{
		protectFn: func(req *domainSpool.ProtectRequest) (*domainSpool.ProtectedDelivery, error) {
			protected = req
			return &domainSpool.ProtectedDelivery{
				DeliveryID:          "opaque-delivery",
				DeviceDigest:        "device-digest",
				SourceSessionDigest: "session-digest",
				EventName:           req.EventName,
				MessageIDDigest:     "message-digest",
				BodyHash:            "body-digest",
				PayloadCiphertext:   []byte("ciphertext"),
				PayloadKeyVersion:   "key-v2",
				SecretVersion:       "secret-v9",
			}, nil
		},
	}
	url := "https://managed.invalid/hook"
	cfg := &domainChatStorage.DeviceWebhookConfig{WebhookURL: &url, WebhookSecret: "write-only-secret"}
	payload := map[string]any{
		"event":      "message",
		"device_id":  "12345@s.whatsapp.net",
		"session_id": "tenant-slot-a",
		"payload":    map[string]any{"id": "wa-message-1", "body": "hello"},
	}

	if err := enqueueManagedWebhookPayload(context.Background(), repo, codec, payload, "message", cfg, managedWebhookAdmissionOptions{SecretVersion: "secret-v9"}); err != nil {
		t.Fatalf("enqueue managed payload: %v", err)
	}
	if protected == nil || string(protected.RawBody) == "" {
		t.Fatal("codec did not receive one serialized raw delivery")
	}
	if repo.enqueued == nil || string(repo.enqueued.PayloadCiphertext) != "ciphertext" {
		t.Fatalf("protected delivery was not persisted: %+v", repo.enqueued)
	}
}

func TestManagedWebhookWorkerDoesNotFollowRedirect(t *testing.T) {
	var redirected bool
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	defer sink.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", sink.URL+"?leak=1")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	repo := &managedSpoolTestRepo{claim: managedClaim()}
	codec := &managedSpoolTestCodec{openFn: func(*domainSpool.Delivery) (*domainSpool.Envelope, error) {
		return &domainSpool.Envelope{
			RawBody:         []byte(`{"event":"message"}`),
			TargetURL:       redirect.URL,
			Signature:       "sha256=opaque",
			SignatureHeader: "X-Hub-Signature-256",
			SecretVersion:   "secret-v9",
			DeliveryID:      "opaque-delivery",
		}, nil
	}}
	worker := newManagedWebhookSpoolWorker(repo, codec, managedWebhookWorkerOptions{Owner: "worker-a"})

	processed, err := worker.processOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process redirect: processed=%v err=%v", processed, err)
	}
	if redirected {
		t.Fatal("managed webhook client followed redirect and leaked delivery to Location")
	}
	if repo.dead != 1 || repo.errorCode != "http_redirect" || repo.completed != 0 || repo.retried != 0 {
		t.Fatalf("redirect did not terminally dead-letter: %+v", repo)
	}
}

func TestManagedWebhookWorkerRetriesNetworkAndDeadLettersCorruptCiphertext(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		repo := &managedSpoolTestRepo{claim: managedClaim()}
		codec := &managedSpoolTestCodec{openFn: func(*domainSpool.Delivery) (*domainSpool.Envelope, error) {
			return &domainSpool.Envelope{
				RawBody:         []byte(`{"event":"message"}`),
				TargetURL:       "http://127.0.0.1:1/managed",
				Signature:       "sha256=opaque",
				SignatureHeader: "X-Hub-Signature-256",
				SecretVersion:   "secret-v9",
				DeliveryID:      "opaque-delivery",
			}, nil
		}}
		worker := newManagedWebhookSpoolWorker(repo, codec, managedWebhookWorkerOptions{Owner: "worker-a", HTTPTimeout: 100 * time.Millisecond})
		processed, err := worker.processOne(context.Background())
		if err != nil || !processed || repo.retried != 1 || repo.errorCode != "network_error" {
			t.Fatalf("network failure was not retried safely: processed=%v err=%v repo=%+v", processed, err, repo)
		}
	})

	t.Run("ciphertext", func(t *testing.T) {
		repo := &managedSpoolTestRepo{claim: managedClaim()}
		codec := &managedSpoolTestCodec{openFn: func(*domainSpool.Delivery) (*domainSpool.Envelope, error) {
			return nil, errors.New("unknown key or corrupt ciphertext")
		}}
		worker := newManagedWebhookSpoolWorker(repo, codec, managedWebhookWorkerOptions{Owner: "worker-a"})
		processed, err := worker.processOne(context.Background())
		if err != nil || !processed || repo.dead != 1 || repo.errorCode != "ciphertext_invalid" {
			t.Fatalf("corrupt ciphertext was not terminally isolated: processed=%v err=%v repo=%+v", processed, err, repo)
		}
	})

	t.Run("keyring temporarily unavailable", func(t *testing.T) {
		repo := &managedSpoolTestRepo{claim: managedClaim()}
		codec := &managedSpoolTestCodec{openFn: func(*domainSpool.Delivery) (*domainSpool.Envelope, error) {
			return nil, domainSpool.ErrCodecUnavailable
		}}
		worker := newManagedWebhookSpoolWorker(repo, codec, managedWebhookWorkerOptions{Owner: "worker-a"})
		processed, err := worker.processOne(context.Background())
		if err != nil || !processed || repo.retried != 1 || repo.dead != 0 || repo.errorCode != "key_unavailable" {
			t.Fatalf("temporary keyring outage was not bounded-retryable: processed=%v err=%v repo=%+v", processed, err, repo)
		}
	})
}

func TestManagedWebhookWorkerReusesExactBodyDeliveryAndVersionOnRetry(t *testing.T) {
	type observed struct {
		body      string
		delivery  string
		version   string
		signature string
	}
	var mu sync.Mutex
	var requests []observed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, observed{
			body:      string(body),
			delivery:  r.Header.Get("X-Leo-Delivery-ID"),
			version:   r.Header.Get("X-Leo-Webhook-Secret-Version"),
			signature: r.Header.Get("X-Hub-Signature-256"),
		})
		attempt := len(requests)
		mu.Unlock()
		if attempt == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	claim := managedClaim()
	repo := &managedSpoolTestRepo{claim: claim}
	codec := &managedSpoolTestCodec{openFn: func(*domainSpool.Delivery) (*domainSpool.Envelope, error) {
		return &domainSpool.Envelope{
			RawBody:         []byte(`{"event":"message","payload":{"body":"exact"}}`),
			TargetURL:       server.URL,
			Signature:       "sha256=fixed-signature",
			SignatureHeader: "X-Hub-Signature-256",
			SecretVersion:   "secret-v9",
			DeliveryID:      "opaque-delivery",
		}, nil
	}}
	worker := newManagedWebhookSpoolWorker(repo, codec, managedWebhookWorkerOptions{Owner: "worker-a"})
	if processed, err := worker.processOne(context.Background()); err != nil || !processed || repo.retried != 1 {
		t.Fatalf("first attempt: processed=%v err=%v repo=%+v", processed, err, repo)
	}
	repo.mu.Lock()
	repo.claim = managedClaim()
	repo.mu.Unlock()
	if processed, err := worker.processOne(context.Background()); err != nil || !processed || repo.completed != 1 {
		t.Fatalf("retry attempt: processed=%v err=%v repo=%+v", processed, err, repo)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatalf("retry changed exact delivery snapshot: %+v", requests)
	}
}

func TestManagedWebhookWorkerReplaysAfter2xxBeforeCompletionCommit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := &managedSpoolTestRepo{claim: managedClaim(), completeErr: errors.New("simulated crash before completed commit")}
	codec := &managedSpoolTestCodec{openFn: func(*domainSpool.Delivery) (*domainSpool.Envelope, error) {
		return &domainSpool.Envelope{
			RawBody: []byte(`{"event":"message"}`), TargetURL: server.URL,
			Signature: "sha256=fixed", SignatureHeader: "X-Hub-Signature-256",
			SecretVersion: "secret-v9", DeliveryID: "opaque-delivery",
		}, nil
	}}
	worker := newManagedWebhookSpoolWorker(repo, codec, managedWebhookWorkerOptions{Owner: "worker-a"})
	if processed, err := worker.processOne(context.Background()); err == nil || !processed || requests != 1 {
		t.Fatalf("2xx/completion crash boundary was not surfaced: processed=%v err=%v requests=%d", processed, err, requests)
	}

	// A restart/expired-lease takeover presents the same protected snapshot.
	repo.mu.Lock()
	repo.completeErr = nil
	repo.claim = managedClaim()
	repo.mu.Unlock()
	if processed, err := worker.processOne(context.Background()); err != nil || !processed || requests != 2 || repo.completed != 1 {
		t.Fatalf("delivery did not converge after replay: processed=%v err=%v requests=%d repo=%+v", processed, err, requests, repo)
	}
}

func TestManagedWebhookWorkerHTTPOutcomePolicy(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		completed int
		retried   int
		dead      int
		code      string
	}{
		{name: "2xx", status: http.StatusAccepted, completed: 1},
		{name: "3xx", status: http.StatusPermanentRedirect, dead: 1, code: "http_redirect"},
		{name: "ordinary 4xx", status: http.StatusUnauthorized, dead: 1, code: "http_client_error"},
		{name: "408", status: http.StatusRequestTimeout, retried: 1, code: "http_retryable"},
		{name: "429", status: http.StatusTooManyRequests, retried: 1, code: "http_retryable"},
		{name: "5xx", status: http.StatusBadGateway, retried: 1, code: "http_retryable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "2")
				}
				w.WriteHeader(tt.status)
			}))
			defer server.Close()
			repo := &managedSpoolTestRepo{claim: managedClaim()}
			codec := &managedSpoolTestCodec{openFn: func(*domainSpool.Delivery) (*domainSpool.Envelope, error) {
				return &domainSpool.Envelope{
					RawBody: []byte(`{"event":"message"}`), TargetURL: server.URL,
					Signature: "sha256=opaque", SignatureHeader: "X-Hub-Signature-256",
					SecretVersion: "secret-v9", DeliveryID: "opaque-delivery",
				}, nil
			}}
			worker := newManagedWebhookSpoolWorker(repo, codec, managedWebhookWorkerOptions{Owner: "worker-a"})
			processed, err := worker.processOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("process outcome: processed=%v err=%v", processed, err)
			}
			if repo.completed != tt.completed || repo.retried != tt.retried || repo.dead != tt.dead || (tt.code != "" && repo.errorCode != tt.code) {
				t.Fatalf("unexpected outcome policy: repo=%+v expected completed=%d retried=%d dead=%d code=%q", repo, tt.completed, tt.retried, tt.dead, tt.code)
			}
		})
	}
}

func TestManagedWebhookSpoolWorkerStartsAndStopsIdempotently(t *testing.T) {
	repo := &managedSpoolTestRepo{}
	codec := &managedSpoolTestCodec{
		protectFn: func(*domainSpool.ProtectRequest) (*domainSpool.ProtectedDelivery, error) {
			return nil, errors.New("unused")
		},
		openFn: func(*domainSpool.Delivery) (*domainSpool.Envelope, error) { return nil, errors.New("unused") },
	}
	worker := newManagedWebhookSpoolWorker(repo, codec, managedWebhookWorkerOptions{PollInterval: time.Millisecond})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("second start: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("stop worker: %v", err)
	}
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

func TestManagedWebhookSpoolReadinessFailsClosedWithoutKeyringWorker(t *testing.T) {
	originalFlag := config.WhatsappWebhookDeviceFailClosed
	managedWebhookRuntime.Lock()
	originalCodec, originalRepo, originalWorker, originalHealthy := managedWebhookRuntime.codec, managedWebhookRuntime.repo, managedWebhookRuntime.worker, managedWebhookRuntime.healthy
	managedWebhookRuntime.codec, managedWebhookRuntime.repo, managedWebhookRuntime.worker, managedWebhookRuntime.healthy = nil, nil, nil, false
	managedWebhookRuntime.Unlock()
	defer func() {
		config.WhatsappWebhookDeviceFailClosed = originalFlag
		managedWebhookRuntime.Lock()
		managedWebhookRuntime.codec, managedWebhookRuntime.repo, managedWebhookRuntime.worker, managedWebhookRuntime.healthy = originalCodec, originalRepo, originalWorker, originalHealthy
		managedWebhookRuntime.Unlock()
	}()

	config.WhatsappWebhookDeviceFailClosed = true
	if ManagedWebhookSpoolReady() {
		t.Fatal("managed route reported ready without keyring/repository/worker")
	}
	config.WhatsappWebhookDeviceFailClosed = false
	if !ManagedWebhookSpoolReady() {
		t.Fatal("legacy route was coupled to the managed spool gate")
	}
}

func TestManagedWebhookCodecInstallAndReadinessRejectTypedNil(t *testing.T) {
	originalFlag := config.WhatsappWebhookDeviceFailClosed
	managedWebhookRuntime.Lock()
	originalCodec, originalRepo, originalWorker, originalHealthy := managedWebhookRuntime.codec, managedWebhookRuntime.repo, managedWebhookRuntime.worker, managedWebhookRuntime.healthy
	managedWebhookRuntime.codec, managedWebhookRuntime.repo, managedWebhookRuntime.worker, managedWebhookRuntime.healthy = nil, nil, nil, false
	managedWebhookRuntime.Unlock()
	defer func() {
		config.WhatsappWebhookDeviceFailClosed = originalFlag
		managedWebhookRuntime.Lock()
		managedWebhookRuntime.codec, managedWebhookRuntime.repo, managedWebhookRuntime.worker, managedWebhookRuntime.healthy = originalCodec, originalRepo, originalWorker, originalHealthy
		managedWebhookRuntime.Unlock()
	}()

	var typedNil *managedSpoolTestCodec
	if err := InstallManagedWebhookCodec(typedNil); !errors.Is(err, domainSpool.ErrCodecUnavailable) {
		t.Fatalf("typed-nil codec was installed: %v", err)
	}

	config.WhatsappWebhookDeviceFailClosed = true
	managedWebhookRuntime.Lock()
	managedWebhookRuntime.codec = typedNil
	managedWebhookRuntime.repo = &managedSpoolTestRepo{}
	managedWebhookRuntime.worker = &managedWebhookSpoolWorker{}
	managedWebhookRuntime.healthy = true
	managedWebhookRuntime.Unlock()
	if ManagedWebhookSpoolReady() {
		t.Fatal("typed-nil codec reported managed readiness green")
	}
}

func TestManagedWebhookSpoolWorkerDoesNotLatchReadinessOnTransientClaimContention(t *testing.T) {
	claimSeen := make(chan struct{}, 1)
	repo := &managedSpoolTestRepo{claimErr: domainSpool.ErrRepositoryBusy, claimSeen: claimSeen}
	codec := &managedSpoolTestCodec{}
	worker := newManagedWebhookSpoolWorker(repo, codec, managedWebhookWorkerOptions{PollInterval: time.Hour})
	storageFailed := make(chan struct{}, 1)
	worker.onStorageFailure = func() { storageFailed <- struct{}{} }
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	select {
	case <-claimSeen:
	case <-time.After(time.Second):
		t.Fatal("worker did not attempt a claim")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("stop worker: %v", err)
	}
	select {
	case <-storageFailed:
		t.Fatal("transient SQLite claim contention latched readiness red")
	default:
	}
}

func TestDispatchWebhookForwardLatchesReadinessOnAdmissionFailure(t *testing.T) {
	originalFlag := config.WhatsappWebhookDeviceFailClosed
	managedWebhookRuntime.Lock()
	originalCodec, originalRepo, originalWorker, originalHealthy := managedWebhookRuntime.codec, managedWebhookRuntime.repo, managedWebhookRuntime.worker, managedWebhookRuntime.healthy
	repo := &managedSpoolTestRepo{}
	codec := &managedSpoolTestCodec{
		protectFn: func(*domainSpool.ProtectRequest) (*domainSpool.ProtectedDelivery, error) {
			return nil, errors.New("unused")
		},
		openFn: func(*domainSpool.Delivery) (*domainSpool.Envelope, error) { return nil, errors.New("unused") },
	}
	managedWebhookRuntime.codec, managedWebhookRuntime.repo = codec, repo
	managedWebhookRuntime.worker, managedWebhookRuntime.healthy = &managedWebhookSpoolWorker{}, true
	managedWebhookRuntime.Unlock()
	defer func() {
		config.WhatsappWebhookDeviceFailClosed = originalFlag
		managedWebhookRuntime.Lock()
		managedWebhookRuntime.codec, managedWebhookRuntime.repo, managedWebhookRuntime.worker, managedWebhookRuntime.healthy = originalCodec, originalRepo, originalWorker, originalHealthy
		managedWebhookRuntime.Unlock()
	}()

	config.WhatsappWebhookDeviceFailClosed = true
	dispatchWebhookForward(context.Background(), func(context.Context) error {
		return errors.New("simulated pre-commit failure")
	})
	if ManagedWebhookSpoolReady() {
		t.Fatal("critical pre-commit failure did not latch managed readiness red")
	}
}
