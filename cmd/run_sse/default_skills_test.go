package run_sse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSkillsSeedOnce(t *testing.T) {
	source, home := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("default"), 0600)
	dest := filepath.Join(home, "skills")
	if err := initializeDefaultSkills(source, dest); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dest, "SKILL.md")
	if b, err := os.ReadFile(file); err != nil || string(b) != "default" {
		t.Fatal(string(b), err)
	}
	os.WriteFile(file, []byte("edited"), 0600)
	os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("upgrade"), 0600)
	if err := initializeDefaultSkills(source, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "edited" {
		t.Fatal("user edits overwritten")
	}
	os.Remove(file)
	if err := initializeDefaultSkills(source, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("user deletion overwritten")
	}
	os.RemoveAll(dest)
	if err := initializeDefaultSkills(source, dest); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dest); err != nil || len(entries) != 0 {
		t.Fatal("deleted skills directory re-seeded", err)
	}
}
func TestDefaultSkillsFailedSeedDoesNotPublish(t *testing.T) {
	source, home := t.TempDir(), t.TempDir()
	os.Symlink("/etc/passwd", filepath.Join(source, "escape"))
	dest := filepath.Join(home, "skills")
	if err := initializeDefaultSkills(source, dest); err == nil {
		t.Fatal("accepted symlink")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("partial skills published")
	}
}
