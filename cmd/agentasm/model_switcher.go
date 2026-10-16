package agentasm

import (
	"errors"
	"sync"

	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
)

type ModelSwitcher struct {
	mu     sync.RWMutex
	models *ai_models.Manager
}

var ErrModelsUnavailable = errors.New("model manager is unavailable")

func NewModelSwitcher(models *ai_models.Manager) *ModelSwitcher {
	return &ModelSwitcher{models: models}
}
func (s *ModelSwitcher) RLock()   { s.mu.RLock() }
func (s *ModelSwitcher) RUnlock() { s.mu.RUnlock() }
func (s *ModelSwitcher) Lock()    { s.mu.Lock() }
func (s *ModelSwitcher) Unlock()  { s.mu.Unlock() }
func (s *ModelSwitcher) SwitchModel(ref string, effort ...string) error {
	if s == nil || s.models == nil {
		return ErrModelsUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SwitchModelLocked(ref, effort...)
}

// SwitchModelLocked publishes the selection under the same lock used by Chat/Resume.
func (s *ModelSwitcher) SwitchModelLocked(ref string, effort ...string) error {
	if s == nil || s.models == nil {
		return ErrModelsUnavailable
	}
	return s.models.SetActiveModel(ref, effort...)
}
