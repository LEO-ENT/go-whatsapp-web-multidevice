// Package webhookspool defines the durable source-side delivery contract for
// managed per-device webhooks. It deliberately contains no crypto, HTTP, SQL,
// or process-global configuration.
package webhookspool

import (
	"context"
	"errors"
	"time"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusRetry      Status = "retry"
	StatusCompleted  Status = "completed"
	StatusDead       Status = "dead"
)

var (
	ErrCodecUnavailable = errors.New("managed webhook payload codec unavailable")
	// ErrRepositoryBusy identifies transient SQLite writer contention after the
	// claim path exhausted its bounded in-call retry. Admission deliberately
	// does not use this sentinel: a failed durable commit remains fail-closed.
	ErrRepositoryBusy  = errors.New("managed webhook spool repository busy")
	ErrPayloadTooLarge = errors.New("managed webhook payload exceeds configured limit")
	ErrInvalidEnvelope = errors.New("managed webhook envelope is invalid")
)

// Envelope is the exact, encrypted-at-rest delivery snapshot. Retries must use
// these RawBody bytes and configuration fields verbatim; they must never
// re-marshal the original event or re-read mutable device configuration.
type Envelope struct {
	RawBody                   []byte `json:"raw_body"`
	TargetURL                 string `json:"target_url"`
	Signature                 string `json:"signature"`
	SignatureHeader           string `json:"signature_header"`
	SecretVersion             string `json:"secret_version"`
	DeliveryID                string `json:"delivery_id"`
	WebhookInsecureSkipVerify bool   `json:"webhook_insecure_skip_verify"`
}

// ProtectRequest contains sensitive values only at the codec boundary. A
// production codec/keyring is responsible for per-device keyed digests and an
// authenticated encryption envelope. None of these plaintext values may be
// persisted or logged by the spool repository.
type ProtectRequest struct {
	DeviceID                  string
	SourceSessionID           string
	EventName                 string
	MessageID                 string
	RawBody                   []byte
	TargetURL                 string
	WebhookSecretVersion      string
	WebhookInsecureSkipVerify bool
}

// ProtectedDelivery is the only representation accepted by durable storage.
// Identity fields are deterministic, purpose-separated keyed digests. BodyHash
// is likewise keyed; it is not a plain hash of message content.
type ProtectedDelivery struct {
	DeliveryID          string
	DeviceDigest        string
	SourceSessionDigest string
	EventName           string
	MessageIDDigest     string
	BodyHash            string
	PayloadCiphertext   []byte
	PayloadKeyVersion   string
	SecretVersion       string
}

// Codec is intentionally an injected contract. GOWA does not currently own a
// production tenant/device keyring, so the managed route must fail closed until
// a conforming implementation is installed. Reusing the webhook signing secret
// as an encryption/digest key is forbidden.
type Codec interface {
	Protect(context.Context, *ProtectRequest) (*ProtectedDelivery, error)
	Open(context.Context, *Delivery) (*Envelope, error)
}

type KeyMaterial struct {
	Version       string
	EncryptionKey []byte
	// DigestKey is purpose-separated from encryption/signing material and must
	// remain stable across ordinary payload/signing rotations so tombstones keep
	// their deterministic identity. Destroying it is an explicit RTBF tradeoff.
	DigestKey            []byte
	WebhookSecret        []byte
	WebhookSecretVersion string
}

// Keyring resolves per-device material. Current is used only at admission;
// Resolve must keep old versions available while any non-terminal spool row
// references them. Resolve only needs to return the requested payload
// encryption version; exact signatures are already inside the ciphertext.
// Implementations must not derive these keys from webhook signing secrets or
// process-global defaults.
type Keyring interface {
	Current(context.Context, string) (*KeyMaterial, error)
	Resolve(context.Context, string, string) (*KeyMaterial, error)
}

type EnqueueRequest struct {
	DeliveryID          string
	DeviceDigest        string
	SourceSessionDigest string
	EventName           string
	MessageIDDigest     string
	BodyHash            string
	PayloadCiphertext   []byte
	PayloadKeyVersion   string
	SecretVersion       string
	MaxAttempts         int
	MaxAge              time.Duration
}

type Delivery struct {
	ID                  int64
	DeliveryID          string
	DeviceDigest        string
	SourceSessionDigest string
	EventName           string
	MessageIDDigest     string
	BodyHash            string
	PayloadCiphertext   []byte
	PayloadKeyVersion   string
	SecretVersion       string
	Status              Status
	AttemptCount        int
	MaxAttempts         int
	NextAttemptAt       time.Time
	DeadlineAt          time.Time
	Owner               string
	LeaseUntil          *time.Time
	FenceToken          int64
	LastErrorCode       string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CompletedAt         *time.Time
	DeadAt              *time.Time
	PayloadPurgedAt     *time.Time
}

// Repository transitions are owner+fence CAS operations. All eligibility,
// lease, retry, and terminal timestamps are derived from the SQLite DB clock.
type Repository interface {
	EnqueueManagedWebhookDelivery(context.Context, *EnqueueRequest) (*Delivery, bool, error)
	ClaimManagedWebhookDelivery(context.Context, string, time.Duration) (*Delivery, error)
	CompleteManagedWebhookDelivery(context.Context, int64, string, int64) (bool, error)
	RetryManagedWebhookDelivery(context.Context, int64, string, int64, string, time.Duration) (Status, bool, error)
	DeadLetterManagedWebhookDelivery(context.Context, int64, string, int64, string) (bool, error)
	GetManagedWebhookDelivery(context.Context, int64) (*Delivery, error)
	ListManagedWebhookDeadLetters(context.Context, int) ([]*Delivery, error)
	PurgeTerminalManagedWebhookPayloads(context.Context, time.Time, int) (int64, error)
}
