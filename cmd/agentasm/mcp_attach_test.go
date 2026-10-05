package agentasm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAssembleReusesServiceMCPPool(t *testing.T) {
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "shared", Version: "test"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "alive"}}}, nil
		})
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, nil))
	defer httpServer.Close()
	config.EnvAndFileConf.MCPServers = map[string]config.MCPServerConf{"shared": {URL: httpServer.URL}}
	connectCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := ConnectMCPServers(connectCtx)
	defer pool.Close()
	cancel() // 连接初始化 context 结束，不得影响后续请求。
	// 请求不得重新读取配置并连接；只使用服务启动时已建立的 Pool。
	config.EnvAndFileConf.MCPServers = nil
	home, work := t.TempDir(), t.TempDir()
	var previousRegistry tools.Registry
	for i := range 2 {
		assembled, err := Assemble(context.Background(), Input{
			Mode: ModeCode, HomeDir: home, WorkDir: work, SessionID: fmt.Sprintf("shared-%d", i), MCPPool: pool,
		})
		if err != nil {
			t.Fatal(err)
		}
		reg := assembled.Service.ToolRegistry
		if reg == previousRegistry {
			t.Fatal("agents must have independent registries")
		}
		previousRegistry = reg
		assembled.Cleanup()
		result := reg.Execute(context.Background(), &sharedkernel.ToolCall{
			ID: "echo", Name: "mcp__shared__echo", Arguments: json.RawMessage(`{}`),
		})
		if result.IsError || result.Output != "alive" {
			t.Fatalf("request cleanup closed shared MCP session: %+v", result)
		}
	}
	if _, err := Assemble(context.Background(), Input{
		Mode: ModeCode, HomeDir: home, WorkDir: work, SessionID: "../invalid", MCPPool: pool,
	}); err == nil {
		t.Fatal("invalid session must fail assembly")
	}
	result := previousRegistry.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "after-failure", Name: "mcp__shared__echo", Arguments: json.RawMessage(`{}`),
	})
	if result.IsError {
		t.Fatalf("failed assembly closed shared session: %+v", result)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	result = previousRegistry.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "after-shutdown", Name: "mcp__shared__echo", Arguments: json.RawMessage(`{}`),
	})
	if !result.IsError {
		t.Fatal("service shutdown must close shared connections")
	}
}

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

func TestMCPServersFromConfCopiesHTTPHeaders(t *testing.T) {
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	config.EnvAndFileConf.MCPServers = map[string]config.MCPServerConf{
		"remote": {
			URL:     "https://mcp.example.com/mcp",
			Headers: map[string]string{"Authorization": "Bearer token"},
		},
	}

	servers := mcpServersFromConf()
	got := servers["remote"]
	if got.URL != "https://mcp.example.com/mcp" || got.Headers["Authorization"] != "Bearer token" {
		t.Fatalf("HTTP server config = %+v", got)
	}
	// 转换结果不能与全局配置共享可变 header map。
	got.Headers["Authorization"] = "changed"
	if config.EnvAndFileConf.MCPServers["remote"].Headers["Authorization"] != "Bearer token" {
		t.Fatal("HTTP headers were not defensively copied")
	}
}
