package agentasm

import (
	"context"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

// TestAssembleCodeModeMCPServerFailOpen 验证 code 模式装配会读取全局配置里的
// mcp_servers（接线生效），且坏 server 走 fail-open：装配照常成功、无 MCP
// 工具注册、cleanup 链可用。坏命令连接在 exec 启动即失败，测试不起真实进程。
func TestAssembleCodeModeMCPServerFailOpen(t *testing.T) {
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	config.EnvAndFileConf.MCPServers = map[string]config.MCPServerConf{
		"broken": {Command: "/nonexistent/laxcode-agentasm-mcp-test"},
	}

	assembled, err := Assemble(context.Background(), Input{Mode: ModeCode, WorkDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("broken MCP server must not break assembly: %v", err)
	}
	defer assembled.Cleanup()

	for _, definition := range assembled.Service.ToolRegistry.GetAvailableTools() {
		if strings.HasPrefix(definition.Name, "mcp__") {
			t.Errorf("unavailable server must not register tools, got %s", definition.Name)
		}
	}
}

// TestAssembleIgnoresDisabledMCPServers 验证 enabled=false 的 server 不参与
// 接线（静默跳过，不产生连接尝试）。
func TestAssembleIgnoresDisabledMCPServers(t *testing.T) {
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	disabled := false
	config.EnvAndFileConf.MCPServers = map[string]config.MCPServerConf{
		"paused": {Command: "/nonexistent/laxcode-agentasm-mcp-test", Enabled: &disabled},
	}

	assembled, err := Assemble(context.Background(), Input{Mode: ModeCode, WorkDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer assembled.Cleanup()
	for _, definition := range assembled.Service.ToolRegistry.GetAvailableTools() {
		if strings.HasPrefix(definition.Name, "mcp__paused__") {
			t.Errorf("disabled server must not register tools, got %s", definition.Name)
		}
	}
}
