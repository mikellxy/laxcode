package agentasm

import (
	"errors"
	"sync"

	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

// ModelSwitcher serializes model changes against active Web requests.
type ModelSwitcher struct {
	mu     sync.RWMutex
	router RouterClientReplacer
	models *ai_models.Manager
}

var ErrRouterUnavailable = errors.New("model switching requires a running LLM router")

func NewModelSwitcher(router RouterClientReplacer, models *ai_models.Manager) *ModelSwitcher {
	return &ModelSwitcher{router: router, models: models}
}

func (s *ModelSwitcher) RLock()   { s.mu.RLock() }
func (s *ModelSwitcher) RUnlock() { s.mu.RUnlock() }
func (s *ModelSwitcher) Lock()    { s.mu.Lock() }
func (s *ModelSwitcher) Unlock()  { s.mu.Unlock() }

func (s *ModelSwitcher) SwitchModel(ref string, effort ...string) error {
	if s == nil || s.router == nil {
		return ErrRouterUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SwitchModelLocked(ref, effort...)
}

// SwitchModelLocked publishes a validated selection and its client under the
// same lock used by agent assembly and Chat/Resume. Failure keeps both unchanged.
func (s *ModelSwitcher) SwitchModelLocked(ref string, effort ...string) error {
	if s == nil || s.router == nil {
		return ErrRouterUnavailable
	}
	client, err := s.models.StreamClient(ref, effort...)
	if err != nil {
		return err
	}
	if err := s.models.SetActiveModel(ref, effort...); err != nil {
		return err
	}
	s.router.ReplaceClient(client)
	return nil
}

// NewModels wires settings inputs into the model manager at startup.
func NewModels(homeDir string) (*ai_models.Manager, error) {
	c := config.EnvAndFileConf
	return ai_models.New(homeDir, c.ProviderList, ai_models.Options{
		Model: c.Model, CompactionModel: c.CompactionModel,
		ContextWindow: c.OpenaiContextWindow, MaxOutputTokens: c.OpenaiMaxOutputTokens,
		CompactionContextWindow: c.CompactionOpenaiContextWindow, CompactionMaxOutputTokens: c.CompactionOpenaiMaxOutputTokens,
	})
}
