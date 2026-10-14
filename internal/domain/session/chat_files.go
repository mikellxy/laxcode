package session

import "context"

// ChatFile paths are relative to the user's root, never host paths.
type ChatFile struct {
	Path       string `json:"path"`
	ChangeType string `json:"change_type"`
}

type ChatFilesRepository interface {
	UpsertChatFiles(ctx context.Context, sessionID, chatID string, files []ChatFile) error
	ListChatFiles(ctx context.Context, sessionID, chatID string) ([]ChatFile, error)
}
