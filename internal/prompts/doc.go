// Package prompts registers MCP prompt templates for the libgen server.
//
// A prompt here is an instruction-generating template: its handler may call the
// libgen client to gather candidate records, then returns a text message that
// tells the calling model what to do next (call get_details, then download).
// Prompts never perform downloads themselves.
package prompts
