package agentasm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func TestCloudToolBoundaries(t *testing.T) {
	root := t.TempDir()
	home, work, temp := filepath.Join(root, "agenthome"), filepath.Join(root, "demo"), filepath.Join(root, "tmp")
	for _, dir := range []string{home, work, temp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	assembled, err := Assemble(context.Background(), Input{Mode: ModeCode, UserRoot: root, HomeDir: home, WorkDir: work, TempDir: temp})
	if err != nil {
		t.Fatal(err)
	}
	defer assembled.Cleanup()
	if !strings.Contains(assembled.Session.Messages[0].Content, "项目路径只能从 /demo 开始") {
		t.Fatal("cloud system prompt lacks display path rules")
	}
	registry := assembled.Service.ToolRegistry
	if sandbox, ok := registry.(interface{ BashSandboxed() bool }); !ok || !sandbox.BashSandboxed() {
		t.Fatal("cloud Bash was not wired to a sandbox")
	}
	for _, rule := range []string{tools.CloudBashEnvironment, tools.CloudBashOfflineHint} {
		if !strings.Contains(assembled.Session.Messages[0].Content, rule) {
			t.Fatal("cloud prompt lacks Bash environment rules")
		}
	}
	if !strings.Contains(assembled.Session.Messages[0].Content, layout.SkillsRoot(home)) || !strings.Contains(assembled.Session.Messages[0].Content, "权限为只读") {
		t.Fatal("cloud prompt lacks read-only Skills mount path")
	}
	for _, definition := range registry.GetAvailableTools() {
		if definition.Name == tools.ToolBash && (!strings.Contains(definition.Description, "可以读取和执行其中的脚本") || strings.Contains(definition.Description, "不要用 Bash")) {
			t.Fatal("cloud Bash description does not allow read-only Skill scripts")
		}
	}
	run := func(name string, args any) *sharedkernel.ToolResult {
		raw, _ := json.Marshal(args)
		call := &sharedkernel.ToolCall{ID: "test", Name: name, Arguments: raw}
		confirmation, err := registry.Confirmation(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		if confirmation != nil {
			t.Fatal("cloud file tool requested approval")
		}
		return registry.Execute(context.Background(), call)
	}
	if got := run("write_file", map[string]string{"path": "../tmp/note.txt", "content": "hello cloud"}); got.IsError {
		t.Fatalf("within user root: %+v", got)
	}
	if got := run("read_file", map[string]string{"path": "../tmp/note.txt"}); got.IsError || !strings.Contains(got.Output, "hello cloud") {
		t.Fatalf("read: %+v", got)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read_file", "write_file", "edit_file", "grep", "glob"} {
		for _, path := range []string{secret, "escape/secret.txt"} {
			args := map[string]any{"path": path, "content": "overwrite", "pattern": "secret", "edits": []map[string]string{{"old_text": "secret", "new_text": "overwrite"}}}
			raw, _ := json.Marshal(args)
			got := registry.Execute(tools.WithApprovalGrants(context.Background(), []string{secret}), &sharedkernel.ToolCall{Name: name, Arguments: raw})
			if !got.IsError {
				t.Fatalf("%s accepted escape %s: %+v", name, path, got)
			}
		}
	}
	if data, _ := os.ReadFile(secret); string(data) != "secret" {
		t.Fatal("outside file changed")
	}
	got := run("grep", map[string]string{"path": "../tmp", "pattern": "hello", "include": "*.{txt,md}"})
	if got.IsError || !strings.Contains(got.Output, "hello cloud") {
		t.Fatalf("rooted grep: %+v", got)
	}
	got = run("glob", map[string]string{"path": "../tmp", "pattern": "**/*.txt"})
	if got.IsError || !strings.Contains(got.Output, "note.txt") {
		t.Fatalf("rooted glob: %+v", got)
	}
	got = run("run_sub_agent", map[string]string{"work_dir": outside, "task": "read secret"})
	if !got.IsError {
		t.Fatalf("subagent accepted outside workdir: %+v", got)
	}
}
