package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
)

type readRootRunner struct {
	searchCWD string
	globCWD   string
}

func (*readRootRunner) Resolve(path string) (RipgrepPathInfo, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return RipgrepPathInfo{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return RipgrepPathInfo{}, err
	}
	return RipgrepPathInfo{RealPath: real, IsDir: info.IsDir()}, nil
}

func (r *readRootRunner) Search(_ context.Context, cwd, _ string, _ string, _ string, _ int) (GrepSearchResult, error) {
	r.searchCWD = cwd
	return GrepSearchResult{Matches: []GrepMatch{{Path: "SKILL.md", Line: 1, Text: "skill body"}}}, nil
}

func (r *readRootRunner) Glob(_ context.Context, cwd, _ string, _ int) (GlobSearchResult, error) {
	r.globCWD = cwd
	return GlobSearchResult{Files: []string{"SKILL.md"}}, nil
}

func TestGlobalSkillRootIsReadOnly(t *testing.T) {
	workDir, homeDir := t.TempDir(), t.TempDir()
	skillsRoot := filepath.Join(homeDir, ".laxcode", "skills")
	skillDir := filepath.Join(skillsRoot, "demo")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("skill body\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	read := NewReadFileTool(workDir, workfs.New(), skillsRoot)
	raw, _ := json.Marshal(map[string]any{"path": skillFile, "start_line_no": 1, "start_bytes": 1})
	out, err := read.Execute(context.Background(), raw)
	if err != nil || !strings.HasPrefix(out, "skill body\n") {
		t.Fatalf("read global skill: out=%q err=%v", out, err)
	}

	runner := &readRootRunner{}
	realSkillDir, err := filepath.EvalSymlinks(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	grepRaw, _ := json.Marshal(map[string]any{"pattern": "skill", "path": skillDir})
	if _, err := NewGrepTool(workDir, runner, skillsRoot).Execute(context.Background(), grepRaw); err != nil {
		t.Fatalf("grep global skill: %v", err)
	}
	if runner.searchCWD != realSkillDir {
		t.Fatalf("grep cwd=%q, want %q", runner.searchCWD, realSkillDir)
	}
	globRaw, _ := json.Marshal(map[string]any{"pattern": "*.md", "path": skillDir})
	if _, err := NewGlobTool(workDir, runner, skillsRoot).Execute(context.Background(), globRaw); err != nil {
		t.Fatalf("glob global skill: %v", err)
	}
	if runner.globCWD != realSkillDir {
		t.Fatalf("glob cwd=%q, want %q", runner.globCWD, realSkillDir)
	}

	writeRaw, _ := json.Marshal(map[string]any{"path": skillFile, "content": "changed"})
	if _, err := NewWriteFileTool(workDir, workfs.New()).Execute(context.Background(), writeRaw); err == nil {
		t.Fatal("write_file must reject the global skills path")
	}
	editRaw, _ := json.Marshal(map[string]any{
		"path": skillFile, "edits": []map[string]string{{"old_text": "skill body", "new_text": "changed"}},
	})
	if _, err := NewEditFileTool(workDir, workfs.New()).Execute(context.Background(), editRaw); err == nil {
		t.Fatal("edit_file must reject the global skills path")
	}
	data, err := os.ReadFile(skillFile)
	if err != nil || string(data) != "skill body\n" {
		t.Fatalf("skill content changed: %q err=%v", data, err)
	}
}

func TestReadOnlyRootRejectsSymlinkEscape(t *testing.T) {
	workDir, skillsRoot, outside := t.TempDir(), t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillsRoot, "escape.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"path": link, "start_line_no": 1, "start_bytes": 1})
	if _, err := NewReadFileTool(workDir, workfs.New(), skillsRoot).Execute(context.Background(), raw); err == nil {
		t.Fatal("read_file must reject a symlink escaping the read-only root")
	}
}

func TestPlanRootCanBeWrittenWithoutOpeningSkills(t *testing.T) {
	workDir, homeDir := t.TempDir(), t.TempDir()
	planRoot := filepath.Join(homeDir, ".laxcode", "sessions", "session-1")
	if err := os.MkdirAll(planRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(planRoot, "plan.md")
	writeRaw, _ := json.Marshal(map[string]any{"path": planFile, "content": "draft"})
	if _, err := NewWriteFileTool(workDir, workfs.New(), planRoot).Execute(context.Background(), writeRaw); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	editRaw, _ := json.Marshal(map[string]any{
		"path": planFile, "edits": []map[string]string{{"old_text": "draft", "new_text": "done"}},
	})
	if _, err := NewEditFileTool(workDir, workfs.New(), planRoot).Execute(context.Background(), editRaw); err != nil {
		t.Fatalf("edit plan file: %v", err)
	}
	data, err := os.ReadFile(planFile)
	if err != nil || string(data) != "done" {
		t.Fatalf("plan content=%q err=%v", data, err)
	}

	skillsRoot := filepath.Join(homeDir, ".laxcode", "skills")
	if err := os.MkdirAll(skillsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillsRoot, "SKILL.md")
	skillRaw, _ := json.Marshal(map[string]any{"path": skillFile, "content": "changed"})
	if _, err := NewWriteFileTool(workDir, workfs.New(), planRoot).Execute(context.Background(), skillRaw); err == nil {
		t.Fatal("plan write root must not make global skills writable")
	}
}
