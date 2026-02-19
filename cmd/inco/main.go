package main

import (
	"fmt"
	"os"
	"path/filepath"

	inco "github.com/incognito-design/inco/internal/inco"
)

const usage = `inco — invisible constraints, invincible code.

Usage:
  inco gen [dir]       Scan source files and generate overlay
  inco build [args]    Run gen + go build -overlay
  inco test [args]     Run gen + go test -overlay
  inco run [args]      Run gen + go run -overlay
  inco audit [dir]     Contract coverage report
  inco clean [dir]     Remove .inco_cache

If [dir] is omitted, the current directory is used.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(0)
	}

	switch os.Args[1] {
	case "gen":
		dir := getDir(2)
		if err := runGen(dir); err != nil {
			fatal(err)
		}
	case "build":
		if err := runGen("."); err != nil {
			fatal(err)
		}
		runGo("build", ".", os.Args[2:])
	case "test":
		if err := runGen("."); err != nil {
			fatal(err)
		}
		runGo("test", ".", os.Args[2:])
	case "run":
		if err := runGen("."); err != nil {
			fatal(err)
		}
		runGo("run", ".", os.Args[2:])
	case "audit":
		dir := getDir(2)
		result, err := runAudit(dir)
		if err != nil {
			fatal(err)
		}
		result.PrintReport(os.Stdout)
	case "clean":
		dir := getDir(2)
		if err := os.RemoveAll(filepath.Join(dir, ".inco_cache")); err != nil {
			fatal(err)
		}
		fmt.Println("inco: cache cleaned")
	default:
		fmt.Fprintf(os.Stderr, "inco: unknown command %q\n", os.Args[1])
		fmt.Print(usage)
		os.Exit(1)
	}
}

func getDir(argIdx int) string {
	if len(os.Args) > argIdx {
		return os.Args[argIdx]
	}
	return "."
}

func runGen(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	return inco.NewEngine(absDir).Run()
}

func runAudit(dir string) (*inco.AuditResult, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return inco.Audit(absDir)
}

func runGo(subcmd, dir string, extraArgs []string) {
	overlayPath := filepath.Join(dir, ".inco_cache", "overlay.json")
	if _, err := os.Stat(overlayPath); os.IsNotExist(err) {
		execGo(subcmd, extraArgs)
		return
	}
	absOverlay, err := filepath.Abs(overlayPath)
	if err != nil {
		fatal(err)
	}
	args := append([]string{fmt.Sprintf("-overlay=%s", absOverlay)}, extraArgs...)
	execGo(subcmd, args)
}

func execGo(subcmd string, args []string) {
	cmd := execCommand("go", append([]string{subcmd}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "inco: %v\n", err)
	os.Exit(1)
}
