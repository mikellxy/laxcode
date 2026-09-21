package agentasm

// Package agentasm 是 cmd 层的组合根（composition root）：把交互模式
// （cmd/run_cli）、one-shot 模式（cmd/run_oneshot）与评估模式
// （cmd/run_evaluate）共用的 Agent 装配逻辑收口到 Assemble，消除重复。装配产物
// 是一个可直接 Run 的 ReActService 及其会话与清理钩子；各端的输入解析、校验、
// 事件呈现与主循环仍留在前端。
//
// 之所以独立成包而非放进 cmd/main：main 是 package main，不可被导入，且它已
// import 两个前端，反向依赖会成环。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/artifactstore"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/llmprovider"
	infrastructurerouter "github.com/mikellxy/laxcode/internal/infrastructure/llmrouter"
	"github.com/mikellxy/laxcode/internal/infrastructure/ripgrep"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/shell"
	"github.com/mikellxy/laxcode/internal/infrastructure/skillrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
)

// Input 是装配 ReActService 所需、且因前端而异的输入。
type Input struct {
	// WorkDir 是 Agent 工作目录（沙箱根）：交互模式取 cwd，单次运行模式取 -workdir。
	WorkDir string
	// SessionID 为空则由 session 层以毫秒精度时间串新建；非空则续聊该会话。
	SessionID string
	// PlanMode 为真时在系统提示词追加 Plan Mode 工作流段。
	PlanMode bool
	// SystemPrompt 非空时替换默认 coding-agent 系统提示词。评估等复用同一
	// ReActService、但职责不同的前端通过它注入专用角色；普通前端留空。
	SystemPrompt string
	// Consumer 是 ReAct 事件回调：交互模式接 stdout 彩色打印，单次模式静默丢弃。
	Consumer func(*reactservice.ReactEvent)
	// Router 仅交互模式注入，用于 /model 在两轮 Chat 之间替换本地网关 client。
	Router RouterClientReplacer
}

type RouterClientReplacer interface {
	ReplaceClient(domainrouter.StreamClient)
}

// Assembled 是装配产物。
type Assembled struct {
	// Service 是已完成会话初始化的主 Agent 服务（已注册 bash/write/read/edit +
	// 子 Agent），前端直接调 Chat 发一轮对话。
	Service *reactservice.ReActService
	// Session 是 Service 持有的主会话，供前端读取 ID / token 统计。
	Session *session.Session
	// Skills 是启动时已校验的技能快照，供 CLI 补全与显式技能调用使用。
	Skills []prompt.Skill
	// Cleanup 回收带生命周期的资源，调用方 defer 一次；以 sync.Once 保证幂等，
	// 使信号处理与正常退出路径可各自安全调用。顺序：先 Close 工具注册表（回收
	// bash 后台进程与临时文件），再 Shutdown tracer（flush 关闭阶段产生的 span）。
	Cleanup func()

	switchModel func(string) error
}

// SwitchModel rebuilds both clients for subsequent Chat calls. It must be called
// only while no Chat is in progress.
func (a *Assembled) SwitchModel(ref string) error {
	if a.switchModel == nil {
		return errors.New("model switching is unavailable")
	}
	return a.switchModel(ref)
}

// Assemble 装配一个可直接运行的 ReActService：会话（含系统提示词）、tracer、
// 工具集（含子 Agent）、LLM provider。OpenAI 凭据取自 config.EnvAndFileConf，
// 调用前须已由调用方校验（本函数不重复校验，缺失会在 Run 时才暴露）。
// SystemPrompt 为空时生成默认 coding-agent prompt，非空时原样采用调用方的专用
// prompt。返回的 error 仅来自会话初始化 / 系统提示词写入。
func Assemble(ctx context.Context, in Input) (*Assembled, error) {
	// session：状态与完整历史写 SQLite；JSONL 冷备及 artifact 仍按 session
	// 写入本地目录。SessionID 为空则新建。
	sessRepo, err := sessionrepo.NewSqliteSessionRepo(
		layout.SessionDB(in.WorkDir), layout.SessionRoot(in.WorkDir))
	if err != nil {
		return nil, err
	}
	artifactStore := artifactstore.New(layout.SessionRoot(in.WorkDir))
	sess := session.NewSession(in.SessionID)
	// 系统提示词：技能索引在启动时快照一次（会话期内不刷新）；技能发现端口
	// 以领域类型接收即完成编译期断言（同 workFS）。Plan Mode 的会话规划目录
	// 由布局包算好后注入，领域层不再自行拼路径。
	var skillSrc prompt.SkillSource = skillrepo.New()
	skills := prompt.LoadSkills(skillSrc, in.WorkDir, warnSkillSkip)
	var plan *prompt.PlanMode
	if in.PlanMode {
		plan = &prompt.PlanMode{SessionDir: layout.SessionDir(in.WorkDir, sess.ID)}
	}
	sysPrompt := in.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = prompt.GetSysPrompt(in.WorkDir, skills, plan)
	}

	// tracer：配置 OTLP endpoint 时上报远端，否则维持 filetrace 本地落盘。
	traceHandle, err := newTraceHandle(ctx, layout.TracingLog(in.WorkDir, sess.ID))
	if err != nil {
		_ = sessRepo.Close()
		return nil, fmt.Errorf("init tracing: %w", err)
	}
	tracer := traceHandle.Tracer

	// tools：默认工具集；子 Agent 须在 svc 建好后注册进同一 registry（见下）。
	// workFS 是文件类工具（read/write/edit）唯一的 os 触点实现，此处以领域
	// 端口类型接收即完成编译期断言（infra/workfs 不反向导入 domain，避免与
	// domain 内部测试成环）。
	var workFS tools.WorkFS = workfs.New()
	// shellRunner 与本次运行同生命周期：登记命令派生的后台进程与输出临时
	// 文件，由 Cleanup 里的 toolReg.Close() 统一回收。
	shellRunner := shell.New()
	toolReg := tools.NewDefaultRegistry(tracer)
	toolReg.Register(tools.NewBashTool(in.WorkDir, shellRunner, artifactStore, sess.ID))
	toolReg.Register(tools.NewWriteFileTool(in.WorkDir, workFS))
	toolReg.Register(tools.NewReadFileTool(in.WorkDir, workFS))
	toolReg.Register(tools.NewEditFileTool(in.WorkDir, workFS))
	ripgrepRunner := ripgrep.New()
	toolReg.Register(tools.NewGrepTool(in.WorkDir, ripgrepRunner))
	toolReg.Register(tools.NewGlobTool(in.WorkDir, ripgrepRunner))

	// provider + service
	c := config.EnvAndFileConf
	llmClient := llmprovider.NewOpenApiProviderWithStreamGateway(
		c.OpenaiApiKey, c.OpenaiBaseUrl, c.OpenaiModel, c.LlmRouterURL,
		c.OpenaiContextWindow, c.OpenaiMaxOutputTokens)
	contextSummaryLLMClient := llmprovider.NewOpenApiProvider(
		c.CompactionOpenaiApiKey, c.CompactionOpenaiBaseUrl, c.CompactionOpenaiModel,
		c.CompactionOpenaiContextWindow, c.CompactionOpenaiMaxOutputTokens)
	svc := reactservice.NewReActService(sess, sessRepo, llmClient, contextSummaryLLMClient, toolReg,
		in.Consumer, tracer, artifactStore)
	// 子 Agent 复用 svc 的 LLMClient/tracer/Repo 派生隔离子服务，注册进同一
	// toolReg（svc 持其引用，late register 对 svc 可见）。
	toolReg.Register(reactservice.NewSubAgent(svc, in.WorkDir,
		reactservice.SubAgentDeps{
			WorkFS:   workFS,
			Ripgrep:  ripgrepRunner,
			SkillSrc: skillSrc,
			// 每个子 Agent 各自新建：其 childReg.Close() 只回收自己派生的
			// 后台进程，不会波及主 Agent 尚在运行的后台服务
			NewShell: func() tools.ShellRunner { return shell.New() },
		}))

	// cleanup 必须在任何可能失败的初始化之前建好：会话加载 / 系统提示词写盘
	// 失败时调用方拿不到 Assembled，已获取的资源（filetrace 日志句柄、工具
	// 注册表里的 bash 后台进程与临时文件）只能由本函数负责回收。
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = toolReg.Close()
			_ = sessRepo.Close()
			_ = traceHandle.Shutdown(ctx)
		})
	}

	// 会话初始化放在资源装配之后：子 Agent 工具需先注册进 toolReg，而
	// InitSysPrompt 写入的系统提示词含技能索引，与工具集属于同一份启动快照。
	if err := svc.InitSession(ctx); err != nil {
		cleanup()
		return nil, err
	}
	if err := svc.InitSysPrompt(ctx, sysPrompt); err != nil {
		cleanup()
		return nil, err
	}

	switchModel := func(ref string) error {
		if in.Router == nil {
			return errors.New("model switching requires a running LLM router")
		}
		resolved, err := config.ResolveModel(ref)
		if err != nil {
			return err
		}
		routerClient := infrastructurerouter.NewOpenAIStreamClient(
			resolved.OpenaiApiKey, resolved.OpenaiBaseUrl, resolved.UpstreamModel)
		provider := llmprovider.NewOpenApiProviderWithStreamGateway(
			resolved.OpenaiApiKey, resolved.OpenaiBaseUrl, resolved.UpstreamModel, c.LlmRouterURL,
			c.OpenaiContextWindow, c.OpenaiMaxOutputTokens)

		// TUI 只会在输入阶段执行切换，因此主 Service 没有进行中的 Chat。
		// Router 自身按请求快照 client，外部并发请求也不会在流中途切换。
		in.Router.ReplaceClient(routerClient)
		svc.ReplaceLLMClient(provider)
		return config.SetActiveModel(ref)
	}

	return &Assembled{
		Service:     svc,
		Session:     sess,
		Skills:      append([]prompt.Skill(nil), skills...),
		Cleanup:     cleanup,
		switchModel: switchModel,
	}, nil
}

// warnSkillSkip 是技能跳过警告的落点：写 stderr 而非 stdout，使 one-shot 模式
// 的 stdout JSON 契约与交互模式的彩色输出都不被污染，警告仍可被用户看到。
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
