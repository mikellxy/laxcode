// Package qaservice orchestrates query embedding and vector retrieval for QA prompts.
package qaservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
)

const retrievalLimit = 4

var ErrEmptyQuery = errors.New("qa: query is empty")

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

// Enrich 为每个用户问题执行向量化与知识库召回，返回召回的知识片段。
// 调用方负责在 chat 根 span 内调用本方法，并把片段挂到用户消息的工作集
// 副本上；模型看到的拼接文本由 Message.ModelContent 生成。
func (s *Service) Enrich(ctx context.Context, query string) ([]sharedkernel.MemoryChunk, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrEmptyQuery
	}

	embedStartedAt := time.Now()
	embedCtx, embedSpan := telemetry.Start(ctx, s.tracer, telemetry.SpanQueryEmbedding)
	vector, err := s.embedder.Embed(embedCtx, query)
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
		return nil, fmt.Errorf("embed query: %w", err)
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
		return nil, fmt.Errorf("retrieve knowledge chunks: %w", err)
	}
	recalled := make([]sharedkernel.MemoryChunk, 0, len(chunks))
	for _, chunk := range chunks {
		recalled = append(recalled, sharedkernel.MemoryChunk{ID: chunk.ID, Content: chunk.Content})
	}
	return recalled, nil
}
