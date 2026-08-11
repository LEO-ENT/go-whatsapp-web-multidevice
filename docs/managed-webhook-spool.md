# Managed webhook source spool

`WHATSAPP_WEBHOOK_DEVICE_FAIL_CLOSED=true` selects the G3 managed per-device path. It is a rollout gate, not only a fallback switch.

## Admission boundary

- GOWA resolves a valid per-device destination and session, serializes the webhook once, and commits an encrypted delivery to `managed_webhook_spool` before starting delivery work.
- Storage contains only purpose-separated keyed digests, version metadata, an opaque delivery ID, FSM/lease/fence fields, and authenticated ciphertext. Raw bodies, URLs, JIDs, session IDs, and signing secrets are not stored in plaintext.
- A storage error, missing session, invalid configuration, missing codec/keyring, or ambiguous commit blocks managed delivery and latches managed readiness red. There is no direct-goroutine or global-webhook fallback. Because WhatsApp has already invoked the event callback, this is reported as a critical admission gap; zero-loss starts only after the spool commit.
- Chatwoot forwarding remains an independent path.

## Keyring contract and rollout block

GOWA exposes `webhookspool.Keyring` and `InstallManagedWebhookCodec`. Admission requires per-device AES-256 encryption material, a separate stable HMAC digest key, the per-device webhook signing secret, and both payload/signing versions. Ordinary encryption/signing rotation must not rotate the digest key, or tombstone identity would change. Old payload encryption versions remain resolvable while spool rows reference them; retry/open does not need old signing material because the exact signature is already encrypted in the snapshot.

The repository does not provide a global key, derive encryption from the webhook signing secret, or persist plaintext key material. Therefore managed mode intentionally fails `/health` readiness until the deployment installs a conforming keyring/codec and the worker starts. Legacy mode remains unchanged while the flag is `false`.

## Delivery semantics

- Atomic SQLite claims and retry/deadline decisions use the DB clock, an owner, a lease, attempt count, and monotonic fencing token. An expired lease can be taken over; stale owners cannot complete/retry/dead-letter.
- Retries reuse the exact raw bytes, target, signature, secret version, and opaque delivery ID stored in the encrypted envelope.
- Only `2xx` completes. `3xx` is terminal and redirects are never followed. Network errors, timeouts, `408`, `429`, and `5xx` retry with bounded exponential backoff and bounded `Retry-After`. Other `4xx` responses dead-letter by default.
- A temporarily unavailable key version retries within the same budget; malformed metadata, wrong-key authentication, or corrupt ciphertext dead-letter without attempting network delivery.
- Retry count and wall-clock deadline are both bounded. Dead letters are queryable through the repository. Terminal ciphertext can be purged after retention while the pseudonymous identity/fence/version tombstone remains and continues rejecting replay.

## Shutdown and rollback

REST and MCP shutdown stop the worker before the SQLite connection is closed. Operational rollback disables the managed gate/dequeue and preserves the spool/tombstones for diagnosis; it does not drop the table or replay through the legacy direct path.
