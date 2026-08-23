package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMarshalModelFacing_ToolClearsIcons verifies a *mcp.Tool's Icons never
// survive into the marshaled bytes, while every other field round-trips.
func TestMarshalModelFacing_ToolClearsIcons(t *testing.T) {
	tool := &mcp.Tool{
		Name: "search", Description: "does a thing",
		Icons: []mcp.Icon{{Source: "data:image/svg+xml;base64," + strings.Repeat("QQ", 100), MIMEType: "image/svg+xml"}},
	}
	data, err := marshalModelFacing(tool)
	if err != nil {
		t.Fatalf("marshalModelFacing() error = %v", err)
	}
	if strings.Contains(string(data), "icons") {
		t.Errorf("marshaled tool still carries an icons key: %s", data)
	}

	var decoded mcp.Tool
	if uErr := json.Unmarshal(data, &decoded); uErr != nil {
		t.Fatalf("unmarshal: %v", uErr)
	}
	if decoded.Name != tool.Name || decoded.Description != tool.Description {
		t.Errorf("decoded = %+v, want name/description preserved from %+v", decoded, tool)
	}
}

// TestMarshalModelFacing_ToolWithoutIconsIsUnaffected pins that a tool with no
// Icons of its own marshals identically whether or not it goes through this
// function, so the clearing is a no-op for the common case.
func TestMarshalModelFacing_ToolWithoutIconsIsUnaffected(t *testing.T) {
	tool := &mcp.Tool{Name: "search", Description: "does a thing"}
	got, err := marshalModelFacing(tool)
	if err != nil {
		t.Fatalf("marshalModelFacing() error = %v", err)
	}
	want, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("marshalModelFacing(%+v) = %s, want %s", tool, got, want)
	}
}

// TestMarshalModelFacing_PromptClearsIcons mirrors
// TestMarshalModelFacing_ToolClearsIcons for *mcp.Prompt.
func TestMarshalModelFacing_PromptClearsIcons(t *testing.T) {
	prompt := &mcp.Prompt{
		Name: "acquire_book", Description: "does a thing",
		Icons: []mcp.Icon{{Source: "data:image/svg+xml;base64," + strings.Repeat("QQ", 100), MIMEType: "image/svg+xml"}},
	}
	data, err := marshalModelFacing(prompt)
	if err != nil {
		t.Fatalf("marshalModelFacing() error = %v", err)
	}
	if strings.Contains(string(data), "icons") {
		t.Errorf("marshaled prompt still carries an icons key: %s", data)
	}

	var decoded mcp.Prompt
	if uErr := json.Unmarshal(data, &decoded); uErr != nil {
		t.Fatalf("unmarshal: %v", uErr)
	}
	if decoded.Name != prompt.Name || decoded.Description != prompt.Description {
		t.Errorf("decoded = %+v, want name/description preserved from %+v", decoded, prompt)
	}
}

// TestMarshalModelFacing_DefaultPassesThrough verifies a type with no
// presentation-only fields to strip marshals exactly like plain json.Marshal —
// the fallback branch a future catalog entry type would hit until it earns its
// own case in the switch.
func TestMarshalModelFacing_DefaultPassesThrough(t *testing.T) {
	entry := struct {
		Name string `json:"name"`
	}{Name: "plain"}
	got, err := marshalModelFacing(&entry)
	if err != nil {
		t.Fatalf("marshalModelFacing() error = %v", err)
	}
	want, err := json.Marshal(&entry)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("marshalModelFacing(%+v) = %s, want %s", entry, got, want)
	}
}
