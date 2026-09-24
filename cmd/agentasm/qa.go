package agentasm

import (
	"context"
	"sync"

	"github.com/mikellxy/laxcode/internal/application/qaservice"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	infraembedding "github.com/mikellxy/laxcode/internal/infrastructure/embedding"
	infrakb "github.com/mikellxy/laxcode/internal/infrastructure/knowledgebase"
)

type QAInput struct {
	KBPath    string
	WorkDir   string
	HomeDir   string
	SessionID string
	Consumer  func(*reactservice.ReactEvent)
}

type QAAssembled struct {
	tracer  telemetry.Tracer
	Service *reactservice.ReActService
	Session *session.Session
	Cleanup func()
}

// AssembleQA creates the knowledge-base QA service. Its ReAct registry is
// deliberately empty: retrieved chunks are the only external context in this
// version of QA mode.
func AssembleQA(ctx context.Context, in QAInput) (*QAAssembled, error) {
	if err := config.ValidateKBPath(in.KBPath); err != nil {
		return nil, err
	}
	assembled, err := assembleToolless(ctx, in)
	if err != nil {
		return nil, err
	}
	react, sess := assembled.Service, assembled.Session
	c := config.EnvAndFileConf
	dimensions := c.EmbeddingVecDim
	if dimensions == 0 {
		dimensions = knowledgebase.EmbeddingDimensions
	}
	retriever, err := infrakb.NewSQLiteVecRetriever(in.KBPath, dimensions)
	if err != nil {
		assembled.Cleanup()
		return nil, err
	}
	embedder := infraembedding.NewOpenAIClient(
		c.EmbedOpenaiApiKey, c.EmbedOpenaiBaseUrl, c.EmbedOpenaiModel, dimensions)
	react.SetPromptEnricher(qaservice.New(embedder, retriever, assembled.tracer, dimensions))

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = retriever.Close()
			assembled.Cleanup()
		})
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
