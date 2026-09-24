package agentasm

import (
	"errors"
	"sync"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	infrastructurerouter "github.com/mikellxy/laxcode/internal/infrastructure/llmrouter"
)

// ModelSwitcher is the single model-switching entry point for CLI and SSE.
// A long-lived CLI service needs its LLM client replaced; SSE creates a new
// service per request and only needs the router and runtime configuration.
type ModelSwitcher struct {
	mu      sync.RWMutex
	router  RouterClientReplacer
	service *reactservice.ReActService
}

var ErrRouterUnavailable = errors.New("model switching requires a running LLM router")

func NewModelSwitcher(router RouterClientReplacer, service *reactservice.ReActService) *ModelSwitcher {
	return &ModelSwitcher{router: router, service: service}
}

// RLock and RUnlock keep SSE assembly and its Chat or Resume on one model.
// A switch waits until active calls release their read locks.
func (s *ModelSwitcher) RLock()   { s.mu.RLock() }
func (s *ModelSwitcher) RUnlock() { s.mu.RUnlock() }
func (s *ModelSwitcher) Lock()    { s.mu.Lock() }
func (s *ModelSwitcher) Unlock()  { s.mu.Unlock() }

// SwitchModel affects subsequent requests. Callers with a long-lived service
// must invoke it between Chat calls, when that service is idle.
func (s *ModelSwitcher) SwitchModel(ref string) error {
	if s == nil || s.router == nil {
		return ErrRouterUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SwitchModelLocked(ref)
}

// SwitchModelLocked 完成与 SwitchModel 相同的切换，但要求调用方已持有写锁。
// 供 handleAddModel 在同一次加锁内完成「添加首个模型 → 激活 → 路由器替换
// 上游 client」，避免目录在两步之间被并发读取到不一致状态。
func (s *ModelSwitcher) SwitchModelLocked(ref string) error {
	if s == nil || s.router == nil {
		return ErrRouterUnavailable
	}
	resolved, err := config.ResolveModel(ref)
	if err != nil {
		return err
	}
	client := infrastructurerouter.NewOpenAIStreamClient(
		resolved.OpenaiApiKey, resolved.OpenaiBaseUrl, resolved.UpstreamModel)
	if err := config.SetActiveModel(ref); err != nil {
		return err
	}
	s.router.ReplaceClient(client)
	if s.service != nil {
		s.service.ReplaceLLMClient(newMainProvider())
	}
	return nil
}
