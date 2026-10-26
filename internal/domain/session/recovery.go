package session

import "github.com/mikellxy/laxcode/internal/domain/sharedkernel"

// CanResume reports whether the persisted conversation has an unfinished user turn.
func CanResume(hasUser bool, tail sharedkernel.Message) bool {
	return hasUser && (tail.Role != sharedkernel.RoleAssistant || len(tail.ToolCalls) != 0 ||
		(tail.FinishReason != "" && tail.FinishReason != sharedkernel.FinishReasonStop))
}
