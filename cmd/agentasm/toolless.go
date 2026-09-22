package agentasm

import (
	"context"
	"sync"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/llmprovider"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

func assembleToolless(ctx context.Context, in QAInput) (*QAAssembled, error) {
	sessRepo, err := sessionrepo.NewSqliteSessionRepo(
		layout.SessionDB(in.WorkDir), layout.SessionRoot(in.WorkDir))
	if err != nil {
		return nil, err
	}
	sess := session.NewSession(in.SessionID)
	traceHandle, err := newTraceHandle(ctx, layout.TracingLog(in.WorkDir, sess.ID))
	if err != nil {
		_ = sessRepo.Close()
		return nil, err
	}
	tracer := traceHandle.Tracer
	toolReg := tools.NewDefaultRegistry(tracer)

	c := config.EnvAndFileConf
	// 主 provider 按当前活跃模型构建，token 预算取该模型的 limit（未声明时
	// 回退全局窗口配置，见 newMainProvider）。
	llmClient := newMainProvider()
	contextSummaryLLMClient := llmprovider.NewOpenApiProvider(
		c.CompactionOpenaiApiKey, c.CompactionOpenaiBaseUrl, c.CompactionOpenaiModel,
		c.CompactionOpenaiContextWindow, c.CompactionOpenaiMaxOutputTokens)
	react := reactservice.NewReActService(sess, sessRepo, llmClient, contextSummaryLLMClient,
		toolReg, in.Consumer, tracer)

	var once sync.Once
	cleanup := func() {
		once.Do(func() { _ = toolReg.Close(); _ = sessRepo.Close(); _ = traceHandle.Shutdown(context.Background()) })
	}
	if err := react.InitSession(ctx); err != nil {
		cleanup()
		return nil, err
	}
	return &QAAssembled{Service: react, Session: sess, Cleanup: cleanup, tracer: tracer}, nil
}
