package inco

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRelease verifies that Release generates .go from .inco.go shadows
// and renames .inco.go → .inco backup.
func TestRelease(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.inco.go": `package main

func main() {
	x := 42
	// @require x > 0
	_ = x
}
`,
	})

	// 1. Generate overlay.
	e := NewEngine(dir)
	e.Run()
	if len(e.Overlay.Replace) == 0 {
		t.Fatal("expected overlay entries after gen")
	}

	// 2. Release.
	Release(dir)

	// 3. Check released .go file exists.
	releasePath := filepath.Join(dir, "main.go")
	releaseContent, err := os.ReadFile(releasePath)
	if err != nil {
		t.Fatalf("released file not found: %v", err)
	}
	rc := string(releaseContent)

	// Must have generated-code header.
	if !strings.HasPrefix(rc, releaseHeader) {
		t.Error("released file missing generated-code header")
	}
	// Must contain the guard.
	if !strings.Contains(rc, "if !(x > 0)") {
		t.Error("released file missing injected guard")
	}
	// Must preserve //line directives for stack traces.
	if !strings.Contains(rc, "//line ") {
		t.Error("released file should contain //line directives")
	}

	// 4. Check original .inco.go was renamed to .inco.
	if _, err := os.Stat(filepath.Join(dir, "main.inco.go")); !os.IsNotExist(err) {
		t.Error("original .inco.go should be renamed away")
	}
	backupPath := filepath.Join(dir, "main.inco")
	if _, err := os.Stat(backupPath); err != nil {
		t.Error("backup .inco file should exist")
	}
}

// TestRelease_SkipsNonIncoGo ensures Release ignores plain .go files.
func TestRelease_SkipsNonIncoGo(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"plain.go": `package main

func main() {
	// @require true
}
`,
	})

	e := NewEngine(dir)
	e.Run()
	Release(dir)

	// plain.go should NOT have been renamed or duplicated.
	if _, err := os.Stat(filepath.Join(dir, "plain.inco")); !os.IsNotExist(err) {
		t.Error("plain.go should not be backed up")
	}
}

// TestReleaseClean restores .inco → .inco.go and removes generated .go.
func TestReleaseClean(t *testing.T) {
	dir := setupDir(t, map[string]string{
		"main.inco.go": `package main

func main() {
	// @require true
}
`,
	})

	e := NewEngine(dir)
	e.Run()
	Release(dir)

	// Verify state after release.
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Fatal("released .go file should exist before clean")
	}

	// Clean.
	ReleaseClean(dir)

	// Generated .go removed.
	if _, err := os.Stat(filepath.Join(dir, "main.go")); !os.IsNotExist(err) {
		t.Error("generated .go should be removed after clean")
	}

	// Original .inco.go restored.
	if _, err := os.Stat(filepath.Join(dir, "main.inco.go")); err != nil {
		t.Error("original .inco.go should be restored after clean")
	}

	// No .inco backup left.
	if _, err := os.Stat(filepath.Join(dir, "main.inco")); !os.IsNotExist(err) {
		t.Error(".inco backup should be gone after clean")
	}
}

// TestReleasePathFor tests the helper function.
func TestReleasePathFor(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/a/b/main.inco.go", "/a/b/main.go"},
		{"/src/engine.inco.go", "/src/engine.go"},
	}
	for _, tt := range tests {
		got := releasePathFor(tt.input)
		if got != tt.want {
			t.Errorf("releasePathFor(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestBackupPathFor tests the backup helper function.
func TestBackupPathFor(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/a/b/main.inco.go", "/a/b/main.inco"},
		{"/src/engine.inco.go", "/src/engine.inco"},
	}
	for _, tt := range tests {
		got := backupPathFor(tt.input)
		if got != tt.want {
			t.Errorf("backupPathFor(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
