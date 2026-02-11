package voicemail

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/url"
	"sort"
	"strings"
)

// validateTwilioSignature verifies the X-Twilio-Signature header against the
// expected HMAC-SHA1 of the full request URL plus sorted POST parameters.
// See https://www.twilio.com/docs/usage/security#validating-requests
func validateTwilioSignature(authToken, fullURL, signature string, params url.Values) bool {
	if authToken == "" || signature == "" {
		return false
	}

	var buf strings.Builder
	buf.WriteString(fullURL)

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		buf.WriteString(k)
		buf.WriteString(params.Get(k))
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(buf.String()))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}
