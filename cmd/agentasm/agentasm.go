package agentasm

// Package agentasm 是 cmd 层的组合根（composition root）：把交互模式
// （cmd/run_cli）、SSE 模式（cmd/run_sse）与评估模式（cmd/run_evaluate）共用的
// Agent 装配逻辑收口到 Assemble，消除重复。装配产物
// 是一个可直接 Run 的 ReActService 及其会话与清理钩子；各端的输入解析、校验、
// 事件呈现与主循环仍留在前端。
//
// 之所以独立成包而非放进 cmd/main：main 是 package main，不可被导入，且它已
// import 两个前端，反向依赖会成环。

import (
	"context"
	"fmt"
	"os"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/llmprovider"
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
	// Mode selects the explicit agent capability profile.
	Mode Mode
	// KBPath is required by ModeRAG.
	KBPath string
	// WorkDir 是 Agent 工作目录（沙箱根）：交互模式取 cwd，服务或评估模式取显式配置。
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
	// Consumer 是 ReAct 事件回调；交互模式用它渲染事件，留空时静默丢弃。
	Consumer func(*reactservice.ReactEvent)
	// Router 仅交互模式注入，用于 /model 在两轮 Chat 之间替换本地网关 client。
	Router RouterClientReplacer
}

type Mode string

const (
	ModeCode     Mode = "code"
	ModeRAG      Mode = "rag"
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
	// Skills 是启动时已校验的技能快照，供 CLI 补全与显式技能调用使用。
	Skills []prompt.Skill
	// Cleanup 回收带生命周期的资源，调用方 defer 一次；以 sync.Once 保证幂等，
	// 使信号处理与正常退出路径可各自安全调用。顺序：先 Close 工具注册表（回收
	// bash 后台进程与临时文件），再 Shutdown tracer（flush 关闭阶段产生的 span）。
	Cleanup func()

	// Switcher 供交互模式在两轮 Chat 之间切换模型。
	Switcher *ModelSwitcher
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
	if in.Mode != ModeCode && in.Mode != ModeRAG && in.Mode != ModeEvaluate {
		return nil, fmt.Errorf("unsupported agent mode %q", in.Mode)
	}
	if in.PlanMode && in.Mode != ModeCode {
		return nil, fmt.Errorf("plan mode requires code mode")
	}
	core, err := assembleCore(ctx, in.WorkDir, in.HomeDir, in.SessionID, in.Consumer, in.Mode == ModeCode)
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
		skillSrc = skillrepo.New(homeDir)
		skills = prompt.LoadSkills(skillSrc, in.WorkDir, warnSkillSkip)
		skillsRoot = layout.SkillsRoot(homeDir)
		readRoots = append([]string{skillsRoot}, readRoots...)
	}
	if in.Mode == ModeCode && in.PlanMode {
		planDir := layout.SessionDir(homeDir, sess.ID)
		plan = &prompt.PlanMode{SessionDir: planDir}
		readRoots = append(readRoots, planDir)
		writeRoots = append(writeRoots, planDir)
	}
	// Tool registration is owned entirely by this composition root. RAG exposes
	// none; evaluation receives read-only inspection tools; code receives the
	// complete coding profile.
	var workFS tools.WorkFS = workfs.New()
	toolReg := core.registry
	var ripgrepRunner *ripgrep.Runner
	if in.Mode == ModeCode || in.Mode == ModeEvaluate {
		ripgrepRunner = ripgrep.New()
		toolReg.Register(tools.NewReadFileTool(in.WorkDir, workFS, readRoots...))
		toolReg.Register(tools.NewGrepTool(in.WorkDir, ripgrepRunner, readRoots...))
		toolReg.Register(tools.NewGlobTool(in.WorkDir, ripgrepRunner, readRoots...))
	}
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
	}

	svc := core.service
	if err := svc.InitSession(ctx); err != nil {
		cleanup()
		return nil, err
	}

	var sysPrompt string
	switch in.Mode {
	case ModeCode:
		sysPrompt = in.SystemPrompt
		if sysPrompt == "" {
			sysPrompt = prompt.GetSysPrompt(in.WorkDir, skills, plan, skillsRoot)
		}
	case ModeEvaluate:
		if in.SystemPrompt == "" {
			cleanup()
			return nil, fmt.Errorf("evaluate mode requires a system prompt")
		}
		sysPrompt = in.SystemPrompt
	case ModeRAG:
		var ragCleanup func()
		ragCleanup, err = configureRAG(svc, core.tracer, in.KBPath)
		if err != nil {
			cleanup()
			return nil, err
		}
		baseCleanup := cleanup
		cleanup = func() { ragCleanup(); baseCleanup() }
		sysPrompt = prompt.GetRAGSysPrompt()
	}
	if err := svc.InitSysPrompt(ctx, sysPrompt); err != nil {
		cleanup()
		return nil, err
	}

	return &Assembled{
		Service: svc,
		Session: sess,
		Skills:  append([]prompt.Skill(nil), skills...),
		Cleanup: cleanup,
		Switcher: func() *ModelSwitcher {
			if in.Mode == ModeCode {
				return NewModelSwitcher(in.Router, svc)
			}
			return nil
		}(),
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

// warnSkillSkip 是技能跳过警告的落点：写 stderr 而非 stdout，使评估模式的
// stdout JSON 契约与交互模式的彩色输出都不被污染，警告仍可被用户看到。
// 不得把它改成空实现：技能 frontmatter 解析失败将被静默后，模型侧表现为
// “技能没生效”而无任何线索。
func warnSkillSkip(msg string) {
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
