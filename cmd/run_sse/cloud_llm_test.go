package run_sse

import (
	"context"
	"encoding/json"

	"github.com/mikellxy/laxcode/internal/domain/llmprovider"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type cloudFlowLLM struct{ calls int }

func (l *cloudFlowLLM) Generate(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	l.calls++
	if l.calls == 1 {
		return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, TokenUsed: sharedkernel.TokenStatistics{TokenInput: 20}, ToolCalls: []sharedkernel.ToolCall{{ID: "bash-1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf build"}`)}}}, nil
	}
	return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "finished", TokenUsed: sharedkernel.TokenStatistics{TokenInput: 25}}, nil
}

func (l *cloudFlowLLM) GenerateStream(ctx context.Context, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition, _ func(sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	return l.Generate(ctx, msgs, defs)
}

func (*cloudFlowLLM) CountInputTokens(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition) (int, error) {
	return 10, nil
}

func (*cloudFlowLLM) ContextBudget() llmprovider.ContextBudget {
	return llmprovider.ContextBudget{ContextWindow: 1000, ReservedOutputTokens: 100}
}
