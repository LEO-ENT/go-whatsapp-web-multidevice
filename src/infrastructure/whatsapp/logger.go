package whatsapp

import (
	"strings"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// filteredLogger wraps the default whatsmeow logger to downgrade noisy websocket EOF errors
// that are expected during reconnect cycles. Without this wrapper, the library logs those EOFs
// as errors even though the client automatically reconnects and continues working.
type filteredLogger struct {
	base waLog.Logger
}

const websocketEOFErrorMsg = "Error reading from websocket: failed to get reader: failed to read frame header: EOF"

const (
	redactedWhatsmeowEvent  = "whatsapp_client.event_redacted"
	redactedWebsocketEOF    = "whatsapp_client.websocket_eof"
	redactedWhatsmeowModule = "whatsapp_client"
)

func isWebsocketEOFError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, strings.ToLower(websocketEOFErrorMsg)) ||
		(strings.Contains(lower, "error reading from websocket") && strings.Contains(lower, "failed to read frame header: eof"))
}

func newFilteredLogger(base waLog.Logger) waLog.Logger {
	return &filteredLogger{base: base}
}

func (l *filteredLogger) Errorf(msg string, args ...any) {
	if isWebsocketEOFError(msg) {
		l.base.Debugf(redactedWebsocketEOF)
		return
	}

	l.base.Errorf(redactedWhatsmeowEvent)
}

func (l *filteredLogger) Warnf(_ string, _ ...any) {
	l.base.Warnf(redactedWhatsmeowEvent)
}

func (l *filteredLogger) Infof(_ string, _ ...any) {
	l.base.Infof(redactedWhatsmeowEvent)
}

func (l *filteredLogger) Debugf(_ string, _ ...any) {
	l.base.Debugf(redactedWhatsmeowEvent)
}

func (l *filteredLogger) Sub(_ string) waLog.Logger {
	return newFilteredLogger(l.base.Sub(redactedWhatsmeowModule))
}
