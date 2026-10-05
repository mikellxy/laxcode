package agentasm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
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

func TestAssemblePhaseDurations(t *testing.T) {
	// 使用真实 SQLite/技能恢复路径，避免连接用户配置的外部 MCP 服务。
	previousServers := config.EnvAndFileConf.MCPServers
	config.EnvAndFileConf.MCPServers = nil
	t.Cleanup(func() { config.EnvAndFileConf.MCPServers = previousServers })
	const prefix = "laxcode.assemble."
	for _, tc := range []struct {
		name        string
		mode        Mode
		failRepo    bool
		failRestore bool
		failPrompt  bool
		phases      []string
	}{
		{name: "code", mode: ModeCode, phases: []string{"repo_init_ms", "skills_load_ms", "mcp_connect_ms", "session_restore_ms", "sys_prompt_init_ms"}},
		{name: "evaluate", mode: ModeEvaluate, phases: []string{"repo_init_ms", "session_restore_ms", "sys_prompt_init_ms"}},
		{name: "repo failure", mode: ModeCode, failRepo: true, phases: []string{"repo_init_ms"}},
		{name: "restore failure", mode: ModeCode, failRestore: true, phases: []string{"repo_init_ms", "skills_load_ms", "mcp_connect_ms", "session_restore_ms"}},
		{name: "prompt failure", mode: ModeEvaluate, failPrompt: true, phases: []string{"repo_init_ms", "session_restore_ms", "sys_prompt_init_ms"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "tracing.log")
			provider, err := filetrace.New(logPath)
			if err != nil {
				t.Fatal(err)
			}
			handle := tracing.New(provider)
			defer handle.Shutdown(context.Background())
			ctx, span := telemetry.Start(context.Background(), handle.Tracer, telemetry.SpanAgentAssemble)
			in := Input{Mode: tc.mode, WorkDir: t.TempDir(), HomeDir: t.TempDir(), Tracer: handle.Tracer, SessionID: "phase-test", SystemPrompt: "system"}
			if tc.failRepo {
				in.HomeDir = filepath.Join(in.HomeDir, "file")
				if err := os.WriteFile(in.HomeDir, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failRestore {
				in.SessionID = "../invalid"
			}
			if tc.failPrompt {
				in.SystemPrompt = ""
			}
			assembled, err := Assemble(ctx, in)
			if (err != nil) != (tc.failRepo || tc.failRestore || tc.failPrompt) {
				t.Fatalf("unexpected assembly result: %v", err)
			}
			if assembled != nil {
				assembled.Cleanup()
			}
			telemetry.CloseSpan(span, telemetry.WithErr(err))
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 1 {
				t.Fatalf("phase timing created extra spans: %s", raw)
			}
			var record struct {
				Name     string             `json:"name"`
				Duration float64            `json:"duration_ms"`
				Attrs    map[string]float64 `json:"attributes"`
			}
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			if record.Name != telemetry.SpanAgentAssemble || len(record.Attrs) != len(tc.phases) {
				t.Fatalf("unexpected phase attributes: %s", raw)
			}
			total := 0.0
			for _, phase := range tc.phases {
				ms, ok := record.Attrs[prefix+phase]
				if !ok || ms < 0 {
					t.Fatalf("missing or invalid duration %s: %s", phase, raw)
				}
				total += ms
			}
			if total > record.Duration {
				t.Fatalf("phase durations overlap: total=%f span=%f", total, record.Duration)
			}
		})
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
