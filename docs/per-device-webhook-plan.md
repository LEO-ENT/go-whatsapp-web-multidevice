# Per-Device Webhook Implementation Plan

## Status: IMPLEMENTED

## Overview

Add per-device webhook support where each device can have its own webhook URL. Legacy deployments keep the global fallback by default. `WHATSAPP_WEBHOOK_DEVICE_FAIL_CLOSED=true` now selects the managed path: exact deliveries are encrypted and committed to the SQLite source spool before any delivery goroutine/network I/O, and neither global nor direct fallback is allowed. The flag requires an injected per-device keyring/codec; readiness stays red while that dependency or the worker is unavailable.

## Routing and secret safety semantics

- A successful lookup with no device webhook is a compatibility case: it uses the global webhook only while `WHATSAPP_WEBHOOK_DEVICE_FAIL_CLOSED=false`.
- A storage/backend lookup error is not equivalent to missing configuration. It always suppresses generic webhook delivery and returns a routing error; it never falls back globally.
- A non-empty device webhook URL must be an absolute HTTP(S) URL without user-info credentials. Invalid persisted configuration always suppresses delivery and global fallback.
- The Chatwoot path remains independent: suppressing the generic webhook does not intentionally disable an otherwise eligible Chatwoot forward.
- `webhook_secret` is write-only. `POST /devices`, `PATCH /devices/{device_id}/webhook`, and `GET /devices/{device_id}/webhook` expose `webhook_secret_configured` instead of returning the stored value.
- Delivery logs omit webhook URLs, backend error text, device JIDs and secrets from fail-closed paths.

## Flow Diagram

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                         WHATSAPP EVENT RECEIVED                             │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  event_handler.go :: handler()                                             │
│  - Routes event to appropriate handler (message, receipt, etc.)             │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  Event-specific handler (e.g., event_message_handler.go)                   │
│  - Creates webhook payload with device_id                                   │
│  - Calls forwardMessageToWebhook()                                          │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  webhook_forward.go :: forwardPayloadToConfiguredWebhooks()                 │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │  STEP 1: Extract device JID from payload["device_id"]                │  │
│  │  STEP 2: Call getWebhookConfigForDevice(deviceJID)                    │  │
│  │          - Looks up device record by JID                              │  │
│  │          - If device has custom webhook_url, return device config      │  │
│  │          - Missing config: apply deployment compatibility gate          │  │
│  │          - Lookup/validation error: suppress generic delivery           │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
                    ┌───────────────┴───────────────┐
                    │                               │
                    ▼                               ▼
         ┌──────────────────┐           ┌────────────────────────┐
         │ Device has custom │           │ Device webhook URL is  │
         │ webhook config?   │           │ NULL or empty?          │
         └──────────────────────┘           └─────────────────────────┘
                    │                               │
              YES   │                               │ YES
                    ▼                               ▼
    ┌────────────────────────┐           ┌─────────────────────────┐
    │ Use device webhook    │           │ Use global config       │
    │ config (override)     │           │ Global fallback or drop │
    └────────────────────────┘           └─────────────────────────┘
                    │                               │
                    └───────────────┬───────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  webhook_forward.go :: forwardToWebhooks()                                  │
│  - Iterates webhook URLs                                                    │
│  - For each URL: submitWebhook() with HMAC signature                         │
│  - 5 retries with exponential backoff                                       │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
                    ┌───────────────┴───────────────┐
                    │                               │
                    ▼                               ▼
         ┌──────────────────┐           ┌────────────────────────┐
         │ All webhooks     │           │ Some webhooks failed   │
         │ succeeded        │           │ (partial failure OK)   │
         └──────────────────┘           └────────────────────────┘
```

## API Flow: Set Device Webhook

```text
Client                    REST API                    UseCase                  Repository
  │                          │                           │                          │
  │ PATCH /devices/:id/webhook│                          │                          │
  │ { "webhook_url": "...",  │                           │                          │
  │   "webhook_secret": "..."}                           │                          │
  │─────────────────────────>│                           │                          │
  │                          │ SetDeviceWebhookConfig()   │                          │
  │                          │──────────────────────────>│                          │
  │                          │                           │ SetDeviceWebhookConfig() │
  │                          │                           │────────────────────────>│
  │                          │                           │                          │
  │                          │                           │                          │
  │ 200 OK                   │                           │                          │
  │<─────────────────────────│                           │                          │
  │                          │                           │                          │
```

## Files Modified

| File | Change |
|------|--------|
| `domains/chatstorage/chatstorage.go` | Added webhook URL, secret, event whitelist, and TLS-skip fields plus `DeviceWebhookConfig` |
| `domains/chatstorage/interfaces.go` | Added `GetDeviceRecordByJID`, URL-only helpers, and full webhook config helpers |
| `infrastructure/chatstorage/sqlite_repository.go` | Added migrations #31-#34, updated queries, implemented new methods |
| `infrastructure/whatsapp/chatstorage_wrapper.go` | Added wrapper methods for new interface methods |
| `infrastructure/whatsapp/webhook_forward.go` | Added `getWebhookConfigForDevice()`, device event filtering, and updated forwarding logic |
| `infrastructure/whatsapp/device_manager.go` | Added `GetStorage()` method |
| `domains/device/interfaces.go` | Added URL-only and full-config webhook methods to `IDeviceUsecase` |
| `usecase/device.go` | Implemented URL-only and full-config webhook methods |
| `ui/rest/device.go` | Added `PATCH /devices/:device_id/webhook`, `GET /devices/:device_id/webhook` |
| `infrastructure/whatsapp/webhook_forward_test.go` | Added per-device webhook tests |
| `usecase/device_test.go` | Added device service tests |
| `docs/openapi.yaml` | Added webhook API documentation |

## Database Changes

Migrations #31-#34 add the per-device webhook fields to the `devices` table:

- `webhook_url TEXT DEFAULT NULL`
- `webhook_secret TEXT DEFAULT ''`
- `webhook_events TEXT DEFAULT ''`
- `webhook_insecure_skip_verify BOOLEAN DEFAULT FALSE`

## API Endpoints

- `PATCH /devices/{device_id}/webhook` - Set device-specific webhook configuration
  - Body: `{ "webhook_url": "https://example.com/webhook", "webhook_secret": "secret", "webhook_events": "message,message.ack", "webhook_insecure_skip_verify": false }`
  - The response returns `webhook_secret_configured`, never `webhook_secret`
  - Set to empty string `""` to clear; global fallback then depends on `WHATSAPP_WEBHOOK_DEVICE_FAIL_CLOSED`

- `GET /devices/{device_id}/webhook` - Get device-specific webhook configuration
  - Returns empty values if not set plus `webhook_secret_configured`; the secret remains write-only

## Testing

### Unit Tests

**`infrastructure/whatsapp/webhook_forward_test.go`:**
- `TestGetWebhookConfigForDevice_NoDeviceID` - Returns global webhook config when device JID is empty
- `TestGetWebhookConfigForDevice_DeviceNotFound` - Falls back to global when device not found
- `TestGetWebhookConfigForDevice_FallbackToGlobal` - Falls back to global when device has no custom webhook
- `TestGetWebhookConfigForDevice_DeviceSpecificOverride` - Uses the full device-specific webhook config when set
- `TestForwardPayloadToConfiguredWebhooks_WithDeviceSpecificWebhook` - Uses device-specific webhook when set
- `TestForwardPayloadToConfiguredWebhooks_DeviceWebhookCleared_FallsBackToGlobal` - Falls back to global when device webhook is cleared
- `TestForwardPayloadToConfiguredWebhooks_ManagedMissingConfigFailsClosedWhenEnabled` - Drops instead of falling back when the deployment gate is enabled
- `TestForwardPayloadToConfiguredWebhooks_DeviceLookupErrorFailsClosed` - Storage errors never route to the global webhook and logs omit sensitive identifiers
- `TestForwardPayloadToConfiguredWebhooks_InvalidDeviceConfigDoesNotFallbackOrLeak` - Invalid stored URLs never route or leak into logs
- `TestForwardPayloadToConfiguredWebhooks_DeviceWebhookOnly_NoGlobal` - Uses device-specific webhook with no global webhook
- `TestSQLiteRepositoryGetsDeviceWebhookConfigByJID` - Persists and resolves the full device webhook config
- `TestAddDevice_ForwardsFullWebhookConfig` - Accepts full webhook config when creating a device
- `TestGetDeviceWebhook_DoesNotExposeSecret` - GET returns only secret configuration state
- `TestUpdateDeviceWebhook_DoesNotEchoSecret` - PATCH accepts but never echoes the write-only secret

**`usecase/device_test.go`:**
- `TestDeviceServiceInterface` - Verifies interface implementation
- `TestSetDeviceWebhook_InvalidManager` - Error handling for nil manager
- `TestGetDeviceWebhook_InvalidManager` - Error handling for nil manager

## OpenAPI Documentation

Added to `docs/openapi.yaml`:

- `GET /devices/{device_id}/webhook` - Get device webhook configuration
- `PATCH /devices/{device_id}/webhook` - Set device webhook configuration
