// Package mcp 把外部 MCP（Model Context Protocol）server 接入 laxcode 工具
// 体系：为每个已配置的 stdio 或 Streamable HTTP server 建立连接、拉取
// tools/list 快照，并把每个工具经 domain/tools.MCPTool 适配器注册进工具注册表。
//
// 生命周期（P1）：连接随每次 Agent 装配建立、随装配 cleanup 终止。单个
// server 连接失败不阻塞装配（fail-open，warn 后跳过），由调用方决定警告
// 呈现方式。SDK 依赖收口在本包，domain/tools 只见 MCPClient 端口。
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	clientName    = "laxcode"
	clientVersion = "dev"
)

// 连接与单次工具调用的默认时限。connectTimeout 覆盖 spawn + initialize +
// tools/list 全程：装配发生在请求路径上（SSE 头已发出），坏配置必须快速
// 失败而不是拖住整个会话。callTimeout 防止单个 server 挂起阻塞 ReAct 轮。
// 以变量而非常量暴露，测试可缩短。
var (
	connectTimeout = 20 * time.Second
	callTimeout    = 120 * time.Second
)

// ServerConfig 声明一条 MCP server 连接：Command/Args/Env 使用 stdio，
// URL/Headers 使用 Streamable HTTP。两种形态互斥，由配置层负责校验。
type ServerConfig struct {
	Command string
	Args    []string
	// Env 是追加给子进程的环境变量（K=V，在 os.Environ 之上）。
	Env     []string
	URL     string
	Headers map[string]string

	// transport 覆盖派生出的传输实现：生产路径留 nil（按 Command 构建
	// stdio 传输），包内测试注入 InMemory 传输以不起真实进程。
	transport mcpsdk.Transport
}

// Pool 持有全部成功建立的 server 连接及其工具快照，非并发安全：连接与
// 注册都发生在装配期（单 goroutine），之后的调用经 serverConn 交给 SDK
// 的会话（SDK 内部按 JSON-RPC 请求串行化）。
type Pool struct {
	conns []*serverConn
}

// serverConn 是一条已初始化的 server 连接，实现 tools.MCPClient 端口。
type serverConn struct {
	name    string
	session *mcpsdk.ClientSession
	tools   []tools.MCPToolInfo
}

// Attach 是组合根入口：连接全部已启用的 server（fail-open），把发现的
// 工具注册进 reg，返回关闭全部连接的 cleanup；未配置或全部不可用时返回
// nil，调用方可无条件链式挂接。
func Attach(ctx context.Context, servers map[string]ServerConfig, reg tools.Registry, warn func(string)) func() {
	if len(servers) == 0 {
		return nil
	}
	pool := Connect(ctx, servers, warn)
	if len(pool.conns) == 0 {
		return nil
	}
	pool.Register(reg)
	return func() { _ = pool.Close() }
}

// Connect 逐个启动 server 并拉取工具快照。按 server 名排序保证装配行为
// 确定性（注册顺序、告警顺序稳定）。无法连接的 server 跳过并经 warn 告警。
func Connect(ctx context.Context, servers map[string]ServerConfig, warn func(string)) *Pool {
	if warn == nil {
		warn = func(string) {}
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	pool := &Pool{}
	for _, name := range names {
		startedAt := time.Now()
		conn, err := connectServer(ctx, name, servers[name])
		durationMs := float64(time.Since(startedAt)) / float64(time.Millisecond)
		status, level, toolCount := "connected", slog.LevelInfo, 0
		if err != nil {
			status, level = "failed", slog.LevelWarn
		} else {
			toolCount = len(conn.tools)
			if toolCount == 0 {
				status, level = "no_tools", slog.LevelWarn
			}
		}
		attrs := []any{"chat_id", telemetry.ChatIDFromContext(ctx), "server_name", name,
			"duration_ms", durationMs, "status", status, "tool_count", toolCount}
		if spanCtx := telemetry.SpanFromContext(ctx).SpanContext(); spanCtx.IsValid() {
			attrs = append(attrs, "trace_id", spanCtx.TraceID().String(), "span_id", spanCtx.SpanID().String())
		}
		if err != nil {
			attrs = append(attrs, "error", err.Error())
		}
		slog.Log(ctx, level, "mcp_connect", attrs...)
		if err != nil {
			warn(fmt.Sprintf("mcp server %q unavailable, skipped: %v", name, err))
			continue
		}
		if len(conn.tools) == 0 {
			// 连接成功但无工具（如未声明 tools capability）：保留连接没有
			// 意义，直接回收并以告警提示配置可能有误。
			warn(fmt.Sprintf("mcp server %q exposes no tools, skipped", name))
			_ = conn.session.Close()
			continue
		}
		pool.conns = append(pool.conns, conn)
	}
	return pool
}

// Register 把每条连接的工具快照逐个包装为 tools.MCPTool 注册进 reg。
// 工具名经 mcp__<server>__<tool> 命名空间化，同名冲突由 registry 的覆盖
// 语义兜底（见 MCPToolName 的截断说明）。
func (p *Pool) Register(reg tools.Registry) {
	for _, conn := range p.conns {
		for _, info := range conn.tools {
			reg.Register(tools.NewMCPTool(conn.name, info, conn))
		}
	}
}

// Close 终止全部连接。stdio 由 SDK 负责回收子进程；Streamable HTTP 会
// 取消 SSE 接收，并在有 session ID 时发送尽力而为的 DELETE。
func (p *Pool) Close() error {
	var errs []error
	for _, conn := range p.conns {
		if err := conn.session.Close(); err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: %w", conn.name, err))
		}
	}
	return errors.Join(errs...)
}

func connectServer(ctx context.Context, name string, cfg ServerConfig) (*serverConn, error) {
	transport := cfg.transport
	if transport == nil {
		if strings.TrimSpace(cfg.URL) != "" {
			httpTransport, err := newStreamableHTTPTransport(cfg.URL, cfg.Headers)
			if err != nil {
				return nil, err
			}
			transport = httpTransport
		} else if strings.TrimSpace(cfg.Command) == "" {
			return nil, errors.New("command is required")
		} else {
			cmd := exec.Command(cfg.Command, cfg.Args...)
			cmd.Env = append(os.Environ(), cfg.Env...)
			transport = &mcpsdk.CommandTransport{Command: cmd}
		}
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: clientName, Version: clientVersion}, nil)
	// 连接期 ctx 只约束 spawn/initialize/tools/list；会话本身独立于该 ctx
	// 存活，后续 CallTool 使用各自的请求 ctx。
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	conn := &serverConn{name: name, session: session}
	if err := conn.loadTools(connectCtx); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return conn, nil
}

// newStreamableHTTPTransport 构造 MCP Streamable HTTP 客户端。认证等静态
// header 由 RoundTripper 注入所有协议请求（initialize、tools/list、POST
// 调用、独立 SSE GET 与关闭 DELETE）。跨源重定向不会携带这些 header。
func newStreamableHTTPTransport(endpoint string, headers map[string]string) (mcpsdk.Transport, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid streamable HTTP endpoint %q", endpoint)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported streamable HTTP scheme %q", parsed.Scheme)
	}

	transport := &mcpsdk.StreamableClientTransport{Endpoint: endpoint}
	if len(headers) > 0 {
		transport.HTTPClient = &http.Client{Transport: &headerRoundTripper{
			base:    http.DefaultTransport,
			scheme:  parsed.Scheme,
			host:    parsed.Host,
			headers: cloneHeaders(headers),
		}}
	}
	return transport, nil
}

type headerRoundTripper struct {
	base    http.RoundTripper
	scheme  string
	host    string
	headers http.Header
}

func cloneHeaders(src map[string]string) http.Header {
	dst := make(http.Header, len(src))
	for key, value := range src {
		dst.Set(key, value)
	}
	return dst
}

func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	// 不把 Authorization 等敏感 header 注入跨源重定向请求。
	if req.URL.Scheme == t.scheme && req.URL.Host == t.host {
		for key, values := range t.headers {
			cloned.Header[key] = append([]string(nil), values...)
		}
	}
	return t.base.RoundTrip(cloned)
}

// loadTools 拉取工具快照。SDK 的 Tools 迭代器内部处理分页。
func (c *serverConn) loadTools(ctx context.Context) error {
	if c.session.InitializeResult().Capabilities.Tools == nil {
		return nil
	}
	for tool, err := range c.session.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		if tool == nil {
			continue
		}
		c.tools = append(c.tools, tools.MCPToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: normalizeInputSchema(tool.InputSchema),
		})
	}
	return nil
}

// CallTool 实现 tools.MCPClient 端口：转发原始 JSON 参数，受 callTimeout
// 约束，把 content 块降级拼接为文本。
func (c *serverConn) CallTool(ctx context.Context, toolName string, args json.RawMessage) (tools.MCPToolOutput, error) {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	// SDK 会对 nil 参数兜底为空对象，这里一并归一空白参数。
	var arguments any = map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		arguments = args
	}
	res, err := c.session.CallTool(callCtx, &mcpsdk.CallToolParams{Name: toolName, Arguments: arguments})
	if err != nil {
		return tools.MCPToolOutput{}, err
	}
	return tools.MCPToolOutput{Text: contentText(res), IsError: res.IsError}, nil
}

// contentText 把 CallToolResult 的内容降级为模型可读文本：文本块直接拼接，
// 图片/资源等富内容以占位说明保留（P1 不支持富内容回传模型）。
func contentText(res *mcpsdk.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		switch v := c.(type) {
		case *mcpsdk.TextContent:
			sb.WriteString(v.Text)
		case *mcpsdk.ImageContent:
			fmt.Fprintf(&sb, "[mcp image content: %s, %d bytes]", v.MIMEType, len(v.Data))
		case *mcpsdk.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				sb.WriteString(v.Resource.Text)
			} else if v.Resource != nil {
				fmt.Fprintf(&sb, "[mcp resource: %s]", v.Resource.URI)
			}
		default:
			// 未知内容类型（新协议版本）：尽力 JSON 序列化，避免静默丢失。
			if raw, err := json.Marshal(c); err == nil {
				sb.Write(raw)
			}
		}
	}
	// 无文本内容但带结构化输出时，序列化为 JSON 文本，保证模型可见。
	if sb.Len() == 0 && res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			sb.Write(raw)
		}
	}
	return sb.String()
}

// normalizeInputSchema 把 SDK 侧 any 类型的 inputSchema 归一为
// map[string]any：客户端视角通常是 map；其他形态（如 RawMessage）经 JSON
// 往返转换；缺失/非对象时回退最小对象 schema。
func normalizeInputSchema(schema any) map[string]any {
	switch s := schema.(type) {
	case nil:
	case map[string]any:
		if len(s) > 0 {
			return s
		}
	default:
		if raw, err := json.Marshal(s); err == nil {
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil && len(m) > 0 {
				return m
			}
		}
	}
	return map[string]any{"type": "object"}
}
