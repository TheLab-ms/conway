package voicemail

import (
	"fmt"
	"strings"
)

// voiceTwiML generates TwiML that plays a greeting and records a voicemail.
func voiceTwiML(greeting, recordingCallbackURL string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Response>
    <Say voice="alice">%s</Say>
    <Record maxLength="60" playBeep="true" recordingStatusCallback="%s" recordingStatusCallbackMethod="POST"/>
    <Say voice="alice">Thank you for your message. Goodbye.</Say>
</Response>`, xmlEscape(greeting), xmlEscape(recordingCallbackURL))
}

// hangupTwiML generates TwiML that plays a message and hangs up.
func hangupTwiML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Response>
    <Say voice="alice">We are sorry, the voicemail system is not available at this time. Goodbye.</Say>
    <Hangup/>
</Response>`
}

// xmlEscape escapes special XML characters.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
