package qaservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
)

type fakeEmbedder struct {
	vector []float32
	err    error
}

func (f *fakeEmbedder) Embed(context.Context, string) ([]float32, error) {
	return f.vector, f.err
}

type fakeRetriever struct {
	chunks   []knowledgebase.Chunk
	err      error
	gotLimit int
	calls    int
}

func (f *fakeRetriever) Search(_ context.Context, _ []float32, limit int) ([]knowledgebase.Chunk, error) {
	f.calls++
	f.gotLimit = limit
	return f.chunks, f.err
}

func TestEnrichReturnsRecalledChunks(t *testing.T) {
	embedder := &fakeEmbedder{vector: make([]float32, knowledgebase.EmbeddingDimensions)}
	retriever := &fakeRetriever{chunks: []knowledgebase.Chunk{
		{ID: "a", Content: "chunk one", Distance: 0.1},
		{ID: "b", Content: "chunk two\n", Distance: 0.2},
	}}
	svc := New(embedder, retriever, nil)

	chunks, err := svc.Enrich(context.Background(), "  question  ")
	if err != nil {
		t.Fatal(err)
	}
	if retriever.gotLimit != 4 {
		t.Fatalf("retrieval limit = %d, want 4", retriever.gotLimit)
	}
	want := []sharedkernel.MemoryChunk{{ID: "a", Content: "chunk one"}, {ID: "b", Content: "chunk two\n"}}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunks = %+v, want %+v", chunks, want)
	}
}

func TestEnrichRejectsWrongEmbeddingDimensionBeforeRetrieval(t *testing.T) {
	retriever := &fakeRetriever{}
	svc := New(&fakeEmbedder{vector: []float32{1}}, retriever, nil)
	_, err := svc.Enrich(context.Background(), "question")
	if err == nil || !strings.Contains(err.Error(), "got 1, want 1024") {
		t.Fatalf("unexpected error: %v", err)
	}
	if retriever.calls != 0 {
		t.Fatal("retriever must not run after dimension mismatch")
	}
}

func TestEnrichUsesConfiguredEmbeddingDimension(t *testing.T) {
	retriever := &fakeRetriever{chunks: []knowledgebase.Chunk{{ID: "one", Content: "example"}}}
	svc := New(&fakeEmbedder{vector: make([]float32, 7)}, retriever, nil, 7)
	chunks, err := svc.Enrich(context.Background(), "question")
	if err != nil || len(chunks) != 1 || chunks[0].ID != "one" {
		t.Fatalf("chunks = %+v, error = %v", chunks, err)
	}
}

func TestEnrichStopsOnRetrievalError(t *testing.T) {
	svc := New(
		&fakeEmbedder{vector: make([]float32, knowledgebase.EmbeddingDimensions)},
		&fakeRetriever{err: errors.New("db unavailable")},
		nil,
	)
	_, err := svc.Enrich(context.Background(), "question")
	if err == nil || !strings.Contains(err.Error(), "db unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnrichWithNoChunksReturnsEmpty(t *testing.T) {
	svc := New(
		&fakeEmbedder{vector: make([]float32, knowledgebase.EmbeddingDimensions)},
		&fakeRetriever{},
		nil,
	)
	chunks, err := svc.Enrich(context.Background(), "question")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("empty retrieval must yield no chunks, got %+v", chunks)
	}
}

func TestEnrichTraceSpansAreDirectChatChildren(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tracing.log")
	provider, err := filetrace.New(logPath)
	if err != nil {
		t.Fatalf("filetrace.New: %v", err)
	}
	handle := tracing.New(provider)
	svc := New(
		&fakeEmbedder{vector: make([]float32, knowledgebase.EmbeddingDimensions)},
		&fakeRetriever{chunks: []knowledgebase.Chunk{{Content: "chunk"}}},
		handle.Tracer,
	)

	ctx, chatSpan := telemetry.Start(context.Background(), handle.Tracer, telemetry.SpanChat)
	if _, err := svc.Enrich(ctx, "question"); err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	telemetry.CloseSpan(chatSpan)
	if err := handle.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	type traceRecord struct {
		TraceID      string         `json:"trace_id"`
		SpanID       string         `json:"span_id"`
		ParentSpanID string         `json:"parent_span_id"`
		Name         string         `json:"name"`
		Attributes   map[string]any `json:"attributes"`
	}
	var records []traceRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record traceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("Unmarshal trace record: %v", err)
		}
		records = append(records, record)
	}
	if len(records) != 3 {
		t.Fatalf("want 3 spans, got %d: %+v", len(records), records)
	}

	var root traceRecord
	for _, record := range records {
		if record.Name == telemetry.SpanChat {
			root = record
		}
	}
	if root.SpanID == "" || root.ParentSpanID != "" {
		t.Fatalf("chat must be the root span: %+v", root)
	}
	for _, record := range records {
		if record.Name == telemetry.SpanChat {
			continue
		}
		if record.TraceID != root.TraceID || record.ParentSpanID != root.SpanID {
			t.Errorf("span %s is not a direct chat child: %+v", record.Name, record)
		}
		switch record.Name {
		case telemetry.SpanQueryEmbedding:
			if got := record.Attributes["laxcode.embedding.dimensions"]; got != float64(knowledgebase.EmbeddingDimensions) {
				t.Errorf("embedding dimensions = %v", got)
			}
		case telemetry.SpanVectorRetrieval:
			if got := record.Attributes["laxcode.retrieval.limit"]; got != float64(retrievalLimit) {
				t.Errorf("retrieval limit = %v", got)
			}
			if got := record.Attributes["laxcode.retrieval.result_count"]; got != float64(1) {
				t.Errorf("retrieval result count = %v", got)
			}
		default:
			t.Errorf("unexpected child span %q", record.Name)
		}
	}
}
