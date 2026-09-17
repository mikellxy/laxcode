package qaservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/knowledgebase"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
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
}

func (f *fakeRetriever) Search(_ context.Context, _ []float32, limit int) ([]knowledgebase.Chunk, error) {
	f.gotLimit = limit
	return f.chunks, f.err
}

type fakeAnswerer struct {
	prompt string
	calls  int
}

func (f *fakeAnswerer) Chat(_ context.Context, prompt string) (*sharedkernel.Message, error) {
	f.calls++
	f.prompt = prompt
	return &sharedkernel.Message{Content: "answer"}, nil
}

func TestAnswerBuildsRetrievedPrompt(t *testing.T) {
	embedder := &fakeEmbedder{vector: make([]float32, knowledgebase.EmbeddingDimensions)}
	retriever := &fakeRetriever{chunks: []knowledgebase.Chunk{{Content: "chunk one"}, {Content: "chunk two\n"}}}
	answerer := &fakeAnswerer{}
	svc := New(embedder, retriever, answerer)

	msg, err := svc.Answer(context.Background(), "  question  ")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "answer" || retriever.gotLimit != 10 {
		t.Fatalf("unexpected result: msg=%+v limit=%d", msg, retriever.gotLimit)
	}
	want := "question\n相关文档:\nchunk one\nchunk two"
	if answerer.prompt != want {
		t.Fatalf("prompt = %q, want %q", answerer.prompt, want)
	}
}

func TestAnswerRejectsWrongEmbeddingDimensionBeforeRetrieval(t *testing.T) {
	answerer := &fakeAnswerer{}
	svc := New(&fakeEmbedder{vector: []float32{1}}, &fakeRetriever{}, answerer)
	_, err := svc.Answer(context.Background(), "question")
	if err == nil || !strings.Contains(err.Error(), "got 1, want 1024") {
		t.Fatalf("unexpected error: %v", err)
	}
	if answerer.calls != 0 {
		t.Fatal("answerer must not run after dimension mismatch")
	}
}

func TestAnswerStopsOnRetrievalError(t *testing.T) {
	answerer := &fakeAnswerer{}
	svc := New(
		&fakeEmbedder{vector: make([]float32, knowledgebase.EmbeddingDimensions)},
		&fakeRetriever{err: errors.New("db unavailable")},
		answerer,
	)
	_, err := svc.Answer(context.Background(), "question")
	if err == nil || !strings.Contains(err.Error(), "db unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if answerer.calls != 0 {
		t.Fatal("answerer must not run after retrieval failure")
	}
}

func TestAnswerWithNoChunksStillCallsAnswerer(t *testing.T) {
	answerer := &fakeAnswerer{}
	svc := New(
		&fakeEmbedder{vector: make([]float32, knowledgebase.EmbeddingDimensions)},
		&fakeRetriever{},
		answerer,
	)
	if _, err := svc.Answer(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answerer.prompt, "未召回到相关文档") {
		t.Fatalf("missing empty retrieval marker: %q", answerer.prompt)
	}
}
