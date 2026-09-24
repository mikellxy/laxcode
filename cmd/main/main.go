package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/cmd/run_cli"
	"github.com/mikellxy/laxcode/cmd/run_evaluate"
	"github.com/mikellxy/laxcode/cmd/run_sse"
	applicationrouter "github.com/mikellxy/laxcode/internal/application/llm_router"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	infrastructurerouter "github.com/mikellxy/laxcode/internal/infrastructure/llmrouter"
)

const llmRouterShutdownTimeout = 5 * time.Second

func main() {
	if err := config.ParseEnvAndFile(); err != nil {
		panic(err)
	}
	if err := config.ParseCli(); err != nil {
		panic(err)
	}
	// 模式闸门：交互 CLI / evaluate 保持 fail-fast，必须有可用
	// 模型；SSE 模式允许零模型启动，进入页面后经 POST /api/models 添加。
	if !config.CliConf.SSE && strings.TrimSpace(config.EnvAndFileConf.Model) == "" {
		panic("no model configured: interactive and evaluate modes require a model; " +
			"add provider_list to ~/.laxcode/settings.json or set OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL_NAME")
	}

	// Browser code mode is intentionally single-instance per user. Acquire the
	// guard before starting the local LLM router or any other server resource;
	// run_sse publishes the resolved random port through the locked file later.
	var codeInstance *run_sse.CodeInstanceGuard
	if config.CliConf.SSE && config.CliConf.Mode == config.SSEModeCode {
		homeDir, homeErr := os.UserHomeDir()
		if homeErr != nil {
			fmt.Fprintln(os.Stderr, homeErr)
			return
		}
		var acquireErr error
		codeInstance, acquireErr = run_sse.AcquireCodeInstanceGuard(homeDir)
		if acquireErr != nil {
			fmt.Fprintln(os.Stderr, acquireErr)
			return
		}
		defer codeInstance.Close()
	}

	logFile, err := configureSlog(appLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize log %s: %v\n", appLogPath, err)
		os.Exit(1)
	}
	defer logFile.Close()

	// 模型路由器独立使用 llmrouter.log；任何启动模式都先在 goroutine 中启动
	// 本地 HTTP server，再把实际端点写入运行时配置供 agentasm 注入 provider。
	routerLogger, routerLogFile, err := newFileLogger(llmRouterLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize log %s: %v\n", llmRouterLogPath, err)
		_ = logFile.Close()
		os.Exit(1)
	}
	routerServer := applicationrouter.NewHTTPServer(infrastructurerouter.NewOpenAIStreamClient(
		config.EnvAndFileConf.OpenaiApiKey,
		config.EnvAndFileConf.OpenaiBaseUrl,
		config.EnvAndFileConf.OpenaiModel,
	), routerLogger)
	runningRouter, err := routerServer.Start(config.EnvAndFileConf.LlmRouterAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = routerLogFile.Close()
		_ = logFile.Close()
		os.Exit(1)
	}
	config.EnvAndFileConf.LlmRouterURL = runningRouter.Endpoint()
	shutdownRouter := func() {
		ctx, cancel := context.WithTimeout(context.Background(), llmRouterShutdownTimeout)
		defer cancel()
		if err := runningRouter.Shutdown(ctx); err != nil {
			routerLogger.Error("llmrouter_shutdown_failed", "error", err)
		}
		_ = routerLogFile.Close()
	}
	defer shutdownRouter()

	// 模式分发，优先级 evaluate > sse > cli。evaluate 保留 os.Exit
	// 契约；sse 起阻塞式 HTTP 服务（同时指定 qa 时由 run_sse
	// 装配知识库 QA），其余进入默认 TUI 交互模式。
	switch {
	case config.CliConf.Evaluate:
		exitCode := run_evaluate.Run()
		shutdownRouter()
		_ = logFile.Close() // os.Exit 不执行 defer，显式关闭。
		os.Exit(exitCode)
	case config.CliConf.SSE:
		// sse server：阻塞式监听，接受 POST /chat 并把 ReAct 事件以 SSE 流式回传；
		// SIGINT/SIGTERM 触发优雅关闭后 Run 返回。routerServer 一并注入，供
		// POST /api/model 切换模型时替换路由器的上游 client。
		run_sse.Run(routerServer, codeInstance)
	default:
		run_cli.Run(routerServer)
	}
}
