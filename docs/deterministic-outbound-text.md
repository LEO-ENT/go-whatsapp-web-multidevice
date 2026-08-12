# Release note: deterministic outbound text IDs

`POST /send/message` now accepts two optional fields for callers that persist an
outbox effect before sending:

```json
{
  "phone": "<destination>",
  "message": "<text>",
  "provider_message_id": "3EB0A1B2C3D4E5F6071829",
  "provider_timeout_ms": 20000
}
```

`provider_message_id` must be exactly `3EB0` followed by 18 uppercase hexadecimal
characters, matching the canonical Whatsmeow web-message shape pinned by this
release. It is an opaque receipt, not a place for a phone/JID, email, URL, query,
message content, tenant identifier, or secret. Production callers should derive it
from a stable outbox effect UUID plus a versioned, domain-separated HMAC and use a
different result for every distinct effect.

`provider_timeout_ms` is bounded from 1000 through 25000 ms. A deterministic ID
without an explicit timeout uses 20000 ms. The caller must hold a lease longer than
the selected provider timeout; the production outbox contract currently uses 30
seconds. When both fields are absent, GoWA calls Whatsmeow without
`SendRequestExtra`, preserving its legacy random ID and default timeout behavior.

Retries of one logical text effect must reuse the exact same ID. This closes the
transport identity gap after an ambiguous timeout or crash-after-send, but it is not
a durable GoWA deduplication ledger and does not claim user-visible exactly-once
delivery. The caller remains responsible for durable outbox state and reconciliation.
The existing success response exposes the opaque receipt only as
`results.message_id`; status, error, and log paths do not echo it or message PII.

The Whatsmeow logger boundary is fail-closed at every level, including debug builds:
it does not render upstream format arguments or error values and forwards only a
fixed safe event category. Sublogger names are redacted too. This deliberately trades
provider-log detail for the guarantee that message IDs, JIDs, phone/device suffixes,
message text, URLs, queries, and secrets cannot cross the application log boundary.
The expected idle-websocket EOF remains a distinct safe debug category so operators
can count reconnect noise without receiving the underlying payload. That downgrade
matches only the pinned upstream format plus an `errors.Is(..., io.EOF)` classification
(which does not render the error), or the exact legacy preformatted EOF string. A
non-EOF error with the same format and any preformatted string with an added suffix
remain redacted error events.

Proxy setup and read/delivered receipt logs use the same fail-closed rule outside the
Whatsmeow adapter: proxy URLs, configuration errors, device IDs, message IDs, JIDs,
timestamps, and receipt structures are never rendered. Operators receive only fixed
proxy result codes and receipt status with a message-count shape. The downstream
Chatwoot read-receipt synchronizer likewise emits only fixed lookup, missing-source,
last-seen update, and mark-read failure categories; neither correlation IDs nor
backend/HTTP errors are rendered. All receipt-path logging, including linked-device
skips and missing storage, is routed through a nominal fixed-category boundary that
is enforced by a Go AST/type-aware package test.

This MVP applies only to text sends. Media sends, read receipts, and presence updates
remain outside the idempotent contract and must be treated as non-idempotent/BLOCKED
by production orchestration until dedicated provider semantics are implemented.
