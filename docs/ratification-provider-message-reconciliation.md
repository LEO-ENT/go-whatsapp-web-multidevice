# Provider message reconciliation ratification

Date: 2026-08-12

Freeze base: `75606c4b9b780e9423d9f4d0ca39d706ce77ac38`

Feature commit: `d7bda59`

Contract/docs/CI commit: `cc7bb47`

Decision: **APPROVE**, subject to preserving the semantics below when the commits
are restacked.

## Ratified contract

- `POST /provider/messages/lookup` is registered behind the existing REST Basic
  Auth and device middleware. The canonical ID is in the JSON body rather than the
  URL, and the lookup is scoped by the authenticated request's selected device.
- `PRESENT` requires durable positive evidence for the exact
  `(device_id, provider_message_id)`: a successful provider ACK, a primary-device
  delivered/read receipt for an outgoing message, or a compatible durable outgoing
  message row. Its receipt contains only `provider_message_id`.
- A local miss, cross-device match, incoming/history-only row, missing context, or
  unavailable, corrupt, or timed-out storage returns `UNKNOWN` without a receipt.
- The concrete positive-only SQLite authority never returns `PROVABLY_ABSENT`.
  That status remains reserved for a future persistent authority capable of proving
  non-acceptance. Empty asynchronous history or retry/cache state is not authority.
- Lookup never sends or retries. It provides neither a blind-retry signal nor an
  exactly-once delivery guarantee.

## Evidence

TDD RED was observed before implementation: provider domain/API symbols and the
exact repository methods used by the new tests did not exist. The GREEN suite now
covers canonical ID validation, API-to-usecase-to-store argument preservation,
Basic Auth, selected-device ownership, cross-device and unknown IDs, closed/corrupt/
cancelled storage, restart persistence, positive ACK and outgoing receipt capture,
and redacted HTTP, error, and log surfaces.

The following offline gates passed from `src/` using Go 1.25.5 and the pinned local
module/build caches:

```text
go test -tags purego -count=1 ./...
go vet -tags purego ./...
go build -tags purego ./...
```

The changed Go files also passed a normalized full-blob `gofmt` comparison,
`git diff --check` passed, and `docs/openapi.yaml` parsed as OpenAPI 3.0.0 with the
new path and schemas. CI includes a pure-Go provider reconciliation contract test.

Pinned Graphify 0.9.26 completed an AST update for this worktree: 3,076 nodes,
7,701 edges, and 170 communities. Generated `graphify-out/` state is local and
ignored rather than committed as source.

## Restack boundary

The implementation and its documentation are the two commits in
`75606c4..cc7bb47`. This ratification is intentionally a later, separable commit
and must not be treated as an additional runtime dependency.
