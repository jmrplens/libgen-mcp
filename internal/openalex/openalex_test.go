package openalex

import (
	"context"
	"testing"
)

// TestNewRequest_SendsTheKeyAsABearerTokenAndNeverInTheURL pins where the key
// travels: in the Authorization header, with the URL left exactly as given, and
// not at all when the key is blank.
func TestNewRequest_SendsTheKeyAsABearerTokenAndNeverInTheURL(t *testing.T) {
	const rawURL = "https://api.openalex.org/works?search=dna"
	cases := []struct {
		name string
		key  string
		want string
	}{
		{name: "a key", key: "s3cret", want: "Bearer s3cret"},
		{name: "a key with surrounding space", key: "  s3cret \n", want: "Bearer s3cret"},
		{name: "no key", key: "", want: ""},
		{name: "a blank key", key: "   ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := NewRequest(context.Background(), rawURL, tc.key)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			if got := req.Header.Get("Authorization"); got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
			if got := req.URL.String(); got != rawURL {
				t.Errorf("URL = %q, want it unchanged as %q", got, rawURL)
			}
			if req.Header.Get("User-Agent") == "" || req.Header.Get("Accept") != "application/json" {
				t.Errorf("headers = %v, want a User-Agent and a JSON Accept", req.Header)
			}
		})
	}
}

// TestNewRequest_RefusesAMalformedURL checks a URL net/http cannot parse is an
// error rather than a request.
func TestNewRequest_RefusesAMalformedURL(t *testing.T) {
	if _, err := NewRequest(context.Background(), "http://[::1", ""); err == nil {
		t.Fatal("NewRequest accepted a malformed URL")
	}
}
