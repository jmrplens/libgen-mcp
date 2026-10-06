// The handshake Instructions text and its renderer.

package instructions

import (
	"fmt"
	"strings"
)

// The pieces Render assembles. The text is the one place that tells a
// connecting model how the tools chain together, since each tool's own
// description documents only itself. It goes straight into the model's system
// prompt, so it stays short and names only what a client cannot otherwise infer
// from the tool list. cmd/server's TestServerInstructionsNameEveryToolAndPrompt
// guards that every name below still exists on the registered surface.
//
// It is served text, so it follows the gateway rule: ASCII prose with no
// semicolon. A sentence that wanted one is split rather than given a
// substitute, because a hyphen where a dash was reads as a range.
const (
	opening = "personal-library-mcp searches, retrieves and reads books, papers, comics, magazines and standards." +
		" No tool needs an account or an API key."
	// openingNoFetch is the opening for a deployment that does not fetch
	// files: it still searches and retrieves, but reading a file's text is
	// the client's to do.
	openingNoFetch = "personal-library-mcp searches and retrieves books, papers, comics, magazines and standards." +
		" No tool needs an account or an API key."

	workflow = "WORKFLOW: the tools chain by identifier. search returns each record's md5 (books)" +
		" or doi (articles). Carry that identifier into the next call."

	stepSearch  = "search: find candidate records across the catalog and, when needed, open-access sources."
	stepDetails = "get_details: full metadata and ready-to-paste BibTeX and RIS for a record you already identified," +
		" or for a reference pasted as citation. cite_as adds styles such as APA or IEEE, and related lists" +
		" the works it cites or that cite it. It does not fetch the file. Use it whenever a citation is requested."
	// Two download steps, because the two deployments honor different
	// contracts and the numbered step is what a model follows. Telling it a
	// link-only server saves the file is the same defect the tool's own
	// description was rewritten to remove, one layer up.
	stepDownload = "download: save the file by md5 (book), doi (article) or isbn (openly licensed book sources)." +
		" resolve_only=true returns a link without saving."
	stepDownloadLinkOnly = "download: resolve a copy by md5 (book), doi (article) or isbn (openly licensed book sources)." +
		" This deployment always returns a link to fetch yourself, never a saved file."
	stepRead = "read: extract, paginate, search within (find), outline, or read one table-of-contents entry" +
		" (section) of a file's text by the same md5/doi (or a local path). It fetches the file itself," +
		" so it does not require calling download first."

	// noFetch is stated once, where a model that expected to read text will
	// look: this deployment has no read tool, and the way to a file's
	// contents is the link.
	noFetch = "THIS DEPLOYMENT DOES NOT FETCH FILES. There is no read tool here. download returns a" +
		" direct link. Fetch it yourself to read the file's text."

	prompts = "PROMPTS: acquire_book, research_topic, get_paper and download_troubleshoot wrap these" +
		" tools into ready-made, step-by-step workflows. Prefer one of them over calling the tools ad hoc when the" +
		" user's request matches its shape."
)

// Render returns the Instructions for the surface a deployment registers. A
// server that may not fetch file bodies has no read tool, so the text neither
// numbers a step for it nor claims the server reads files: instructions naming
// a tool that is not there cost the model the same wasted turn that hiding the
// tool exists to save.
//
// linkOnly is the download tool's contract, which is a separate question: a
// remote deployment with fetching enabled still serves read and still only
// ever returns links. Not fetching implies link-only. The reverse does not
// hold.
func Render(serverFetch, linkOnly bool) string {
	download := stepDownload
	if linkOnly {
		download = stepDownloadLinkOnly
	}
	open, steps := opening, []string{stepSearch, stepDetails, download, stepRead}
	if !serverFetch {
		open, steps = openingNoFetch, []string{stepSearch, stepDetails, download}
	}
	numbered := make([]string, 0, len(steps))
	for i, step := range steps {
		numbered = append(numbered, fmt.Sprintf("%d. %s", i+1, step))
	}
	chain := workflow + "\n" + strings.Join(numbered, "\n")

	paragraphs := []string{open, chain}
	if !serverFetch {
		paragraphs = append(paragraphs, noFetch)
	}
	return strings.Join(append(paragraphs, prompts), "\n\n")
}

// Variant names one combination of the two switches Render takes.
type Variant struct {
	// Name says which deployment the variant is, for a report.
	Name string
	// ServerFetch and LinkOnly are Render's arguments.
	ServerFetch, LinkOnly bool
}

// Variants lists every combination a deployment can serve. Not fetching
// implies link-only, so a server that neither fetches nor saves is the third
// and last: an audit of served text covers all three.
func Variants() []Variant {
	return []Variant{
		{Name: "local", ServerFetch: true, LinkOnly: false},
		{Name: "remote with read", ServerFetch: true, LinkOnly: true},
		{Name: "remote without read", ServerFetch: false, LinkOnly: true},
	}
}
