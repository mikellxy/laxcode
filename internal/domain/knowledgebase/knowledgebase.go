// Package knowledgebase defines the ports used by knowledge-base question
// answering. Implementations live in infrastructure packages.
package knowledgebase

import "context"

const EmbeddingDimensions = 1024

// Chunk is one knowledge-base passage returned by vector search.
type Chunk struct {
	ID       int64
	Content  string
	Distance float64
}

// Embedder converts user text into the vector space used by the knowledge base.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Retriever returns chunks ordered from most to least relevant.
type Retriever interface {
	Search(ctx context.Context, vector []float32, limit int) ([]Chunk, error)
}
