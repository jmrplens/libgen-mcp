package toolutil

import (
	"encoding/base64"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// decodeIconSVG extracts and decodes the raw SVG source a data: URI icon
// carries, failing the test if the URI is not the "data:<mime>;base64,<data>"
// shape [icon] produces.
func decodeIconSVG(t *testing.T, ic mcp.Icon) string {
	t.Helper()
	prefix := "data:" + svgMIME + ";base64,"
	if !strings.HasPrefix(ic.Source, prefix) {
		t.Fatalf("icon source = %q, want a %q-prefixed data URI", ic.Source, prefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ic.Source, prefix))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	return string(raw)
}

// TestIcon_SingleThemelessEntry pins the shape [icon] produces: exactly one
// entry, the right MIME type and a scalable size, no theme, and a source that
// decodes back to the exact SVG passed in.
func TestIcon_SingleThemelessEntry(t *testing.T) {
	got := icon(svgSearch)
	if len(got) != 1 {
		t.Fatalf("icon() returned %d entries, want 1", len(got))
	}
	entry := got[0]
	if entry.MIMEType != svgMIME {
		t.Errorf("MIMEType = %q, want %q", entry.MIMEType, svgMIME)
	}
	if len(entry.Sizes) != 1 || entry.Sizes[0] != "any" {
		t.Errorf("Sizes = %v, want [any]", entry.Sizes)
	}
	if entry.Theme != "" {
		t.Errorf("Theme = %q, want empty: a currentColor icon needs no theme", entry.Theme)
	}
	if decodeIconSVG(t, entry) != svgSearch {
		t.Error("decoded source does not round-trip to the original SVG")
	}
}

// TestIconBrand_SingleEntryNoTheme pins the brand mark to the same
// single-entry, themeless shape every other icon uses: under SEP-2575 the
// whole Implementation (Icons included) rides in the `_meta` of every
// response, not just the handshake, so a two-theme pair would double that
// recurring cost.
func TestIconBrand_SingleEntryNoTheme(t *testing.T) {
	if len(IconBrand) != 1 {
		t.Fatalf("IconBrand has %d entries, want 1 (no light/dark pair)", len(IconBrand))
	}
	if IconBrand[0].Theme != "" {
		t.Errorf("IconBrand[0].Theme = %q, want empty: a currentColor icon needs no theme", IconBrand[0].Theme)
	}
	if decodeIconSVG(t, IconBrand[0]) != svgBrand {
		t.Error("IconBrand does not round-trip to svgBrand")
	}
}

// TestPackageIcons_AreWellFormedSVG guards every hand-written SVG constant in
// this package against a malformed edit (an unclosed tag, a stray quote): each
// must parse as well-formed XML with a top-level <svg> element. It cannot
// catch a shape that parses but renders wrong — the icons were checked
// visually before being committed — but it does catch the mechanical breakage
// a future hand edit is most likely to introduce.
func TestPackageIcons_AreWellFormedSVG(t *testing.T) {
	named := map[string][]mcp.Icon{
		"IconSearch":               IconSearch,
		"IconDetails":              IconDetails,
		"IconDownload":             IconDownload,
		"IconRead":                 IconRead,
		"IconAcquireBook":          IconAcquireBook,
		"IconResearchTopic":        IconResearchTopic,
		"IconGetPaper":             IconGetPaper,
		"IconDownloadTroubleshoot": IconDownloadTroubleshoot,
		"IconBrand":                IconBrand,
	}
	for name, icons := range named {
		for i, ic := range icons {
			svg := decodeIconSVG(t, ic)
			var doc struct {
				XMLName xml.Name `xml:"svg"`
			}
			if err := xml.Unmarshal([]byte(svg), &doc); err != nil {
				t.Errorf("%s[%d]: not well-formed SVG: %v", name, i, err)
			}
		}
	}
}
