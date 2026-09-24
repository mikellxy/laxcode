// Package qaservice orchestrates query embedding and vector retrieval for RAG prompts.
package qaservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
)

const retrievalLimit = 4

var ErrEmptyQuery = errors.New("rag: query is empty")

type Service struct {
	embedder   knowledgebase.Embedder
	retriever  knowledgebase.Retriever
	tracer     telemetry.Tracer
	dimensions int
}

func New(embedder knowledgebase.Embedder, retriever knowledgebase.Retriever, tracer telemetry.Tracer, dimensions ...int) *Service {
	dim := knowledgebase.EmbeddingDimensions
	if len(dimensions) > 0 && dimensions[0] > 0 {
		dim = dimensions[0]
	}
	return &Service{
		embedder:   embedder,
		retriever:  retriever,
		tracer:     telemetry.OrNoop(tracer),
		dimensions: dim,
	}
}

// Handle implements reactservice.BeforeUserQuery. Retrieval failures are
// request-fatal in RAG mode and therefore bubble to Chat.
func (s *Service) Handle(ctx context.Context, query reactservice.UserQuery) (reactservice.UserQuery, error) {
	raw := strings.TrimSpace(query.Original)
	if raw == "" {
		return reactservice.UserQuery{}, ErrEmptyQuery
	}

	embedStartedAt := time.Now()
	embedCtx, embedSpan := telemetry.Start(ctx, s.tracer, telemetry.SpanQueryEmbedding)
	vector, err := s.embedder.Embed(embedCtx, raw)
	if err == nil && len(vector) != s.dimensions {
		err = fmt.Errorf("embedding dimension mismatch: got %d, want %d",
			len(vector), s.dimensions)
	}
	if len(vector) > 0 {
		embedSpan.SetAttributes(telemetry.AttrEmbeddingDims.Int(len(vector)))
	}
	telemetry.CloseSpan(embedSpan,
		telemetry.WithErr(err),
		telemetry.WithTimeCostMs(time.Since(embedStartedAt).Milliseconds()),
	)
	if err != nil {
		return reactservice.UserQuery{}, fmt.Errorf("embed query: %w", err)
	}

	retrievalStartedAt := time.Now()
	retrievalCtx, retrievalSpan := telemetry.Start(ctx, s.tracer, telemetry.SpanVectorRetrieval,
		telemetry.AttrRetrievalLimit.Int(retrievalLimit),
	)
	chunks, err := s.retriever.Search(retrievalCtx, vector, retrievalLimit)
	retrievalSpan.SetAttributes(telemetry.AttrRetrievalCount.Int(len(chunks)))
	telemetry.CloseSpan(retrievalSpan,
		telemetry.WithErr(err),
		telemetry.WithTimeCostMs(time.Since(retrievalStartedAt).Milliseconds()),
	)
	if err != nil {
		return reactservice.UserQuery{}, fmt.Errorf("retrieve knowledge chunks: %w", err)
	}
	contents := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		contents = append(contents, chunk.Content)
	}
	query.ModelInput = prompt.WrapKnowledgeQuery(query.ModelInput, contents)
	return query, nil
}
