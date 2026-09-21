package agentasm

import (
	"context"
	"testing"
)

func TestAssembleSSEHasNoToolsAndDedicatedPrompt(t *testing.T) {
	a, err := AssembleSSE(context.Background(), Input{WorkDir: t.TempDir(), SessionID: "sse"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Cleanup()
	if len(a.Service.ToolRegistry.GetAvailableTools()) != 0 {
		t.Fatal("SSE exposes tools")
	}
	if len(a.Session.Messages) != 1 || a.Session.Messages[0].Content == "" {
		t.Fatal("missing SSE system prompt")
	}
}
