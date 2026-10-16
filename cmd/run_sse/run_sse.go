package run_sse

import (
	"context"

	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
)

// shutdownTimeout 是优雅关闭等待在途 SSE 流结束的上限。一次完整 ReAct 生成可能
// 较长，故给一个宽松兜底值而非无限等待；超时后强制 Close 断开连接，由 r.Context()
// 取消驱动在途 Chat 收敛、Cleanup 回收资源。
const shutdownTimeout = 15 * time.Second

// fatal 由 Run 在资源清理完成后调用，保留命令失败时的非零退出码。
func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// Run serves per-user cloud workspaces until SIGINT/SIGTERM.
func Run(codeInstance *CodeInstanceGuard) {
	if err := run(codeInstance); err != nil {
		fatal(err)
	}
}

func run(codeInstance *CodeInstanceGuard) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var tracer telemetry.Tracer
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		traceHandle, err := tracing.NewOTLP(context.Background())
		if err != nil {
			return fmt.Errorf("init tracing: %w", err)
		}
		tracer = traceHandle.Tracer
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = traceHandle.Shutdown(ctx)
		}()
	}

	users := newCloudUsers(homeDir, tracer)
	defer users.Close()

	// ctx 由 SIGINT/SIGTERM 取消，驱动优雅关闭。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", config.Current.Addr)
	if err != nil {
		return fmt.Errorf("listen SSE server on %s: %w", config.Current.Addr, err)
	}
	actualAddr := listener.Addr().String()
	srv := &http.Server{Addr: actualAddr, Handler: users}
	if err := codeInstance.Publish("http://" + actualAddr); err != nil {
		_ = listener.Close()
		return err
	}
	if ctx.Err() != nil {
		_ = listener.Close()
		return nil
	}

	fmt.Printf("LaxCode cloud prototype listening on %s (users: %s)\n", actualAddr, filepath.Join(homeDir, "laxcode"))
	fmt.Printf("Cloud Chat/Resume in-flight limit: %d\n", cloudMaxInFlightChats)
	fmt.Printf(">>> create a project and session, then POST /chat with its session_id\n")

	// 监听在独立 goroutine：ListenAndServe 阻塞至服务关闭；ErrServerClosed 是
	// Shutdown/Close 的正常结果，其余错误（如端口占用）经 errChan 回流主 goroutine。
	errChan := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	// 等待关闭信号或监听致命错误，二者任一即结束服务循环。
	var serveErr error
	select {
	case serveErr = <-errChan:
	case <-ctx.Done():
	}

	// 优雅关闭：停收新连接并等在途 SSE 流结束；超时则强制 Close 断开，r.Context()
	// 随之取消，驱动在途 Chat 从 LLM/工具调用收敛，各请求的 defer Cleanup 得以执行。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
	}
	return serveErr
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	// Go 1.22+ 的方法+路径模式：方法不匹配时由 ServeMux 自动回 405，
	// 无需在各 handler 内重复判方法。
	mux.HandleFunc("POST /chat", s.handleChat)
	mux.HandleFunc("GET /skills", s.handleListSkills)
	mux.HandleFunc("POST /api/sessions/{session_id}/resume", s.handleResume)
	mux.HandleFunc("POST /api/model", s.handleSwitchModel)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("DELETE /api/sessions/{session_id}", s.handleDeleteSession)
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("GET /api/sessions/{session_id}/messages", s.handleHistory)
	mux.HandleFunc("GET /api/sessions/{session_id}/chats/{chat_id}/files.zip", s.handleDownloadChatFiles)
	mux.HandleFunc("GET /api/sessions/{session_id}/chats/{chat_id}/file", s.handlePreviewChatFile)
	mux.HandleFunc("GET /api/sessions/{session_id}/context", s.handleSessionContext)
	mux.HandleFunc("GET /api/sessions/{session_id}/performance", s.handlePerformance)
	mux.HandleFunc("POST /api/evaluations", s.handleCreateEvaluation)
	mux.HandleFunc("GET /api/evaluations", s.handleListEvaluations)
	mux.HandleFunc("GET /api/sessions/{session_id}/evaluations/{job_id}/report", s.handleDownloadEvaluationReport)
	mux.HandleFunc("GET /api/models", s.handleListModels)
	mux.HandleFunc("POST /api/models", s.handleAddModel)
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	return mux
}
