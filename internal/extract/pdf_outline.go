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

// largePDFOutlineReason is reported for a PDF that has a readable text layer
// and an outline with more items than maxOutlineItems or nested deeper than
// maxOutlineDepth. No real table of contents comes near either bound, and
// listing the part before the bound would present it as the whole, so none of
// it is listed. TestLargePDFOutlineReason holds the two numbers to the bounds.
const largePDFOutlineReason = "the embedded table of contents is larger than this reader lists (over 20000 entries, or nested over 64 levels deep), so none of it is listed; the text layer is readable, so read the text sequentially or use find"

// Bounds on the outline walk. An outline is a linked structure the file
// controls, so a /Next or /First that leads back to an item already read is a
// cycle, and the walk stops at it rather than going round it. The item budget
// is what bounds the work when the structure is merely enormous, the same
// answer pageTreeReason gives a page tree. maxOutlineItems is far past any
// real table of contents, and maxOutlineDepth matches maxPageTreeDepth.
//
// The name tree has no budget of names, because no number is past every real
// one: a document holds a named destination for every link target, and the
// Intel 64 and IA-32 manual holds 292,930 of them. A budget of 50,000 left
// most of its entries on page 0 with nothing saying so. readNameTree reads
// each object of the tree once instead, so the read is as long as the tree
// the file holds, keeps only the names the outline asks for, and ends with
// the read's context. maxNameTreeDepth bounds its descent.
const (
	maxOutlineItems  = 20_000
	maxOutlineDepth  = 64
	maxNameTreeDepth = 32
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
	entries, outline, err := pdfBookmarkEntries(ctx, d)
	if err != nil {
		return OutlineResult{}, err
	}
	if len(entries) > 0 {
		return OutlineResult{Format: "pdf", Extractable: true, Entries: entries}, nil
	}
	return pdfNoOutlineResult(ctx, d, outline)
}

// outlineState is what a walk found an outline to be.
type outlineState int

// The three things an outline walk can find.
const (
	// outlineWhole is an outline read to its end, or no outline at all.
	outlineWhole outlineState = iota
	// outlineDamaged is an outline the walk could not follow to its end: the
	// reader broke on an item, or a link an item states leads to no item.
	outlineDamaged
	// outlineTooLarge is an outline with items past the walk's item budget or
	// depth bound.
	outlineTooLarge
)

// pdfBookmarkEntries reads the PDF's outline, flattened in document order, and
// says what it found.
//
// A file the reader will not open, or whose page tree is unsafe to walk,
// yields no entries and no verdict of its own: the text-layer probe that
// follows produces one, in the words the text path uses for the same file. A
// walk that panics part-way, that meets a link to nothing, or that reaches a
// bound with items left drops whatever it had collected and says which,
// because a table of contents cut short would be read as the whole of one.
// Only ctx ending yields an error.
func pdfBookmarkEntries(ctx context.Context, d document) (entries []OutlineEntry, outline outlineState, err error) {
	defer func() {
		if recover() != nil {
			entries, outline, err = nil, outlineDamaged, nil
		}
	}()
	r, strs := outlineReader(d)
	if r == nil {
		return nil, outlineWhole, nil
	}
	w := newOutlineWalk(r.Trailer(), strs)
	outlines := w.link(&w.root, "Outlines")
	w.walk(ctx, w.link(&outlines, "First"), 0)
	if w.state == outlineWhole {
		w.resolveNames(ctx)
	}
	if w.state != outlineWhole {
		return nil, w.state, w.items.err
	}
	return w.entries, outlineWhole, w.items.err
}

// stringDecoder returns the bytes of raw, a string the reader handed over
// that the file wrote in the object in, or false when they cannot be told.
type stringDecoder func(raw string, in objRef) (string, bool)

// readerStrings decodes the strings of a file the reader decrypts itself: as
// they come, or, when the file is encrypted with AES, without the padding the
// reader leaves on them.
func readerStrings(aesPadded bool) stringDecoder {
	return func(raw string, _ objRef) (string, bool) {
		if aesPadded {
			return unpadAES(raw), true
		}
		return raw, true
	}
}

// outlineReader opens d for the outline walk and says how the walk reads its
// strings. A file the reader opens and decrypts correctly is walked as the
// reader hands it over; the reader opens a crypt filter (V=4) only when it is
// AES-128, so V=4 there is AES. One encrypted with an RC4 key the reader
// decrypts into other bytes, or refused in a way selfDecryptable names, is
// walked with its encryption hidden from the reader and its strings decrypted
// by selfDecrypting, which reads them as the text path cannot: only the
// strings, and only of objects outside a compressed object stream, which the
// reader would have to decrypt. Anything else, or a page tree unsafe to walk,
// yields a nil reader, and the text-layer probe that follows gives the reason.
func outlineReader(d document) (*pdf.Reader, stringDecoder) {
	r, err := openReader(d)
	if err == nil && !rc4KeyTooShort(r) {
		return walkable(r, readerStrings(encryptionOf(r).version == 4))
	}
	if err == nil || selfDecryptable(err) {
		return walkable(selfDecrypting(d))
	}
	return nil, nil
}

// walkable returns r and strs, or nothing when there is no reader or its page
// tree is unsafe to walk.
func walkable(r *pdf.Reader, strs stringDecoder) (*pdf.Reader, stringDecoder) {
	if r == nil || pageTreeReason(r) != "" {
		return nil, nil
	}
	return r, strs
}

// pdfNoOutlineResult decides what to report for a PDF that yielded no outline
// entries, by asking the same question the text path asks: does this file have
// a text layer? A readable one is extractable with no entries, saying whether
// the outline is absent, damaged or too large to list; a text-free one is
// reported as scanned; an unreadable one carries the reader's own diagnosis,
// which for an encrypted file says it is encrypted. The text path's answer
// for a file whose outline the walk decrypts points at outline mode, which is
// where this is, and which listed nothing, so it is encryptedPDFReason here.
func pdfNoOutlineResult(ctx context.Context, d document, outline outlineState) (OutlineResult, error) {
	state, reason, err := probePDFTextLayer(ctx, d)
	if err != nil {
		return OutlineResult{}, err
	}
	if reason == partlyEncryptedPDFReason {
		reason = encryptedPDFReason
	}
	switch state {
	case pdfTextAbsent:
		return OutlineResult{Format: "pdf", Reason: noTextLayerReason}, nil
	case pdfTextUnreadable:
		return OutlineResult{Format: "pdf", Reason: reason}, nil
	}
	switch outline {
	case outlineDamaged:
		return OutlineResult{Format: "pdf", Extractable: true, Reason: damagedPDFOutlineReason, unreadable: true}, nil
	case outlineTooLarge:
		return OutlineResult{Format: "pdf", Extractable: true, Reason: largePDFOutlineReason, unreadable: true}, nil
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
	if b.left <= 0 || b.ended(ctx) {
		return false
	}
	b.left--
	return true
}

// ended reports whether ctx, or an earlier step's, has ended, keeping the
// error, without taking a step.
func (b *walkBudget) ended(ctx context.Context) bool {
	if b.err == nil {
		b.err = ctx.Err()
	}
	return b.err != nil
}

// node is a value the outline walk reads and the object its strings are
// written in, whose key encrypts them in an encrypted file: the object the
// value is, when it was reached through a reference, and otherwise the object
// of the value that holds it. refs is dictRefs(v), read in by child on first
// use, since an item is asked for four or five of its keys.
type node struct {
	v    pdf.Value
	in   objRef
	refs map[string]objRef
}

// outlineWalk is the state of one outline read.
type outlineWalk struct {
	// root is the document catalog, where pages and named destinations are
	// looked up.
	root node
	// strs decodes the strings the reader hands over.
	strs stringDecoder
	// items bounds the outline items read, and keeps the context's error for
	// the name tree read as well.
	items walkBudget
	// seen holds a digest of each item read, to stop at a cycle.
	seen map[[sha256.Size]byte]bool
	// pages numbers each /Page leaf by its "id gen" reference. It is built on
	// the first destination that names a page.
	pages map[string]int
	// legacyDests is the catalog's PDF 1.1 /Dests dictionary, read in on first
	// use and kept: the reader keeps no object it has read, so looking a name
	// up through the catalog parsed the whole dictionary again for every
	// outline item that named one.
	legacyDests *pdf.Value
	// named holds, by name, the index of each entry whose destination is a
	// name /Dests does not hold, for the name tree to give a page once the
	// walk is done. A document holds a destination for every link in it and
	// the outline names a few of them, so the tree is read once, for these.
	named map[string][]int
	// entries is the outline so far, in document order.
	entries []OutlineEntry
	// state is what the walk has found the outline to be so far.
	state outlineState
}

// newOutlineWalk starts an outline read of the document whose trailer is
// trailer, whose strings strs decodes.
func newOutlineWalk(trailer pdf.Value, strs stringDecoder) *outlineWalk {
	t := node{v: trailer}
	return &outlineWalk{
		root:  child(&t, "Root"),
		strs:  strs,
		items: walkBudget{left: maxOutlineItems},
		seen:  map[[sha256.Size]byte]bool{},
		named: map[string][]int{},
	}
}

// child returns what the dictionary from holds under key, and the object its
// strings are written in: the object key refers to, or, for a value written
// in place, from's.
func child(from *node, key string) node {
	if from.refs == nil {
		from.refs = dictRefs(from.v)
	}
	in, ok := from.refs[key]
	if !ok {
		in = from.in
	}
	return node{v: from.v.Key(key), in: in}
}

// element returns the array arr's element i, and the object its strings are
// written in, which refs, arr's arrayRefs, says as child does.
func element(arr node, refs map[int]objRef, i int) node {
	in, ok := refs[i]
	if !ok {
		in = arr.in
	}
	return node{v: arr.v.Index(i), in: in}
}

// str returns the bytes of the string n, decoded as the walk's strings are.
// A string that cannot be decoded, an AES one that does not decrypt to a
// padded whole, is damage, and reads as empty.
func (w *outlineWalk) str(n node) string {
	s, ok := w.strs(n.v.RawString(), n.in)
	if !ok {
		w.state = outlineDamaged
	}
	return s
}

// walk appends item and its following siblings at level, each followed by its
// own children one level down. It stops at an item it has read before, which
// is where a cycle repeats the outline, and at an item past the depth bound or
// the item budget, which it records as outlineTooLarge. The budget is also
// spent once ctx has ended, and then the walk's error is what its caller
// returns.
func (w *outlineWalk) walk(ctx context.Context, item node, level int) {
	for item.v.Kind() == pdf.Dict {
		if level >= maxOutlineDepth || !w.items.spend(ctx) {
			w.state = outlineTooLarge
			return
		}
		if !w.firstVisit(item.v) {
			return
		}
		if title := outlineTitle(textString(w.str(child(&item, "Title")))); title != "" {
			page := w.destPage(&item, len(w.entries))
			w.entries = append(w.entries, OutlineEntry{Title: title, Level: level, Page: page})
		}
		w.walk(ctx, w.link(&item, "First"), level+1)
		item = w.link(&item, "Next")
	}
}

// link returns what from's key leads to. A reference that leads to no
// dictionary (to a free object, to one past the end of the cross-reference
// table, or to something that is not an item), and a value written in place
// that is neither null nor a dictionary, are recorded as outlineDamaged: the
// outline goes on past them, so what was read before them is not the whole. A
// key from does not state is the end of its chain, and so is one written as
// null, which ISO 32000-1 (7.3.9) makes the same as leaving the key out. That
// is how pdfcpu removes an outline: it writes /Outlines null.
func (w *outlineWalk) link(from *node, key string) node {
	to := child(from, key)
	if _, ref := from.refs[key]; to.v.Kind() != pdf.Dict && (ref || !to.v.IsNull()) {
		w.state = outlineDamaged
	}
	return to
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
// document. A name, looked up as a name and a string as a string, is read
// from the PDF 1.1 /Dests dictionary, and one that is not there gives 0 for
// now: it is recorded under at, the index of the entry that asks for it, for
// resolveNames to look up in the name tree.
func (w *outlineWalk) destPage(item *node, at int) int {
	dest := child(item, "Dest")
	if dest.v.IsNull() {
		action := child(item, "A")
		if action.v.Key("S").Name() != "GoTo" {
			return 0
		}
		dest = child(&action, "D")
	}
	var name string
	switch dest.v.Kind() {
	case pdf.Name:
		name = dest.v.Name()
	case pdf.String:
		name = w.str(dest)
	default:
		return w.pageOf(dest.v)
	}
	if legacy := w.legacy(name); !legacy.IsNull() {
		return w.pageOf(legacy)
	}
	w.named[name] = append(w.named[name], at)
	return 0
}

// legacy looks name up in the PDF 1.1 /Dests dictionary, which is read in
// once, on first use.
func (w *outlineWalk) legacy(name string) pdf.Value {
	if w.legacyDests == nil {
		d := w.root.v.Key("Dests")
		w.legacyDests = &d
	}
	return w.legacyDests.Key(name)
}

// pageOf returns the 1-based page an explicit destination names, a
// destination stored as a dictionary being its /D, and 0 when it names no
// page of this document.
func (w *outlineWalk) pageOf(dest pdf.Value) int {
	if dest.Kind() == pdf.Dict {
		dest = dest.Key("D")
	}
	if dest.Kind() != pdf.Array {
		return 0
	}
	m := leadingReferenceRE.FindStringSubmatch(dest.String())
	if m == nil {
		return 0
	}
	if w.pages == nil {
		w.pages = pageRefIndex(w.root.v.Key("Pages"))
	}
	return w.pages[m[1]+" "+m[2]]
}

// resolveNames reads the /Names /Dests name tree, when an entry named a
// destination /Dests does not hold, and gives each such entry the page the
// tree's destination for its name leads to.
func (w *outlineWalk) resolveNames(ctx context.Context) {
	if len(w.named) == 0 {
		return
	}
	names := child(&w.root, "Names")
	w.readNameTree(ctx, child(&names, "Dests"), 0, map[objRef]bool{})
}

// readNameTree reads the names the tree rooted at tree defines, for the ones
// w.named holds (readNames). read holds every object reached through a
// reference, a node or a /Names or /Kids array, and the tree skips one it
// holds, so each object is read once however often the tree refers to it,
// and a /Kids array that leads back to an ancestor ends there. It stops at
// the depth bound and once ctx has ended.
func (w *outlineWalk) readNameTree(ctx context.Context, tree node, depth int, read map[objRef]bool) {
	if tree.v.Kind() != pdf.Dict || depth >= maxNameTreeDepth {
		return
	}
	tree.refs = dictRefs(tree.v)
	if unread(tree.refs, "Names", read) {
		w.readNames(ctx, child(&tree, "Names"))
	}
	if !unread(tree.refs, "Kids", read) {
		return
	}
	kids := child(&tree, "Kids")
	kidRefs := arrayRefs(kids.v)
	for i := 0; i < kids.v.Len() && !w.items.ended(ctx); i++ {
		if unread(kidRefs, i, read) {
			w.readNameTree(ctx, element(kids, kidRefs, i), depth+1, read)
		}
	}
}

// readNames gives each entry that asked for a name the /Names array names
// holds the page that name's destination leads to, a later leaf's
// destination replacing an earlier one's. A name is a string, read as str
// reads one, so it matches the name an outline item gives however the file
// is encrypted. It stops once ctx has ended.
func (w *outlineWalk) readNames(ctx context.Context, names node) {
	refs := arrayRefs(names.v)
	for i := 0; i+1 < names.v.Len() && !w.items.ended(ctx); i += 2 {
		at, ok := w.named[w.str(element(names, refs, i))]
		if !ok {
			continue
		}
		page := w.pageOf(element(names, refs, i+1).v)
		for _, e := range at {
			w.entries[e].Page = page
		}
	}
}

// unread reports whether the value refs holds at k is not a reference to an
// object read holds, and records the object it refers to as read.
func unread[K comparable](refs map[K]objRef, k K, read map[objRef]bool) bool {
	ref, ok := refs[k]
	if !ok {
		return true
	}
	if read[ref] {
		return false
	}
	read[ref] = true
	return true
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
