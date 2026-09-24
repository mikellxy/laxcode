package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/skillstore"
)

func skillArgs(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return raw
}

func validSkill(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
}

func TestCreateAndUpdateSkillTools(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := skillstore.New(root)
	create := tools.NewCreateSkillTool(root, store)
	createRaw := skillArgs(t, map[string]any{
		"name":     "pdf-tools",
		"skill_md": validSkill("pdf-tools", "创建 PDF", "旧说明"),
		"supporting_files": []map[string]any{{
			"path": "scripts/render.sh", "content": "#!/bin/sh\necho ok\n", "executable": true,
		}},
	})
	confirmation, err := create.Confirmation(ctx, createRaw)
	if err != nil {
		t.Fatalf("create Confirmation: %v", err)
	}
	if confirmation.Kind != "skill_write" || !strings.Contains(confirmation.Content, "pdf-tools") {
		t.Fatalf("create confirmation = %+v", confirmation)
	}
	if _, err := os.Stat(filepath.Join(root, "pdf-tools")); !os.IsNotExist(err) {
		t.Fatalf("preflight mutated filesystem: %v", err)
	}
	out, err := create.Execute(ctx, createRaw)
	if err != nil {
		t.Fatalf("create Execute: %v", err)
	}
	if !strings.Contains(out, `"status":"created"`) || !strings.Contains(out, `"available_next_session":true`) {
		t.Fatalf("create output = %s", out)
	}
	if _, err := create.Execute(ctx, createRaw); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create error = %v", err)
	}

	update := tools.NewUpdateSkillTool(root, store)
	updateRaw := skillArgs(t, map[string]any{
		"name": "pdf-tools",
		"edits": []map[string]any{{
			"path": "SKILL.md",
			"replacements": []map[string]string{
				{"old_text": "description: 创建 PDF", "new_text": "description: 创建并验证 PDF"},
				{"old_text": "旧说明", "new_text": "新说明"},
			},
		}},
		"create_files": []map[string]any{{"path": "references/checks.md", "content": "# Checks\n"}},
	})
	confirmation, err = update.Confirmation(ctx, updateRaw)
	if err != nil {
		t.Fatalf("update Confirmation: %v", err)
	}
	if confirmation.Kind != "skill_write" || !strings.Contains(confirmation.Content, "精确替换：2 处") {
		t.Fatalf("update confirmation = %+v", confirmation)
	}
	out, err = update.Execute(ctx, updateRaw)
	if err != nil {
		t.Fatalf("update Execute: %v", err)
	}
	if !strings.Contains(out, `"status":"updated"`) {
		t.Fatalf("update output = %s", out)
	}
	snapshot, err := store.Load(ctx, "pdf-tools")
	if err != nil {
		t.Fatalf("load updated skill: %v", err)
	}
	contents := make(map[string]string)
	for _, file := range snapshot.Files {
		contents[file.Path] = string(file.Content)
	}
	if !strings.Contains(contents["SKILL.md"], "description: 创建并验证 PDF") || !strings.Contains(contents["SKILL.md"], "新说明") {
		t.Fatalf("updated SKILL.md = %q", contents["SKILL.md"])
	}
	if contents["references/checks.md"] != "# Checks\n" || contents["scripts/render.sh"] == "" {
		t.Fatalf("updated package contents = %+v", contents)
	}
}

func TestSkillToolsRejectInvalidPathsAndDefinitionsWithoutMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := skillstore.New(root)
	create := tools.NewCreateSkillTool(root, store)
	badPath := skillArgs(t, map[string]any{
		"name": "demo", "skill_md": validSkill("demo", "描述", "正文"),
		"supporting_files": []map[string]string{{"path": "../escape", "content": "x"}},
	})
	if _, err := create.Execute(ctx, badPath); err == nil {
		t.Fatal("create_skill must reject path traversal")
	}
	if _, err := store.Load(ctx, "demo"); err == nil {
		t.Fatal("invalid create persisted a skill")
	}

	good := skillArgs(t, map[string]any{"name": "demo", "skill_md": validSkill("demo", "描述", "正文")})
	if _, err := create.Execute(ctx, good); err != nil {
		t.Fatalf("seed skill: %v", err)
	}
	before, err := store.Load(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	update := tools.NewUpdateSkillTool(root, store)
	invalid := skillArgs(t, map[string]any{
		"name": "demo",
		"edits": []map[string]any{{
			"path":         "SKILL.md",
			"replacements": []map[string]string{{"old_text": "name: demo", "new_text": "name: other"}},
		}},
	})
	if _, err := update.Execute(ctx, invalid); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("invalid update error = %v", err)
	}
	after, err := store.Load(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Fatal("failed update changed the skill package")
	}
}
