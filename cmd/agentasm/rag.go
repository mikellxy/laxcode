package agentasm

import (
	"fmt"
	"sync"

	"github.com/mikellxy/laxcode/internal/application/qaservice"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	infraembedding "github.com/mikellxy/laxcode/internal/infrastructure/embedding"
	infrakb "github.com/mikellxy/laxcode/internal/infrastructure/knowledgebase"
)

func configureRAG(service *reactservice.ReActService, tracer telemetry.Tracer, kbPath string) (func(), error) {
	if err := config.ValidateKBPath(kbPath); err != nil {
		return nil, err
	}
	c := config.EnvAndFileConf
	dimensions := c.EmbeddingVecDim
	if dimensions == 0 {
		dimensions = knowledgebase.EmbeddingDimensions
	}
	retriever, err := infrakb.NewSQLiteVecRetriever(kbPath, dimensions)
	if err != nil {
		return nil, fmt.Errorf("open RAG knowledge base: %w", err)
	}
	embedder := infraembedding.NewOpenAIClient(
		c.EmbedOpenaiApiKey, c.EmbedOpenaiBaseUrl, c.EmbedOpenaiModel, dimensions)
	service.UseBeforeUserQuery(qaservice.New(embedder, retriever, tracer, dimensions))
	var once sync.Once
	return func() { once.Do(func() { _ = retriever.Close() }) }, nil
}
