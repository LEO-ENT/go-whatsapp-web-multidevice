# Provider message reconciliation

`POST /provider/messages/lookup` reconciles one deterministic text
message ID after a send returned an ambiguous timeout or the caller crashed before
persisting the success response.

The endpoint is registered behind GoWA's REST Basic Auth middleware and device
middleware. Production deployments must configure `APP_BASIC_AUTH` and must send
the same `X-Device-Id` that owned the original send. The ID must be the canonical
opaque `3EB0` plus 18 uppercase hexadecimal characters used by deterministic text
sends.

```http
POST /provider/messages/lookup HTTP/1.1
Authorization: Basic <credentials>
X-Device-Id: org_2
Content-Type: application/json

{"provider_message_id":"3EB0A1B2C3D4E5F6071829"}
```

The result is deliberately tri-state:

| Status | Meaning | Receipt | Retry decision |
| --- | --- | --- | --- |
| `PRESENT` | Durable device-owned evidence proves the provider accepted or later acknowledged the message. | Only the opaque `provider_message_id`; no chat, destination, body, device, or timestamp. | Mark/reconcile the outbox effect; do not send it again. |
| `PROVABLY_ABSENT` | A persistent provider authority proved non-acceptance. | None. | A caller may apply its own retry policy. |
| `UNKNOWN` | There is no conclusive authority, the ID belongs to another device, or storage is missing, unavailable, corrupt, or timed out. | None. | Do not retry blindly; leave the effect ambiguous and reconcile later or escalate. |

This release's SQLite authority stores positive evidence only, so it never emits
`PROVABLY_ABSENT`. A missing row is always `UNKNOWN`. The status exists in the
contract so a future persistent provider authority can add honest negative evidence
without collapsing it into a cache miss.

Positive evidence is restart-safe in `provider_message_receipts` and is written:

- after Whatsmeow returns the server acknowledgement from `SendMessage`; or
- after a primary-device delivered/read receipt for an outgoing message.

For compatibility, a durable device-scoped `messages` row with `is_from_me=true`
also proves `PRESENT`. The lookup never starts history sync. History or cache
absence is never used as negative evidence.

Why the Whatsmeow retry store is not an authority: the pinned Whatsmeow version
adds a message to its recent/retry buffer before it sends the transport frame and
before it waits for the server acknowledgement. Presence there proves only that an
attempt was prepared, while an empty in-memory cache proves nothing after eviction
or restart.

The ID is carried in the JSON body rather than the URL so ordinary access logs do
not capture it. Storage failures return HTTP 200 with `UNKNOWN`, not backend details. Invalid IDs
return a fixed HTTP 400 validation response. Authentication and unknown-device
failures are handled before the lookup. Logs use fixed categories and never render
the provider ID, device/JID, destination, content, URL, or backend error.

This is a reconciliation seam, not an exactly-once delivery guarantee. It performs
no retry and does not turn an ambiguous send into a safe retry unless an authority
actually returns `PROVABLY_ABSENT`.
