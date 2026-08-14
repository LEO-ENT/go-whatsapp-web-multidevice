package whatsapp

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type recordedWhatsmeowLog struct {
	level  string
	module string
	text   string
	args   int
}

type recordingWhatsmeowLogger struct {
	module string
	events *[]recordedWhatsmeowLog
}

func newRecordingWhatsmeowLogger() *recordingWhatsmeowLogger {
	events := make([]recordedWhatsmeowLog, 0)
	return &recordingWhatsmeowLogger{module: "root", events: &events}
}

func (l *recordingWhatsmeowLogger) record(level, msg string, args ...any) {
	*l.events = append(*l.events, recordedWhatsmeowLog{
		level:  level,
		module: l.module,
		text:   fmt.Sprintf(msg, args...),
		args:   len(args),
	})
}

func (l *recordingWhatsmeowLogger) Errorf(msg string, args ...any) {
	l.record("error", msg, args...)
}

func (l *recordingWhatsmeowLogger) Warnf(msg string, args ...any) {
	l.record("warn", msg, args...)
}

func (l *recordingWhatsmeowLogger) Infof(msg string, args ...any) {
	l.record("info", msg, args...)
}

func (l *recordingWhatsmeowLogger) Debugf(msg string, args ...any) {
	l.record("debug", msg, args...)
}

func (l *recordingWhatsmeowLogger) Sub(module string) waLog.Logger {
	return &recordingWhatsmeowLogger{module: l.module + "/" + module, events: l.events}
}

type secretErrorSentinel struct {
	calls *int
}

func (e secretErrorSentinel) Error() string {
	(*e.calls)++
	return "sentinel secret=https://user:pass@example.test/path?token=raw"
}

type wrappedEOFErrorSentinel struct {
	calls *int
}

func (e wrappedEOFErrorSentinel) Error() string {
	(*e.calls)++
	return "wrapped EOF secret=https://user:pass@example.test/path?token=raw"
}

func (e wrappedEOFErrorSentinel) Unwrap() error {
	return io.EOF
}

type secretStringSentinel struct {
	calls *int
}

func (s secretStringSentinel) String() string {
	(*s.calls)++
	return "stringer secret=https://user:pass@example.test/path?token=raw"
}

func TestConcreteWhatsmeowClientLoggerRedactsAllLevelsAndShapes(t *testing.T) {
	oldDebug := config.AppDebug
	config.AppDebug = true
	t.Cleanup(func() { config.AppDebug = oldDebug })

	sink := newRecordingWhatsmeowLogger()
	client := whatsmeow.NewClient(&store.Device{}, newFilteredLogger(sink))
	if _, ok := client.Log.(*filteredLogger); !ok {
		t.Fatalf("Whatsmeow client logger = %T, want concrete filteredLogger boundary", client.Log)
	}

	const (
		providerID = "3EB0A1B2C3D4E5F6071829"
		lowerID    = "3eb0a1b2c3d4e5f6071829"
		jid        = "628123456789:37@s.whatsapp.net"
		upperJID   = "628123456789:37@S.WHATSAPP.NET"
		groupJID   = "120363411147004434@g.us"
		message    = "quoted confidential message text"
		secretURL  = "https://user:pass@example.test/path?token=raw&secret=value"
	)

	sentinelCalls := 0
	sentinel := secretErrorSentinel{calls: &sentinelCalls}
	eofSentinelCalls := 0
	eofSentinel := wrappedEOFErrorSentinel{calls: &eofSentinelCalls}
	stringSentinelCalls := 0
	stringSentinel := secretStringSentinel{calls: &stringSentinelCalls}

	// Exact formats from the pinned Whatsmeow send.go boundary.
	client.Log.Warnf("Failed to get peer recipient PN for %s: %v", jid, sentinel)
	client.Log.Warnf("Failed to store message secret key for outgoing message %s: %v", providerID, sentinel)
	client.Log.Warnf("Server returned different participant list hash (%s != %s) when sending to %s. Some devices may not have received the message.", "hash-one", "hash-two", groupJID)
	client.Log.Warnf("Failed to encrypt %s for %s: %v", lowerID, upperJID, sentinel)
	client.Log.Warnf("Failed to get privacy token for %s: %v", jid, sentinel)
	client.Log.Warnf("Got %v error while trying to process prekey bundle for %s, clearing stored identity and retrying", sentinel, upperJID)
	client.Log.Debugf("LID for %s not found, fetching user info", jid)
	client.Log.Debugf("Replacing SendMessage destination with LID %s -> %s", jid, "987654321@lid")
	client.Log.Debugf("Stored message secret key for outgoing message %s", providerID)
	client.Log.Debugf("Processing prekey bundle for %s", upperJID)

	// Formatted and already-concatenated/plain variants at every level.
	client.Log.Errorf("send failed id=%s jid=%s body=%q url=%s err=%v", providerID, jid, message, secretURL, sentinel)
	client.Log.Errorf("plain error " + lowerID + " " + upperJID + " " + message + " " + secretURL)
	client.Log.Warnf("plain warning " + providerID + " " + jid + " " + message)
	client.Log.Infof("send info id=%s participant=%s body=%q", providerID, groupJID, message)
	client.Log.Infof("plain info " + lowerID + " " + upperJID + " " + secretURL)
	client.Log.Debugf("debug id=%s device=%s body=%q", providerID, jid, message)
	client.Log.Debugf("plain debug " + lowerID + " " + upperJID + " " + secretURL)

	// Sublogger names are also untrusted boundary data.
	client.Log.Sub("participant/" + jid + "?secret=value").Warnf("submodule " + providerID)

	// Exact pinned websocket call shape plus non-EOF and preformatted mutants.
	client.Log.Errorf("Error reading from websocket: %v", eofSentinel)
	client.Log.Errorf("Error reading from websocket: %v", sentinel)
	client.Log.Errorf("Error reading from websocket: %v", stringSentinel)
	client.Log.Errorf(websocketEOFErrorMsg)
	client.Log.Errorf(websocketEOFErrorMsg + " for " + jid + " id=" + providerID)

	if sentinelCalls != 0 || eofSentinelCalls != 0 || stringSentinelCalls != 0 {
		t.Fatalf("sentinels rendered before redaction: regular=%d eof=%d stringer=%d", sentinelCalls, eofSentinelCalls, stringSentinelCalls)
	}
	if got := len(*sink.events); got != 23 {
		t.Fatalf("recorded events = %d, want 23", got)
	}

	providerPattern := regexp.MustCompile(`(?i)3eb0[0-9a-f]{18}`)
	jidPattern := regexp.MustCompile(`(?i)[0-9]{5,}(?::[0-9]+)?@(s\.whatsapp\.net|g\.us|lid)`)
	barePhonePattern := regexp.MustCompile(`[0-9]{9,}`)
	for _, event := range *sink.events {
		combined := strings.ToLower(event.module + " " + event.text)
		if providerPattern.MatchString(combined) || jidPattern.MatchString(combined) || barePhonePattern.MatchString(combined) {
			t.Fatalf("structured identifier crossed logger boundary: %#v", event)
		}
		for _, sensitive := range []string{message, "example.test", "?token=", "secret=", "user:pass", "hash-one", "hash-two", "987654321"} {
			if strings.Contains(combined, strings.ToLower(sensitive)) {
				t.Fatalf("sensitive value %q crossed logger boundary: %#v", sensitive, event)
			}
		}
		if event.args != 0 {
			t.Fatalf("logger boundary forwarded %d unrendered arguments: %#v", event.args, event)
		}
		if event.text != "whatsapp_client.event_redacted" && event.text != "whatsapp_client.websocket_eof" {
			t.Fatalf("unsafe log category: %#v", event)
		}
	}

	wantTail := []recordedWhatsmeowLog{
		{level: "debug", module: "root", text: "whatsapp_client.websocket_eof"},
		{level: "error", module: "root", text: "whatsapp_client.event_redacted"},
		{level: "error", module: "root", text: "whatsapp_client.event_redacted"},
		{level: "debug", module: "root", text: "whatsapp_client.websocket_eof"},
		{level: "error", module: "root", text: "whatsapp_client.event_redacted"},
	}
	gotTail := (*sink.events)[len(*sink.events)-len(wantTail):]
	for i := range wantTail {
		if gotTail[i] != wantTail[i] {
			t.Fatalf("websocket classification[%d] = %#v, want %#v", i, gotTail[i], wantTail[i])
		}
	}
}
