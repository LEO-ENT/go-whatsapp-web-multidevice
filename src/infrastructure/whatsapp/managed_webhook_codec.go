package whatsapp

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	domainSpool "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/webhookspool"
)

const managedWebhookCodecVersion = "gowa-managed-spool-v1"

type managedWebhookEnvelopeCodec struct {
	keyring domainSpool.Keyring
}

func NewManagedWebhookEnvelopeCodec(keyring domainSpool.Keyring) domainSpool.Codec {
	return &managedWebhookEnvelopeCodec{keyring: keyring}
}

func (c *managedWebhookEnvelopeCodec) Protect(ctx context.Context, req *domainSpool.ProtectRequest) (*domainSpool.ProtectedDelivery, error) {
	if c == nil || c.keyring == nil || req == nil || strings.TrimSpace(req.DeviceID) == "" ||
		strings.TrimSpace(req.SourceSessionID) == "" || strings.TrimSpace(req.EventName) == "" ||
		strings.TrimSpace(req.MessageID) == "" || len(req.RawBody) == 0 || strings.TrimSpace(req.TargetURL) == "" {
		return nil, domainSpool.ErrInvalidEnvelope
	}
	material, err := c.keyring.Current(ctx, req.DeviceID)
	if err != nil {
		return nil, domainSpool.ErrCodecUnavailable
	}
	if err := validateManagedWebhookProtectKeyMaterial(material); err != nil {
		return nil, err
	}
	secretVersion := strings.TrimSpace(material.WebhookSecretVersion)
	if secretVersion == "" {
		return nil, domainSpool.ErrCodecUnavailable
	}
	if requested := strings.TrimSpace(req.WebhookSecretVersion); requested != "" && requested != secretVersion {
		return nil, domainSpool.ErrCodecUnavailable
	}

	deviceDigest := managedWebhookDigest(material.DigestKey, "device", []byte(req.DeviceID))
	sessionDigest := managedWebhookDigest(material.DigestKey, "source-session", []byte(req.SourceSessionID))
	messageDigest := managedWebhookDigest(material.DigestKey, "message-id", []byte(req.MessageID))
	bodyHash := managedWebhookDigest(material.DigestKey, "raw-body", req.RawBody)
	deliverySeed, err := json.Marshal([]string{deviceDigest, sessionDigest, req.EventName, messageDigest, bodyHash})
	if err != nil {
		return nil, domainSpool.ErrInvalidEnvelope
	}
	deliveryID := "gowa_" + managedWebhookDigest(material.DigestKey, "delivery-id", deliverySeed)
	signatureMAC := hmac.New(sha256.New, material.WebhookSecret)
	_, _ = signatureMAC.Write(req.RawBody)
	signature := "sha256=" + hex.EncodeToString(signatureMAC.Sum(nil))

	envelope := &domainSpool.Envelope{
		RawBody:                   append([]byte(nil), req.RawBody...),
		TargetURL:                 req.TargetURL,
		Signature:                 signature,
		SignatureHeader:           "X-Hub-Signature-256",
		SecretVersion:             secretVersion,
		DeliveryID:                deliveryID,
		WebhookInsecureSkipVerify: req.WebhookInsecureSkipVerify,
	}
	plaintext, err := json.Marshal(envelope)
	if err != nil {
		return nil, domainSpool.ErrInvalidEnvelope
	}
	metadata := &domainSpool.ProtectedDelivery{
		DeliveryID: deliveryID, DeviceDigest: deviceDigest, SourceSessionDigest: sessionDigest,
		EventName: req.EventName, MessageIDDigest: messageDigest, BodyHash: bodyHash,
		PayloadKeyVersion: material.Version, SecretVersion: secretVersion,
	}
	aad, err := managedWebhookAAD(metadata)
	if err != nil {
		return nil, domainSpool.ErrInvalidEnvelope
	}
	ciphertext, err := sealManagedWebhook(material.EncryptionKey, plaintext, aad)
	if err != nil {
		return nil, domainSpool.ErrCodecUnavailable
	}
	metadata.PayloadCiphertext = ciphertext
	return metadata, nil
}

func (c *managedWebhookEnvelopeCodec) Open(ctx context.Context, delivery *domainSpool.Delivery) (*domainSpool.Envelope, error) {
	if c == nil || c.keyring == nil || delivery == nil || len(delivery.PayloadCiphertext) == 0 {
		return nil, domainSpool.ErrInvalidEnvelope
	}
	material, err := c.keyring.Resolve(ctx, delivery.DeviceDigest, delivery.PayloadKeyVersion)
	if err != nil {
		return nil, domainSpool.ErrCodecUnavailable
	}
	if err := validateManagedWebhookEncryptionMaterial(material); err != nil || material.Version != delivery.PayloadKeyVersion {
		return nil, domainSpool.ErrCodecUnavailable
	}
	metadata := &domainSpool.ProtectedDelivery{
		DeliveryID: delivery.DeliveryID, DeviceDigest: delivery.DeviceDigest,
		SourceSessionDigest: delivery.SourceSessionDigest, EventName: delivery.EventName,
		MessageIDDigest: delivery.MessageIDDigest, BodyHash: delivery.BodyHash,
		PayloadKeyVersion: delivery.PayloadKeyVersion, SecretVersion: delivery.SecretVersion,
	}
	aad, err := managedWebhookAAD(metadata)
	if err != nil {
		return nil, domainSpool.ErrInvalidEnvelope
	}
	plaintext, err := openManagedWebhook(material.EncryptionKey, delivery.PayloadCiphertext, aad)
	if err != nil {
		return nil, domainSpool.ErrInvalidEnvelope
	}
	var envelope domainSpool.Envelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		return nil, domainSpool.ErrInvalidEnvelope
	}
	if envelope.DeliveryID != delivery.DeliveryID || envelope.SecretVersion != delivery.SecretVersion {
		return nil, domainSpool.ErrInvalidEnvelope
	}
	return &envelope, nil
}

func validateManagedWebhookEncryptionMaterial(material *domainSpool.KeyMaterial) error {
	if material == nil || strings.TrimSpace(material.Version) == "" || len(material.EncryptionKey) != 32 {
		return domainSpool.ErrCodecUnavailable
	}
	return nil
}

func validateManagedWebhookProtectKeyMaterial(material *domainSpool.KeyMaterial) error {
	if err := validateManagedWebhookEncryptionMaterial(material); err != nil ||
		len(material.DigestKey) < 32 || len(material.WebhookSecret) < 16 || strings.TrimSpace(material.WebhookSecretVersion) == "" {
		return domainSpool.ErrCodecUnavailable
	}
	if bytes.Equal(material.EncryptionKey, material.DigestKey) ||
		bytes.Equal(material.EncryptionKey, material.WebhookSecret) ||
		bytes.Equal(material.DigestKey, material.WebhookSecret) {
		return domainSpool.ErrCodecUnavailable
	}
	return nil
}

func managedWebhookDigest(key []byte, purpose string, value []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(managedWebhookCodecVersion))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(value)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func managedWebhookAAD(value *domainSpool.ProtectedDelivery) ([]byte, error) {
	return json.Marshal(struct {
		CodecVersion         string `json:"codec_version"`
		DeliveryID           string `json:"delivery_id"`
		DeviceDigest         string `json:"device_digest"`
		SessionDigest        string `json:"session_digest"`
		EventName            string `json:"event_name"`
		MessageIDDigest      string `json:"message_id_digest"`
		BodyHash             string `json:"body_hash"`
		PayloadKeyVersion    string `json:"payload_key_version"`
		WebhookSecretVersion string `json:"webhook_secret_version"`
	}{
		CodecVersion: managedWebhookCodecVersion, DeliveryID: value.DeliveryID,
		DeviceDigest: value.DeviceDigest, SessionDigest: value.SourceSessionDigest,
		EventName: value.EventName, MessageIDDigest: value.MessageIDDigest, BodyHash: value.BodyHash,
		PayloadKeyVersion: value.PayloadKeyVersion, WebhookSecretVersion: value.SecretVersion,
	})
}

func sealManagedWebhook(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

func openManagedWebhook(key, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < aead.NonceSize()+aead.Overhead() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce := ciphertext[:aead.NonceSize()]
	return aead.Open(nil, nonce, ciphertext[aead.NonceSize():], aad)
}
