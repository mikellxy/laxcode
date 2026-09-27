package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// fakeMCPClient 是 MCPClient 端口的测试替身，记录调用并返回预设结果。
type fakeMCPClient struct {
	gotName string
	gotArgs json.RawMessage
	output  MCPToolOutput
	err     error
}

func (f *fakeMCPClient) CallTool(_ context.Context, name string, args json.RawMessage) (MCPToolOutput, error) {
	f.gotName = name
	f.gotArgs = args
	return f.output, f.err
}

func TestMCPToolName(t *testing.T) {
	cases := []struct {
		server, tool, want string
	}{
		{"fs", "read_file", "mcp__fs__read_file"},
		// MCP 工具名常见点号（如 repo.search）消毒为下划线。
		{"my.server", "repo.search", "mcp__my_server__repo_search"},
		// 空格等非法字符同样替换。
		{"web api", "search+docs", "mcp__web_api__search_docs"},
	}
	for _, tc := range cases {
		if got := MCPToolName(tc.server, tc.tool); got != tc.want {
			t.Errorf("MCPToolName(%q, %q) = %q, want %q", tc.server, tc.tool, got, tc.want)
		}
	}
	if !strings.HasPrefix(MCPToolName("fs", "read_file"), mcpToolPrefix) {
		t.Errorf("MCP tool name must carry the %q prefix", mcpToolPrefix)
	}
	// 超长名截断到 64（OpenAI function name 上限），且消毒后为纯 ASCII，
	// 字节截断不会切坏多字节字符。
	long := MCPToolName(strings.Repeat("s", 40), strings.Repeat("t", 60))
	if len(long) != mcpMaxToolName {
		t.Errorf("long name length = %d, want %d", len(long), mcpMaxToolName)
	}
}

func TestMCPToolDefinition(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
		"required": []string{"query"},
	}
	tool := NewMCPTool("fs", MCPToolInfo{
		Name:        "search",
		Description: "search files",
		InputSchema: schema,
	}, &fakeMCPClient{})

	def := tool.Definition()
	if def.Name != "mcp__fs__search" {
		t.Errorf("definition name = %q, want mcp__fs__search", def.Name)
	}
	if def.Description != "search files" {
		t.Errorf("definition description = %q, want passthrough", def.Description)
	}
	if def.Parameters["type"] != "object" {
		t.Errorf("definition parameters must pass the server schema through: %v", def.Parameters)
	}
}

func TestMCPToolDefinitionDefaultsEmptySchema(t *testing.T) {
	tool := NewMCPTool("fs", MCPToolInfo{Name: "noop"}, &fakeMCPClient{})
	def := tool.Definition()
	if def.Parameters["type"] != "object" {
		t.Errorf("empty schema must fall back to a minimal object schema, got %v", def.Parameters)
	}
}

func TestMCPToolExecuteSuccess(t *testing.T) {
	client := &fakeMCPClient{output: MCPToolOutput{Text: "echo: hi"}}
	tool := NewMCPTool("fs", MCPToolInfo{Name: "echo", InputSchema: map[string]any{"type": "object"}}, client)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if out != "echo: hi" {
		t.Errorf("Execute output = %q, want %q", out, "echo: hi")
	}
	// server 侧原始工具名原样回传，注册名不泄漏到协议层。
	if client.gotName != "echo" {
		t.Errorf("client got name %q, want raw server tool name %q", client.gotName, "echo")
	}
	if string(client.gotArgs) != `{"message":"hi"}` {
		t.Errorf("client got args %s, want passthrough", client.gotArgs)
	}
}

func TestMCPToolExecuteNormalizesEmptyArgs(t *testing.T) {
	client := &fakeMCPClient{}
	tool := NewMCPTool("fs", MCPToolInfo{Name: "list"}, client)
	if _, err := tool.Execute(context.Background(), json.RawMessage("  ")); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if string(client.gotArgs) != "{}" {
		t.Errorf("empty args must normalize to {}, got %s", client.gotArgs)
	}
}

func TestMCPToolExecuteServerErrorResult(t *testing.T) {
	client := &fakeMCPClient{output: MCPToolOutput{Text: "kaboom", IsError: true}}
	tool := NewMCPTool("fs", MCPToolInfo{Name: "boom"}, client)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("IsError result must surface as a Go error")
	}
	// 双通道：错误给 registry 包装自愈提示，文本作为原始输出保留给模型。
	if out != "kaboom" {
		t.Errorf("error result text = %q, want %q", out, "kaboom")
	}
	if !strings.Contains(err.Error(), "fs.boom") {
		t.Errorf("error must identify server and tool, got %q", err.Error())
	}
}

func TestMCPToolExecuteTransportError(t *testing.T) {
	client := &fakeMCPClient{err: context.DeadlineExceeded}
	tool := NewMCPTool("fs", MCPToolInfo{Name: "slow"}, client)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("transport error must propagate, got out=%q err=%v", out, err)
	}
}

func TestMCPToolBeforeAfterExecInfo(t *testing.T) {
	tool := NewMCPTool("fs", MCPToolInfo{Name: "read"}, &fakeMCPClient{})
	if info := tool.BeforeExecInfo(nil); !strings.HasPrefix(info, "mcp__fs__read") {
		t.Errorf("BeforeExecInfo = %q, want the registered tool name", info)
	}
	if after := tool.AfterExecInfo(nil); after != "" {
		t.Errorf("AfterExecInfo = %q, want empty", after)
	}
}

// TestMCPToolRegistryIntegration 走 DefaultRegistry 全链路：注册、枚举与
// Execute（含错误结果的自愈包装），验证与 ReAct 循环消费路径的兼容性。
func TestMCPToolRegistryIntegration(t *testing.T) {
	reg := NewDefaultRegistry(nil)
	okClient := &fakeMCPClient{output: MCPToolOutput{Text: "ok"}}
	errClient := &fakeMCPClient{output: MCPToolOutput{Text: "raw failure detail", IsError: true}}
	reg.Register(NewMCPTool("fs", MCPToolInfo{
		Name:        "read",
		Description: "read a file",
		InputSchema: map[string]any{"type": "object"},
	}, okClient))
	reg.Register(NewMCPTool("db", MCPToolInfo{
		Name:        "query",
		Description: "run a query",
		InputSchema: map[string]any{"type": "object"},
	}, errClient))

	defs := reg.GetAvailableTools()
	names := make(map[string]bool)
	for _, def := range defs {
		names[def.Name] = true
	}
	if !names["mcp__fs__read"] || !names["mcp__db__query"] {
		t.Fatalf("registry must expose namespaced MCP tools, got %v", names)
	}

	result := reg.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "call-1", Name: "mcp__fs__read", Arguments: json.RawMessage(`{}`),
	})
	if result.IsError || result.Output != "ok" {
		t.Errorf("success path: IsError=%v output=%q", result.IsError, result.Output)
	}
	if result.ToolCallID != "call-1" {
		t.Errorf("ToolCallID = %q, want call-1", result.ToolCallID)
	}

	errResult := reg.Execute(context.Background(), &sharedkernel.ToolCall{
		ID: "call-2", Name: "mcp__db__query", Arguments: json.RawMessage(`{}`),
	})
	if !errResult.IsError {
		t.Error("IsError tool result must be flagged")
	}
	// buildToolResultContent 的约定：错误说明在前、原始输出附后，供模型自纠。
	if !strings.Contains(errResult.Output, "error executing tool") ||
		!strings.Contains(errResult.Output, "raw failure detail") {
		t.Errorf("error output must wrap error and raw content, got %q", errResult.Output)
	}
}
