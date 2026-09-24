package agentasm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func TestPlanModeWritesOnlyItsGlobalSessionDirectory(t *testing.T) {
	homeDir, workDir := t.TempDir(), t.TempDir()
	assembled, err := Assemble(context.Background(), Input{
		Mode: ModeCode, HomeDir: homeDir, WorkDir: workDir, SessionID: "plan-session", PlanMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer assembled.Cleanup()

	planPath := filepath.Join(layout.SessionDir(homeDir, assembled.Session.ID), "plan.md")
	args, _ := json.Marshal(map[string]string{"path": planPath, "content": "plan"})
	result := assembled.Service.ToolRegistry.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "write-plan", Name: tools.ToolWriteFile, Arguments: args,
	})
	if result.Error != nil {
		t.Fatalf("write plan file: %v", result.Error)
	}
	if data, err := os.ReadFile(planPath); err != nil || string(data) != "plan" {
		t.Fatalf("plan content=%q err=%v", data, err)
	}

	skillPath := filepath.Join(layout.SkillsRoot(homeDir), "demo", "SKILL.md")
	args, _ = json.Marshal(map[string]string{"path": skillPath, "content": "changed"})
	result = assembled.Service.ToolRegistry.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "write-skill", Name: tools.ToolWriteFile, Arguments: args,
	})
	if result.Error == nil {
		t.Fatal("Plan Mode must not make the global skills directory writable")
	}
}
