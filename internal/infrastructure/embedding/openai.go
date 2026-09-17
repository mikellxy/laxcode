package embedding

import (
	"context"
	"errors"
	"fmt"

	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type OpenAIClient struct {
	client openai.Client
	model  string
}

var _ knowledgebase.Embedder = (*OpenAIClient)(nil)

func NewOpenAIClient(apiKey, baseURL, model string) *OpenAIClient {
	return &OpenAIClient{
		client: openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL)),
		model:  model,
	}
}

func (c *OpenAIClient) Embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := c.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String(text),
		},
		Model:          c.model,
		Dimensions:     openai.Int(knowledgebase.EmbeddingDimensions),
		EncodingFormat: openai.EmbeddingNewParamsEncodingFormatFloat,
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Data) != 1 {
		return nil, fmt.Errorf("embedding response contains %d vectors, want 1", len(resp.Data))
	}
	if len(resp.Data[0].Embedding) == 0 {
		return nil, errors.New("embedding response contains an empty vector")
	}
	vector := make([]float32, len(resp.Data[0].Embedding))
	for i, value := range resp.Data[0].Embedding {
		vector[i] = float32(value)
	}
	return vector, nil
}
