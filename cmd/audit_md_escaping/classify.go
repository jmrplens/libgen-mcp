package main

import (
	"go/ast"
	"go/token"
)

// verdict is what the classifier concluded about one value reaching a Markdown
// construct. The order is the order of severity: a run takes the worst of what
// a value can be, and an unescaped half of a concatenation condemns the whole
// of it even when the other half is a literal.
type verdict int

const (
	// safe means the value cannot carry a character that changes the
	// document's shape, either because of what it is or because it has already
	// been through an escaper.
	safe verdict = iota
	// unresolved means the classifier could not follow the value to either
	// answer. It is reported separately: a gate that silently counted these as
	// safe would be a gate with a hole in it.
	unresolved
	// unescaped means the value is text this server did not write and no
	// escaper stands between it and the page.
	unescaped
)

// String names the verdict for a report.
func (v verdict) String() string {
	switch v {
	case safe:
		return "safe"
	case unresolved:
		return "unresolved"
	default:
		return "unescaped"
	}
}

// maxDepth bounds the walk backwards from a value to where it came from. The
// real chains are four or five steps (a range variable, a slice, a parameter,
// a caller's literal), so this is slack rather than a limit anyone reaches; it
// exists so a mutually recursive pair of helpers cannot hang the audit.
const maxDepth = 24

// toolutilDir is the package that owns the escapers, named once because both
// the classifier and the entry-point list read it.
const toolutilDir = "internal/toolutil"

// escaperNames are the toolutil functions whose result is safe to interpolate.
// Each neutralizes a superset of what the weakest construct needs, so a value
// that has been through any of them is safe in any context.
//
// StripControlBytes is deliberately absent. It drops the control bytes and
// leaves both the pipe and the angle bracket, so accepting it as an answer
// would pass exactly the values this audit exists to find. MarkdownCodeFence
// is absent for the opposite reason: it returns a fence, not a contained
// value.
//
// The two code-span helpers are here although they write no entity: a code
// span is its own containment, and each sizes its fence past the longest
// backtick run in the value, so nothing inside can end the span or open a
// construct of its own.
var escaperNames = map[string]bool{
	"toolutil.EscapeMdTableCell":       true,
	"toolutil.EscapeMdHeading":         true,
	"toolutil.EscapeMdLinkLabel":       true,
	"toolutil.EscapeMdLinkDestination": true,
	"toolutil.MdTitleLink":             true,
	"toolutil.MdAutolink":              true,
	"toolutil.MdCodeSpan":              true,
	"toolutil.MarkdownFencedBlock":     true,
}

// externalSafe names functions outside the swept packages whose result cannot
// carry catalog text. Their bodies are not read, so they are judged by what
// they are documented to return: a number's decimal form.
var externalSafe = map[string]bool{
	"strconv.Itoa":        true,
	"strconv.FormatInt":   true,
	"strconv.FormatUint":  true,
	"strconv.FormatFloat": true,
	"strconv.FormatBool":  true,
}

// builtinSafe names the builtins whose result is a number or an empty
// container: nothing a formatter prints as text it did not write.
var builtinSafe = map[string]bool{
	"len": true, "cap": true, "min": true, "max": true,
	"make": true, "new": true, "copy": true,
}

// builtinCarries names the builtins made of the arguments they were given, so
// the question passes to all of them. A row built cell by cell in a loop is
// judged by its cells this way.
var builtinCarries = map[string]bool{"append": true}

// passThroughArgs names functions outside the swept packages whose result is
// made of the arguments listed, so the question passes to those arguments
// instead of stopping at a body the audit never read.
//
// It is what lets an escaper be recognized through the transforms wrapped
// around it: a cell composed with strings.Join out of escaped halves is
// escaped, and one composed out of raw halves is not.
var passThroughArgs = map[string][]int{
	"string":             {0},
	"strings.Fields":     {0},
	"strings.Join":       {0},
	"strings.Map":        {1},
	"strings.Repeat":     {0},
	"strings.Replace":    {0, 2},
	"strings.ReplaceAll": {0, 2},
	"strings.Split":      {0},
	"strings.ToLower":    {0},
	"strings.ToTitle":    {0},
	"strings.ToUpper":    {0},
	"strings.Trim":       {0},
	"strings.TrimLeft":   {0},
	"strings.TrimPrefix": {0},
	"strings.TrimRight":  {0},
	"strings.TrimSpace":  {0},
	"strings.TrimSuffix": {0},
	"filepath.Base":      {0},
	"filepath.Clean":     {0},
	"filepath.Ext":       {0},
	"path.Base":          {0},
}

// sprintfName is the one fmt function the classifier reads as a value: its
// result is made of the values it interpolates, so a cell composed of escaped
// halves is safe and one composed of a raw half is not, and the reason names
// the raw half.
const sprintfName = "fmt.Sprintf"

// scope is where an expression is meaningful: the package it was written in,
// the function whose locals and parameters it reads, and the arguments a
// caller bound those parameters to.
type scope struct {
	pkg *auditPkg
	fn  *funcInfo
	env *env
}

// env binds a called function's parameters to the expressions its caller
// passed, together with the scope those expressions are meaningful in.
//
// It is what makes a finding belong to a call site: a shared row helper whose
// callers all pass escaped strings is safe, and a finding belongs at whichever
// caller passes a raw value.
type env struct {
	binds map[string]scopedExpr
}

// scopedExpr is one expression kept with the scope it resolves in.
type scopedExpr struct {
	expr  ast.Expr
	scope scope
}

// classifier answers whether one expression can carry text that changes the
// shape of the Markdown around it.
type classifier struct {
	prog *program
	// visiting guards the recursion against a function that reaches itself and
	// a name assigned from itself.
	visiting map[visit]bool
}

// visit names one thing the classifier is in the middle of answering for.
type visit struct {
	fn   *funcInfo
	name string
}

// newClassifier prepares a classifier over the loaded program.
func newClassifier(prog *program) *classifier {
	return &classifier{prog: prog, visiting: map[visit]bool{}}
}

// worse returns the more severe of two verdicts, with the reason that goes
// with it.
func worse(a verdict, whyA string, b verdict, whyB string) (outcome verdict, explanation string) {
	if b > a {
		return b, whyB
	}
	return a, whyA
}

// classify answers whether the value expr produces can carry a character that
// changes the shape of the Markdown it lands in.
func (c *classifier) classify(expr ast.Expr, s scope, depth int) (outcome verdict, explanation string) {
	if depth > maxDepth {
		return unresolved, "the chain back to this value is longer than the audit follows"
	}
	if expr == nil {
		return unresolved, "it comes from an assignment the audit cannot follow"
	}
	switch typed := ast.Unparen(expr).(type) {
	case *ast.BasicLit:
		return safe, ""
	case *ast.Ident:
		return c.classifyIdent(typed, s, depth)
	case *ast.BinaryExpr:
		return c.classifyBinary(typed, s, depth)
	case *ast.CallExpr:
		return c.classifyCall(typed, s, depth)
	case *ast.CompositeLit:
		return c.worstOf(literalValues(typed, 0), s, depth)
	case *ast.SelectorExpr:
		return c.classifySelector(typed, s, depth)
	case *ast.IndexExpr:
		return c.classify(typed.X, s, depth+1)
	case *ast.SliceExpr:
		return c.classify(typed.X, s, depth+1)
	case *ast.StarExpr:
		return c.classify(typed.X, s, depth+1)
	case *ast.UnaryExpr:
		return c.classify(typed.X, s, depth+1)
	default:
		return unresolved, "the audit does not read this kind of expression"
	}
}

// classifyBinary answers for an operator. Only concatenation produces text, so
// every other operator yields a number or a boolean the page cannot be
// reshaped by.
func (c *classifier) classifyBinary(expr *ast.BinaryExpr, s scope, depth int) (outcome verdict, explanation string) {
	if expr.Op != token.ADD {
		return safe, ""
	}
	return c.worstOf([]ast.Expr{expr.X, expr.Y}, s, depth)
}

// worstOf takes the most severe verdict over a list of expressions.
func (c *classifier) worstOf(exprs []ast.Expr, s scope, depth int) (outcome verdict, explanation string) {
	result, why := safe, ""
	for _, expr := range exprs {
		next, nextWhy := c.classify(expr, s, depth+1)
		result, why = worse(result, why, next, nextWhy)
	}
	return result, why
}

// classifyIdent answers for a name by following it to the values it holds: the
// argument its caller passed for a bound parameter, every argument any caller
// passes for an unbound one, and every assignment for a local.
func (c *classifier) classifyIdent(ident *ast.Ident, s scope, depth int) (outcome verdict, explanation string) {
	switch ident.Name {
	case "true", "false", "nil", "iota":
		return safe, ""
	}
	if s.fn != nil {
		key := visit{fn: s.fn, name: ident.Name}
		if c.visiting[key] {
			return safe, ""
		}
		c.visiting[key] = true
		defer delete(c.visiting, key)
	}
	sources := c.sourcesOf(ident.Name, s)
	if len(sources) == 0 {
		return unresolved, "nothing the audit can see assigns " + ident.Name
	}
	result, why := safe, ""
	for _, source := range sources {
		next, nextWhy := c.classify(source.expr, source.scope, depth+1)
		result, why = worse(result, why, next, nextWhy)
	}
	if result == unescaped && why == "" {
		why = ident.Name + " holds text no escaper was applied to"
	}
	return result, why
}

// sourcesOf lists every expression a name can hold, each in the scope it
// resolves in.
func (c *classifier) sourcesOf(name string, s scope) []scopedExpr {
	if s.env != nil {
		if bound, ok := s.env.binds[name]; ok {
			return []scopedExpr{bound}
		}
	}
	if s.fn == nil {
		return c.packageValue(name, s.pkg)
	}
	var sources []scopedExpr
	for _, expr := range s.fn.locals()[name] {
		sources = append(sources, scopedExpr{expr: expr, scope: s})
	}
	if index, ok := s.fn.isParam(name); ok {
		sources = append(sources, c.callerArgs(s.fn, index)...)
	}
	if len(sources) > 0 {
		return sources
	}
	return c.packageValue(name, s.pkg)
}

// packageValue resolves a name to the constant or variable its package
// declares, so a formatter interpolating its own default label is answered by
// the literal that declared it.
func (c *classifier) packageValue(name string, pkg *auditPkg) []scopedExpr {
	if pkg == nil {
		return nil
	}
	value, ok := pkg.values[name]
	if !ok {
		return nil
	}
	return []scopedExpr{{expr: value, scope: scope{pkg: pkg}}}
}

// callerArgs lists the argument every caller passes in one parameter position.
//
// This is the rule that keeps a shared row helper from being reported once per
// caller: renderTable in internal/prompts is handed cells two callers have
// already escaped, and a finding belongs at whichever caller does not.
func (c *classifier) callerArgs(fn *funcInfo, index int) []scopedExpr {
	var args []scopedExpr
	for _, site := range c.prog.callers[fn] {
		if len(site.call.Args) != len(fn.params) || site.call.Ellipsis != token.NoPos {
			args = append(args, scopedExpr{scope: scope{pkg: site.fn.pkg, fn: site.fn}})
			continue
		}
		args = append(args, scopedExpr{expr: site.call.Args[index], scope: scope{pkg: site.fn.pkg, fn: site.fn}})
	}
	return args
}

// classifySelector answers for a qualified name or a field access.
//
// A field is where the audit stops looking and starts reporting: every struct
// these formatters render is filled from a catalog response, so a string field
// on one holds whatever the person who uploaded the file typed. The exception
// is a field read out of a literal the formatter itself wrote, which is how a
// table of constant labels is judged by its labels.
func (c *classifier) classifySelector(sel *ast.SelectorExpr, s scope, depth int) (outcome verdict, explanation string) {
	base, ok := ast.Unparen(sel.X).(*ast.Ident)
	if !ok {
		return unescaped, exprText(c.prog.fset, sel) + " is external text with no escaper applied"
	}
	if s.pkg != nil && s.pkg.isPackageName(base.Name) {
		return c.qualifiedValue(sel, base.Name, s, depth)
	}
	if values, isLiteral := c.fieldSources(base.Name, s); isLiteral {
		return c.worstOf(values, s, depth)
	}
	return unescaped, exprText(c.prog.fset, sel) + " is external text with no escaper applied"
}

// qualifiedValue answers for another package's constant or variable, when that
// package is one the sweep read.
func (c *classifier) qualifiedValue(sel *ast.SelectorExpr, pkgName string, s scope, depth int) (outcome verdict, explanation string) {
	target := c.prog.packageNamed(s.pkg, pkgName)
	if target == nil {
		return unresolved, "the audit does not read the package " + pkgName
	}
	value, ok := target.values[sel.Sel.Name]
	if !ok {
		return unresolved, "the audit does not read " + exprText(c.prog.fset, sel)
	}
	return c.classify(value, scope{pkg: target}, depth+1)
}

// fieldSources returns the values a field read from name could hold, when name
// is a local the formatter built out of a literal.
//
// Every value in the literal is taken, not the one field the selector names:
// the elements of these tables are constants together, so a table whose every
// value is safe answers for any field of it, and one holding a raw value is
// reported whichever field was read. Matching the field by name would need the
// struct's declared order for a positionally-written element, which buys
// nothing a gate can use.
//
// Only a local is followed. A field of a parameter is a struct the caller
// built from a catalog response, and following it into the caller would answer
// about that response rather than about the value.
func (c *classifier) fieldSources(name string, s scope) ([]ast.Expr, bool) {
	if s.fn == nil {
		return nil, false
	}
	assigned := s.fn.locals()[name]
	if len(assigned) == 0 {
		return nil, false
	}
	var values []ast.Expr
	for _, expr := range assigned {
		if expr == nil {
			return nil, false
		}
		lit, ok := ast.Unparen(expr).(*ast.CompositeLit)
		if !ok {
			return nil, false
		}
		values = append(values, literalValues(lit, 0)...)
	}
	return values, true
}

// literalValues collects the values a composite literal is built from, through
// the literals nested in it: the elements of a slice of rows, and the fields of
// each row.
func literalValues(lit *ast.CompositeLit, depth int) []ast.Expr {
	var values []ast.Expr
	for _, elt := range lit.Elts {
		value := elt
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			value = kv.Value
		}
		if nested, ok := ast.Unparen(value).(*ast.CompositeLit); ok && depth < 4 {
			values = append(values, literalValues(nested, depth+1)...)
			continue
		}
		values = append(values, value)
	}
	return values
}
