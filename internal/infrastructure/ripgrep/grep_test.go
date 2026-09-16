package ripgrep_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/ripgrep"
)

func grepTool(t *testing.T, root string) *tools.GrepTool {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is not installed")
	}
	return tools.NewGrepTool(root, ripgrep.New())
}

func runGrep(t *testing.T, g *tools.GrepTool, args map[string]any) (string, map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result := g.ExecuteResult(context.Background(), raw)
	return result.Output, result.Metadata, result.Error
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGrepSearchAndFileScope(t *testing.T) {
	root := t.TempDir()
	g := grepTool(t, root)
	file := filepath.Join(root, "main.go")
	write(t, file, "first\nneedle\nthird\n")
	write(t, filepath.Join(root, "sibling.go"), "needle\n")
	write(t, filepath.Join(root, "other.txt"), "needle\n")
	write(t, filepath.Join(root, ".hidden.go"), "needle\n")
	write(t, filepath.Join(root, ".git", "tracked.go"), "needle\n")

	out, meta, err := runGrep(t, g, map[string]any{"pattern": "needle", "path": file})
	if err != nil || meta["matches"] != 1 || !strings.Contains(out, file+":\n  Line 2: needle") || strings.Contains(out, "sibling.go") {
		t.Fatalf("file search: output=%q metadata=%v error=%v", out, meta, err)
	}

	out, meta, err = runGrep(t, g, map[string]any{"pattern": "needle", "include": "*.go"})
	if err != nil || meta["matches"] != 3 || !strings.Contains(out, ".hidden.go") || strings.Contains(out, "other.txt") || strings.Contains(out, "tracked.go") {
		t.Fatalf("glob search: output=%q metadata=%v error=%v", out, meta, err)
	}

	out, meta, err = runGrep(t, g, map[string]any{"pattern": "nomatch"})
	if err != nil || out != "No files found" || meta["matches"] != 0 || meta["truncated"] != false {
		t.Fatalf("no matches: output=%q metadata=%v error=%v", out, meta, err)
	}
}

func TestGrepTruncationAndRegexErrors(t *testing.T) {
	root := t.TempDir()
	g := grepTool(t, root)
	file := filepath.Join(root, "many.txt")
	write(t, file, strings.Repeat("needle\n", 100))
	out, meta, err := runGrep(t, g, map[string]any{"pattern": "needle"})
	if err != nil || meta["matches"] != 100 || meta["truncated"] != false || strings.Contains(out, "Results truncated") {
		t.Fatalf("100 matches: output=%q metadata=%v error=%v", out, meta, err)
	}

	write(t, file, strings.Repeat("needle\n", 101))
	out, meta, err = runGrep(t, g, map[string]any{"pattern": "needle"})
	if err != nil || meta["matches"] != 100 || meta["truncated"] != true || !strings.Contains(out, "Results truncated") {
		t.Fatalf("101 matches: output=%q metadata=%v error=%v", out, meta, err)
	}

	_, _, err = runGrep(t, g, map[string]any{"pattern": "["})
	if err == nil || !strings.Contains(err.Error(), "regex") {
		t.Fatalf("invalid regex: %v", err)
	}
}

func TestGrepPathBoundaryAndArguments(t *testing.T) {
	root := t.TempDir()
	g := grepTool(t, root)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "outside.txt"), "needle")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, path := range []string{"../", filepath.Join(root, "escape"), outside} {
		_, _, err := runGrep(t, g, map[string]any{"pattern": "needle", "path": path})
		if err == nil {
			t.Errorf("path %q escaped work directory", path)
		}
	}
	realDir := filepath.Join(root, "real")
	write(t, filepath.Join(realDir, "test.txt"), "needle")
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatal(err)
	}
	out, _, err := runGrep(t, g, map[string]any{"pattern": "needle", "path": alias})
	if err != nil || !strings.Contains(out, filepath.Join(alias, "test.txt")) || strings.Contains(out, filepath.Join(realDir, "test.txt")) {
		t.Fatalf("allowed alias path: output=%q error=%v", out, err)
	}

	write(t, filepath.Join(root, "file.txt"), "needle")
	injected := filepath.Join(root, "injected")
	pattern := fmt.Sprintf("needle; touch %s", injected)
	_, _, err = runGrep(t, g, map[string]any{"pattern": pattern})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(injected); !os.IsNotExist(statErr) {
		t.Fatalf("pattern was interpreted as shell command: %v", statErr)
	}
}

func TestGrepClipsLongLines(t *testing.T) {
	root := t.TempDir()
	g := grepTool(t, root)
	write(t, filepath.Join(root, "long.txt"), strings.Repeat("x", 2001)+"\n")
	out, _, err := runGrep(t, g, map[string]any{"pattern": "x+"})
	if err != nil || !strings.Contains(out, "Line 1: "+strings.Repeat("x", 2000)+"...") || strings.Contains(out, strings.Repeat("x", 2001)) {
		t.Fatalf("long line: output=%q error=%v", out, err)
	}
}
