package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	domainSpool "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/webhookspool"
)

type managedCodecTestKeyring struct {
	material *domainSpool.KeyMaterial
	resolve  *domainSpool.KeyMaterial
}

func (k *managedCodecTestKeyring) Current(context.Context, string) (*domainSpool.KeyMaterial, error) {
	if k.material == nil {
		return nil, errors.New("missing current key")
	}
	return k.material, nil
}

func (k *managedCodecTestKeyring) Resolve(context.Context, string, string) (*domainSpool.KeyMaterial, error) {
	if k.resolve != nil {
		return k.resolve, nil
	}
	if k.material == nil {
		return nil, errors.New("unknown key")
	}
	return k.material, nil
}

func TestManagedWebhookEnvelopeCodecProtectOpenAndDeterministicIdentity(t *testing.T) {
	keys := &domainSpool.KeyMaterial{
		Version:              "device-key-v4",
		EncryptionKey:        bytes.Repeat([]byte{0x42}, 32),
		DigestKey:            bytes.Repeat([]byte{0x24}, 32),
		WebhookSecret:        []byte("per-device-signing-secret"),
		WebhookSecretVersion: "sign-v8",
	}
	keyring := &managedCodecTestKeyring{material: keys}
	codec, err := NewManagedWebhookEnvelopeCodec(keyring)
	if err != nil {
		t.Fatalf("construct codec: %v", err)
	}
	req := &domainSpool.ProtectRequest{
		DeviceID:             "device-jid-sensitive",
		SourceSessionID:      "tenant-session-sensitive",
		EventName:            "message",
		MessageID:            "wa-message-sensitive",
		RawBody:              []byte(`{"event":"message","payload":{"body":"pii"}}`),
		TargetURL:            "https://tenant-sensitive.invalid/hook?token=secret",
		WebhookSecretVersion: "sign-v8",
	}
	first, err := codec.Protect(context.Background(), req)
	if err != nil {
		t.Fatalf("protect first: %v", err)
	}
	second, err := codec.Protect(context.Background(), req)
	if err != nil {
		t.Fatalf("protect second: %v", err)
	}
	if first.DeliveryID != second.DeliveryID || first.DeviceDigest != second.DeviceDigest ||
		first.SourceSessionDigest != second.SourceSessionDigest || first.MessageIDDigest != second.MessageIDDigest ||
		first.BodyHash != second.BodyHash {
		t.Fatalf("identity digests are not deterministic: first=%+v second=%+v", first, second)
	}
	if bytes.Equal(first.PayloadCiphertext, second.PayloadCiphertext) {
		t.Fatal("AEAD ciphertext reused a nonce for identical plaintext")
	}
	for _, forbidden := range [][]byte{req.RawBody, []byte(req.TargetURL), keys.WebhookSecret, []byte(req.DeviceID), []byte(req.SourceSessionID)} {
		if bytes.Contains(first.PayloadCiphertext, forbidden) {
			t.Fatalf("ciphertext contains sensitive plaintext %q", forbidden)
		}
	}

	delivery := &domainSpool.Delivery{
		DeliveryID:          first.DeliveryID,
		DeviceDigest:        first.DeviceDigest,
		SourceSessionDigest: first.SourceSessionDigest,
		EventName:           first.EventName,
		MessageIDDigest:     first.MessageIDDigest,
		BodyHash:            first.BodyHash,
		PayloadCiphertext:   first.PayloadCiphertext,
		PayloadKeyVersion:   first.PayloadKeyVersion,
		SecretVersion:       first.SecretVersion,
	}
	envelope, err := codec.Open(context.Background(), delivery)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(envelope.RawBody, req.RawBody) || envelope.TargetURL != req.TargetURL ||
		envelope.SecretVersion != req.WebhookSecretVersion || envelope.DeliveryID != first.DeliveryID {
		t.Fatalf("opened envelope changed exact delivery: %+v", envelope)
	}
	mac := hmac.New(sha256.New, keys.WebhookSecret)
	_, _ = mac.Write(req.RawBody)
	wantSignature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if envelope.Signature != wantSignature {
		t.Fatalf("signature was not computed over exact raw bytes: got %q want %q", envelope.Signature, wantSignature)
	}

	// Retry/open requires only the retained payload encryption version. Signing
	// and digest material is not re-read or used to recompute the snapshot.
	keyring.resolve = &domainSpool.KeyMaterial{Version: keys.Version, EncryptionKey: keys.EncryptionKey}
	if _, err := codec.Open(context.Background(), delivery); err != nil {
		t.Fatalf("open unnecessarily required signing/digest material: %v", err)
	}
}

func TestManagedWebhookEnvelopeCodecRejectsTamperAndWrongKey(t *testing.T) {
	keys := &domainSpool.KeyMaterial{
		Version:              "device-key-v4",
		EncryptionKey:        bytes.Repeat([]byte{0x42}, 32),
		DigestKey:            bytes.Repeat([]byte{0x24}, 32),
		WebhookSecret:        []byte("per-device-signing-secret"),
		WebhookSecretVersion: "sign-v1",
	}
	keyring := &managedCodecTestKeyring{material: keys}
	codec, err := NewManagedWebhookEnvelopeCodec(keyring)
	if err != nil {
		t.Fatalf("construct codec: %v", err)
	}
	protected, err := codec.Protect(context.Background(), &domainSpool.ProtectRequest{
		DeviceID: "device", SourceSessionID: "session", EventName: "message", MessageID: "m1",
		RawBody: []byte(`{"event":"message"}`), TargetURL: "https://example.invalid/hook",
		WebhookSecretVersion: "sign-v1",
	})
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	delivery := &domainSpool.Delivery{
		DeliveryID: protected.DeliveryID, DeviceDigest: protected.DeviceDigest,
		SourceSessionDigest: protected.SourceSessionDigest, EventName: protected.EventName,
		MessageIDDigest: protected.MessageIDDigest, BodyHash: protected.BodyHash,
		PayloadCiphertext: append([]byte(nil), protected.PayloadCiphertext...),
		PayloadKeyVersion: protected.PayloadKeyVersion, SecretVersion: protected.SecretVersion,
	}
	delivery.PayloadCiphertext[len(delivery.PayloadCiphertext)-1] ^= 0xff
	if _, err := codec.Open(context.Background(), delivery); err == nil {
		t.Fatal("tampered ciphertext authenticated successfully")
	}
	delivery.PayloadCiphertext = protected.PayloadCiphertext
	keyring.resolve = &domainSpool.KeyMaterial{
		Version: keys.Version, EncryptionKey: bytes.Repeat([]byte{0x99}, 32), DigestKey: keys.DigestKey,
		WebhookSecret: keys.WebhookSecret, WebhookSecretVersion: keys.WebhookSecretVersion,
	}
	if _, err := codec.Open(context.Background(), delivery); err == nil {
		t.Fatal("ciphertext opened under wrong encryption key")
	}
}

func TestManagedWebhookEnvelopeCodecRejectsKeyReuseAndVersionMismatch(t *testing.T) {
	shared := bytes.Repeat([]byte{0x42}, 32)
	keyring := &managedCodecTestKeyring{material: &domainSpool.KeyMaterial{
		Version: "device-key-v1", EncryptionKey: shared, DigestKey: shared,
		WebhookSecret: []byte("per-device-signing-secret"), WebhookSecretVersion: "sign-v1",
	}}
	codec, err := NewManagedWebhookEnvelopeCodec(keyring)
	if err != nil {
		t.Fatalf("construct codec: %v", err)
	}
	req := &domainSpool.ProtectRequest{
		DeviceID: "device", SourceSessionID: "session", EventName: "message", MessageID: "m1",
		RawBody: []byte(`{"event":"message"}`), TargetURL: "https://example.invalid/hook",
		WebhookSecretVersion: "sign-v1",
	}
	if _, err := codec.Protect(context.Background(), req); !errors.Is(err, domainSpool.ErrCodecUnavailable) {
		t.Fatalf("reused encryption/digest key was accepted: %v", err)
	}

	keyring.material = &domainSpool.KeyMaterial{
		Version: "device-key-v1", EncryptionKey: bytes.Repeat([]byte{0x42}, 32),
		DigestKey: bytes.Repeat([]byte{0x24}, 32), WebhookSecret: []byte("per-device-signing-secret"),
		WebhookSecretVersion: "sign-v2",
	}
	if _, err := codec.Protect(context.Background(), req); !errors.Is(err, domainSpool.ErrCodecUnavailable) {
		t.Fatalf("requested signing version mismatch was accepted: %v", err)
	}
}

func TestNewManagedWebhookEnvelopeCodecRejectsNilAndTypedNilKeyrings(t *testing.T) {
	var nilKeyring domainSpool.Keyring
	if codec, err := NewManagedWebhookEnvelopeCodec(nilKeyring); codec != nil || !errors.Is(err, domainSpool.ErrCodecUnavailable) {
		t.Fatalf("nil keyring constructed a codec: codec=%T err=%v", codec, err)
	}

	var typedNil *managedCodecTestKeyring
	var typedNilKeyring domainSpool.Keyring = typedNil
	if codec, err := NewManagedWebhookEnvelopeCodec(typedNilKeyring); codec != nil || !errors.Is(err, domainSpool.ErrCodecUnavailable) {
		t.Fatalf("typed-nil keyring constructed a codec: codec=%T err=%v", codec, err)
	}
}
