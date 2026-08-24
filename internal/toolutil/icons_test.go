package toolutil

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"image"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "golang.org/x/image/webp" // registers "webp" with image.DecodeConfig
)

// allIcons returns every exported icon slice keyed by its variable name, so
// the shape tests below run exhaustively rather than over a sample.
func allIcons() map[string][]mcp.Icon {
	return map[string][]mcp.Icon{
		"IconBrand":                IconBrand,
		"IconSearch":               IconSearch,
		"IconDetails":              IconDetails,
		"IconDownload":             IconDownload,
		"IconRead":                 IconRead,
		"IconAcquireBook":          IconAcquireBook,
		"IconResearchTopic":        IconResearchTopic,
		"IconGetPaper":             IconGetPaper,
		"IconDownloadTroubleshoot": IconDownloadTroubleshoot,
	}
}

// decodeDataURI extracts and decodes the payload a data: URI icon carries,
// failing the test if the URI is not the "data:<mime>;base64,<data>" shape
// [icon] produces for its declared MIME type.
func decodeDataURI(t *testing.T, ic mcp.Icon) []byte {
	t.Helper()
	prefix := "data:" + ic.MIMEType + ";base64,"
	if !strings.HasPrefix(ic.Source, prefix) {
		t.Fatalf("icon source = %q, want a %q-prefixed data URI", ic.Source, prefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ic.Source, prefix))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	return raw
}

// TestIcon_ThreeEntryShape pins what [icon] produces for one icon: the
// scalable themeless SVG first, then the light and dark 16×16 WebP
// fallbacks, each with the right MIME type, size and theme. The SVG entry
// leads because a client that accepts SVG should prefer it.
func TestIcon_ThreeEntryShape(t *testing.T) {
	got := icon("search", svgSearch)
	if len(got) != 3 {
		t.Fatalf("icon() returned %d entries, want 3 (SVG + WebP light + WebP dark)", len(got))
	}

	svgEntry := got[0]
	if svgEntry.MIMEType != svgMIME {
		t.Errorf("SVG entry MIMEType = %q, want %q", svgEntry.MIMEType, svgMIME)
	}
	if len(svgEntry.Sizes) != 1 || svgEntry.Sizes[0] != "any" {
		t.Errorf("SVG entry Sizes = %v, want [any]", svgEntry.Sizes)
	}
	if svgEntry.Theme != "" {
		t.Errorf("SVG entry Theme = %q, want empty: a currentColor SVG needs no theme", svgEntry.Theme)
	}
	if string(decodeDataURI(t, svgEntry)) != svgSearch {
		t.Error("SVG entry does not round-trip to the original SVG")
	}

	for i, want := range []mcp.IconTheme{mcp.IconThemeLight, mcp.IconThemeDark} {
		entry := got[i+1]
		if entry.MIMEType != webpMIME {
			t.Errorf("entry %d MIMEType = %q, want %q", i+1, entry.MIMEType, webpMIME)
		}
		if entry.Theme != want {
			t.Errorf("entry %d Theme = %q, want %q", i+1, entry.Theme, want)
		}
		// A raster is not resolution-independent, so unlike the SVG it must
		// advertise the concrete size it was generated at.
		if len(entry.Sizes) != 1 || entry.Sizes[0] != "16x16" {
			t.Errorf("entry %d Sizes = %v, want [16x16]", i+1, entry.Sizes)
		}
	}
}

// TestAllIcons_ThreeEntries verifies no icon was wired up with the old
// single-entry shape, which would silently lose the WebP fallback for the
// clients it exists to serve.
func TestAllIcons_ThreeEntries(t *testing.T) {
	for name, icons := range allIcons() {
		if len(icons) != 3 {
			t.Errorf("%s has %d entries, want 3 (SVG + WebP light + WebP dark)", name, len(icons))
		}
	}
}

// TestAllIcons_BindToTheirOwnSVGAndAssets is the one test that pins WHICH
// artwork each exported variable actually carries, rather than just that it
// carries something well-formed.
//
// It exists because [icon] takes the asset name as a second argument
// independent of the SVG: `IconRead = icon("search", svgSearch)` is a
// perfectly valid call that compiles, embeds real assets, decodes to a real
// 16×16 WebP, and ships the wrong glyph for the read tool. Every other test
// here is name-agnostic and stays green under exactly that mutation, and
// nothing else in the build catches it either — `make check-icon-webp` reads
// the svg<Name> constants, never the variable bindings, and it is not a CI
// gate anyway.
//
// The table is written out by hand on purpose: deriving the expected name
// from the variable would reimplement the mapping under test and assert
// nothing.
func TestAllIcons_BindToTheirOwnSVGAndAssets(t *testing.T) {
	bindings := []struct {
		varName string
		asset   string
		svg     string
		icons   []mcp.Icon
	}{
		{"IconBrand", "brand", svgBrand, IconBrand},
		{"IconSearch", "search", svgSearch, IconSearch},
		{"IconDetails", "details", svgDetails, IconDetails},
		{"IconDownload", "download", svgDownload, IconDownload},
		{"IconRead", "read", svgRead, IconRead},
		{"IconAcquireBook", "acquirebook", svgAcquireBook, IconAcquireBook},
		{"IconResearchTopic", "researchtopic", svgResearchTopic, IconResearchTopic},
		{"IconGetPaper", "getpaper", svgGetPaper, IconGetPaper},
		{"IconDownloadTroubleshoot", "downloadtroubleshoot", svgDownloadTroubleshoot, IconDownloadTroubleshoot},
	}
	if len(bindings) != len(allIcons()) {
		t.Fatalf("table covers %d icons but %d are exported; a new icon needs a row here",
			len(bindings), len(allIcons()))
	}

	for _, b := range bindings {
		t.Run(b.varName, func(t *testing.T) {
			if string(decodeDataURI(t, b.icons[0])) != b.svg {
				t.Errorf("SVG entry does not carry %s's own markup", b.varName)
			}
			for i, variant := range []string{"light", "dark"} {
				want, err := webpFS.ReadFile("icons/webp/" + b.asset + "-" + variant + ".webp")
				if err != nil {
					t.Fatalf("reading the expected %s asset: %v", variant, err)
				}
				if !bytes.Equal(decodeDataURI(t, b.icons[i+1]), want) {
					t.Errorf("%s entry does not carry the bytes of icons/webp/%s-%s.webp",
						variant, b.asset, variant)
				}
			}
		})
	}
}

// TestAllIcons_SVGEntryIsWellFormed guards every hand-written SVG constant
// against a malformed edit (an unclosed tag, a stray quote): each must parse
// as well-formed XML with a top-level <svg> element. It cannot catch a shape
// that parses but renders wrong — the icons and their rasterized fallbacks
// were checked visually before being committed — but it does catch the
// mechanical breakage a future hand edit is most likely to introduce.
func TestAllIcons_SVGEntryIsWellFormed(t *testing.T) {
	for name, icons := range allIcons() {
		t.Run(name, func(t *testing.T) {
			svg := decodeDataURI(t, icons[0])
			var doc struct {
				XMLName xml.Name `xml:"svg"`
			}
			if err := xml.Unmarshal(svg, &doc); err != nil {
				t.Errorf("SVG entry is not well-formed: %v", err)
			}
		})
	}
}

// TestAllIcons_WebPFallbacksDecode verifies both raster entries really are
// decodable 16×16 WebP images, catching a corrupt embed or an asset that
// drifted from the size its Sizes field advertises. Asserting the declared
// MIME type is not enough: the bytes are generated by an external tool
// (cmd/gen_icon_webp shells out to rsvg-convert and cwebp), so nothing else
// in the build proves they are the image this package claims.
func TestAllIcons_WebPFallbacksDecode(t *testing.T) {
	for name, icons := range allIcons() {
		t.Run(name, func(t *testing.T) {
			for i, ic := range icons[1:] {
				raw := decodeDataURI(t, ic)
				cfg, format, err := image.DecodeConfig(strings.NewReader(string(raw)))
				if err != nil {
					t.Fatalf("entry %d: image.DecodeConfig failed: %v", i+1, err)
				}
				if format != "webp" {
					t.Errorf("entry %d: decoded format = %q, want webp", i+1, format)
				}
				if cfg.Width != 16 || cfg.Height != 16 {
					t.Errorf("entry %d: decoded dimensions = %dx%d, want 16x16", i+1, cfg.Width, cfg.Height)
				}
			}
		})
	}
}

// TestAllIcons_LightAndDarkDiffer verifies the two raster variants are not
// byte-identical. They are rendered from the same SVG with only the glyph
// color substituted, so a bug that dropped the substitution — or a
// copy-paste in the generator — would yield two identical files and a
// dark-theme client would get a near-black glyph on its own background.
func TestAllIcons_LightAndDarkDiffer(t *testing.T) {
	for name, icons := range allIcons() {
		t.Run(name, func(t *testing.T) {
			if icons[1].Source == icons[2].Source {
				t.Error("light and dark WebP entries are byte-identical; the color substitution did not take")
			}
		})
	}
}

// TestWebpIcon_PanicsOnMissingAsset verifies webpIcon fails loudly, with a
// message that names the culprit and the fix, rather than returning a broken
// mcp.Icon a client would fail to render at runtime. Every svg<Name>
// constant must have a generated pair; a missing one is a build-time
// mistake.
func TestWebpIcon_PanicsOnMissingAsset(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("webpIcon() did not panic for a name with no embedded asset")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "missing embedded icon asset") || !strings.Contains(msg, "gen_icon_webp") {
			t.Errorf("panic value = %v, want it to name the missing asset and point at cmd/gen_icon_webp", r)
		}
	}()
	webpIcon("this-icon-name-has-no-webp-asset", "light", mcp.IconThemeLight)
}
