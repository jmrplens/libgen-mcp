// Package logging configures the server's structured logger over stderr.
//
// The stdout channel is reserved for the MCP JSON-RPC transport, so all logs are
// emitted in JSON format to os.Stderr via log/slog.
package logging
