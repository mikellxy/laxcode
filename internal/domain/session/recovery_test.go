package session

import (
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestCanResume(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hasUser bool
		tail    sharedkernel.Message
		want    bool
	}{
		{"empty", false, sharedkernel.Message{}, false},
		{"system only", false, sharedkernel.Message{Role: sharedkernel.RoleSystem}, false},
		{"user", true, sharedkernel.Message{Role: sharedkernel.RoleUser}, true},
		{"tool", true, sharedkernel.Message{Role: sharedkernel.RoleTool}, true},
		{"pending tools", true, sharedkernel.Message{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{{ID: "call"}}, FinishReason: "stop"}, true},
		{"complete", true, sharedkernel.Message{Role: sharedkernel.RoleAssistant, FinishReason: "stop"}, false},
		{"legacy complete", true, sharedkernel.Message{Role: sharedkernel.RoleAssistant}, false},
		{"interrupted", true, sharedkernel.Message{Role: sharedkernel.RoleAssistant, FinishReason: "cancelled"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanResume(tc.hasUser, tc.tail); got != tc.want {
				t.Fatalf("CanResume = %v, want %v", got, tc.want)
			}
		})
	}
}
