package whatsapp

import (
	"errors"
	"io"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// filteredLogger wraps the default whatsmeow logger to downgrade noisy websocket EOF errors
// that are expected during reconnect cycles. Without this wrapper, the library logs those EOFs
// as errors even though the client automatically reconnects and continues working.
type filteredLogger struct {
	base waLog.Logger
}

const websocketEOFErrorMsg = "Error reading from websocket: failed to get reader: failed to read frame header: EOF"
const websocketReadErrorFormat = "Error reading from websocket: %v"

const (
	redactedWhatsmeowEvent  = "whatsapp_client.event_redacted"
	redactedWebsocketEOF    = "whatsapp_client.websocket_eof"
	redactedWhatsmeowModule = "whatsapp_client"
)

func isWebsocketEOFError(msg string, args ...any) bool {
	// Preserve the exact preformatted legacy event without accepting attacker-
	// controlled suffixes as an EOF classification.
	if msg == websocketEOFErrorMsg && len(args) == 0 {
		return true
	}
	if msg != websocketReadErrorFormat || len(args) != 1 {
		return false
	}
	err, ok := args[0].(error)
	// errors.Is classifies the wrapped EOF without rendering Error or String.
	return ok && errors.Is(err, io.EOF)
}

func newFilteredLogger(base waLog.Logger) waLog.Logger {
	return &filteredLogger{base: base}
}

func (l *filteredLogger) Errorf(msg string, args ...any) {
	if isWebsocketEOFError(msg, args...) {
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
