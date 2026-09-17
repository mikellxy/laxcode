package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
)

func TestOpenAIClientEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var body struct {
			Input          string `json:"input"`
			Model          string `json:"model"`
			Dimensions     int    `json:"dimensions"`
			EncodingFormat string `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Input != "question" || body.Model != "embed-model" ||
			body.Dimensions != knowledgebase.EmbeddingDimensions || body.EncodingFormat != "float" {
			t.Errorf("unexpected request: %+v", body)
		}
		vector := make([]float32, knowledgebase.EmbeddingDimensions)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"model":  "embed-model",
			"data": []any{map[string]any{
				"object": "embedding", "index": 0, "embedding": vector,
			}},
			"usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient("test-key", server.URL+"/", "embed-model")
	vector, err := client.Embed(context.Background(), "question")
	if err != nil {
		t.Fatal(err)
	}
	if len(vector) != knowledgebase.EmbeddingDimensions {
		t.Fatalf("vector length = %d", len(vector))
	}
}
