package agentasm

import "testing"

func TestQAToolRegistryIsEmpty(t *testing.T) {
	registry := newQAToolRegistry(nil)
	if got := registry.GetAvailableTools(); len(got) != 0 {
		t.Fatalf("QA mode must not expose tools, got %+v", got)
	}
}
