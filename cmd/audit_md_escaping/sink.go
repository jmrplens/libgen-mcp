package main

import (
	"go/ast"
	"go/printer"
	"go/token"
	"maps"
	"strconv"
	"strings"
)

// sinkHole is one place a runtime value lands in a Markdown document: the
// expression that produces it, the construct it lands in, the verb that prints
// it, and the function it was written in.
type sinkHole struct {
	expr ast.Expr
	fn   *funcInfo
	ctx  mdContext
	verb string
	pos  token.Pos
}

// verbWrite is the verb a hole carries when the value is written as it is,
// with no template around it: a WriteString, a Fprint operand.
const verbWrite = "write"

// safeVerbs render a value as something that can carry no Markdown construct:
// a number in any base, a boolean, a pointer. Whatever a catalog put in the
// value, the bytes on the page are digits, "true" or "false".
//
// The quoting verbs are deliberately absent. %q wraps a string in quotes and
// escapes what Go escapes — a newline becomes "\n", which is genuinely
// contained — but it leaves a pipe a pipe, so a %q value still ends the table
// cell it sits in.
var safeVerbs = map[string]bool{
	"%t": true, "%d": true, "%f": true, "%e": true, "%E": true, "%g": true,
	"%G": true, "%b": true, "%o": true, "%O": true, "%x": true, "%X": true,
	"%U": true, "%p": true, "%*": true,
}

// escapable reports whether the escaping verdict applies to this hole.
func (h sinkHole) escapable() bool { return !safeVerbs[h.verb] }

// sprintfDest is the destination a fmt.Sprintf write is followed under.
//
// Sprintf writes into no document of its own, so its template is read from a
// cursor that starts fresh at the beginning of a line rather than from the one
// the surrounding builder is at: the string it returns may be written anywhere,
// and the constructs its own template opens are the ones it answers for. That
// is how the link this server writes — a Sprintf of "[%s](%s)" — is judged as
// a link at all. The name is not a Go identifier, so it can never collide with
// a builder the same function writes to.
const sprintfDest = "fmt.Sprintf()"

// fmtWrites names, per fmt function, which argument is the destination and
// which is the template, with -1 for an argument that function does not take.
var fmtWrites = map[string]struct{ dest, template int }{
	"Fprintf":  {dest: 0, template: 1},
	"Fprint":   {dest: 0, template: -1},
	"Fprintln": {dest: 0, template: -1},
	"Sprintf":  {dest: -1, template: 0},
}

// writeMethods are the strings.Builder methods that put bytes in the document.
var writeMethods = map[string]bool{
	"WriteString": true,
	"Write":       true,
	"WriteByte":   true,
	"WriteRune":   true,
}

// collectHoles walks every function in the swept packages and records the
// places a runtime value lands in the Markdown being assembled.
func collectHoles(prog *program) []sinkHole {
	var holes []sinkHole
	for _, pkg := range prog.order {
		for _, fn := range pkg.declared() {
			walk := &writeWalk{prog: prog, fn: fn, cursors: map[string]docCursor{}}
			walk.body(fn.decl.Body)
			holes = append(holes, walk.holes...)
		}
	}
	return holes
}

// writeWalk is one pass over one function body, carrying the cursor of every
// destination written to in it.
type writeWalk struct {
	prog    *program
	fn      *funcInfo
	cursors map[string]docCursor
	holes   []sinkHole
}

// body walks one function body in source order.
//
// A nested block inherits the cursors it is entered with and hands nothing
// back: a fence opened at the top of a function and written into from a loop is
// followed, while one opened in a branch leaves the outer cursor as it was.
// That is the direction a gate has to err in, since a cursor wrongly left open
// would condemn every value written after it.
func (w *writeWalk) body(block *ast.BlockStmt) {
	var saved []map[string]docCursor
	ast.Inspect(block, func(node ast.Node) bool {
		if node == nil {
			last := len(saved) - 1
			if restore := saved[last]; restore != nil {
				w.cursors = restore
			}
			saved = saved[:last]
			return true
		}
		var restore map[string]docCursor
		if opensScope(node) {
			restore = maps.Clone(w.cursors)
		}
		saved = append(saved, restore)
		if call, ok := node.(*ast.CallExpr); ok {
			w.call(call)
		}
		return true
	})
}

// opensScope reports whether a node's children write into a document the code
// after the node does not continue.
func opensScope(node ast.Node) bool {
	switch node.(type) {
	case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause:
		return true
	default:
		return false
	}
}

// call advances the cursors over one call: a write is followed, and a call
// that hands a destination to a function this walk is not inside stops the
// cursor rather than guessing what it wrote.
func (w *writeWalk) call(call *ast.CallExpr) {
	if w.fmtCall(call) || w.methodCall(call) {
		return
	}
	for _, arg := range call.Args {
		if dest, ok := w.knownDestination(arg); ok {
			w.cursors[dest] = w.cursors[dest].closed()
		}
	}
}

// fmtCall follows a call of the fmt package that puts text in a document.
func (w *writeWalk) fmtCall(call *ast.CallExpr) bool {
	name, ok := w.qualifiedCall(call, "fmt")
	if !ok {
		return false
	}
	at, known := fmtWrites[name]
	if !known || len(call.Args) <= max(at.dest, at.template) {
		return false
	}
	dest := sprintfDest
	if at.dest >= 0 {
		dest = destinationOf(w.prog.fset, call.Args[at.dest])
	} else {
		delete(w.cursors, dest)
	}
	if at.template < 0 {
		w.operandWrite(dest, call.Args[at.dest+1:])
		return true
	}
	w.formatWrite(dest, call.Args[at.template], call.Args[at.template+1:])
	return true
}

// qualifiedCall reports whether a call is of the named package's function, and
// which one, with enough arguments to read.
func (w *writeWalk) qualifiedCall(call *ast.CallExpr, pkgName string) (string, bool) {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	base, ok := ast.Unparen(sel.X).(*ast.Ident)
	if !ok || base.Name != pkgName || !w.fn.pkg.isPackageName(base.Name) || len(call.Args) == 0 {
		return "", false
	}
	return sel.Sel.Name, true
}

// methodCall follows a call of a strings.Builder write method.
func (w *writeWalk) methodCall(call *ast.CallExpr) bool {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || !writeMethods[sel.Sel.Name] || len(call.Args) != 1 {
		return false
	}
	if base, isIdent := ast.Unparen(sel.X).(*ast.Ident); isIdent && w.fn.pkg.isPackageName(base.Name) {
		return false
	}
	dest := destinationOf(w.prog.fset, sel.X)
	if text, isConst := w.characterText(sel.Sel.Name, call.Args[0]); isConst {
		w.cursors[dest] = w.cursors[dest].writeText(text)
		return true
	}
	w.operandWrite(dest, call.Args)
	return true
}

// characterText reads the literal a WriteByte or WriteRune puts in the
// document. A pipe written that way opens a table cell as surely as one in a
// template, and renderTable in internal/prompts closes every cell with one.
func (w *writeWalk) characterText(method string, arg ast.Expr) (string, bool) {
	if method != "WriteByte" && method != "WriteRune" {
		return w.constantText(arg)
	}
	lit, ok := ast.Unparen(arg).(*ast.BasicLit)
	if !ok || lit.Kind != token.CHAR {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// formatWrite follows a write with a template: the constant text advances the
// cursor and every verb records a hole at the construct the text so far has
// opened.
//
// A template the audit cannot read as a constant stops the cursor, and the
// operands are still recorded, judged against the construct the writes before
// them left open.
func (w *writeWalk) formatWrite(dest string, template ast.Expr, args []ast.Expr) {
	text, ok := w.constantText(template)
	if !ok {
		w.operandWrite(dest, args)
		w.cursors[dest] = w.cursors[dest].closed()
		return
	}
	cursor := w.cursors[dest]
	next := 0
	for _, seg := range splitTemplate(text) {
		if seg.verb == "" {
			cursor = cursor.writeText(seg.text)
			continue
		}
		var arg ast.Expr
		if next < len(args) {
			arg = args[next]
		}
		next++
		cursor = w.hole(cursor, arg, seg.verb)
	}
	w.cursors[dest] = cursor
}

// operandWrite follows a write with no template, where every operand is text
// on the page as it is.
func (w *writeWalk) operandWrite(dest string, args []ast.Expr) {
	cursor := w.cursors[dest]
	for _, arg := range args {
		if text, ok := w.constantText(arg); ok {
			cursor = cursor.writeText(text)
			continue
		}
		cursor = w.hole(cursor, arg, verbWrite)
	}
	w.cursors[dest] = cursor
}

// hole records one value landing in the document and advances the cursor past
// it.
//
// Indentation is the exception: a run of spaces the formatter composed is
// still the start of its line, so a value written after it lands in the list
// item or the table row that follows rather than in prose. renderOutline
// indents its entries exactly that way.
func (w *writeWalk) hole(cursor docCursor, arg ast.Expr, verb string) docCursor {
	if arg == nil {
		return cursor.writeValue()
	}
	if indent, ok := w.indentOf(arg); ok {
		return cursor.writeText(indent)
	}
	w.holes = append(w.holes, sinkHole{expr: arg, fn: w.fn, ctx: cursor.context(), verb: verb, pos: arg.Pos()})
	// A hole carries two verdicts when it sits in a hand-written card row: what
	// the value can do to the construct, and what the line itself is. They are
	// separate holes because they are separate questions — a row whose value is
	// properly escaped is still a row the writer should have written — and a
	// run that does not ask the second one must still ask the first.
	if w.writesCardRows() && cardRowShape(cursor.line) {
		w.holes = append(w.holes, sinkHole{expr: arg, fn: w.fn, ctx: ctxCard, verb: verb, pos: arg.Pos()})
	}
	return cursor.writeValue()
}

// writesCardRows reports whether the card rule reads this function at all.
//
// The package that declares the writer is exempt, because the rule's whole
// content is "ask toolutil.Card for the row instead of composing one", and
// toolutil.Card is where the row is composed. Reporting it would be the gate
// pointing at its own answer.
func (w *writeWalk) writesCardRows() bool {
	return w.fn.pkg.dir != toolutilDir
}

// cardRowShape reports whether the text written before a value makes its line
// a card row: an optional list marker, a bold label, and the colon that
// introduces the value.
//
// It is the shape toolutil.Card writes, recognized in the constant text a
// renderer wrote by hand. The label itself is not read — what matters is that
// a renderer is composing a one-object row rather than asking the writer for
// one, because that is where the decision about escaping, emptiness and
// separation gets made a second time.
func cardRowShape(before string) bool {
	line := strings.TrimLeft(before, " \t")
	line = strings.TrimPrefix(line, "- ")
	label, rest, bold := strings.Cut(strings.TrimPrefix(line, "**"), "**")
	if !bold || !strings.HasPrefix(line, "**") || strings.TrimSpace(label) == "" {
		return false
	}
	return strings.HasPrefix(strings.TrimLeft(rest, " "), ":")
}

// knownDestination reports whether an argument names a destination this walk
// is already following.
func (w *writeWalk) knownDestination(arg ast.Expr) (string, bool) {
	name := destinationOf(w.prog.fset, arg)
	if _, ok := w.cursors[name]; !ok {
		return "", false
	}
	return name, true
}

// constantText reads a compile-time string out of an expression written in the
// package this walk is inside.
func (w *writeWalk) constantText(expr ast.Expr) (string, bool) {
	return constantString(w.fn.pkg, expr)
}

// constantString reads a compile-time string out of an expression: a literal,
// a concatenation of literals, or a constant the package declares.
func constantString(pkg *auditPkg, expr ast.Expr) (string, bool) {
	switch typed := ast.Unparen(expr).(type) {
	case *ast.BasicLit:
		if typed.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(typed.Value)
		return value, err == nil
	case *ast.BinaryExpr:
		if typed.Op != token.ADD {
			return "", false
		}
		left, leftOK := constantString(pkg, typed.X)
		right, rightOK := constantString(pkg, typed.Y)
		return left + right, leftOK && rightOK
	case *ast.Ident:
		if pkg == nil {
			return "", false
		}
		value, ok := pkg.values[typed.Name]
		if !ok {
			return "", false
		}
		return constantString(pkg, value)
	default:
		return "", false
	}
}

// indentOf reports whether an operand can only produce indentation, following
// a local one step back to the call that built it: a formatter computes the
// indent on its own line and interpolates the name, which is how renderOutline
// writes a nested entry.
func (w *writeWalk) indentOf(expr ast.Expr) (string, bool) {
	if ident, ok := ast.Unparen(expr).(*ast.Ident); ok {
		assigned := w.fn.locals()[ident.Name]
		if len(assigned) != 1 || assigned[0] == nil {
			return "", false
		}
		return indentText(assigned[0])
	}
	return indentText(expr)
}

// indentText reports whether an expression can only produce indentation, and
// what that indentation is made of.
func indentText(expr ast.Expr) (string, bool) {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return "", false
	}
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Repeat" {
		return "", false
	}
	lit, ok := ast.Unparen(call.Args[0]).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil || value == "" || strings.Trim(value, " \t") != "" {
		return "", false
	}
	return value, true
}

// segment is one piece of a printf template: literal text, or a verb that
// consumes an operand.
type segment struct {
	text string
	verb string
}

// splitTemplate splits a printf template into the text it writes and the verbs
// that interpolate values, in order.
//
// A width or precision given as '*' consumes an operand of its own and is
// emitted as a verb, so the operands after it still line up with their verbs.
func splitTemplate(template string) []segment {
	var segments []segment
	var text strings.Builder
	for i := 0; i < len(template); i++ {
		if template[i] != '%' {
			text.WriteByte(template[i])
			continue
		}
		verb, width, next := readVerb(template, i)
		if verb == "%%" {
			text.WriteByte('%')
			i = next - 1
			continue
		}
		segments = append(segments, segment{text: text.String()})
		text.Reset()
		for range width {
			segments = append(segments, segment{verb: "%*"})
		}
		segments = append(segments, segment{verb: verb})
		i = next - 1
	}
	return append(segments, segment{text: text.String()})
}

// verbFlags are the printf flag characters that may stand between the '%' and
// the verb.
const verbFlags = "+-# 0'"

// readVerb reads one verb starting at the '%' in template[at], and reports the
// verb, how many operands its width and precision consume, and where the verb
// ended.
func readVerb(template string, at int) (verb string, stars, next int) {
	i := at + 1
	for i < len(template) && strings.IndexByte(verbFlags, template[i]) >= 0 {
		i++
	}
	for i < len(template) && (template[i] == '*' || template[i] == '.' || (template[i] >= '0' && template[i] <= '9')) {
		if template[i] == '*' {
			stars++
		}
		i++
	}
	if i >= len(template) {
		return "%v", stars, i
	}
	return "%" + string(template[i]), stars, i + 1
}

// destinationOf names the document a write goes into, which is the receiver or
// the first argument with any address-of taken off it: a strings.Builder is
// written both as &b and as b in these formatters, and both name one document.
func destinationOf(fset *token.FileSet, expr ast.Expr) string {
	if unary, ok := ast.Unparen(expr).(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = unary.X
	}
	return exprText(fset, expr)
}

// exprText renders an expression the way the source spells it, which is what a
// finding prints and what an exemption is written against.
func exprText(fset *token.FileSet, expr ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, expr); err != nil {
		return "<expression>"
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
