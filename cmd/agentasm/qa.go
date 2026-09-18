package agentasm

import (
	"context"
	"sync"

	"github.com/mikellxy/laxcode/internal/application/qaservice"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	infraembedding "github.com/mikellxy/laxcode/internal/infrastructure/embedding"
	infrakb "github.com/mikellxy/laxcode/internal/infrastructure/knowledgebase"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/llmprovider"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

type QAInput struct {
	WorkDir   string
	SessionID string
	Consumer  func(*reactservice.ReactEvent)
}

type QAAssembled struct {
	Service *reactservice.ReActService
	Session *session.Session
	Cleanup func()
}

// AssembleQA creates the knowledge-base QA service. Its ReAct registry is
// deliberately empty: retrieved chunks are the only external context in this
// version of QA mode.
func AssembleQA(ctx context.Context, in QAInput) (*QAAssembled, error) {
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
	toolReg := newQAToolRegistry(tracer)

	c := config.EnvAndFileConf
	llmClient := llmprovider.NewOpenApiProviderWithStreamGateway(
		c.OpenaiApiKey, c.OpenaiBaseUrl, c.OpenaiModel, c.LlmRouterURL,
		c.OpenaiContextWindow, c.OpenaiMaxOutputTokens)
	contextSummaryLLMClient := llmprovider.NewOpenApiProvider(
		c.CompactionOpenaiApiKey, c.CompactionOpenaiBaseUrl, c.CompactionOpenaiModel,
		c.CompactionOpenaiContextWindow, c.CompactionOpenaiMaxOutputTokens)
	react := reactservice.NewReActService(sess, sessRepo, llmClient, contextSummaryLLMClient,
		toolReg, in.Consumer, tracer)

	retriever, err := infrakb.NewSQLiteVecRetriever(layout.KnowledgeBaseDB(in.WorkDir))
	if err != nil {
		_ = toolReg.Close()
		_ = sessRepo.Close()
		_ = traceHandle.Shutdown(ctx)
		return nil, err
	}
	embedder := infraembedding.NewOpenAIClient(
		c.EmbedOpenaiApiKey, c.EmbedOpenaiBaseUrl, c.EmbedOpenaiModel)
	react.SetPromptEnricher(qaservice.New(embedder, retriever, tracer))

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = toolReg.Close()
			_ = retriever.Close()
			_ = sessRepo.Close()
			_ = traceHandle.Shutdown(ctx)
		})
	}

	if err := react.InitSession(ctx); err != nil {
		cleanup()
		return nil, err
	}
	if err := react.InitSysPrompt(ctx, prompt.GetQASysPrompt()); err != nil {
		cleanup()
		return nil, err
	}

	return &QAAssembled{
		Service: react,
		Session: sess,
		Cleanup: cleanup,
	}, nil
}

func newQAToolRegistry(tracer telemetry.Tracer) *tools.DefaultRegistry {
	return tools.NewDefaultRegistry(tracer)
}
