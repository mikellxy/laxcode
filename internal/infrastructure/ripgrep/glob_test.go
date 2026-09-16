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

func globTool(t *testing.T, root string) *tools.GlobTool {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is not installed")
	}
	return tools.NewGlobTool(root, ripgrep.New())
}

func runGlob(t *testing.T, g *tools.GlobTool, args map[string]any) (string, map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result := g.ExecuteResult(context.Background(), raw)
	return result.Output, result.Metadata, result.Error
}

func TestGlobFindsFilesByPattern(t *testing.T) {
	root := t.TempDir()
	g := globTool(t, root)
	write(t, filepath.Join(root, "a.go"), "package a")
	write(t, filepath.Join(root, "nested", "b.go"), "package b")
	write(t, filepath.Join(root, "nested", "b.txt"), "text")
	write(t, filepath.Join(root, ".git", "ignored.go"), "package ignored")

	out, meta, err := runGlob(t, g, map[string]any{"pattern": "**/*.go"})
	if err != nil || meta["count"] != 2 || !strings.Contains(out, filepath.Join(root, "a.go")) ||
		!strings.Contains(out, filepath.Join(root, "nested", "b.go")) || strings.Contains(out, "b.txt") || strings.Contains(out, "ignored.go") {
		t.Fatalf("glob: output=%q metadata=%v error=%v", out, meta, err)
	}

	out, meta, err = runGlob(t, g, map[string]any{"pattern": "*.txt", "path": "nested"})
	if err != nil || meta["count"] != 1 || out != filepath.Join(root, "nested", "b.txt") {
		t.Fatalf("scoped glob: output=%q metadata=%v error=%v", out, meta, err)
	}

	out, meta, err = runGlob(t, g, map[string]any{"pattern": "*.missing"})
	if err != nil || out != "No files found" || meta["count"] != 0 || meta["truncated"] != false {
		t.Fatalf("empty glob: output=%q metadata=%v error=%v", out, meta, err)
	}
}

func TestGlobRejectsFilesAndEscapingPaths(t *testing.T) {
	root := t.TempDir()
	g := globTool(t, root)
	file := filepath.Join(root, "a.go")
	write(t, file, "package a")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "outside.go"), "package outside")
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	for _, path := range []string{file, "../", outside, escape} {
		_, _, err := runGlob(t, g, map[string]any{"pattern": "*.go", "path": path})
		if err == nil {
			t.Errorf("glob accepted invalid path %q", path)
		}
	}

	realDir := filepath.Join(root, "real")
	write(t, filepath.Join(realDir, "inside.go"), "package inside")
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatal(err)
	}
	out, _, err := runGlob(t, g, map[string]any{"pattern": "*.go", "path": alias})
	if err != nil || out != filepath.Join(alias, "inside.go") {
		t.Fatalf("allowed alias: output=%q error=%v", out, err)
	}
}

func TestGlobTruncationAndArguments(t *testing.T) {
	root := t.TempDir()
	g := globTool(t, root)
	for i := 0; i < 100; i++ {
		write(t, filepath.Join(root, fmt.Sprintf("file-%03d.go", i)), "package files")
	}
	out, meta, err := runGlob(t, g, map[string]any{"pattern": "*.go"})
	if err != nil || meta["count"] != 100 || meta["truncated"] != false || strings.Contains(out, "Results are truncated") {
		t.Fatalf("100 files: metadata=%v error=%v", meta, err)
	}
	write(t, filepath.Join(root, "file-100.go"), "package files")
	out, meta, err = runGlob(t, g, map[string]any{"pattern": "*.go"})
	if err != nil || meta["count"] != 100 || meta["truncated"] != true || !strings.Contains(out, "Results are truncated") {
		t.Fatalf("101 files: metadata=%v error=%v", meta, err)
	}

	injected := filepath.Join(root, "injected")
	pattern := fmt.Sprintf("*.go; touch %s", injected)
	_, _, err = runGlob(t, g, map[string]any{"pattern": pattern})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(injected); !os.IsNotExist(statErr) {
		t.Fatalf("pattern was interpreted as shell command: %v", statErr)
	}
}
