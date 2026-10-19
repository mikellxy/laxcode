package run_sse

import (
	"context"
	"errors"
	"github.com/mikellxy/laxcode/workspace"
)

// Existing HTTP regression fixtures exercise the shared host API without an agent runtime.
func (s *server) workspaceAPI() *workspace.API {
	return &workspace.API{Options: workspace.Options{
		UserID: s.userID, UserRoot: s.userRoot, HomeDir: s.homeDir, TempDir: s.tempDir, RuntimeRoot: s.userRoot,
		History: s.history, Catalog: s.catalog, Projects: s.projects, ContextRepo: s.contextRepo, Evaluations: s.evaluations, Models: s.models,
		DeleteGuard: func(_ context.Context, id string) (func(), error) {
			unlock, ok := s.locks.TryLock(id)
			if !ok {
				return nil, errors.New("session busy")
			}
			evaluationUnlock, ok := s.evaluationLocks.TryLock(id)
			if !ok {
				unlock()
				return nil, errors.New("evaluation busy")
			}
			return func() { evaluationUnlock(); unlock() }, nil
		},
	}}
}

const maxChatTextPreviewBytes = 256 << 10
const maxChatImagePreviewBytes = 10 << 20
const maxChatDownloadBytes = 100 << 20
const maxChatDownloadFiles = 200

type sessionDTO = workspace.SessionDTO
type sessionPageDTO = workspace.SessionPageDTO
type projectDTO = workspace.ProjectDTO
type projectListDTO = workspace.ProjectListDTO
type historyPageDTO = workspace.HistoryPageDTO
type providerListModelDTO = workspace.ProviderListModelDTO
type evaluationJobListDTO = workspace.EvaluationJobListDTO
