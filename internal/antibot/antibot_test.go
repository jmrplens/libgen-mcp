// Tests for the interstitial detector.

package antibot

import (
	"net/http"
	"strings"
	"testing"
)

// TestChallenged pins what counts as an interstitial, because the cost of
// getting it wrong runs both ways: too loose and an ordinary refusal stops a
// caller from trying the next mirror, too tight and the giving-up never happens.
//
// Both the status and the markers are required. A 403 alone is an ordinary
// refusal, and a page that mentions ddos-guard while answering 200 is a content
// page that happens to say so.
func TestChallenged(t *testing.T) {
	const interstitial = `<html><head><title>DDoS-Guard</title>` +
		`<link rel="stylesheet" href="/.well-known/ddos-guard/js-challenge/index.css"></head></html>`

	testCases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "the real interstitial", status: http.StatusForbidden, body: interstitial, want: true},
		{name: "mixed case still matches", status: http.StatusForbidden, body: strings.ToUpper(interstitial), want: true},
		{name: "a bare refusal", status: http.StatusForbidden, body: "forbidden", want: false},
		{name: "only one marker", status: http.StatusForbidden, body: "<title>DDoS-Guard</title>", want: false},
		{name: "the same page on a 200", status: http.StatusOK, body: interstitial, want: false},
		{name: "a server error", status: http.StatusInternalServerError, body: interstitial, want: false},
		{name: "an empty body", status: http.StatusForbidden, body: "", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Challenged(tc.status, []byte(tc.body)); got != tc.want {
				t.Errorf("Challenged(%d, %q) = %t, want %t", tc.status, tc.body, got, tc.want)
			}
		})
	}
}
