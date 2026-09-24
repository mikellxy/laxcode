package agentasm

import (
	"context"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/tools"
)

func TestAssembleRegistersSkillManagementTools(t *testing.T) {
	assembled, err := Assemble(context.Background(), Input{WorkDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer assembled.Cleanup()
	names := make(map[string]bool)
	for _, definition := range assembled.Service.ToolRegistry.GetAvailableTools() {
		names[definition.Name] = true
	}
	for _, name := range []string{tools.ToolCreateSkill, tools.ToolUpdateSkill} {
		if !names[name] {
			t.Errorf("assembled registry is missing %s", name)
		}
	}
}

func TestCustomPromptDoesNotExposeSkillManagementTools(t *testing.T) {
	assembled, err := Assemble(context.Background(), Input{
		WorkDir: t.TempDir(), HomeDir: t.TempDir(), SystemPrompt: "specialized evaluator",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer assembled.Cleanup()
	for _, definition := range assembled.Service.ToolRegistry.GetAvailableTools() {
		if definition.Name == tools.ToolCreateSkill || definition.Name == tools.ToolUpdateSkill {
			t.Fatalf("custom-prompt agent must not expose %s", definition.Name)
		}
	}
}
