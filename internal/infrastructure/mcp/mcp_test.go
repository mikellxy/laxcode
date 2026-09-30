package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newFakeServer 在进程内起一个走 InMemory 传输的 MCP server（真实 SDK 协议
// 栈，无子进程），返回客户端侧传输供 ServerConfig.transport 注入。
func newFakeServer(t *testing.T, addTools func(*mcpsdk.Server)) mcpsdk.Transport {
	t.Helper()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake-mcp", Version: "test"}, nil)
	if addTools != nil {
		addTools(server)
	}
	clientT, serverT := mcpsdk.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = server.Run(ctx, serverT) }()
	// Run 在 ctx 取消时自行 Close 会话并等待退出，cancel 即完成回收。
	t.Cleanup(cancel)
	return clientT
}

type echoArgs struct {
	Message string `json:"message"`
}

// addStandardTools 注册 echo / boom（IsError）/ repo.search（带点号名）三个
// 工具，覆盖成功、工具级错误与命名消毒三条路径。
func addStandardTools(server *mcpsdk.Server) {
	schema := func() map[string]any {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
			},
			"required": []string{"message"},
		}
	}
	server.AddTool(&mcpsdk.Tool{
		Name:        "echo",
		Description: "echo the message back",
		InputSchema: schema(),
	}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var args echoArgs
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo: " + args.Message}},
		}, nil
	})
	server.AddTool(&mcpsdk.Tool{
		Name:        "boom",
		Description: "always fails at the tool level",
		InputSchema: schema(),
	}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "kaboom"}},
			IsError: true,
		}, nil
	})
	server.AddTool(&mcpsdk.Tool{
		Name:        "repo.search",
		Description: "dotted tool name",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var args echoArgs
		_ = json.Unmarshal(req.Params.Arguments, &args)
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "searched: " + args.Message}},
		}, nil
	})
}

type warningCollector struct {
	mu      sync.Mutex
	message []string
}

func (w *warningCollector) warn(msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.message = append(w.message, msg)
}

func (w *warningCollector) joined() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.message, "\n")
}

func registryToolNames(reg *tools.DefaultRegistry) map[string]bool {
	names := make(map[string]bool)
	for _, def := range reg.GetAvailableTools() {
		names[def.Name] = true
	}
	return names
}

// TestAttachRegistersAndExecutes 覆盖全链路：InMemory 连接 → tools/list →
// 命名空间注册 → 经 DefaultRegistry 调用（成功、工具级错误、点号名消毒）。
func TestAttachRegistersAndExecutes(t *testing.T) {
	transport := newFakeServer(t, addStandardTools)
	reg := tools.NewDefaultRegistry(nil)
	warnings := &warningCollector{}

	cleanup := Attach(context.Background(), map[string]ServerConfig{
		"fake": {transport: transport},
	}, reg, warnings.warn)
	if cleanup == nil {
		t.Fatal("Attach must return a cleanup when a server is live")
	}
	defer cleanup()

	names := registryToolNames(reg)
	for _, want := range []string{"mcp__fake__echo", "mcp__fake__boom", "mcp__fake__repo_search"} {
		if !names[want] {
			t.Errorf("registry must expose %q, got %v", want, names)
		}
	}
	if warnings.joined() != "" {
		t.Errorf("healthy server must not warn, got %q", warnings.joined())
	}

	// 描述与 schema 必须来自 server 声明，供模型理解工具。
	var echoDef *sharedkernel.ToolDefinition
	for _, def := range reg.GetAvailableTools() {
		if def.Name == "mcp__fake__echo" {
			echoDef = &def
		}
	}
	if echoDef == nil {
		t.Fatal("echo definition missing")
	}
	if echoDef.Description != "echo the message back" {
		t.Errorf("echo description = %q, want server-declared description", echoDef.Description)
	}
	if echoDef.Parameters["type"] != "object" {
		t.Errorf("echo parameters must pass the server schema through: %v", echoDef.Parameters)
	}

	result := reg.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "call-1", Name: "mcp__fake__echo",
		Arguments: json.RawMessage(`{"message":"hi"}`),
	})
	if result.IsError || result.Output != "echo: hi" {
		t.Errorf("echo result: IsError=%v output=%q", result.IsError, result.Output)
	}

	boom := reg.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "call-2", Name: "mcp__fake__boom",
		Arguments: json.RawMessage(`{"message":"x"}`),
	})
	if !boom.IsError {
		t.Error("boom must surface as an error result")
	}
	if !strings.Contains(boom.Output, "kaboom") {
		t.Errorf("boom output must retain the server content for self-healing, got %q", boom.Output)
	}

	// 注册名消毒为 repo_search，调用仍按 server 原名 repo.search 路由。
	dotted := reg.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "call-3", Name: "mcp__fake__repo_search",
		Arguments: json.RawMessage(`{"message":"query"}`),
	})
	if dotted.IsError || dotted.Output != "searched: query" {
		t.Errorf("dotted-name result: IsError=%v output=%q", dotted.IsError, dotted.Output)
	}
}

// TestAttachFailsOpen 验证坏 server 不阻塞装配：命令不存在时跳过并告警，
// 其余 server 正常接入。
func TestAttachFailsOpen(t *testing.T) {
	transport := newFakeServer(t, addStandardTools)
	reg := tools.NewDefaultRegistry(nil)
	warnings := &warningCollector{}

	cleanup := Attach(context.Background(), map[string]ServerConfig{
		"broken": {Command: "/nonexistent/laxcode-mcp-test"},
		"fake":   {transport: transport},
	}, reg, warnings.warn)
	if cleanup == nil {
		t.Fatal("one live server must still attach")
	}
	defer cleanup()

	if !strings.Contains(warnings.joined(), `mcp server "broken" unavailable`) {
		t.Errorf("broken server must warn, got %q", warnings.joined())
	}
	names := registryToolNames(reg)
	if names["mcp__broken__anything"] {
		t.Error("broken server must not register tools")
	}
	if !names["mcp__fake__echo"] {
		t.Error("healthy server must still register tools")
	}
}

// TestStreamableHTTPEndToEnd 覆盖远程 Streamable HTTP：自定义鉴权 header
// 必须出现在握手、tools/list 和 tools/call 的全部请求上。
func TestStreamableHTTPEndToEnd(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "http-fake", Version: "test"}, nil)
	addStandardTools(server)
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	reg := tools.NewDefaultRegistry(nil)
	warnings := &warningCollector{}
	cleanup := Attach(context.Background(), map[string]ServerConfig{
		"remote": {
			URL:     httpServer.URL,
			Headers: map[string]string{"Authorization": "Bearer test-token"},
		},
	}, reg, warnings.warn)
	if cleanup == nil {
		t.Fatalf("Streamable HTTP server must attach; warnings: %s", warnings.joined())
	}
	defer cleanup()
	if warnings.joined() != "" {
		t.Fatalf("healthy HTTP server must not warn, got %q", warnings.joined())
	}

	result := reg.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "http-call", Name: "mcp__remote__echo",
		Arguments: json.RawMessage(`{"message":"over http"}`),
	})
	if result.IsError || result.Output != "echo: over http" {
		t.Fatalf("HTTP echo: IsError=%v output=%q", result.IsError, result.Output)
	}
}

// TestAttachNoToolsSkipped：连接成功但零工具的 server 不进池。
func TestAttachNoToolsSkipped(t *testing.T) {
	transport := newFakeServer(t, nil)
	reg := tools.NewDefaultRegistry(nil)
	warnings := &warningCollector{}

	cleanup := Attach(context.Background(), map[string]ServerConfig{
		"empty": {transport: transport},
	}, reg, warnings.warn)
	if cleanup != nil {
		t.Fatal("a server without tools must yield a nil cleanup")
	}
	if !strings.Contains(warnings.joined(), "exposes no tools") {
		t.Errorf("empty server must warn, got %q", warnings.joined())
	}
}

// TestPoolCloseIdempotentShape：cleanup 后再次调用不 panic（幂等由调用方
// defer 语义与 sync.Once 保证，这里只约束 Close 本身可安全重复）。
func TestPoolCloseAfterAttach(t *testing.T) {
	transport := newFakeServer(t, addStandardTools)
	reg := tools.NewDefaultRegistry(nil)

	cleanup := Attach(context.Background(), map[string]ServerConfig{
		"fake": {transport: transport},
	}, reg, nil)
	if cleanup == nil {
		t.Fatal("attach expected")
	}
	cleanup()
	// 关闭后调用应失败（session 已断），且不 panic。
	result := reg.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "call-x", Name: "mcp__fake__echo",
		Arguments: json.RawMessage(`{"message":"late"}`),
	})
	if !result.IsError {
		t.Error("executing after cleanup must surface an error")
	}
}

// TestMCPServerHelperProcess 不是常规测试：由 TestStdioEndToEnd 以子进程
// 方式重新执行测试二进制充当 stdio MCP server。直接运行时立即跳过。
// server.Run 在客户端关闭连接后返回；os.Exit 抑制测试框架的 PASS 输出，
// 避免污染 stdout（协议通道）。
func TestMCPServerHelperProcess(t *testing.T) {
	if os.Getenv("LAXCODE_MCP_HELPER") != "1" {
		t.Skip("helper process only; re-executed by TestStdioEndToEnd")
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stdio-fake", Version: "test"}, nil)
	addStandardTools(server)
	// env_report 回显环境变量，验证 ServerConfig.Env 真实透传到子进程。
	server.AddTool(&mcpsdk.Tool{
		Name:        "env_report",
		Description: "report helper env",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{
				Text: "extra=" + os.Getenv("LAXCODE_MCP_EXTRA"),
			}},
		}, nil
	})
	_ = server.Run(context.Background(), &mcpsdk.StdioTransport{})
	os.Exit(0)
}

// TestStdioEndToEnd 覆盖真实 stdio 路径：spawn 子进程（re-exec 测试二进制
// 充当 MCP server）→ initialize 握手 → tools/list → 注册 → 经管道调用工具
// → env 透传 → cleanup 终止子进程。InMemory 测试不经过 CommandTransport，
// 本测试补齐进程级链路。
func TestStdioEndToEnd(t *testing.T) {
	reg := tools.NewDefaultRegistry(nil)
	cleanup := Attach(context.Background(), map[string]ServerConfig{
		"stdio": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestMCPServerHelperProcess"},
			Env:     []string{"LAXCODE_MCP_HELPER=1", "LAXCODE_MCP_EXTRA=42"},
		},
	}, reg, nil)
	if cleanup == nil {
		t.Fatal("stdio server must attach")
	}
	defer cleanup()

	names := registryToolNames(reg)
	if !names["mcp__stdio__echo"] || !names["mcp__stdio__env_report"] {
		t.Fatalf("stdio server tools missing, got %v", names)
	}

	result := reg.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "call-1", Name: "mcp__stdio__echo",
		Arguments: json.RawMessage(`{"message":"hi"}`),
	})
	if result.IsError || result.Output != "echo: hi" {
		t.Errorf("stdio echo: IsError=%v output=%q", result.IsError, result.Output)
	}

	envResult := reg.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "call-2", Name: "mcp__stdio__env_report",
		Arguments: json.RawMessage(`{}`),
	})
	if envResult.IsError || envResult.Output != "extra=42" {
		t.Errorf("env passthrough: IsError=%v output=%q", envResult.IsError, envResult.Output)
	}
}
