// Package qaservice orchestrates embedding, vector retrieval and answer generation.
package qaservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const retrievalLimit = 10

var ErrEmptyQuery = errors.New("qa: query is empty")

type Answerer interface {
	Chat(ctx context.Context, prompt string) (*sharedkernel.Message, error)
}

type Service struct {
	embedder  knowledgebase.Embedder
	retriever knowledgebase.Retriever
	answerer  Answerer
}

func New(embedder knowledgebase.Embedder, retriever knowledgebase.Retriever, answerer Answerer) *Service {
	return &Service{embedder: embedder, retriever: retriever, answerer: answerer}
}

// Answer performs retrieval for every user query and sends the augmented prompt
// to the conversation-aware answer service.
func (s *Service) Answer(ctx context.Context, query string) (*sharedkernel.Message, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrEmptyQuery
	}

	vector, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vector) != knowledgebase.EmbeddingDimensions {
		return nil, fmt.Errorf("embedding dimension mismatch: got %d, want %d",
			len(vector), knowledgebase.EmbeddingDimensions)
	}

	chunks, err := s.retriever.Search(ctx, vector, retrievalLimit)
	if err != nil {
		return nil, fmt.Errorf("retrieve knowledge chunks: %w", err)
	}
	return s.answerer.Chat(ctx, buildPrompt(query, chunks))
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
