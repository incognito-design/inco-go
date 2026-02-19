package inco

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupDir creates a temp directory with Go source files and returns its path.
func setupDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// readShadow returns the content of the first shadow file in the overlay.
func readShadow(t *testing.T, e *Engine) string {
	t.Helper()
	for _, sp := range e.Overlay.Replace {
		data, err := os.ReadFile(sp)
		if err != nil {
			t.Fatalf("reading shadow: %v", err)
		}
		return string(data)
	}
	t.Fatal("no shadow files")
	return ""
}

// --- No directives ---

func TestEngine_NoDirectives(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	if len(e.Overlay.Replace) != 0 {
		t.Errorf("expected 0 overlay entries, got %d", len(e.Overlay.Replace))
	}
}

// --- Default action (panic) ---

func TestEngine_RequireExpr_DefaultPanic(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

import "fmt"

func Greet(name string) {
	// @require len(name) > 0
	fmt.Println(name)
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	if !strings.Contains(shadow, "!(len(name) > 0)") {
		t.Errorf("shadow should contain negated condition, got:\n%s", shadow)
	}
	if !strings.Contains(shadow, "panic(") {
		t.Error("shadow should contain panic (default action)")
	}
	if !strings.Contains(shadow, "require violation") {
		t.Error("shadow should contain default violation message")
	}
}

// --- Panic with custom message ---

func TestEngine_RequirePanicCustomMsg(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

import "fmt"

func Process(x int) {
	// @require x > 0 panic("x must be positive")
	fmt.Println(x)
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	if !strings.Contains(shadow, `panic("x must be positive")`) {
		t.Errorf("shadow should contain custom panic message, got:\n%s", shadow)
	}
}

// --- Multiple directives ---

func TestEngine_MultipleDirectives(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

import "fmt"

func Process(name string, age int) {
	// @require len(name) > 0
	// @require age > 0
	fmt.Println(name, age)
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	if !strings.Contains(shadow, "!(len(name) > 0)") {
		t.Error("missing first condition")
	}
	if !strings.Contains(shadow, "!(age > 0)") {
		t.Error("missing second condition")
	}
	// Verify order: name check before age check.
	nameIdx := strings.Index(shadow, "len(name)")
	ageIdx := strings.Index(shadow, "age > 0")
	if nameIdx > ageIdx {
		t.Error("directives not in source order")
	}
}

// --- Line directives ---

func TestEngine_LineDirectives(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

import "fmt"

func Hello(name string) {
	// @require len(name) > 0
	fmt.Println(name)
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	if !strings.Contains(shadow, "//line") {
		t.Error("shadow should contain //line directives")
	}
}

// --- Overlay JSON ---

func TestEngine_OverlayJSON(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

func Do(x int) {
	// @require x > 0
	_ = x
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}

	overlayPath := filepath.Join(dir, ".inco_cache", "overlay.json")
	data, err := os.ReadFile(overlayPath)
	if err != nil {
		t.Fatalf("overlay.json not found: %v", err)
	}

	var ov Overlay
	if err := json.Unmarshal(data, &ov); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(ov.Replace) != 1 {
		t.Errorf("overlay has %d entries, want 1", len(ov.Replace))
	}
	for _, sp := range ov.Replace {
		if _, err := os.Stat(sp); err != nil {
			t.Errorf("shadow file missing: %s", sp)
		}
	}
}

// --- Skips hidden dirs ---

func TestEngine_SkipsHiddenDirs(t *testing.T) {
	dir := setupDir(t, map[string]string{
		".hidden/main.go": `package hidden

func X(x int) {
	// @require x > 0
}
`,
		"main.go": "package main\n\nfunc main() {}\n",
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	if len(e.Overlay.Replace) != 0 {
		t.Errorf("should skip hidden dirs, got %d", len(e.Overlay.Replace))
	}
}

// --- Content hash stability ---

func TestEngine_ContentHashStable(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

func Do(x int) {
	// @require x > 0
	_ = x
}
`,
	})

	e1 := NewEngine(dir)
	if err := e1.Run(); err != nil {
		t.Fatal(err)
	}
	var p1 string
	for _, p := range e1.Overlay.Replace {
		p1 = p
	}

	e2 := NewEngine(dir)
	if err := e2.Run(); err != nil {
		t.Fatal(err)
	}
	var p2 string
	for _, p := range e2.Overlay.Replace {
		p2 = p
	}

	if filepath.Base(p1) != filepath.Base(p2) {
		t.Errorf("shadow names differ: %s vs %s", filepath.Base(p1), filepath.Base(p2))
	}
}

// --- Closure support ---

func TestEngine_Closure(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

import "fmt"

func Outer() {
	f := func(x int) {
		// @require x > 0
		fmt.Println(x)
	}
	f(42)
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	if !strings.Contains(shadow, "!(x > 0)") {
		t.Error("should process directives inside closures")
	}
}

// ---------------------------------------------------------------------------
// @must tests
// ---------------------------------------------------------------------------

func TestEngine_Must_DefaultPanic(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

import "fmt"

func Run() {
	result, _ := fmt.Println("hello") // @must
	_ = result
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	// _ should be replaced with __inco_err
	if !strings.Contains(shadow, "__inco_err") {
		t.Errorf("should contain __inco_err, got:\n%s", shadow)
	}
	// Should have if __inco_err != nil { panic(__inco_err) }
	if !strings.Contains(shadow, "__inco_err != nil") {
		t.Errorf("should contain error check, got:\n%s", shadow)
	}
	if !strings.Contains(shadow, "panic(__inco_err)") {
		t.Errorf("should panic with error var, got:\n%s", shadow)
	}
	// Inline comment should be stripped
	if strings.Contains(shadow, "// @must") {
		t.Error("inline comment should be stripped from shadow")
	}
}

func TestEngine_Must_Return_removed(t *testing.T) {
	// Return action removed — @must now only supports panic.
	// This test verifies that @must with no explicit action defaults to panic.
	dir := setupDir(t, map[string]string{
		"main.go": `package main

import "fmt"

func SafeRun() {
	_, _ = fmt.Println("hello") // @must
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	if !strings.Contains(shadow, "panic(__inco_err)") {
		t.Errorf("should contain panic, got:\n%s", shadow)
	}
}

func TestEngine_Must_PanicWithSubstitution(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

import "fmt"

func Fetch() {
	_, _ = fmt.Println("hello") // @must panic(fmt.Sprintf("failed: %v", _))
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	// _ in action args should be replaced with __inco_err
	if !strings.Contains(shadow, `panic(fmt.Sprintf("failed: %v", __inco_err))`) {
		t.Errorf("_ should be substituted with __inco_err, got:\n%s", shadow)
	}
}

func TestEngine_Must_PanicCustomMsg(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

import "fmt"

func Run() {
	_, _ = fmt.Println("hello") // @must panic("print failed")
	fmt.Println("done")
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	if !strings.Contains(shadow, `panic("print failed")`) {
		t.Errorf("should contain custom panic message, got:\n%s", shadow)
	}
}

// ---------------------------------------------------------------------------
// @ensure tests
// ---------------------------------------------------------------------------

func TestEngine_Ensure_DefaultPanic(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

func Run() {
	m := map[string]int{"a": 1}
	v, _ := m["a"] // @ensure
	_ = v
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	if !strings.Contains(shadow, "__inco_ok") {
		t.Errorf("should contain __inco_ok, got:\n%s", shadow)
	}
	if !strings.Contains(shadow, "!__inco_ok") {
		t.Errorf("should contain !__inco_ok, got:\n%s", shadow)
	}
	if !strings.Contains(shadow, "ensure violation") {
		t.Errorf("should contain ensure violation message, got:\n%s", shadow)
	}
}

func TestEngine_Ensure_Panic(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.go": `package main

func Run() {
	m := map[string]int{"a": 1}
	v, _ := m["a"] // @ensure panic("key not found")
	_ = v
}
`,
	})
	e := NewEngine(dir)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	shadow := readShadow(t, e)
	if !strings.Contains(shadow, "__inco_ok") {
		t.Errorf("should contain __inco_ok, got:\n%s", shadow)
	}
	if !strings.Contains(shadow, `panic("key not found")`) {
		t.Errorf("should contain custom panic msg, got:\n%s", shadow)
	}
}
