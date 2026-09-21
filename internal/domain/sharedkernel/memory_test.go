package sharedkernel

import (
	"strings"
	"testing"
)

func TestMemoryCloneAndModelContent(t *testing.T) {
	m := Message{Role: RoleUser, Content: "original", MemoryChunks: []MemoryChunk{{ID: "a", Content: "fact"}}}
	copy := m.Clone()
	copy.MemoryChunks[0].Content = "changed"
	if m.MemoryChunks[0].Content != "fact" || m.Content != "original" || !strings.Contains(m.ModelContent(), "fact") {
		t.Fatal("memory alias or serialization")
	}
}

func TestRAGChunksCloneAndModelContent(t *testing.T) {
	m := Message{Role: RoleUser, Content: "question", RAGChunks: []MemoryChunk{
		{ID: "a", Content: "chunk one"},
		{ID: "b", Content: "chunk two\n"},
	}}
	copy := m.Clone()
	copy.RAGChunks[0].Content = "changed"
	if m.RAGChunks[0].Content != "chunk one" {
		t.Fatal("rag chunks aliased")
	}
	want := "question\n相关文档:\nchunk one\nchunk two"
	if got := m.ModelContent(); got != want {
		t.Fatalf("ModelContent = %q, want %q", got, want)
	}
	without := Message{Role: RoleUser, Content: "question"}
	if got := without.ModelContent(); got != "question" {
		t.Fatalf("ModelContent without chunks = %q", got)
	}
}
