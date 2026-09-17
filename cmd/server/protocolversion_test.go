package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestNegotiableProtocolVersionsIsTheSDKsListNarrowed pins both halves: the list
// comes from the SDK rather than from a copy here, and a stateful deployment
// drops the revisions its transport cannot serve.
//
// The narrowing is not cosmetic. StreamableServerTransport.SupportsProtocolVersion
// refuses every revision at or above 2026-07-28 unless the transport is
// stateless, so a stateful deployment that advertised one would be handing a
// client the single answer that cannot work — in the very error whose job is to
// say what to retry with.
func TestNegotiableProtocolVersionsIsTheSDKsListNarrowed(t *testing.T) {
	sdk := mcp.SupportedProtocolVersions()
	if len(sdk) == 0 {
		t.Fatal("the SDK reports no supported versions, so this test measures nothing")
	}

	t.Run("stateless advertises what the SDK supports", func(t *testing.T) {
		if got := negotiableProtocolVersions(true); !slices.Equal(got, sdk) {
			t.Errorf("negotiableProtocolVersions(true) = %q, want the SDK's own list %q", got, sdk)
		}
	})

	t.Run("stateful drops the stateless-only revisions", func(t *testing.T) {
		got := negotiableProtocolVersions(false)
		if len(got) == 0 {
			t.Fatal("a stateful deployment advertises nothing at all")
		}
		for _, version := range got {
			if version >= protocolVersionStatelessOnly {
				t.Errorf("%q is advertised by a stateful deployment, which cannot serve it", version)
			}
			if !slices.Contains(sdk, version) {
				t.Errorf("%q is advertised but the SDK does not support it", version)
			}
		}
		// And it is a narrowing rather than a different list: every SDK version
		// below the cut is still there.
		for _, version := range sdk {
			if version < protocolVersionStatelessOnly && !slices.Contains(got, version) {
				t.Errorf("%q was dropped although a stateful transport serves it", version)
			}
		}
	})
}

// versionRequest is a tools/list POST announcing a protocol revision, or none
// when version is empty.
func versionRequest(t *testing.T, version string) *http.Request {
	t.Helper()
	r := probeRequest(t, `{"jsonrpc":"2.0","id":42,"method":"tools/list"}`)
	if version != "" {
		r.Header.Set(protocolVersionHeader, version)
	}
	return r
}

// TestProtocolVersionGuardedPassesWhatTheDeploymentNegotiates is the negative
// half, and it contains the subtlety: a request with **no** version header
// passes, because it may be an initialize for any revision. That is the SDK's
// own condition, and a guard that refused it would refuse every first contact.
func TestProtocolVersionGuardedPassesWhatTheDeploymentNegotiates(t *testing.T) {
	for _, version := range append([]string{""}, mcp.SupportedProtocolVersions()...) {
		t.Run("version "+version, func(t *testing.T) {
			var reached bool
			handler := protocolVersionGuarded(true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusTeapot)
			}))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, versionRequest(t, version))

			if !reached || rec.Code != http.StatusTeapot {
				t.Errorf("status = %d, reached = %v: a version this deployment negotiates was refused (%q)", rec.Code, reached, rec.Body.String())
			}
		})
	}
}

// TestProtocolVersionGuardedAnswersAnUnrecognizedLegacyVersion is the branch
// this exists for.
//
// The SDK answers a revision at or above 2026-07-28 with a JSON-RPC error of its
// own; anything older gets http.Error's plain text. That difference is what
// matters, because the specification's backward-compatibility rule reads an
// unrecognizable 400 as an initialization-era server — so the client downgrades
// to the withdrawn HTTP+SSE transport, issues a GET, and a stateless deployment
// answers 405. It ends with no transport instead of one retry.
func TestProtocolVersionGuardedAnswersAnUnrecognizedLegacyVersion(t *testing.T) {
	var reached bool
	handler := protocolVersionGuarded(true, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, versionRequest(t, "2024-01-01"))

	if reached {
		t.Error("the refused request reached the handler behind the guard")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	body := decodeHTTPRefusal(t, rec)
	if body.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", body.JSONRPC)
	}
	if string(body.ID) != "42" {
		t.Errorf("id = %q, want the request's own id", body.ID)
	}
	if body.Error.Code != codeUnsupportedProtocolVersion {
		t.Errorf("code = %d, want %d, the code the specification names", body.Error.Code, codeUnsupportedProtocolVersion)
	}

	// The data member is what makes the refusal actionable: without the list a
	// client has nothing to retry with, which is the state the plain-text body
	// left it in.
	var envelope struct {
		Error struct {
			Data unsupportedVersionData `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding the data member: %v", err)
	}
	if envelope.Error.Data.Requested != "2024-01-01" {
		t.Errorf("data.requested = %q, want the version that was asked for", envelope.Error.Data.Requested)
	}
	if !slices.Equal(envelope.Error.Data.Supported, negotiableProtocolVersions(true)) {
		t.Errorf("data.supported = %q, want what this deployment negotiates", envelope.Error.Data.Supported)
	}
}

// TestProtocolVersionGuardedRefusesAStatelessOnlyVersionOnAStatefulServer is
// the case the narrowing buys, and the one a client can act on.
//
// A stateful deployment cannot serve 2026-07-28. Before the narrowing it would
// either advertise it — telling the client to retry with the one value that
// cannot work — or leave the SDK to answer, which for that revision it does
// natively. Here the guard answers first, and the list it returns has the
// revision removed.
func TestProtocolVersionGuardedRefusesAStatelessOnlyVersionOnAStatefulServer(t *testing.T) {
	handler := protocolVersionGuarded(false, teapotHandler())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, versionRequest(t, protocolVersionStatelessOnly))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var envelope struct {
		Error struct {
			Data unsupportedVersionData `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding the refusal: %v (%q)", err, rec.Body.String())
	}
	if slices.Contains(envelope.Error.Data.Supported, protocolVersionStatelessOnly) {
		t.Errorf("data.supported = %q, and it offers the one revision this deployment cannot serve",
			envelope.Error.Data.Supported)
	}
	if len(envelope.Error.Data.Supported) == 0 {
		t.Error("data.supported is empty, so the client has nothing to retry with")
	}
}
