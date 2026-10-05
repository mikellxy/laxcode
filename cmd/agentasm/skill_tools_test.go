package agentasm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
)

func TestAssembleRegistersSkillManagementTools(t *testing.T) {
	assembled, err := Assemble(context.Background(), Input{Mode: ModeCode, WorkDir: t.TempDir(), HomeDir: t.TempDir()})
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

func TestAssembleDoesNotCloseInjectedTracer(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tracing.log")
	provider, err := filetrace.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	handle := tracing.New(provider)
	defer handle.Shutdown(context.Background())
	ctx, root := handle.Tracer.Start(context.Background(), "request-after-cleanup")
	assembled, err := Assemble(ctx, Input{Mode: ModeCode, WorkDir: t.TempDir(), HomeDir: t.TempDir(), Tracer: handle.Tracer})
	if err != nil {
		t.Fatal(err)
	}
	assembled.Cleanup()
	assembled.Cleanup()
	root.End()
	data, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(data), `"name":"request-after-cleanup"`) {
		t.Fatalf("injected tracer was closed before request span ended: %s, %v", data, err)
	}
}

func TestEvaluatorExposesOnlyReadOnlyInspectionTools(t *testing.T) {
	assembled, err := Assemble(context.Background(), Input{
		Mode: ModeEvaluate, WorkDir: t.TempDir(), HomeDir: t.TempDir(), SystemPrompt: "specialized evaluator",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer assembled.Cleanup()
	definitions := assembled.Service.ToolRegistry.GetAvailableTools()
	if len(definitions) != 3 {
		t.Fatalf("evaluator tools = %+v", definitions)
	}
	for _, definition := range definitions {
		switch definition.Name {
		case tools.ToolReadFile, tools.ToolGrep, tools.ToolGlob:
		default:
			t.Fatalf("evaluator exposes non-inspection tool %s", definition.Name)
		}
	}
}
