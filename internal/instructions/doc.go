// Package instructions holds the text this server sends as the Instructions of
// its initialize result, and renders it for the surface a deployment registers.
//
// It is a package of its own rather than constants in cmd/server because two
// commands need the same text: the server, which serves it, and
// cmd/audit_gateway_chars, which holds everything served to the gateway rule
// (ASCII prose, no semicolon). A main package cannot be imported, so text kept
// there was text the gate could not read.
package instructions
