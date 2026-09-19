package main

import (
	"go/ast"
	"go/token"
)

// classifyCall answers for a call: an escaper makes its result safe, a
// conversion or a string transform passes the question to its operand, and a
// function declared in the swept tree is answered by what its returns are,
// with its parameters bound to what this call site passed.
func (c *classifier) classifyCall(call *ast.CallExpr, s scope, depth int) (outcome verdict, explanation string) {
	name := c.callName(call, s)
	switch {
	case escaperNames[name], externalSafe[name], builtinSafe[name]:
		return safe, ""
	case builtinCarries[name]:
		return c.worstOf(call.Args, s, depth)
	case name == sprintfName:
		return c.classifySprintf(call, s, depth)
	}
	if carried, ok := passThroughArgs[name]; ok {
		return c.worstOf(argsAt(call, carried), s, depth)
	}
	if callee := c.calleeOf(call, s); callee != nil {
		return c.classifyReturns(callee, call, s, depth)
	}
	return unresolved, "the audit does not read what " + name + " returns"
}

// callName names a call the way the classifier's tables spell it: a toolutil
// helper by the package that declares it whatever the caller imports it as, a
// qualified call by its package's local name, and everything else by its own.
func (c *classifier) callName(call *ast.CallExpr, s scope) string {
	if callee := c.calleeOf(call, s); callee != nil && callee.pkg.dir == toolutilDir {
		return "toolutil." + callee.name
	}
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if base, ok := ast.Unparen(fun.X).(*ast.Ident); ok && s.pkg != nil && s.pkg.isPackageName(base.Name) {
			return base.Name + "." + fun.Sel.Name
		}
		return fun.Sel.Name
	default:
		return "this call"
	}
}

// calleeOf resolves a call to the declaration the sweep read for it.
func (c *classifier) calleeOf(call *ast.CallExpr, s scope) *funcInfo {
	if s.pkg == nil {
		return nil
	}
	return c.prog.calleeOf(s.pkg, call)
}

// argsAt picks the arguments a pass-through carries, ignoring an index the
// call does not have: a variadic transform may be called with fewer.
func argsAt(call *ast.CallExpr, indices []int) []ast.Expr {
	args := make([]ast.Expr, 0, len(indices))
	for _, index := range indices {
		if index < len(call.Args) {
			args = append(args, call.Args[index])
		}
	}
	return args
}

// classifySprintf answers for a value composed with fmt.Sprintf by asking the
// same question of every operand it interpolates, verb by verb: a cell
// composed of escaped halves is safe, one composed of a raw half is not, and
// the reason names the raw half.
//
// A template the audit cannot read as a constant leaves every operand judged,
// which is the answer that invents no exemption.
func (c *classifier) classifySprintf(call *ast.CallExpr, s scope, depth int) (outcome verdict, explanation string) {
	if len(call.Args) == 0 {
		return unresolved, "fmt.Sprintf with no template"
	}
	text, ok := constantString(s.pkg, call.Args[0])
	if !ok {
		return c.worstOf(call.Args[1:], s, depth)
	}
	args, next := call.Args[1:], 0
	result, why := safe, ""
	for _, seg := range splitTemplate(text) {
		if seg.verb == "" {
			continue
		}
		if next >= len(args) {
			break
		}
		arg := args[next]
		next++
		if safeVerbs[seg.verb] {
			continue
		}
		verdictOf, reason := c.classify(arg, s, depth+1)
		result, why = worse(result, why, verdictOf, reason)
	}
	return result, why
}

// classifyReturns answers for a declared function by taking the worst of its
// return expressions, judged with its parameters bound to what the call site
// passed.
//
// A function returning more than one value is not followed: a call the audit
// reads as a single value has one result, and matching a result to a position
// would need the signature's own order for no gain a gate can use.
func (c *classifier) classifyReturns(callee *funcInfo, call *ast.CallExpr, s scope, depth int) (outcome verdict, explanation string) {
	if callee.results != 1 {
		return unresolved, "the audit does not follow " + callee.name + ", whose result is not a single value"
	}
	key := visit{fn: callee}
	if c.visiting[key] {
		return safe, ""
	}
	c.visiting[key] = true
	defer delete(c.visiting, key)

	inner := scope{pkg: callee.pkg, fn: callee, env: bindParams(callee, call, s)}
	result, why, found := safe, "", false
	for _, returned := range returnedExprs(callee.decl.Body) {
		found = true
		next, nextWhy := c.classify(returned, inner, depth+1)
		result, why = worse(result, why, next, nextWhy)
	}
	if !found {
		return unresolved, callee.name + " has no return the audit can read"
	}
	return result, why
}

// returnedExprs lists the single-valued returns of a function body, the ones
// inside a nested closure excluded: a closure's return answers for the closure
// rather than for the function that declares it.
func returnedExprs(body *ast.BlockStmt) []ast.Expr {
	var returned []ast.Expr
	ast.Inspect(body, func(node ast.Node) bool {
		if _, isLit := node.(*ast.FuncLit); isLit {
			return false
		}
		ret, ok := node.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		returned = append(returned, ret.Results[0])
		return true
	})
	return returned
}

// bindParams binds a called function's parameters to the arguments this call
// passes. A variadic call and one whose arguments do not line up with the
// signature bind nothing, which leaves the callee's parameters to be resolved
// from every caller instead.
func bindParams(callee *funcInfo, call *ast.CallExpr, caller scope) *env {
	if len(call.Args) != len(callee.params) || call.Ellipsis != token.NoPos {
		return nil
	}
	binds := make(map[string]scopedExpr, len(callee.params))
	for i, name := range callee.params {
		if name == "_" {
			continue
		}
		binds[name] = scopedExpr{expr: call.Args[i], scope: caller}
	}
	return &env{binds: binds}
}
