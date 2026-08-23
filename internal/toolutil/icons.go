// Package toolutil holds small pieces of tool-registration infrastructure
// shared across the tool and prompt surfaces. Today that is only the icon set
// below; it exists as its own package so cmd/server, internal/tools and
// internal/prompts can all reach it without an import cycle.
package toolutil

import (
	"encoding/base64"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Brand icon: the two theme variants of the logo already published at
// site/src/assets/logo-{light,dark}.svg (dark marks for a light background,
// light marks for a dark one). Kept as literal copies here rather than an
// embed directive reaching into the site assets: that directive cannot reach
// outside its own package directory (see the root VERSION embed's doc
// comment), and the site's own build reads them directly, so there is no
// single directory both could embed from. The svg source is small and
// effectively static, so a manual copy carries little drift risk.
const (
	svgBrandLight = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="libgen-mcp"><path d="M28 8 h8 v14 h8 L32 36 20 22 h8 z" fill="#0f766e"/><path d="M32 39 C22 34 12 34 6 38 L6 51 C12 47 22 47 32 52 Z" fill="#0d9488"/><path d="M32 39 C42 34 52 34 58 38 L58 51 C52 47 42 47 32 52 Z" fill="#14b8a6"/></svg>`
	svgBrandDark  = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="libgen-mcp"><path d="M28 8 h8 v14 h8 L32 36 20 22 h8 z" fill="#99f6e4"/><path d="M32 39 C22 34 12 34 6 38 L6 51 C12 47 22 47 32 52 Z" fill="#2dd4bf"/><path d="M32 39 C42 34 52 34 58 38 L58 51 C52 47 42 47 32 52 Z" fill="#5eead4"/></svg>`
)

// Feature icons: one per tool and one per prompt, each a single-color
// currentColor SVG so it renders correctly against any client theme without
// needing a light/dark pair — unlike the brand mark above, none of these
// carries its own brand colors.
const (
	svgSearch               = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16"><circle cx="6.5" cy="6.5" r="4.5" fill="none" stroke="currentColor" stroke-width="1.5"/><line x1="10" y1="10" x2="14.5" y2="14.5" stroke="currentColor" stroke-width="1.5"/></svg>`
	svgDetails              = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16"><circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" stroke-width="1.5"/><line x1="8" y1="7" x2="8" y2="11.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/><circle cx="8" cy="4.5" r="1" fill="currentColor"/></svg>`
	svgDownload             = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 1.5v8m-3-3l3 3 3-3"/><line x1="2.5" y1="13.5" x2="13.5" y2="13.5"/></svg>`
	svgRead                 = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16"><path fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" d="M8 3C6 1.8 3.5 1.8 2 2.5v9.5c1.5-.7 4-.7 6 .5 2-1.2 4.5-1.2 6-.5V2.5C12.5 1.8 10 1.8 8 3z"/><line x1="8" y1="3" x2="8" y2="12.5" stroke="currentColor" stroke-width="1.3"/></svg>`
	svgAcquireBook          = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16"><rect x="3" y="2" width="10" height="12" rx="1" fill="none" stroke="currentColor" stroke-width="1.5"/><line x1="6" y1="2" x2="6" y2="14" stroke="currentColor" stroke-width="1.2"/><path fill="currentColor" d="M9.5 2v5l1.5-1.2L12.5 7V2"/></svg>`
	svgResearchTopic        = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16"><rect x="2" y="4" width="9" height="10" rx="1" fill="none" stroke="currentColor" stroke-width="1.3"/><rect x="5" y="1.5" width="9" height="10" rx="1" fill="none" stroke="currentColor" stroke-width="1.3"/><line x1="7" y1="4.5" x2="12" y2="4.5" stroke="currentColor" stroke-width="1"/></svg>`
	svgGetPaper             = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16"><path fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" d="M3.5 1.5h6l3 3v10h-9z"/><path fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" d="M9.5 1.5v3h3"/><line x1="5.5" y1="9" x2="10.5" y2="9" stroke="currentColor" stroke-width="1" stroke-linecap="round"/><line x1="5.5" y1="11.3" x2="9" y2="11.3" stroke="currentColor" stroke-width="1" stroke-linecap="round"/></svg>`
	svgDownloadTroubleshoot = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" stroke-linecap="round"><path d="M11 2.5a3 3 0 0 0-3.9 3.9L2 11.5 4.5 14l5.1-5.1a3 3 0 0 0 3.9-3.9l-2 2-1.5-.5-.5-1.5z"/></svg>`
)

const svgMIME = "image/svg+xml"

// icon wraps a single-variant SVG string as a one-element [mcp.Icon] slice
// with a base64-encoded data URI. Base64 encoding is required because raw SVG
// markup contains characters (<, >, ", spaces, #) that are not valid in an
// unencoded RFC 2397 data URI; the MCP spec also documents base64 as the
// canonical form for embedded image data.
//
// Sizes is set to ["any"] to advertise that the SVG is resolution-independent
// and can be rendered at any size by the client.
func icon(svg string) []mcp.Icon {
	return []mcp.Icon{dataURIIcon(svg, "")}
}

// brandIcon wraps a light-background and a dark-background SVG variant as a
// two-element [mcp.Icon] slice, each tagged with the [mcp.IconTheme] it was
// drawn for. It is only needed for the brand mark: the feature icons above
// use currentColor and so need no theme variant of their own.
func brandIcon(light, dark string) []mcp.Icon {
	return []mcp.Icon{dataURIIcon(light, mcp.IconThemeLight), dataURIIcon(dark, mcp.IconThemeDark)}
}

func dataURIIcon(svg string, theme mcp.IconTheme) mcp.Icon {
	encoded := base64.StdEncoding.EncodeToString([]byte(svg))
	return mcp.Icon{
		Source:   "data:" + svgMIME + ";base64," + encoded,
		MIMEType: svgMIME,
		Sizes:    []string{"any"},
		Theme:    theme,
	}
}

// IconBrand is the libgen-mcp logo, used on the MCP Implementation
// (serverInfo). It is the only icon in this package that needs a theme pair:
// everything else below is a currentColor line icon, one per tool
// (IconSearch, IconDetails, IconDownload, IconRead) and one per prompt
// (IconAcquireBook, IconResearchTopic, IconGetPaper,
// IconDownloadTroubleshoot).
var (
	IconBrand                = brandIcon(svgBrandLight, svgBrandDark)
	IconSearch               = icon(svgSearch)
	IconDetails              = icon(svgDetails)
	IconDownload             = icon(svgDownload)
	IconRead                 = icon(svgRead)
	IconAcquireBook          = icon(svgAcquireBook)
	IconResearchTopic        = icon(svgResearchTopic)
	IconGetPaper             = icon(svgGetPaper)
	IconDownloadTroubleshoot = icon(svgDownloadTroubleshoot)
)
