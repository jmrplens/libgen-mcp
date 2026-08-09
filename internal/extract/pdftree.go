package extract

import "github.com/ledongthuc/pdf"

// A PDF page tree is a tree only by convention: nothing in the file format
// stops a /Pages node from listing an ancestor — or itself — in its /Kids, and
// ledongthuc/pdf's Reader.Page walks that structure with no cycle check at all.
// Its descent loop then never terminates. A 224-byte file is enough: pdf.Open
// succeeds, NumPage answers 1, and Page(1) never returns, spinning in a hot
// loop that allocates roughly a gigabyte a second, so one such file pins a core
// and drags the whole process's garbage collector down with it.
//
// Nothing outside the library can stop that loop once it starts. It makes no
// system call, consults no context and never returns, so a caller can only
// abandon the goroutine running it — permanently. The only way not to pay the
// cost is not to make the call, so every entry point that is about to hand a
// file to Reader.Page walks the page tree itself first, within the bounds
// below, and refuses the file when the walk does not finish inside them.

// Bounds on the pre-flight page-tree walk.
//
// maxPageTreeDepth is the one that decides the question: Reader.Page loops
// forever exactly when its descent never ends, so capping descent depth is a
// direct test for the pathology rather than a proxy for it. 64 is far past any
// real document — a balanced tree that deep addresses 2^64 pages, and producers
// in practice emit a flat tree (depth 1) or a shallow one.
//
// maxPageTreeKids bounds the total work instead, so a file that is merely
// enormous, or whose nodes are cross-linked into a cycle-free graph that fans
// out exponentially, cannot make the check itself the expensive part. 200k kid
// inspections is an order of magnitude past the longest documents that exist.
const (
	maxPageTreeDepth = 64
	maxPageTreeKids  = 200_000
)

// cyclicPDFReason is the diagnosis for a PDF whose page tree cannot be walked
// within the bounds above. It is worded as a read failure rather than as a
// missing text layer: the pages may well be full of text, but no mode can reach
// them, and telling the caller the file is scanned would send it looking for an
// OCR it does not need. Shared so every read mode words it the same way.
const cyclicPDFReason = "cannot read PDF (malformed page tree: cyclic or nested beyond any legitimate depth)"

// pageTreeReason returns cyclicPDFReason when r's page tree is unsafe to hand to
// Reader.Page, and "" when it is safe. A document with no page tree at all is
// safe by the same argument: Reader.Page's loop only runs while it is standing
// on a /Pages node, so with none to stand on it returns immediately.
func pageTreeReason(r *pdf.Reader) string {
	kids := maxPageTreeKids
	if walkPageTree(r.Trailer().Key("Root").Key("Pages"), 0, &kids) {
		return ""
	}
	return cyclicPDFReason
}

// walkPageTree reports whether the subtree rooted at node terminates within the
// depth and kid budgets, decrementing *kids once per child inspected. It follows
// exactly what Reader.Page follows — a descent from one /Pages node into the
// next — so a tree it walks to the end is a tree Reader.Page cannot get lost in.
func walkPageTree(node pdf.Value, depth int, kids *int) bool {
	// A /Page leaf, or a dangling reference (which resolves to a null value),
	// ends the descent: Reader.Page steps into neither.
	if node.Key("Type").Name() != "Pages" {
		return true
	}
	if depth >= maxPageTreeDepth {
		return false
	}
	children := node.Key("Kids")
	for i := range children.Len() {
		if *kids <= 0 {
			return false
		}
		*kids--
		if !walkPageTree(children.Index(i), depth+1, kids) {
			return false
		}
	}
	return true
}
