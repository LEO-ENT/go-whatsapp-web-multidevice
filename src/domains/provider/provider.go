package provider

import "context"

// MessageStatus is the reconciliation result for one canonical provider message ID.
// A caller must not infer absence from UNKNOWN.
type MessageStatus string

const (
	MessagePresent        MessageStatus = "PRESENT"
	MessageProvablyAbsent MessageStatus = "PROVABLY_ABSENT"
	MessageUnknown        MessageStatus = "UNKNOWN"
)

// MessageReceipt is deliberately redacted to the caller-supplied opaque provider
// message ID. It never exposes device, chat, recipient, body, or timestamps.
type MessageReceipt struct {
	ProviderMessageID string `json:"provider_message_id"`
}

// MessageLookupRequest keeps the opaque ID out of the URL, where access loggers
// commonly render it without application-level redaction.
type MessageLookupRequest struct {
	ProviderMessageID string `json:"provider_message_id"`
}

// MessageLookup represents the provider reconciliation result. Receipt is only
// populated for PRESENT.
type MessageLookup struct {
	Status  MessageStatus   `json:"status"`
	Receipt *MessageReceipt `json:"receipt,omitempty"`
}

// IMessageLookupUsecase reconciles an exact provider message ID for the device
// bound to the request context.
type IMessageLookupUsecase interface {
	LookupMessage(ctx context.Context, providerMessageID string) (MessageLookup, error)
}
