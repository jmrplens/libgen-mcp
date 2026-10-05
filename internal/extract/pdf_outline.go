package extract

import (
	"context"
	"crypto/sha256"
	"regexp"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

// noPDFOutlineReason is reported for a PDF that has a readable text layer and
// carries no outline: the document really is readable, it simply has no table
// of contents.
//
// This reason and the ones below are each one literal rather than a
// concatenation: a + in a package-level declaration is a mutant no test can
// reach, since a declaration is in no coverage block.
const noPDFOutlineReason = "no embedded table of contents; the text layer is readable, so read the text sequentially or use find"

// damagedPDFOutlineReason is reported for a PDF that has a readable text layer
// and an outline the reader could not walk to the end: an outline object that
// does not parse, or one that is not where the cross-reference table says. The
// document does have a table of contents, so saying it has none would be false,
// and the pages are still there to read.
const damagedPDFOutlineReason = "the embedded table of contents is damaged and could not be read; the text layer is readable, so read the text sequentially or use find"

// Bounds on the outline walk. An outline is a linked structure the file
// controls, so a /Next or /First that leads back to an item already read is a
// cycle, and the walk stops at it rather than going round it. The budgets are
// what bound the work when the structure is merely enormous, the same answer
// pageTreeReason gives a page tree.
//
// maxOutlineItems is far past any real table of contents, and maxOutlineDepth
// matches maxPageTreeDepth. maxNameTreeVisits bounds the named destinations
// read in, of which a document holds one per link target rather than one per
// outline entry, so it is the larger of the two.
const (
	maxOutlineItems   = 20_000
	maxOutlineDepth   = 64
	maxNameTreeVisits = 50_000
	maxNameTreeDepth  = 32
)

// ledongthuc/pdf resolves a reference as it is read and keeps no object number
// on the value it returns, so the page an outline entry points at cannot be
// recognized by asking the page for its number. What it does keep is the text
// form of a value, and Value.String prints a reference it has not resolved as
// "N G R". The page tree's /Kids arrays and an explicit destination's first
// element are both printed that way, which is how a destination is matched to
// a page here. TestValueString_PrintsAnUnresolvedReference pins the format.
var (
	// referenceRE finds every reference in an array's text form.
	referenceRE = regexp.MustCompile(`(\d+) (\d+) R`)
	// leadingReferenceRE finds the reference that opens an array's text form,
	// which in an explicit destination is the page.
	leadingReferenceRE = regexp.MustCompile(`^\[(\d+) (\d+) R`)
)

// pdfOutline reads a PDF's embedded outline and returns it as a flat, in-order
// OutlineResult. The reader can panic on malformed input, so the walk is
// guarded: a panic becomes a diagnosis rather than a crash. A canceled ctx
// yields the context error.
//
// When no entries come back, the file is not simply declared TOC-less: it is
// probed for a text layer first, so a scanned PDF gets the same diagnosis here
// as it gets from Extract and Search. Reporting "no table of contents" for a
// document that has no text at all is worse than an error — it tells the caller
// the pages are readable and costs it another round trip to find out they are
// not.
func pdfOutline(ctx context.Context, d document) (OutlineResult, error) {
	if err := ctx.Err(); err != nil {
		return OutlineResult{}, err
	}
	entries, damaged, err := pdfBookmarkEntries(ctx, d)
	if err != nil {
		return OutlineResult{}, err
	}
	if len(entries) > 0 {
		return OutlineResult{Format: "pdf", Extractable: true, Entries: entries}, nil
	}
	return pdfNoOutlineResult(ctx, d, damaged)
}

// pdfBookmarkEntries reads the PDF's outline, flattened in document order.
//
// A file the reader will not open, or whose page tree is unsafe to walk,
// yields no entries and no verdict of its own: the text-layer probe that
// follows produces one, in the words the text path uses for the same file. A
// walk that panics part-way reports damaged, and whatever it had collected is
// dropped, because a table of contents cut short at an unknown point would be
// read as the whole of one. Only ctx ending yields an error.
func pdfBookmarkEntries(ctx context.Context, d document) (entries []OutlineEntry, damaged bool, err error) {
	defer func() {
		if recover() != nil {
			entries, damaged, err = nil, true, nil
		}
	}()
	r, why := openPDF(d)
	if why != "" {
		return nil, false, nil
	}
	root := r.Trailer().Key("Root")
	// The reader opens a crypt filter (V=4) only when it is AESV2, so V=4 is
	// AES here.
	w := newOutlineWalk(root, encryptionOf(r).version == 4)
	w.walk(ctx, root.Key("Outlines").Key("First"), 0)
	return w.entries, false, w.items.err
}

// pdfNoOutlineResult decides what to report for a PDF that yielded no outline
// entries, by asking the same question the text path asks: does this file have
// a text layer? A readable one is extractable with no entries, saying whether
// the outline is absent or damaged; a text-free one is reported as scanned; an
// unreadable one carries the reader's own diagnosis, which for an encrypted
// file names the encryption.
func pdfNoOutlineResult(ctx context.Context, d document, damaged bool) (OutlineResult, error) {
	state, reason, err := probePDFTextLayer(ctx, d)
	if err != nil {
		return OutlineResult{}, err
	}
	switch state {
	case pdfTextAbsent:
		return OutlineResult{Format: "pdf", Reason: noTextLayerReason}, nil
	case pdfTextUnreadable:
		return OutlineResult{Format: "pdf", Reason: reason}, nil
	}
	if damaged {
		return OutlineResult{Format: "pdf", Extractable: true, Reason: damagedPDFOutlineReason}, nil
	}
	return OutlineResult{Format: "pdf", Extractable: true, Reason: noPDFOutlineReason}, nil
}

// walkBudget bounds one walk over a structure the file controls: it allows a
// fixed number of steps, and none once the walk's context has ended, keeping
// the context's error for the walk to return.
type walkBudget struct {
	left int
	err  error
}

// spend takes one step from the budget and reports whether the walk, running
// under ctx, may take it.
func (b *walkBudget) spend(ctx context.Context) bool {
	if b.left <= 0 || b.err != nil {
		return false
	}
	if err := ctx.Err(); err != nil {
		b.err = err
		return false
	}
	b.left--
	return true
}

// outlineWalk is the state of one outline read.
type outlineWalk struct {
	// root is the document catalog, where pages and named destinations are
	// looked up.
	root pdf.Value
	// aes is set when the file is encrypted with AES, whose padding the
	// reader leaves on every string it decrypts.
	aes bool
	// items bounds the outline items read.
	items walkBudget
	// seen holds a digest of each item read, to stop at a cycle.
	seen map[[sha256.Size]byte]bool
	// pages numbers each /Page leaf by its "id gen" reference. It is built on
	// the first destination that names a page.
	pages map[string]int
	// dests holds the name tree's named destinations, read in on first use.
	dests map[string]nameEntry
	// entries is the outline so far, in document order.
	entries []OutlineEntry
}

// nameEntry is where a name tree holds a destination: its position in a leaf's
// /Names array. The destination is resolved when an outline item asks for it
// rather than when the tree is read, because a document holds a destination
// for every link in it and the outline names a few of them.
type nameEntry struct {
	names pdf.Value
	at    int
}

// newOutlineWalk starts an outline read of the document whose catalog is
// root, encrypted with AES when aes is set.
func newOutlineWalk(root pdf.Value, aes bool) *outlineWalk {
	return &outlineWalk{
		root:  root,
		aes:   aes,
		items: walkBudget{left: maxOutlineItems},
		seen:  map[[sha256.Size]byte]bool{},
	}
}

// str returns the bytes of the string v, without the padding AES left on it
// when the file is encrypted with AES.
func (w *outlineWalk) str(v pdf.Value) string {
	if w.aes {
		return unpadAES(v.RawString())
	}
	return v.RawString()
}

// walk appends item and its following siblings at level, each followed by its
// own children one level down. It stops at the depth bound, when the item
// budget is spent or ctx has ended, and at an item it has read before.
func (w *outlineWalk) walk(ctx context.Context, item pdf.Value, level int) {
	if level >= maxOutlineDepth {
		return
	}
	for ; item.Kind() == pdf.Dict; item = item.Key("Next") {
		if !w.items.spend(ctx) || !w.firstVisit(item) {
			return
		}
		if title := outlineTitle(textString(w.str(item.Key("Title")))); title != "" {
			w.entries = append(w.entries, OutlineEntry{Title: title, Level: level, Page: w.destPage(ctx, item)})
		}
		w.walk(ctx, item.Key("First"), level+1)
	}
}

// firstVisit reports whether item has not been read before, recording it.
//
// An item is recognized by its dictionary's text form, which prints its /Next,
// /Prev, /First and /Parent as references, so two visits printing the same
// text are one item met twice. It is kept as a digest because the text also
// holds the title, which a file can make as long as it likes.
func (w *outlineWalk) firstVisit(item pdf.Value) bool {
	key := sha256.Sum256([]byte(item.String()))
	if w.seen[key] {
		return false
	}
	w.seen[key] = true
	return true
}

// outlineTitle returns an outline item's decoded title as text to show: a tab,
// a line break or another whitespace control becomes a space, which is what a
// producer that wrote "1\tPreface" meant, and every other C0 or C1 control is
// removed, since it renders as nothing or as a box. Bytes that are not UTF-8,
// which textString never returns, become U+FFFD.
func outlineTitle(text string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case !unicode.IsControl(r):
			return r
		case unicode.IsSpace(r):
			return ' '
		default:
			return -1
		}
	}, text))
}

// destPage resolves an outline item's /Dest, or the /D of its GoTo action, to
// a 1-based page number, and returns 0 when it does not lead to a page of this
// document.
func (w *outlineWalk) destPage(ctx context.Context, item pdf.Value) int {
	dest := item.Key("Dest")
	if dest.IsNull() {
		action := item.Key("A")
		if action.Key("S").Name() != "GoTo" {
			return 0
		}
		dest = action.Key("D")
	}
	dest = w.explicit(ctx, dest)
	if dest.Kind() != pdf.Array {
		return 0
	}
	m := leadingReferenceRE.FindStringSubmatch(dest.String())
	if m == nil {
		return 0
	}
	if w.pages == nil {
		w.pages = pageRefIndex(w.root.Key("Pages"))
	}
	return w.pages[m[1]+" "+m[2]]
}

// explicit turns a named destination into the explicit one it names, and
// returns an explicit destination as it is. A name is looked up as a name and
// a string as a string, and a destination stored as a dictionary is its /D.
func (w *outlineWalk) explicit(ctx context.Context, dest pdf.Value) pdf.Value {
	switch dest.Kind() {
	case pdf.Name:
		dest = w.named(ctx, dest.Name())
	case pdf.String:
		dest = w.named(ctx, w.str(dest))
	}
	if dest.Kind() == pdf.Dict {
		return dest.Key("D")
	}
	return dest
}

// named looks a destination name up in the PDF 1.1 /Dests dictionary, and
// then in the /Names /Dests name tree, which is read in on first use, under
// ctx.
func (w *outlineWalk) named(ctx context.Context, name string) pdf.Value {
	if dest := w.root.Key("Dests").Key(name); !dest.IsNull() {
		return dest
	}
	if w.dests == nil {
		w.dests = map[string]nameEntry{}
		visits := walkBudget{left: maxNameTreeVisits}
		w.collectNameTree(ctx, w.root.Key("Names").Key("Dests"), 0, &visits)
	}
	e, ok := w.dests[name]
	if !ok {
		return pdf.Value{}
	}
	return e.names.Index(e.at)
}

// collectNameTree records in w.dests where the name tree rooted at node holds
// each name, a later leaf's entry replacing an earlier one's. A name is a
// string, read as str reads one, so it matches the name an outline item gives
// whether or not AES padded either. It stops at the depth bound and when
// visits is spent or ctx has ended, so a /Kids array that leads back to an
// ancestor ends the read rather than repeating it.
func (w *outlineWalk) collectNameTree(ctx context.Context, node pdf.Value, depth int, visits *walkBudget) {
	if node.Kind() != pdf.Dict || depth >= maxNameTreeDepth {
		return
	}
	names := node.Key("Names")
	for i := 0; i+1 < names.Len(); i += 2 {
		if !visits.spend(ctx) {
			return
		}
		w.dests[w.str(names.Index(i))] = nameEntry{names: names, at: i + 1}
	}
	kids := node.Key("Kids")
	for i := range kids.Len() {
		if !visits.spend(ctx) {
			return
		}
		w.collectNameTree(ctx, kids.Index(i), depth+1, visits)
	}
}

// pageRefIndex numbers every /Page leaf under root by its "id gen" reference,
// in the order Reader.Page counts them and within the bounds walkPageTree
// uses.
func pageRefIndex(root pdf.Value) map[string]int {
	idx := pageIndex{byRef: map[string]int{}, kids: maxPageTreeKids}
	idx.add(root, 0)
	return idx.byRef
}

// pageIndex is the state of one page numbering: the pages numbered so far,
// how many have been met, and how many more /Kids entries may be inspected.
type pageIndex struct {
	byRef map[string]int
	count int
	kids  int
}

// add numbers the pages under node, which sits depth levels below the root.
// A /Kids array is matched to its references by position, so a node whose
// array holds a page written in place, which has no reference to match,
// counts its pages without indexing any of them.
func (idx *pageIndex) add(node pdf.Value, depth int) {
	if depth >= maxPageTreeDepth {
		return
	}
	children := node.Key("Kids")
	refs := referenceRE.FindAllStringSubmatch(children.String(), -1)
	aligned := len(refs) == children.Len()
	for i := range children.Len() {
		if idx.kids <= 0 {
			return
		}
		idx.kids--
		kid := children.Index(i)
		switch kid.Key("Type").Name() {
		case "Pages":
			idx.add(kid, depth+1)
		case "Page":
			idx.count++
			if aligned {
				idx.byRef[refs[i][1]+" "+refs[i][2]] = idx.count
			}
		}
	}
}
