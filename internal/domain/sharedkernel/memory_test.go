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
