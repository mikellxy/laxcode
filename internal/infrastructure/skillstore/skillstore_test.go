package skillstore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/tools"
)

func TestStoreCreateLoadAndReplace(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	ctx := context.Background()
	initial := []tools.SkillPackageFile{
		{Path: "SKILL.md", Content: []byte("skill")},
		{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Executable: true},
	}
	if err := store.Create(ctx, "demo", initial); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(ctx, "demo", initial); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second Create error = %v, want fs.ErrExist", err)
	}
	snapshot, err := store.Load(ctx, "demo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(snapshot.Files) != 2 || snapshot.Revision == "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if !snapshot.Files[1].Executable {
		t.Fatalf("executable bit was not preserved: %+v", snapshot.Files)
	}
	updated := []tools.SkillPackageFile{
		{Path: "SKILL.md", Content: []byte("updated")},
		{Path: "references/guide.md", Content: []byte("guide")},
	}
	if err := store.Replace(ctx, "demo", snapshot.Revision, updated); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	after, err := store.Load(ctx, "demo")
	if err != nil {
		t.Fatalf("Load after replace: %v", err)
	}
	if after.Revision == snapshot.Revision || len(after.Files) != 2 {
		t.Fatalf("replacement snapshot = %+v", after)
	}
	if _, err := os.Stat(filepath.Join(root, "demo", "scripts", "run.sh")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed file remains after full package replacement: %v", err)
	}
	if err := store.Replace(ctx, "demo", snapshot.Revision, updated); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("stale Replace error = %v, want ErrRevisionChanged", err)
	}
}

func TestStoreRejectsSymlinkPackageEntries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := New(root).Load(context.Background(), "demo"); err == nil {
		t.Fatal("Load must reject a symlink inside a skill package")
	}
}

func TestStoreRejectsInvalidPackagePath(t *testing.T) {
	err := New(t.TempDir()).Create(context.Background(), "demo", []tools.SkillPackageFile{{
		Path: "../escape", Content: []byte("x"),
	}})
	if err == nil {
		t.Fatal("Create must reject a package-relative path escape")
	}
}
