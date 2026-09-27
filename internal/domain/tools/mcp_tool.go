package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// MCP 工具接入的领域侧适配器：把一个外部 MCP（Model Context Protocol）
// server 暴露的工具包装为 BaseTool，融入现有注册表与 ReAct 循环。
// SDK/传输相关的连接管理在 internal/infrastructure/mcp，经 MCPClient 端口
// 注入，本包不依赖任何 MCP 协议实现。

const (
	// mcpToolPrefix 是注册进 registry 的 MCP 工具名前缀。内置工具名
	// （bash/read_file 等）不带前缀，天然不会与 MCP 工具冲突；不同 server
	// 的同名工具也由 server 段隔离。
	mcpToolPrefix = "mcp__"
	// mcpMaxToolName 对齐 OpenAI function name 的长度上限（64），超长截断。
	// 截断可能引入极端场景下的同名覆盖（DefaultRegistry.Register 同名静默
	// 覆盖），可接受的 P1 权衡。
	mcpMaxToolName = 64
)

// MCPToolInfo 是从已连接 MCP server 的 tools/list 快照取出的一条工具元数据。
type MCPToolInfo struct {
	// Name 是 server 侧声明的原始工具名（未消毒），调用时原样回传。
	Name string
	// Description 是 server 提供的工具描述，原样透传给模型。
	Description string
	// InputSchema 是工具入参的 JSON Schema 对象，直接作为
	// sharedkernel.ToolDefinition.Parameters 透传给 LLM。
	InputSchema map[string]any
}

// MCPToolOutput 是一次 MCP 工具调用的领域级结果。
type MCPToolOutput struct {
	// Text 是 content 文本块拼接出的正文（P1 不支持图片等富内容，非文本
	// 块由基础设施层降级为占位说明）。
	Text string
	// IsError 为真表示 server 在 content 中报告了工具级错误：MCP 规范要求
	// 此类错误对模型可见以便自纠，因此适配层会把它转换为 Go error 走
	// registry 的自愈包装，同时保留原始文本。
	IsError bool
}

// MCPClient 是面向单个已连接 MCP server 的传输端口，由基础设施层
// （internal/infrastructure/mcp）实现。toolName 是 server 侧原始工具名。
type MCPClient interface {
	CallTool(ctx context.Context, toolName string, args json.RawMessage) (MCPToolOutput, error)
}

// MCPTool 把单个 MCP server 工具适配为 BaseTool。它是薄代理：定义来自
// server 快照，Execute 转发原始 JSON 参数并拼接文本结果。
type MCPTool struct {
	server string
	info   MCPToolInfo
	client MCPClient
}

// NewMCPTool 构造适配器。serverName 仅用于工具名命名空间与错误信息；
// client 必须非 nil 且生命周期覆盖本工具（连接由调用方统一回收，本工具
// 不实现 Closer）。
func NewMCPTool(serverName string, info MCPToolInfo, client MCPClient) *MCPTool {
	return &MCPTool{server: serverName, info: info, client: client}
}

// MCPToolName 生成注册名：mcp__<server>__<tool>。两段都消毒为
// [a-zA-Z0-9_-]（OpenAI function name 字符集，MCP 工具名常见点号会被
// 替换为下划线），总长截断到 64。
func MCPToolName(serverName, toolName string) string {
	name := mcpToolPrefix + sanitizeMCPNameSegment(serverName) + "__" + sanitizeMCPNameSegment(toolName)
	if len(name) > mcpMaxToolName {
		name = name[:mcpMaxToolName]
	}
	return name
}

func sanitizeMCPNameSegment(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (t *MCPTool) Name() string { return MCPToolName(t.server, t.info.Name) }

func (t *MCPTool) Definition() sharedkernel.ToolDefinition {
	params := t.info.InputSchema
	if len(params) == 0 {
		// 防御：schema 缺失时给出最小合法对象 schema，保证下游
		// ToolParamOfFunction 序列化不因 nil/空 schema 失败。
		params = map[string]any{"type": "object"}
	}
	return sharedkernel.ToolDefinition{
		Name:        t.Name(),
		Description: t.info.Description,
		Parameters:  params,
	}
}

func (t *MCPTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// 模型可能产出空参数；SDK 对 nil 参数会兜底为空对象，这里显式归一。
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	out, err := t.client.CallTool(ctx, t.info.Name, args)
	if err != nil {
		return "", fmt.Errorf("mcp tool %s.%s failed: %w", t.server, t.info.Name, err)
	}
	if out.IsError {
		// 返回 (文本, error) 双通道：registry 的 buildToolResultContent 会把
		// 错误说明与原始输出一起回写给模型，符合 MCP「工具级错误须对模型
		// 可见以自纠」的规范意图。
		return out.Text, fmt.Errorf("mcp tool %s.%s reported an error", t.server, t.info.Name)
	}
	return out.Text, nil
}

func (t *MCPTool) BeforeExecInfo(json.RawMessage) string { return t.Name() + "()" }
func (t *MCPTool) AfterExecInfo(json.RawMessage) string  { return "" }
