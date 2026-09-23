// Detection of anti-bot interstitials in an HTTP response.

package antibot

import (
	"net/http"
	"strings"
)

// BodyPrefix is how much of a refusal body a caller reads to classify it. An
// interstitial is under a kilobyte, anything longer is not one, and reading
// further would mean buffering a page the caller has already decided to discard.
const BodyPrefix = 4 << 10

// markers are the strings an interstitial carries and a content page does not.
// Both are required together with the status, because a bare 403 is an ordinary
// refusal and must keep its ordinary handling.
var markers = []string{"ddos-guard", "js-challenge"}

// Challenged reports whether a response with this status and body prefix is an
// anti-bot interstitial rather than an ordinary refusal. Matching is
// case-insensitive, and a 200 carrying the markers is a page that happens to
// mention them, not a challenge.
func Challenged(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, marker := range markers {
		if !strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}
