package inco

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// Overlay is the JSON structure consumed by `go build -overlay`.
type Overlay struct {
	Replace map[string]string `json:"Replace"`
}

// Engine scans Go source files for @require directives and produces an
// overlay that injects the corresponding if-statements at compile time.
type Engine struct {
	Root    string
	Overlay Overlay
	fset    *token.FileSet
}

// NewEngine creates an engine rooted at the given directory.
func NewEngine(root string) *Engine {
	return &Engine{
		Root:    root,
		Overlay: Overlay{Replace: make(map[string]string)},
		fset:    token.NewFileSet(),
	}
}

// ---------------------------------------------------------------------------
// Run — top-level entry point
// ---------------------------------------------------------------------------

// Run scans all Go source files under Root, processes @require directives,
// and writes the overlay + shadow files into .inco_cache/.
func (e *Engine) Run() error {
	packages, err := e.scanPackages()
	if err != nil {
		return fmt.Errorf("scanning packages: %w", err)
	}

	for _, pkg := range packages {
		if err := e.processPackage(pkg); err != nil {
			return err
		}
	}

	if len(e.Overlay.Replace) > 0 {
		if err := e.writeOverlay(); err != nil {
			return fmt.Errorf("writing overlay: %w", err)
		}
		fmt.Fprintf(os.Stderr, "inco: overlay written to %s (%d file(s) mapped)\n",
			filepath.Join(e.Root, ".inco_cache", "overlay.json"),
			len(e.Overlay.Replace))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Package scanning
// ---------------------------------------------------------------------------

type pkgBundle struct {
	Dir   string
	Files map[string]*ast.File // absolute path → AST
	Paths []string             // sorted absolute paths (deterministic iteration)
}

// scanPackages walks Root, parses every non-test .go file, and groups them
// by directory (= Go package).
func (e *Engine) scanPackages() ([]*pkgBundle, error) {
	dirFiles := make(map[string]map[string]*ast.File)

	err := filepath.WalkDir(e.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, parseErr := parser.ParseFile(e.fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		dir := filepath.Dir(path)
		if dirFiles[dir] == nil {
			dirFiles[dir] = make(map[string]*ast.File)
		}
		dirFiles[dir][path] = f
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort directories for deterministic order.
	dirs := make([]string, 0, len(dirFiles))
	for d := range dirFiles {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	result := make([]*pkgBundle, 0, len(dirs))
	for _, dir := range dirs {
		files := dirFiles[dir]
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		result = append(result, &pkgBundle{Dir: dir, Files: files, Paths: paths})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Package & file processing
// ---------------------------------------------------------------------------

func (e *Engine) processPackage(pkg *pkgBundle) error {
	for _, path := range pkg.Paths {
		f := pkg.Files[path]
		if err := e.processFile(path, f); err != nil {
			return fmt.Errorf("processing %s: %w", path, err)
		}
	}
	return nil
}

// processFile scans a single source file for directives,
// generates injected if-blocks via text replacement, and writes a shadow.
func (e *Engine) processFile(path string, f *ast.File) error {
	// 1. Collect directive lines from AST comments.
	directives := make(map[int]*Directive) // 1-based line → Directive
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			d := ParseDirective(c.Text)
			if d != nil {
				line := e.fset.Position(c.Pos()).Line
				directives[line] = d
			}
		}
	}
	if len(directives) == 0 {
		return nil
	}

	// 2. Read source as lines.
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(src), "\n")

	// 3. Classify directives: standalone (@require) vs inline (@must/@ensure).
	standalone := make(map[int]*Directive)
	inline := make(map[int]*Directive)

	for lineNum, d := range directives {
		idx := lineNum - 1
		if idx < 0 || idx >= len(lines) {
			continue
		}
		trimmed := strings.TrimSpace(lines[idx])
		isStandaloneLine := strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*")

		switch d.Kind {
		case KindRequire:
			if isStandaloneLine {
				standalone[lineNum] = d
			}
		case KindMust, KindEnsure:
			if !isStandaloneLine {
				inline[lineNum] = d
			}
		}
	}
	if len(standalone) == 0 && len(inline) == 0 {
		return nil
	}

	// 4. Build output: replace/augment directive lines, add //line directives
	//    to preserve source mapping.
	var output []string
	prevWasDirective := false

	for idx, line := range lines {
		lineNum := idx + 1

		if d, ok := standalone[lineNum]; ok {
			// Standalone @require: replace comment line with if-block.
			indent := extractIndent(line)
			output = append(output, fmt.Sprintf("%s//line %s:%d", indent, path, lineNum))
			output = append(output, e.generateIfBlock(d, indent, path, lineNum))
			prevWasDirective = true
		} else if d, ok := inline[lineNum]; ok {
			// Inline @must/@ensure: rewrite code line + inject if-block after.
			indent := extractIndent(line)
			varName, rewritten := e.rewriteInlineLine(line, d)
			output = append(output, fmt.Sprintf("%s//line %s:%d", indent, path, lineNum))
			output = append(output, rewritten)
			output = append(output, e.generateInlineIfBlock(d, indent, path, lineNum, varName))
			prevWasDirective = true
		} else {
			if prevWasDirective {
				indent := extractIndent(line)
				output = append(output, fmt.Sprintf("%s//line %s:%d", indent, path, lineNum))
				prevWasDirective = false
			}
			output = append(output, line)
		}
	}

	// 5. Collect package references from injected code, add missing imports.
	content := strings.Join(output, "\n")
	content = e.addMissingImports(content, f, directives)

	// 6. Write shadow file.
	return e.writeShadow(path, []byte(content))
}

// ---------------------------------------------------------------------------
// Code generation
// ---------------------------------------------------------------------------

// generateIfBlock returns the text of the injected if-statement.
//
//	if !(expr) {
//	    panic(...)
//	}
func (e *Engine) generateIfBlock(d *Directive, indent, path string, line int) string {
	cond := fmt.Sprintf("!(%s)", d.Expr)
	body := e.buildPanicBody(d, path, line)
	return fmt.Sprintf("%sif %s {\n%s\t%s\n%s}", indent, cond, indent, body, indent)
}

// buildPanicBody generates a panic statement.
//
//   - With ActionArgs  → panic(arg)
//   - Default          → panic("require violation: <expr> (at file:line)")
func (e *Engine) buildPanicBody(d *Directive, path string, line int) string {
	if len(d.ActionArgs) > 0 {
		return "panic(" + d.ActionArgs[0] + ")"
	}
	relPath := path
	if rel, err := filepath.Rel(e.Root, path); err == nil {
		relPath = rel
	}
	msg := fmt.Sprintf("require violation: %s (at %s:%d)", d.Expr, relPath, line)
	return fmt.Sprintf("panic(%q)", msg)
}

// ---------------------------------------------------------------------------
// Inline directive helpers (@must / @ensure)
// ---------------------------------------------------------------------------

// rewriteInlineLine strips the inline @must/@ensure comment and replaces the
// last blank identifier (_) with a generated variable name.
func (e *Engine) rewriteInlineLine(line string, d *Directive) (varName, rewritten string) {
	keyword := "@must"
	varName = "__inco_err"
	if d.Kind == KindEnsure {
		keyword = "@ensure"
		varName = "__inco_ok"
	}
	codePart := stripInlineComment(line, keyword)
	rewritten = replaceLastBlank(codePart, varName)
	return
}

// generateInlineIfBlock builds the if-block for an inline @must/@ensure.
func (e *Engine) generateInlineIfBlock(d *Directive, indent, path string, line int, varName string) string {
	var cond string
	switch d.Kind {
	case KindMust:
		cond = varName + " != nil"
	default: // KindEnsure
		cond = "!" + varName
	}
	body := e.buildInlinePanicBody(d, path, line, varName)
	return fmt.Sprintf("%sif %s {\n%s\t%s\n%s}", indent, cond, indent, body, indent)
}

// buildInlinePanicBody produces the panic statement inside the if-block for
// @must / @ensure directives.
func (e *Engine) buildInlinePanicBody(d *Directive, path string, line int, varName string) string {
	if len(d.ActionArgs) > 0 {
		return "panic(" + substituteBlank(d.ActionArgs[0], varName) + ")"
	}
	if d.Kind == KindMust {
		return "panic(" + varName + ")"
	}
	// KindEnsure — descriptive message.
	relPath := path
	if rel, err := filepath.Rel(e.Root, path); err == nil {
		relPath = rel
	}
	return fmt.Sprintf("panic(%q)", fmt.Sprintf("ensure violation at %s:%d", relPath, line))
}

// stripInlineComment removes the inline directive comment from a code line.
func stripInlineComment(line, keyword string) string {
	for _, prefix := range []string{"// " + keyword, "//" + keyword, "/* " + keyword} {
		if idx := strings.Index(line, prefix); idx >= 0 {
			return strings.TrimRight(line[:idx], " \t")
		}
	}
	return line
}

// replaceLastBlank replaces the last standalone blank identifier (_) with
// replacement.  A blank is standalone when not adjacent to other identifier
// characters (letters, digits, _).
func replaceLastBlank(code, replacement string) string {
	for i := len(code) - 1; i >= 0; i-- {
		if code[i] != '_' {
			continue
		}
		prevOk := i == 0 || !isIdentChar(code[i-1])
		nextOk := i == len(code)-1 || !isIdentChar(code[i+1])
		if prevOk && nextOk {
			return code[:i] + replacement + code[i+1:]
		}
	}
	return code
}

// substituteBlank replaces all standalone _ identifiers in expr with varName.
// This allows users to reference the captured variable via _ in action args:
//
//	// @must return(nil, _)   →   return nil, __inco_err
func substituteBlank(expr, varName string) string {
	var buf strings.Builder
	for i := 0; i < len(expr); i++ {
		if expr[i] == '_' {
			prevOk := i == 0 || !isIdentChar(expr[i-1])
			nextOk := i == len(expr)-1 || !isIdentChar(expr[i+1])
			if prevOk && nextOk {
				buf.WriteString(varName)
				continue
			}
		}
		buf.WriteByte(expr[i])
	}
	return buf.String()
}

// isIdentChar reports whether b can appear in a Go identifier.
func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// ---------------------------------------------------------------------------
// Import management
// ---------------------------------------------------------------------------

// stdlibPackages is a set of commonly used standard library packages that
// might appear in directive action arguments.
var stdlibPackages = map[string]string{
	"fmt":     "fmt",
	"errors":  "errors",
	"strings": "strings",
	"strconv": "strconv",
	"log":     "log",
	"os":      "os",
	"io":      "io",
	"math":    "math",
	"time":    "time",
	"context": "context",
	"sync":    "sync",
	"sort":    "sort",
	"bytes":   "bytes",
	"regexp":  "regexp",
	"path":    "path",
	"net":     "net",
	"http":    "net/http",
	"json":    "encoding/json",
	"xml":     "encoding/xml",
	"sql":     "database/sql",
	"reflect": "reflect",
	"unsafe":  "unsafe",
}

// pkgRefRe matches package-qualified identifiers like fmt.Errorf, errors.New.
var pkgRefRe = regexp.MustCompile(`\b([a-zA-Z_]\w*)\.\w+`)

// addMissingImports re-parses the shadow content, detects package references
// in directive action args, and adds missing imports via astutil.AddImport.
func (e *Engine) addMissingImports(content string, origFile *ast.File, directives map[int]*Directive) string {
	// 1. Collect all package-qualified identifiers from directives.
	needed := make(map[string]bool)
	for _, d := range directives {
		sources := d.ActionArgs
		if d.Expr != "" {
			sources = append(sources, d.Expr)
		}
		for _, s := range sources {
			for _, match := range pkgRefRe.FindAllStringSubmatch(s, -1) {
				needed[match[1]] = true
			}
		}
	}
	if len(needed) == 0 {
		return content
	}

	// 2. Determine which packages are already imported.
	imported := make(map[string]bool)
	for _, imp := range origFile.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		// Use local name if aliased, otherwise last segment.
		var name string
		if imp.Name != nil {
			name = imp.Name.Name
		} else {
			parts := strings.Split(path, "/")
			name = parts[len(parts)-1]
		}
		imported[name] = true
	}

	// 3. Find which needed packages are missing.
	var toAdd []string
	for pkg := range needed {
		if imported[pkg] {
			continue
		}
		if _, ok := stdlibPackages[pkg]; ok {
			toAdd = append(toAdd, pkg)
		}
	}
	if len(toAdd) == 0 {
		return content
	}

	// 4. Re-parse the shadow content and add imports via astutil.
	fset := token.NewFileSet()
	shadowAST, err := parser.ParseFile(fset, "", content, parser.ParseComments)
	if err != nil {
		// If parsing fails, return content as-is.
		return content
	}
	for _, pkg := range toAdd {
		astutil.AddImport(fset, shadowAST, stdlibPackages[pkg])
	}

	// 5. Re-render.
	var buf strings.Builder
	if err := format.Node(&buf, fset, shadowAST); err != nil {
		return content
	}
	return buf.String()
}

// ---------------------------------------------------------------------------
// Shadow & overlay I/O
// ---------------------------------------------------------------------------

func (e *Engine) writeShadow(origPath string, content []byte) error {
	cacheDir := filepath.Join(e.Root, ".inco_cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}

	hash := sha256.Sum256(content)
	shadowName := fmt.Sprintf("%s_%x.go",
		strings.TrimSuffix(filepath.Base(origPath), ".go"),
		hash[:8])
	shadowPath := filepath.Join(cacheDir, shadowName)

	if err := os.WriteFile(shadowPath, content, 0o644); err != nil {
		return err
	}
	e.Overlay.Replace[origPath] = shadowPath
	return nil
}

func (e *Engine) writeOverlay() error {
	cacheDir := filepath.Join(e.Root, ".inco_cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e.Overlay, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cacheDir, "overlay.json"), data, 0o644)
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

// extractIndent returns the leading whitespace of a line.
func extractIndent(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}
