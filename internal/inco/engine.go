package inco

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

// Overlay represents the go build -overlay JSON format.
type Overlay struct {
	Replace map[string]string `json:"Replace"`
}

// Engine is the core processor that scans Go source files,
// parses contract directives, injects assertion code, and
// produces overlay mappings for `go build -overlay`.
type Engine struct {
	Root      string // project root directory
	CacheDir  string // .inco_cache directory path
	Overlay   Overlay
	typeCache map[string]*packageCache
}

type packageCache struct {
	fset     *token.FileSet
	files    map[string]*ast.File
	resolver *TypeResolver
}

// NewEngine creates a new Engine rooted at the given directory.
func NewEngine(root string) (e *Engine) {
	// @require len(root) > 0, "root must not be empty"
	// @ensure -nd e
	cache := filepath.Join(root, ".inco_cache")
	return &Engine{
		Root:      root,
		CacheDir:  cache,
		Overlay:   Overlay{Replace: make(map[string]string)},
		typeCache: make(map[string]*packageCache),
	}
}

// Run executes the full pipeline: scan -> parse -> inject -> write overlay.
func (e *Engine) Run() error {
	if err := os.MkdirAll(e.CacheDir, 0o755); err != nil {
		return fmt.Errorf("inco: create cache dir: %w", err)
	}

	err := filepath.Walk(e.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip hidden dirs, vendor, testdata, and cache itself
		if info.IsDir() {
			base := info.Name()
			if strings.HasPrefix(base, ".") || base == "vendor" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		return e.processFile(path)
	})
	if err != nil {
		return err
	}

	return e.writeOverlay()
}

// processFile scans a single Go file for contract directives.
// If any are found, it generates a shadow file and registers it in the overlay.
func (e *Engine) processFile(path string) error {
	// @require len(path) > 0, "path must not be empty"
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("inco: abs path %s: %w", path, err)
	}

	f, fset, resolver, err := e.loadFileWithTypes(absPath)
	if err != nil {
		return err
	}

	directives := e.collectDirectives(f, fset)
	if len(directives) == 0 {
		return nil // nothing to do
	}

	// Read original source lines for //line mapping
	origLines, err := readLines(absPath)
	if err != nil {
		return fmt.Errorf("inco: read original %s: %w", path, err)
	}

	// Inject assertions into AST
	e.injectAssertions(f, fset, directives, resolver)

	// Strip all comments to prevent go/printer from displacing them
	// into injected code. The shadow file is for compilation only.
	f.Comments = nil

	// Generate shadow file content
	var buf strings.Builder
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&buf, fset, f); err != nil {
		return fmt.Errorf("inco: print shadow for %s: %w", path, err)
	}

	// Post-process: inject //line directives to map back to original source
	shadowContent := injectLineDirectives(buf.String(), origLines, absPath)

	// Compute content hash for stable cache filenames
	hash := contentHash(shadowContent)
	base := strings.TrimSuffix(filepath.Base(path), ".go")
	shadowName := fmt.Sprintf("%s_%s.go", base, hash[:12])
	shadowPath := filepath.Join(e.CacheDir, shadowName)

	if err := os.WriteFile(shadowPath, []byte(shadowContent), 0o644); err != nil {
		return fmt.Errorf("inco: write shadow %s: %w", shadowPath, err)
	}

	e.Overlay.Replace[absPath] = shadowPath
	return nil
}

func (e *Engine) loadFileWithTypes(path string) (*ast.File, *token.FileSet, *TypeResolver, error) {
	// @require len(path) > 0, "path must not be empty"
	dir := filepath.Dir(path)
	cache, ok := e.typeCache[dir]
	if !ok {
		var err error
		cache, err = e.loadPackage(dir)
		if err != nil {
			return nil, nil, nil, err
		}
		e.typeCache[dir] = cache
	}

	file := cache.files[path]
	if file == nil {
		return nil, nil, nil, fmt.Errorf("inco: file not found in package: %s", path)
	}

	return file, cache.fset, cache.resolver, nil
}

func (e *Engine) loadPackage(dir string) (*packageCache, error) {
	// @require len(dir) > 0, "dir must not be empty"
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		name := info.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("inco: parse dir %s: %w", dir, err)
	}

	var pkg *ast.Package
	for _, p := range pkgs {
		pkg = p
		break
	}
	if pkg == nil {
		return nil, fmt.Errorf("inco: no package in %s", dir)
	}

	fileMap := make(map[string]*ast.File)
	var files []*ast.File
	for _, f := range pkg.Files {
		filename := fset.Position(f.Pos()).Filename
		abs, err := filepath.Abs(filename)
		if err != nil {
			return nil, fmt.Errorf("inco: abs file %s: %w", filename, err)
		}
		fileMap[abs] = f
		files = append(files, f)
	}

	info := &types.Info{
		Types:  make(map[ast.Expr]types.TypeAndValue),
		Defs:   make(map[*ast.Ident]types.Object),
		Uses:   make(map[*ast.Ident]types.Object),
		Scopes: make(map[ast.Node]*types.Scope),
	}
	conf := &types.Config{
		Importer: importer.ForCompiler(fset, "source", nil),
		Error:    func(err error) {}, // collect but don't abort on first error
	}
	pkgTypes, err := conf.Check(pkg.Name, fset, files, info)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inco: typecheck warning in %s: %v\n", dir, err)
	}

	resolver := &TypeResolver{Info: info, Fset: fset, Pkg: pkgTypes}
	return &packageCache{fset: fset, files: fileMap, resolver: resolver}, nil
}

// directiveInfo associates a parsed Directive with its position in the AST.
type directiveInfo struct {
	Directive *Directive
	Pos       token.Pos
	Comment   *ast.Comment
}

// collectDirectives walks the AST comment map and extracts all contract directives.
func (e *Engine) collectDirectives(f *ast.File, fset *token.FileSet) []directiveInfo {
	// @require -nd f, fset
	var result []directiveInfo
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			d := ParseDirective(c.Text)
			if d != nil {
				result = append(result, directiveInfo{
					Directive: d,
					Pos:       c.Pos(),
					Comment:   c,
				})
			}
		}
	}
	return result
}

// injectAssertions modifies the AST by inserting assertion statements
// after each contract directive comment.
func (e *Engine) injectAssertions(f *ast.File, fset *token.FileSet, directives []directiveInfo, resolver *TypeResolver) {
	// @require -nd f, fset
	// Build a position -> directive lookup
	dirMap := make(map[token.Pos]*directiveInfo)
	for i := range directives {
		dirMap[directives[i].Pos] = &directives[i]
	}

	var importsToAdd []string

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BlockStmt:
			if node != nil {
				var added []string
				node.List, added = e.processStmtList(node.List, node.Lbrace, fset, dirMap, resolver, f)
				importsToAdd = append(importsToAdd, added...)
			}
		case *ast.CaseClause:
			if node != nil {
				var added []string
				node.Body, added = e.processStmtList(node.Body, node.Colon, fset, dirMap, resolver, f)
				importsToAdd = append(importsToAdd, added...)
			}
		case *ast.CommClause:
			if node != nil {
				var added []string
				node.Body, added = e.processStmtList(node.Body, node.Colon, fset, dirMap, resolver, f)
				importsToAdd = append(importsToAdd, added...)
			}
		}
		return true
	})

	if len(importsToAdd) > 0 {
		for _, path := range uniqStrings(importsToAdd) {
			astutil.AddImport(fset, f, path)
		}
	}
}

// processStmtList inspects a statement list and injects assertions where directives are found.
// startPos is the position of the opening brace or colon that precedes the first statement.
func (e *Engine) processStmtList(stmts []ast.Stmt, startPos token.Pos, fset *token.FileSet, dirMap map[token.Pos]*directiveInfo, resolver *TypeResolver, f *ast.File) ([]ast.Stmt, []string) {
	// @require -nd fset, dirMap, f
	var newList []ast.Stmt
	var importsToAdd []string

	for i, stmt := range stmts {
		// Check if any directive is associated with this statement
		// by looking at comments that appear before this statement's position
		// and after the previous statement (or block/clause start).
		var prevEnd token.Pos
		if i > 0 {
			prevEnd = stmts[i-1].End()
		} else {
			prevEnd = startPos
		}

		// Collect directives that appear between prevEnd and this statement.
		// Sort by position to ensure deterministic injection order.
		var between []token.Pos
		for pos := range dirMap {
			if pos > prevEnd && pos < stmt.Pos() {
				between = append(between, pos)
			}
		}
		sort.Slice(between, func(a, b int) bool { return between[a] < between[b] })

		var pendingMust *directiveInfo
		var pendingMustPos token.Pos
		for _, pos := range between {
			di := dirMap[pos]
			if di.Directive.Kind == Must {
				// Block-mode @must: directive on its own line, applies to next statement
				pendingMust = di
				pendingMustPos = pos
			} else {
				generated, added := e.generateAssertion(di, fset, resolver, f)
				newList = append(newList, generated...)
				importsToAdd = append(importsToAdd, added...)
				delete(dirMap, pos) // consumed
			}
		}

		// Handle block-mode @must: applies to the next assignment statement
		if pendingMust != nil {
			if assign, ok := stmt.(*ast.AssignStmt); ok {
				mustStmts := e.generateMustForAssign(assign, fset, pendingMust)
				newList = append(newList, stmt)
				newList = append(newList, mustStmts...)
				stmt = nil                     // mark as handled
				delete(dirMap, pendingMustPos) // consumed
			}
		}

		// Handle inline // @must on assignment statements (same line)
		if stmt != nil {
			if assign, ok := stmt.(*ast.AssignStmt); ok {
				for pos, di := range dirMap {
					stmtLine := fset.Position(stmt.Pos()).Line
					commentLine := fset.Position(pos).Line
					if di.Directive.Kind == Must && commentLine == stmtLine {
						mustStmts := e.generateMustForAssign(assign, fset, di)
						newList = append(newList, stmt)
						newList = append(newList, mustStmts...)
						stmt = nil          // mark as handled
						delete(dirMap, pos) // consumed
						break
					}
				}
			}
		}

		if stmt != nil {
			newList = append(newList, stmt)
		}
	}

	return newList, importsToAdd
}

// generateAssertion creates assertion statements from a directive.
func (e *Engine) generateAssertion(di *directiveInfo, fset *token.FileSet, resolver *TypeResolver, f *ast.File) ([]ast.Stmt, []string) {
	// @require -nd di, fset, f
	pos := fset.Position(di.Pos)
	loc := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)

	switch di.Directive.Kind {
	case Require:
		return e.generateRequire(di.Directive, loc, resolver, di.Pos, f)
	case Ensure:
		return e.generateEnsure(di.Directive, loc, resolver, di.Pos, f)
	default:
		return nil, nil
	}
}

// generateRequire generates `if <cond> { panic(...) }` statements for require directives.
func (e *Engine) generateRequire(d *Directive, loc string, resolver *TypeResolver, pos token.Pos, f *ast.File) ([]ast.Stmt, []string) {
	// @require -nd d
	// @require len(loc) > 0, "loc must not be empty"
	if d.ND {
		return e.generateNDChecks(d.Vars, loc, "require", resolver, pos, f)
	}
	if d.Expr != "" {
		msg := d.Message
		if msg == "" {
			msg = fmt.Sprintf("inco // require violation: %s", d.Expr)
		}
		if resolver != nil {
			if warn := resolver.EvalRequireExpr(pos, d.Expr); warn != "" {
				fmt.Fprintf(os.Stderr, "inco: %s at %s\n", warn, loc)
			}
		}
		expr, err := parser.ParseExpr(d.Expr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "inco: parse @require expr failed at %s: %v\n", loc, err)
			return nil, nil
		}
		cond := &ast.UnaryExpr{Op: token.NOT, X: &ast.ParenExpr{X: expr}}
		return []ast.Stmt{makeIfPanicStmt(cond, fmt.Sprintf("%s at %s", msg, loc))}, nil
	}
	return nil, nil
}

// generateEnsure wraps the check in a defer for postcondition checking.
func (e *Engine) generateEnsure(d *Directive, loc string, resolver *TypeResolver, pos token.Pos, f *ast.File) ([]ast.Stmt, []string) {
	// @require -nd d
	// @require len(loc) > 0, "loc must not be empty"
	if d.ND {
		inner, imports := e.generateNDChecks(d.Vars, loc, "ensure", resolver, pos, f)
		return []ast.Stmt{makeDeferStmt(inner)}, imports
	}
	if d.Expr != "" {
		msg := d.Message
		if msg == "" {
			msg = fmt.Sprintf("inco // ensure violation: %s", d.Expr)
		}
		expr, err := parser.ParseExpr(d.Expr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "inco: parse @ensure expr failed at %s: %v\n", loc, err)
			return nil, nil
		}
		cond := &ast.UnaryExpr{Op: token.NOT, X: &ast.ParenExpr{X: expr}}
		inner := []ast.Stmt{makeIfPanicStmt(cond, fmt.Sprintf("%s at %s", msg, loc))}
		return []ast.Stmt{makeDeferStmt(inner)}, nil
	}
	return nil, nil
}

// generateNDChecks generates non-defaulted zero-value panic checks with type awareness.
func (e *Engine) generateNDChecks(vars []string, loc string, protocol string, resolver *TypeResolver, pos token.Pos, f *ast.File) ([]ast.Stmt, []string) {
	// @require len(vars) > 0, "vars must not be empty"
	// @require len(loc) > 0, "loc must not be empty"
	var stmts []ast.Stmt
	var importsToAdd []string

	funcType := findEnclosingFuncType(f, pos)
	for _, v := range vars {
		var typ types.Type
		if resolver != nil {
			typ = resolver.ResolveVarType(funcType, v)
		}

		var currentPkg *types.Package
		if resolver != nil {
			currentPkg = resolver.Pkg
		}
		zeroExpr := ZeroCheckExpr(v, typ, currentPkg)
		if zeroExpr == nil {
			zeroExpr = &ast.BinaryExpr{X: ast.NewIdent(v), Op: token.EQL, Y: ast.NewIdent("nil")}
		}

		desc := ZeroValueDesc(typ)
		msg := fmt.Sprintf("inco // %s -nd violation: [%s] is defaulted (%s) at %s", protocol, v, desc, loc)
		stmts = append(stmts, makeIfPanicStmt(zeroExpr, msg))

		if resolver != nil {
			if imp := NeedsImport(typ, resolver.Pkg); imp != "" {
				importsToAdd = append(importsToAdd, imp)
			}
		}
	}

	return stmts, importsToAdd
}

// generateMustForAssign injects error checking after an assignment that uses _ for the error.
// It replaces the LAST blank identifier on the LHS, since Go convention places the error
// as the final return value.
func (e *Engine) generateMustForAssign(assign *ast.AssignStmt, fset *token.FileSet, di *directiveInfo) []ast.Stmt {
	// @require -nd assign, fset, di
	pos := fset.Position(di.Pos)
	loc := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)

	// Find the LAST _ (blank identifier) in LHS — that's the error position by Go convention.
	var lastBlank *ast.Ident
	for _, lhs := range assign.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if ok && ident.Name == "_" {
			lastBlank = ident
		}
	}

	if lastBlank != nil {
		errVar := fmt.Sprintf("_inco_err_%d", pos.Line)
		lastBlank.Name = errVar

		// Ensure it's a short variable declaration so the new name is declared
		if assign.Tok == token.ASSIGN {
			assign.Tok = token.DEFINE
		}

		msg := fmt.Sprintf("inco // must violation at %s", loc)
		return []ast.Stmt{makeIfPanicErrStmt(errVar, msg)}
	}

	// If no blank identifier found, check for explicit err variable
	for _, lhs := range assign.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if ok && ident.Name == "err" {
			msg := fmt.Sprintf("inco // must violation at %s", loc)
			return []ast.Stmt{makeIfPanicErrStmt("err", msg)}
		}
	}

	return nil
}

// writeOverlay writes the overlay.json file to the cache directory.
func (e *Engine) writeOverlay() error {
	if len(e.Overlay.Replace) == 0 {
		return nil
	}

	data, err := json.MarshalIndent(e.Overlay, "", "  ")
	if err != nil {
		return fmt.Errorf("inco: marshal overlay: %w", err)
	}

	path := filepath.Join(e.CacheDir, "overlay.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("inco: write overlay.json: %w", err)
	}

	fmt.Printf("inco: overlay written to %s (%d file(s) mapped)\n", path, len(e.Overlay.Replace))
	return nil
}

// --- AST construction helpers ---

// makeIfPanicStmt builds: if <cond> { panic("<msg>") }
func makeIfPanicStmt(cond ast.Expr, msg string) *ast.IfStmt {
	// @require -nd cond
	// @require len(msg) > 0, "msg must not be empty"
	return &ast.IfStmt{
		Cond: cond,
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun:  ast.NewIdent("panic"),
						Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(msg)}},
					},
				},
			},
		},
	}
}

// makeIfPanicErrStmt builds: if <errVar> != nil { panic("<msg>: " + <errVar>.Error()) }
func makeIfPanicErrStmt(errVar string, msg string) *ast.IfStmt {
	// @require len(errVar) > 0, "errVar must not be empty"
	// @require len(msg) > 0, "msg must not be empty"
	return &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  ast.NewIdent(errVar),
			Op: token.NEQ,
			Y:  ast.NewIdent("nil"),
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun: ast.NewIdent("panic"),
						Args: []ast.Expr{
							&ast.BinaryExpr{
								X:  &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(msg + ": ")},
								Op: token.ADD,
								Y: &ast.CallExpr{
									Fun: &ast.SelectorExpr{
										X:   ast.NewIdent(errVar),
										Sel: ast.NewIdent("Error"),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// makeDeferStmt wraps statements in: defer func() { ... }()
func makeDeferStmt(stmts []ast.Stmt) *ast.DeferStmt {
	// @require len(stmts) > 0, "stmts must not be empty"
	return &ast.DeferStmt{
		Call: &ast.CallExpr{
			Fun: &ast.FuncLit{
				Type: &ast.FuncType{Params: &ast.FieldList{}},
				Body: &ast.BlockStmt{List: stmts},
			},
		},
	}
}

func uniqStrings(items []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

// contentHash returns a hex-encoded SHA-256 hash of the content.
func contentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// readLines reads a file and returns its lines (without newlines).
func readLines(path string) ([]string, error) {
	// @require len(path) > 0, "path must not be empty"
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// injectLineDirectives compares the shadow output with the original source lines
// and inserts `//line` directives after injected blocks to restore correct line mapping.
//
// Strategy: walk shadow lines and original lines together. When a shadow line matches
// the next expected original line, they are "in sync". When shadow lines don't match
// (i.e. they are injected code), we let them pass. Once we re-sync, we emit a
// `//line original.go:N` directive to snap the compiler's line counter back.
func injectLineDirectives(shadow string, origLines []string, absPath string) string {
	// @require len(absPath) > 0, "absPath must not be empty"
	shadowLines := strings.Split(shadow, "\n")

	origIdx := 0 // pointer into original lines
	var result []string
	needsLineDirective := false

	for _, sLine := range shadowLines {
		trimmed := strings.TrimSpace(sLine)

		// Try to match against the current original line
		if origIdx < len(origLines) {
			origTrimmed := strings.TrimSpace(origLines[origIdx])

			if trimmed == origTrimmed {
				// Lines match — we are in sync
				if needsLineDirective {
					// Emit //line to snap back to the correct original line number
					// (origIdx is 0-based, line numbers are 1-based)
					result = append(result, fmt.Sprintf("//line %s:%d", absPath, origIdx+1))
					needsLineDirective = false
				}
				result = append(result, sLine)
				origIdx++
				continue
			}

			// Skip consecutive contract comment lines in the original source.
			// These were stripped from the AST and replaced with injected code.
			skipped := false
			for origIdx < len(origLines) && isContractComment(strings.TrimSpace(origLines[origIdx])) {
				origIdx++
				skipped = true
			}
			if skipped && origIdx < len(origLines) {
				origTrimmed = strings.TrimSpace(origLines[origIdx])
				if trimmed == origTrimmed {
					if needsLineDirective {
						result = append(result, fmt.Sprintf("//line %s:%d", absPath, origIdx+1))
						needsLineDirective = false
					}
					result = append(result, sLine)
					origIdx++
					continue
				}
			}
		}

		// This shadow line is injected code (no match in original)
		result = append(result, sLine)
		needsLineDirective = true
	}

	return strings.Join(result, "\n")
}

// isContractComment checks if a line is an inco contract comment that was stripped.
func isContractComment(line string) bool {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "//") {
		return false
	}
	s = strings.TrimSpace(s[2:])
	return strings.HasPrefix(s, "@require") ||
		strings.HasPrefix(s, "@ensure") ||
		strings.HasPrefix(s, "@must")
}
