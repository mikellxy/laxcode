package main

import (
	"context"
	"fmt"
	"os"
	"time"

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
	// One browser coding backend per user; publish its address after listening.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	codeInstance, err := run_sse.AcquireCodeInstanceGuard(homeDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer codeInstance.Close()

	logFile, err := configureSlog(appLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize log %s: %v\n", appLogPath, err)
		os.Exit(1)
	}
	defer logFile.Close()

	// 模型路由器独立使用 llmrouter.log；先在 goroutine 中启动
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

	run_sse.Run(routerServer, codeInstance)
}
