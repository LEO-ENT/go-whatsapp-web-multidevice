package send

const (
	// ProviderMessageIDLength matches whatsmeow's canonical web message ID shape:
	// the 3EB0 prefix followed by 9 bytes encoded as uppercase hexadecimal.
	ProviderMessageIDLength = 22

	// ProviderTimeoutMinMS and ProviderTimeoutMaxMS keep the provider wait below
	// the caller's 30-second outbox lease. The default applies only when a
	// deterministic provider message ID is supplied without an explicit timeout.
	ProviderTimeoutMinMS     = 1_000
	ProviderTimeoutDefaultMS = 20_000
	ProviderTimeoutMaxMS     = 25_000
)

type MessageRequest struct {
	BaseRequest
	Message           string   `json:"message" form:"message"`
	ReplyMessageID    *string  `json:"reply_message_id" form:"reply_message_id"`
	Mentions          []string `json:"mentions,omitempty" form:"mentions"` // List of phone numbers/JIDs to mention (ghost mentions)
	ProviderMessageID *string  `json:"provider_message_id,omitempty" form:"provider_message_id"`
	ProviderTimeoutMS *int     `json:"provider_timeout_ms,omitempty" form:"provider_timeout_ms"`
}
