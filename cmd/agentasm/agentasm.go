package agentasm

// Package agentasm assembles coding and read-only evaluation agents for the Web backend.

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/llmprovider"
	mcpserver "github.com/mikellxy/laxcode/internal/infrastructure/mcp"
	"github.com/mikellxy/laxcode/internal/infrastructure/ripgrep"
	"github.com/mikellxy/laxcode/internal/infrastructure/shell"
	"github.com/mikellxy/laxcode/internal/infrastructure/skillrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/skillstore"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
)

// Input 是装配 ReActService 所需、且因前端而异的输入。
type Input struct {
	// Tracer 由入口注入时，其生命周期归入口所有；nil 保留独立装配的默认追踪。
	Tracer telemetry.Tracer
	// MCPPool 由后端服务持有；非 nil 时仅注册工具，Cleanup 不关闭共享连接。
	// nil 保留独立装配时连接并自行回收的行为。
	MCPPool *mcpserver.Pool
	// Mode selects the explicit agent capability profile.
	Mode Mode
	// WorkDir 是会话持久绑定的 Agent 工作目录（沙箱根）。
	WorkDir string
	// HomeDir 是全局数据根的用户主目录；空值使用 os.UserHomeDir。
	// 测试可显式注入临时目录，避免触碰真实用户数据。
	HomeDir string
	// ReadRoots are additional absolute, read-only roots for command-specific
	// workflows such as evaluating one immutable session history.
	ReadRoots []string
	// SessionID 为空则由 session 层以毫秒精度时间串新建；非空则续聊该会话。
	SessionID string
	// PlanMode 为真时在系统提示词追加 Plan Mode 工作流段。
	PlanMode bool
	// SystemPrompt 非空时替换默认 coding-agent 系统提示词。评估等复用同一
	// ReActService、但职责不同的前端通过它注入专用角色；普通前端留空。
	SystemPrompt string
	// Consumer 是 ReAct 事件回调；留空时静默丢弃。
	Consumer func(*reactservice.ReactEvent)
}

type Mode string

const (
	ModeCode     Mode = "code"
	ModeEvaluate Mode = "evaluate"
)

type RouterClientReplacer interface {
	ReplaceClient(domainrouter.StreamClient)
}

// Assembled 是装配产物。
type Assembled struct {
	// Service 是已完成会话初始化的主 Agent 服务（已注册 bash/write/read/edit、
	// Skill 管理与子 Agent），前端直接调 Chat 发一轮对话。
	Service *reactservice.ReActService
	// Session 是 Service 持有的主会话，供前端读取 ID / token 统计。
	Session *session.Session
	// Cleanup 回收带生命周期的资源，调用方 defer 一次；以 sync.Once 保证幂等，
	// 使信号处理与正常退出路径可各自安全调用。顺序：先 Close 工具注册表（回收
	// bash 后台进程与临时文件），再 Shutdown tracer（flush 关闭阶段产生的 span）。
	Cleanup func()
}

// newMainProvider 按当前活跃模型构建主 provider：凭据取运行时配置（由
// config.SetActiveModel 维护），token 预算经 config.ActiveModelBudget 解析，
// 模型级 limit（limit.context / limit.output）优先，未声明时回退全局窗口
// 配置。
func newMainProvider() *llmprovider.OpenApiProvider {
	c := config.EnvAndFileConf
	contextWindow, maxOutput := config.ActiveModelBudget()
	return llmprovider.NewOpenApiProviderWithStreamGateway(
		c.OpenaiApiKey, c.OpenaiBaseUrl, c.OpenaiModel, c.LlmRouterURL,
		contextWindow, maxOutput)
}

// Assemble 装配一个可直接运行的 ReActService：会话（含系统提示词）、tracer、
// 工具集（含子 Agent）、LLM provider。OpenAI 凭据取自 config.EnvAndFileConf，
// 调用前须已由调用方校验（本函数不重复校验，缺失会在 Run 时才暴露）。
// SystemPrompt 为空时生成默认 coding-agent prompt，非空时原样采用调用方的专用
// prompt。返回的 error 仅来自会话初始化 / 系统提示词写入。
func Assemble(ctx context.Context, in Input) (*Assembled, error) {
	if in.Mode != ModeCode && in.Mode != ModeEvaluate {
		return nil, fmt.Errorf("unsupported agent mode %q", in.Mode)
	}
	if in.PlanMode && in.Mode != ModeCode {
		return nil, fmt.Errorf("plan mode requires code mode")
	}
	core, err := assembleCore(ctx, in.WorkDir, in.HomeDir, in.SessionID, in.Consumer, in.Mode == ModeCode, in.Tracer)
	if err != nil {
		return nil, err
	}
	homeDir, sess := core.homeDir, core.session
	sess.Mode = string(in.Mode)
	cleanup := core.cleanup
	var skills []prompt.Skill
	var skillSrc prompt.SkillSource
	var skillsRoot string
	readRoots := append([]string(nil), in.ReadRoots...)
	var writeRoots []string
	var plan *prompt.PlanMode
	if in.Mode == ModeCode {
		skillsStart := time.Now()
		skillSrc = skillrepo.New(homeDir)
		skills = prompt.LoadSkills(skillSrc, in.WorkDir, warnSkillSkip)
		skillsRoot = layout.SkillsRoot(homeDir)
		readRoots = append([]string{skillsRoot}, readRoots...)
		telemetry.SpanFromContext(ctx).SetAttributes(telemetry.AttrAssembleSkillsLoadMs.Float64(float64(time.Since(skillsStart)) / float64(time.Millisecond)))
	}
	if in.Mode == ModeCode && in.PlanMode {
		planDir := layout.SessionDir(homeDir, sess.ID)
		plan = &prompt.PlanMode{SessionDir: planDir}
		readRoots = append(readRoots, planDir)
		writeRoots = append(writeRoots, planDir)
	}
	// Evaluation receives read-only inspection tools; code receives the complete coding profile.
	var workFS tools.WorkFS = workfs.New()
	toolReg := core.registry
	ripgrepRunner := ripgrep.New()
	toolReg.Register(tools.NewReadFileTool(in.WorkDir, workFS, readRoots...))
	toolReg.Register(tools.NewGrepTool(in.WorkDir, ripgrepRunner, readRoots...))
	toolReg.Register(tools.NewGlobTool(in.WorkDir, ripgrepRunner, readRoots...))
	if in.Mode == ModeCode {
		shellRunner := shell.New()
		toolReg.Register(tools.NewBashTool(in.WorkDir, shellRunner, core.artifacts, sess.ID))
		toolReg.Register(tools.NewWriteFileTool(in.WorkDir, workFS, writeRoots...))
		toolReg.Register(tools.NewEditFileTool(in.WorkDir, workFS, writeRoots...))
		toolReg.Register(tools.NewReadArtifactTool(core.artifacts, sess.ID))
		skillStore := skillstore.New(skillsRoot)
		toolReg.Register(tools.NewCreateSkillTool(skillsRoot, skillStore))
		toolReg.Register(tools.NewUpdateSkillTool(skillsRoot, skillStore))
		toolReg.Register(reactservice.NewSubAgent(core.service, in.WorkDir,
			reactservice.SubAgentDeps{
				WorkFS:     workFS,
				Ripgrep:    ripgrepRunner,
				SkillSrc:   skillSrc,
				SkillsRoot: skillsRoot,
				// 每个子 Agent 各自新建：其 childReg.Close() 只回收自己派生的
				// 后台进程，不会波及主 Agent 尚在运行的后台服务
				NewShell: func() tools.ShellRunner { return shell.New() },
			}))
		// 后端注入共享连接，装配只向本 Agent 的 registry 注册工具。
		// 独立装配仍自行连接和回收；子 Agent 不接入 MCP 工具。
		mcpStart := time.Now()
		if in.MCPPool != nil {
			in.MCPPool.Register(toolReg)
			telemetry.SpanFromContext(ctx).SetAttributes(telemetry.AttrAssembleMCPRegisterMs.Float64(float64(time.Since(mcpStart)) / float64(time.Millisecond)))
		} else {
			mcpCleanup := attachMCPServers(ctx, toolReg)
			telemetry.SpanFromContext(ctx).SetAttributes(telemetry.AttrAssembleMCPConnectMs.Float64(float64(time.Since(mcpStart)) / float64(time.Millisecond)))
			if mcpCleanup != nil {
				baseCleanup := cleanup
				cleanup = func() { mcpCleanup(); baseCleanup() }
			}
		}
	}

	svc := core.service
	sessionStart := time.Now()
	err = svc.InitSession(ctx)
	telemetry.SpanFromContext(ctx).SetAttributes(telemetry.AttrAssembleSessionRestoreMs.Float64(float64(time.Since(sessionStart)) / float64(time.Millisecond)))
	if err != nil {
		cleanup()
		return nil, err
	}

	sysPromptStart := time.Now()
	var sysPrompt string
	switch in.Mode {
	case ModeCode:
		sysPrompt = in.SystemPrompt
		if sysPrompt == "" {
			sysPrompt = prompt.GetSysPrompt(in.WorkDir, skills, plan, skillsRoot)
		}
	case ModeEvaluate:
		if in.SystemPrompt == "" {
			telemetry.SpanFromContext(ctx).SetAttributes(telemetry.AttrAssembleSysPromptInitMs.Float64(float64(time.Since(sysPromptStart)) / float64(time.Millisecond)))
			cleanup()
			return nil, fmt.Errorf("evaluate mode requires a system prompt")
		}
		sysPrompt = in.SystemPrompt
	}
	err = svc.InitSysPrompt(ctx, sysPrompt)
	telemetry.SpanFromContext(ctx).SetAttributes(telemetry.AttrAssembleSysPromptInitMs.Float64(float64(time.Since(sysPromptStart)) / float64(time.Millisecond)))
	if err != nil {
		cleanup()
		return nil, err
	}

	return &Assembled{
		Service: svc,
		Session: sess,
		Cleanup: cleanup,
	}, nil
}

func resolveHomeDir(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return homeDir, nil
}

// warnSkillSkip 是技能跳过警告的落点：写 stderr 而非 stdout，避免污染交互
// 模式的输出，同时保留可诊断告警。
// 不得把它改成空实现：技能 frontmatter 解析失败将被静默后，模型侧表现为
// “技能没生效”而无任何线索。
func warnSkillSkip(msg string) {
	fmt.Fprintf(os.Stderr, "laxcode: %s\n", msg)
}

// attachMCPServers 连接 settings.json 声明的已启用 MCP server 并把其工具
// 注册进 reg，返回关闭连接的 cleanup；未配置或全部不可用时返回 nil。
// 连接/告警语义见 internal/infrastructure/mcp.Attach。
func attachMCPServers(ctx context.Context, reg tools.Registry) func() {
	servers := mcpServersFromConf()
	if len(servers) == 0 {
		return nil
	}
	return mcpserver.Attach(ctx, servers, reg, warnMCPSkip)
}

// ConnectMCPServers 在后端启动时连接当前配置的 MCP server；调用方持有并关闭 Pool。
// 即使没有可用 server 也返回非 nil 空池，避免请求装配时重复尝试连接。
func ConnectMCPServers(ctx context.Context) *mcpserver.Pool {
	return mcpserver.Connect(ctx, mcpServersFromConf(), warnMCPSkip)
}

// mcpServersFromConf 把配置层的 MCPServerConf 过滤为已启用条目并转成
// 传输层 ServerConfig；disabled 条目是用户意图，静默跳过。
func mcpServersFromConf() map[string]mcpserver.ServerConfig {
	conf := config.EnvAndFileConf.MCPServers
	if len(conf) == 0 {
		return nil
	}
	servers := make(map[string]mcpserver.ServerConfig, len(conf))
	for name, sc := range conf {
		if !sc.IsEnabled() {
			continue
		}
		servers[name] = mcpserver.ServerConfig{
			Command: sc.Command,
			Args:    append([]string(nil), sc.Args...),
			Env:     append([]string(nil), sc.Env...),
			URL:     sc.URL,
			Headers: cloneStringMap(sc.Headers),
		}
	}
	return servers
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// warnMCPSkip 与 warnSkillSkip 同语义：MCP server 接入失败/被跳过的告警
// 写 stderr，不污染任何模式的 stdout 契约。
func warnMCPSkip(msg string) {
	fmt.Fprintf(os.Stderr, "laxcode: %s\n", msg)
}

// newTraceHandle 在配置标准 OTLP endpoint 时使用批量 HTTP exporter，否则按
// logPath 构造默认 filetrace Provider；本地日志无法创建时回退 noop。
func newTraceHandle(ctx context.Context, logPath string) (*tracing.Handle, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		return tracing.NewOTLP(ctx)
	}

	f, err := filetrace.New(logPath)
	if err != nil {
		return tracing.New(nil), nil
	}
	return tracing.New(f), nil
}
