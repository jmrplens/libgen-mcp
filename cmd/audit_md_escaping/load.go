package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// auditDirs are the packages swept when no directory is named on the command
// line: the two that render Markdown and the one that owns the escapers they
// are supposed to render it through.
var auditDirs = []string{"internal/tools", "internal/prompts", "internal/toolutil"}

// program is the parsed, indexed source one run reasons over.
type program struct {
	fset  *token.FileSet
	order []*auditPkg
	// byDir finds a package by its repository-relative directory, which is how
	// a qualified call is followed into the package that declares it.
	byDir map[string]*auditPkg
	// callers maps a declared function to every call of it in the swept tree,
	// which is how a parameter nobody bound is resolved to the values that
	// reach it.
	callers map[*funcInfo][]callSite
}

// auditPkg is one parsed package: its files, the functions declared in them,
// and the two lookups the classifier needs to read a name.
type auditPkg struct {
	dir   string
	name  string
	files []*ast.File
	// funcs maps a declared function's name to it.
	funcs map[string]*funcInfo
	// methods maps a method's bare name to it, kept apart from funcs so a
	// call on a receiver cannot resolve to a plain function that happens to
	// share the name — b.String() is not a package function called String.
	// No two methods in these packages share a name; a syntax-only walk has
	// no receiver type to tell them apart if two ever do.
	methods map[string]*funcInfo
	// imports maps an import's local name to its path, gathered across the
	// package's files rather than per file, so a selector can be told apart
	// from a field access. Two files aliasing one name differently would
	// collide; none in this tree does.
	imports map[string]string
	// values maps a package-level constant or variable to its initializer, so
	// a formatter interpolating its own package's default label is answered by
	// the literal that declared it.
	values map[string]ast.Expr
}

// funcInfo is one declared function the audit may walk or look inside.
type funcInfo struct {
	pkg    *auditPkg
	decl   *ast.FuncDecl
	name   string
	params []string
	// results is how many values the function returns, because a call the
	// audit follows into a function is only answered when there is one value
	// to answer for.
	results int
	// assigns maps a local name to every expression assigned to it, built on
	// first use. A name declared twice in one body collapses into one entry:
	// these formatters shadow nothing, and the collapse can only widen what a
	// value might be, never narrow it.
	assigns map[string][]ast.Expr
}

// callSite is one call of a declared function, kept with the function it was
// written in so its arguments resolve in their own scope.
type callSite struct {
	call *ast.CallExpr
	fn   *funcInfo
}

// loadProgram parses and indexes the named directories, rooted at root.
func loadProgram(root string, dirs []string) (*program, error) {
	prog := &program{
		fset:    token.NewFileSet(),
		byDir:   map[string]*auditPkg{},
		callers: map[*funcInfo][]callSite{},
	}
	for _, dir := range dirs {
		pkg, err := prog.parsePackage(root, dir)
		if err != nil {
			return nil, err
		}
		prog.order = append(prog.order, pkg)
		prog.byDir[dir] = pkg
	}
	for _, pkg := range prog.order {
		prog.indexCalls(pkg)
	}
	return prog, nil
}

// parsePackage parses one package directory, test files excluded: a test's
// Markdown is its own fixture and is not served to anybody.
func (p *program) parsePackage(root, dir string) (*auditPkg, error) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	pkg := &auditPkg{
		dir:     dir,
		funcs:   map[string]*funcInfo{},
		methods: map[string]*funcInfo{},
		imports: map[string]string{},
		values:  map[string]ast.Expr{},
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(p.fset, filepath.Join(root, dir, name), nil, parser.ParseComments)
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, name), parseErr)
		}
		pkg.name = file.Name.Name
		pkg.files = append(pkg.files, file)
		pkg.index(file)
	}
	if len(pkg.files) == 0 {
		return nil, fmt.Errorf("no Go source in %s", dir)
	}
	return pkg, nil
}

// index records one file's imports, package-level values and declarations.
func (a *auditPkg) index(file *ast.File) {
	for _, spec := range file.Imports {
		a.imports[importName(spec)] = strings.Trim(spec.Path.Value, `"`)
	}
	for _, decl := range file.Decls {
		switch typed := decl.(type) {
		case *ast.FuncDecl:
			a.indexFunc(typed)
		case *ast.GenDecl:
			a.indexValues(typed)
		}
	}
}

// importName is the local name an import is read by: its alias when it has
// one, and otherwise the last element of its path, which is right for every
// import in these packages.
func importName(spec *ast.ImportSpec) string {
	if spec.Name != nil {
		return spec.Name.Name
	}
	return path.Base(strings.Trim(spec.Path.Value, `"`))
}

// indexFunc records one declared function.
func (a *auditPkg) indexFunc(decl *ast.FuncDecl) {
	if decl.Body == nil {
		return
	}
	fn := &funcInfo{pkg: a, decl: decl, name: decl.Name.Name}
	for _, field := range decl.Type.Params.List {
		for _, name := range field.Names {
			fn.params = append(fn.params, name.Name)
		}
		if len(field.Names) == 0 {
			fn.params = append(fn.params, "_")
		}
	}
	if decl.Type.Results != nil {
		for _, field := range decl.Type.Results.List {
			fn.results += max(1, len(field.Names))
		}
	}
	if decl.Recv != nil {
		a.methods[fn.name] = fn
		return
	}
	a.funcs[fn.name] = fn
}

// declared lists every function and method the package declares, in a fixed
// order so two runs over the same tree produce the same report.
func (a *auditPkg) declared() []*funcInfo {
	names := make([]string, 0, len(a.funcs)+len(a.methods))
	for name := range a.funcs {
		names = append(names, name)
	}
	for name := range a.methods {
		names = append(names, "."+name)
	}
	sort.Strings(names)
	declared := make([]*funcInfo, 0, len(names))
	for _, name := range names {
		if method, ok := strings.CutPrefix(name, "."); ok {
			declared = append(declared, a.methods[method])
			continue
		}
		declared = append(declared, a.funcs[name])
	}
	return declared
}

// indexValues records the single-valued constants and variables a package
// declares at its top level.
func (a *auditPkg) indexValues(decl *ast.GenDecl) {
	if decl.Tok != token.CONST && decl.Tok != token.VAR {
		return
	}
	for _, spec := range decl.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok || len(value.Names) != len(value.Values) {
			continue
		}
		for i, name := range value.Names {
			a.values[name.Name] = value.Values[i]
		}
	}
}

// indexCalls records every call of a declared function, against the function
// it was written in.
func (p *program) indexCalls(pkg *auditPkg) {
	for _, fn := range pkg.declared() {
		p.indexCallsIn(fn, fn.decl.Body)
	}
}

// indexCallsIn records the calls written inside one function body.
func (p *program) indexCallsIn(enclosing *funcInfo, body *ast.BlockStmt) {
	if enclosing == nil {
		return
	}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if callee := p.calleeOf(enclosing.pkg, call); callee != nil {
			p.callers[callee] = append(p.callers[callee], callSite{call: call, fn: enclosing})
		}
		return true
	})
}

// calleeOf resolves a call to the function it names, when that function is
// declared in the swept tree.
//
// A bare identifier is looked up in the calling package and a qualified one in
// the package the caller imports under that name. A method call resolves by
// its bare name, for the reason auditPkg.funcs gives.
func (p *program) calleeOf(pkg *auditPkg, call *ast.CallExpr) *funcInfo {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return pkg.funcs[fun.Name]
	case *ast.SelectorExpr:
		base, ok := ast.Unparen(fun.X).(*ast.Ident)
		if !ok {
			return nil
		}
		if target := p.packageNamed(pkg, base.Name); target != nil {
			return target.funcs[fun.Sel.Name]
		}
		if pkg.isPackageName(base.Name) {
			return nil
		}
		return pkg.methods[fun.Sel.Name]
	default:
		return nil
	}
}

// packageNamed resolves an identifier to the swept package it names, when the
// calling package imports one under that name.
func (p *program) packageNamed(pkg *auditPkg, name string) *auditPkg {
	importPath, ok := pkg.imports[name]
	if !ok {
		return nil
	}
	for dir, candidate := range p.byDir {
		if strings.HasSuffix(importPath, "/"+dir) {
			return candidate
		}
	}
	return nil
}

// isPackageName reports whether an identifier names an imported package rather
// than a value, which is what tells a qualified call from a method call and a
// package's constant from a struct field.
func (a *auditPkg) isPackageName(name string) bool {
	_, ok := a.imports[name]
	return ok
}

// locals returns the expressions each local name in a function can hold,
// building the index on first use.
func (f *funcInfo) locals() map[string][]ast.Expr {
	if f.assigns != nil {
		return f.assigns
	}
	f.assigns = map[string][]ast.Expr{}
	ast.Inspect(f.decl.Body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.AssignStmt:
			f.indexAssign(typed)
		case *ast.ValueSpec:
			f.indexSpec(typed)
		case *ast.RangeStmt:
			f.indexRange(typed)
		}
		return true
	})
	return f.assigns
}

// indexAssign records an assignment's right-hand sides against the names they
// land in. An assignment whose sides do not line up — a call yielding two
// values, say — records a hole the classifier reports as unresolved rather
// than judging the name on its other assignments alone.
func (f *funcInfo) indexAssign(stmt *ast.AssignStmt) {
	for i, lhs := range stmt.Lhs {
		name, ok := assignedName(lhs)
		if !ok {
			continue
		}
		if len(stmt.Lhs) != len(stmt.Rhs) {
			f.assigns[name] = append(f.assigns[name], nil)
			continue
		}
		f.assigns[name] = append(f.assigns[name], stmt.Rhs[i])
	}
}

// indexSpec records a var declaration's initializers. A declaration with no
// initializer leaves the name holding whatever a later assignment put there,
// which indexAssign covers, so the zero value is recorded as a hole rather
// than as a safe empty string.
func (f *funcInfo) indexSpec(spec *ast.ValueSpec) {
	for i, name := range spec.Names {
		if name.Name == "_" {
			continue
		}
		if len(spec.Values) != len(spec.Names) {
			f.assigns[name.Name] = append(f.assigns[name.Name], nil)
			continue
		}
		f.assigns[name.Name] = append(f.assigns[name.Name], spec.Values[i])
	}
}

// indexRange records a loop's key and value against the collection they come
// from, so a value interpolated straight out of a range is judged by what it
// was ranged over.
func (f *funcInfo) indexRange(stmt *ast.RangeStmt) {
	for _, target := range []ast.Expr{stmt.Key, stmt.Value} {
		if target == nil {
			continue
		}
		if name, ok := assignedName(target); ok {
			f.assigns[name] = append(f.assigns[name], stmt.X)
		}
	}
}

// assignedName returns the local name an assignment target names, when the
// target is a plain identifier. A field, an index and the blank identifier are
// all skipped: none is a name the classifier can follow backwards.
func assignedName(expr ast.Expr) (string, bool) {
	ident, ok := ast.Unparen(expr).(*ast.Ident)
	if !ok || ident.Name == "_" {
		return "", false
	}
	return ident.Name, true
}

// isParam reports whether name is one of the function's parameters, and where
// in the signature it sits.
func (f *funcInfo) isParam(name string) (int, bool) {
	for i, param := range f.params {
		if param == name {
			return i, true
		}
	}
	return 0, false
}

// position resolves a source position for a report.
func (p *program) position(pos token.Pos) token.Position {
	return p.fset.Position(pos)
}
