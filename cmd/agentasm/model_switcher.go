package agentasm

import (
	"errors"
	"os"
	"sync"

	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
	"github.com/mikellxy/laxcode/internal/infrastructure/chatgpt"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	infrastructurerouter "github.com/mikellxy/laxcode/internal/infrastructure/llmrouter"
)

// ModelSwitcher serializes model changes against active Web requests.
type ModelSwitcher struct {
	mu      sync.RWMutex
	router  RouterClientReplacer
	homeDir string
}

var ErrRouterUnavailable = errors.New("model switching requires a running LLM router")

func NewModelSwitcher(router RouterClientReplacer, homes ...string) *ModelSwitcher {
	home, _ := os.UserHomeDir()
	if len(homes) > 0 {
		home = homes[0]
	}
	return &ModelSwitcher{router: router, homeDir: home}
}

// RLock and RUnlock keep SSE assembly and its Chat or Resume on one model.
// A switch waits until active calls release their read locks.
func (s *ModelSwitcher) RLock()   { s.mu.RLock() }
func (s *ModelSwitcher) RUnlock() { s.mu.RUnlock() }
func (s *ModelSwitcher) Lock()    { s.mu.Lock() }
func (s *ModelSwitcher) Unlock()  { s.mu.Unlock() }

// SwitchModel affects subsequent requests after active requests finish.
func (s *ModelSwitcher) SwitchModel(ref string, effort ...string) error {
	if s == nil || s.router == nil {
		return ErrRouterUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SwitchModelLocked(ref, effort...)
}

// SwitchModelLocked 完成与 SwitchModel 相同的切换，但要求调用方已持有写锁。
// 供 handleAddModel 在同一次加锁内完成「添加首个模型 → 激活 → 路由器替换
// 上游 client」，避免目录在两步之间被并发读取到不一致状态。
func (s *ModelSwitcher) SwitchModelLocked(ref string, effort ...string) error {
	if s == nil || s.router == nil {
		return ErrRouterUnavailable
	}
	resolved, err := config.ResolveModel(ref)
	if err != nil {
		return err
	}
	if len(effort) > 0 {
		resolved.ReasoningEffort = effort[0]
	}
	if err := chatgpt.ValidateEffort(resolved.UpstreamModel, resolved.ReasoningEffort); err != nil {
		return err
	}
	client := NewRouterClient(s.homeDir, resolved)
	if err := config.SetActiveModel(ref); err != nil {
		return err
	}
	config.EnvAndFileConf.ReasoningEffort = resolved.ReasoningEffort
	if config.EnvAndFileConf.CompactionModel == resolved.Ref {
		config.EnvAndFileConf.CompactionReasoningEffort = resolved.ReasoningEffort
	}
	s.router.ReplaceClient(client)
	return nil
}

func NewRouterClient(homeDir string, model config.ResolvedModel) domainrouter.StreamClient {
	if model.AuthType == "oauth" {
		return infrastructurerouter.NewChatGPTStreamClient(homeDir, model.CredentialRef, model.UpstreamModel).WithReasoningEffort(model.ReasoningEffort)
	}
	return infrastructurerouter.NewOpenAIStreamClient(model.OpenaiApiKey, model.OpenaiBaseUrl, model.UpstreamModel).WithReasoningEffort(model.ReasoningEffort)
}
