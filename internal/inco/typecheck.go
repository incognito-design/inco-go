package inco

import (
	"go/ast"
	"go/token"
	"go/types"
)

// ---------------------------------------------------------------------------
// Function type lookup
// ---------------------------------------------------------------------------

// findEnclosingFuncType returns the *ast.FuncType of the innermost function
// (FuncDecl or FuncLit) that contains pos.  Returns nil if pos is not inside
// any function body.
func findEnclosingFuncType(f *ast.File, pos token.Pos) *ast.FuncType {
	var best *ast.FuncType
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil && fn.Body.Pos() <= pos && pos <= fn.Body.End() {
				best = fn.Type
			}
		case *ast.FuncLit:
			if fn.Body != nil && fn.Body.Pos() <= pos && pos <= fn.Body.End() {
				best = fn.Type
			}
		}
		return true
	})
	return best
}

// allReturnsNamed reports whether every return value in funcType has a name.
func allReturnsNamed(funcType *ast.FuncType) bool {
	if funcType == nil || funcType.Results == nil {
		return true // void function — bare return is valid
	}
	for _, field := range funcType.Results.List {
		if len(field.Names) == 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Zero-value generation
// ---------------------------------------------------------------------------

// zeroReturnValues produces a string slice of zero-value text representations
// for the return types of funcType, using types.Info for resolution.
// Returns nil if type information is insufficient.
func zeroReturnValues(funcType *ast.FuncType, info *types.Info) []string {
	if funcType == nil || funcType.Results == nil || info == nil {
		return nil
	}
	var vals []string
	for _, field := range funcType.Results.List {
		typ := info.TypeOf(field.Type)
		zv := zeroValueText(typ)
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			vals = append(vals, zv)
		}
	}
	return vals
}

// zeroValueText returns the Go source text for the zero value of typ.
func zeroValueText(typ types.Type) string {
	if typ == nil {
		return "nil"
	}
	switch t := typ.Underlying().(type) {
	case *types.Basic:
		info := t.Info()
		switch {
		case info&types.IsString != 0:
			return `""`
		case info&types.IsBoolean != 0:
			return "false"
		case info&types.IsInteger != 0:
			return "0"
		case info&types.IsFloat != 0:
			return "0"
		case info&types.IsComplex != 0:
			return "0"
		}
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan,
		*types.Signature, *types.Interface:
		return "nil"
	case *types.Struct:
		if named, ok := typ.(*types.Named); ok {
			return named.Obj().Name() + "{}"
		}
		return "struct{}{}"
	case *types.Array:
		if named, ok := typ.(*types.Named); ok {
			return named.Obj().Name() + "{}"
		}
		return "nil" // fallback
	}
	return "nil" // fallback
}
