package run_sse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

// shutdownTimeout 是优雅关闭等待在途 SSE 流结束的上限。一次完整 ReAct 生成可能
// 较长，故给一个宽松兜底值而非无限等待；超时后强制 Close 断开连接，由 r.Context()
// 取消驱动在途 Chat 收敛、Cleanup 回收资源。
const shutdownTimeout = 15 * time.Second

// fatal 用于启动期错误：此时尚未进入服务循环，直接写 stderr 并 os.Exit(1) 安全。
func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// Run 启动 sse server 并阻塞至收到 SIGINT/SIGTERM 优雅关闭。它是 main 启动的
// HTTP 前端入口：装配（session/tracer/tools/provider/ReActService）经
// cmd/agentasm 组合根按「每请求一次」完成（见
// handler），本函数只负责 server 级配置、路由注册与生命周期管理。router 是
// main 启动的本地 LLM 路由器，供模型切换端点替换其上游 client。
func Run(router agentasm.RouterClientReplacer, codeInstance *CodeInstanceGuard) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	s := newServer(homeDir, config.CliConf.Plan)
	s.tokenBudget = config.CliConf.TokenBudget
	s.switcher = agentasm.NewModelSwitcher(router)
	historyRepo, err := sessionrepo.NewSqliteSessionRepo(
		layout.SessionDB(homeDir), layout.SessionRoot(homeDir))
	if err != nil {
		fatal(fmt.Errorf("init session history repository: %w", err))
	}
	defer historyRepo.Close()
	s.history = historyRepo
	s.catalog = historyRepo
	s.projects = historyRepo
	s.contextRepo = historyRepo
	s.evaluations = historyRepo
	if err := historyRepo.FailActiveEvaluationJobs(context.Background(), "evaluation interrupted by server restart"); err != nil {
		fatal(fmt.Errorf("reconcile evaluation jobs: %w", err))
	}
	mux := http.NewServeMux()
	// Go 1.22+ 的方法+路径模式：方法不匹配时由 ServeMux 自动回 405，
	// 无需在各 handler 内重复判方法。
	mux.HandleFunc("POST /chat", s.handleChat)
	mux.HandleFunc("POST /api/sessions/{session_id}/resume", s.handleResume)
	mux.HandleFunc("POST /api/sessions/{session_id}/approvals/{approval_id}", s.handleApproval)
	mux.HandleFunc("POST /api/model", s.handleSwitchModel)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("POST /api/directory-picker", s.handlePickDirectory)
	mux.HandleFunc("GET /api/sessions/{session_id}/messages", s.handleHistory)
	mux.HandleFunc("GET /api/sessions/{session_id}/context", s.handleSessionContext)
	mux.HandleFunc("POST /api/evaluations", s.handleCreateEvaluation)
	mux.HandleFunc("GET /api/evaluations", s.handleListEvaluations)
	mux.HandleFunc("GET /api/models", s.handleListModels)
	mux.HandleFunc("POST /api/models", s.handleAddModel)
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// ctx 由 SIGINT/SIGTERM 取消，驱动优雅关闭。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", config.CliConf.Addr)
	if err != nil {
		fatal(fmt.Errorf("listen SSE server on %s: %w", config.CliConf.Addr, err))
	}
	actualAddr := listener.Addr().String()
	srv := &http.Server{Addr: actualAddr, Handler: mux}
	if err := codeInstance.Publish("http://" + actualAddr); err != nil {
		_ = listener.Close()
		fatal(err)
	}

	fmt.Printf("LaxCode SSE code listening on %s (data: %s)\n", actualAddr, layout.Root(homeDir))
	fmt.Printf(">>> create a session with work_dir, then POST /chat with its session_id\n")

	// 监听在独立 goroutine：ListenAndServe 阻塞至服务关闭；ErrServerClosed 是
	// Shutdown/Close 的正常结果，其余错误（如端口占用）经 errChan 回流主 goroutine。
	errChan := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	// 等待关闭信号或监听致命错误，二者任一即结束服务循环。
	select {
	case err := <-errChan:
		fatal(err)
	case <-ctx.Done():
	}

	// 优雅关闭：停收新连接并等在途 SSE 流结束；超时则强制 Close 断开，r.Context()
	// 随之取消，驱动在途 Chat 从 LLM/工具调用收敛，各请求的 defer Cleanup 得以执行。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
	}
	s.stopEvaluations()
}
