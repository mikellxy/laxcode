// Package qaservice orchestrates query embedding and vector retrieval for QA prompts.
package qaservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
)

const retrievalLimit = 10

var ErrEmptyQuery = errors.New("qa: query is empty")

type Service struct {
	embedder  knowledgebase.Embedder
	retriever knowledgebase.Retriever
	tracer    telemetry.Tracer
}

func New(embedder knowledgebase.Embedder, retriever knowledgebase.Retriever, tracer telemetry.Tracer) *Service {
	return &Service{
		embedder:  embedder,
		retriever: retriever,
		tracer:    telemetry.OrNoop(tracer),
	}
}

// Enrich 为每个用户问题执行向量化与知识库召回，返回扩充后的模型提示词。
// 调用方负责在 chat 根 span 内调用本方法并继续 LLM 生成。
func (s *Service) Enrich(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", ErrEmptyQuery
	}

	embedStartedAt := time.Now()
	embedCtx, embedSpan := telemetry.Start(ctx, s.tracer, telemetry.SpanQueryEmbedding)
	vector, err := s.embedder.Embed(embedCtx, query)
	if err == nil && len(vector) != knowledgebase.EmbeddingDimensions {
		err = fmt.Errorf("embedding dimension mismatch: got %d, want %d",
			len(vector), knowledgebase.EmbeddingDimensions)
	}
	if len(vector) > 0 {
		embedSpan.SetAttributes(telemetry.AttrEmbeddingDims.Int(len(vector)))
	}
	telemetry.CloseSpan(embedSpan,
		telemetry.WithErr(err),
		telemetry.WithTimeCostMs(time.Since(embedStartedAt).Milliseconds()),
	)
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
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
		return "", fmt.Errorf("retrieve knowledge chunks: %w", err)
	}
	return buildPrompt(query, chunks), nil
}

func buildPrompt(query string, chunks []knowledgebase.Chunk) string {
	var b strings.Builder
	b.WriteString(query)
	b.WriteString("\n相关文档:\n")
	if len(chunks) == 0 {
		b.WriteString("（未召回到相关文档）")
		return b.String()
	}
	for i, chunk := range chunks {
		b.WriteString(chunk.Content)
		if i < len(chunks)-1 && !strings.HasSuffix(chunk.Content, "\n") {
			b.WriteByte('\n')
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}
