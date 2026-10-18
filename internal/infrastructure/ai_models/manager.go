// Package ai_models owns each user's model catalog and runtime selections.
package ai_models

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"os"
	"strings"
	"sync"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

// Manager is shared by the composition root and model management endpoints.
// ModelSwitcher coordinates publishing selections with active requests.
// ReActService receives only LLMClient instances with their own limit snapshots.
type Manager struct {
	mu      sync.RWMutex
	homeDir string
	state   modelState
}

type modelState struct {
	providers     []config.Provider
	active        ResolvedModel
	compaction    ResolvedModel
	compactionRef string
}

// New consumes merged configuration without reading its sources.
func New(homeDir string, conf config.Conf) (*Manager, error) {
	s := modelState{active: ResolvedModel{Ref: conf.Model}, compactionRef: conf.CompactionModel,
		providers: make([]config.Provider, len(conf.ProviderList))}
	// Keep runtime mutations isolated from the startup configuration and other managers.
	for i := range conf.ProviderList {
		s.providers[i] = conf.ProviderList[i].DeepCopy()
	}
	if err := s.initializeSelection(); err != nil {
		return nil, err
	}
	return &Manager{homeDir: homeDir, state: s}, nil
}

// LoadSelection restores the user preference over the deployment catalog at startup.
func (m *Manager) LoadSelection() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(layout.ModelSelection(m.homeDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved struct {
		Model  string `json:"model"`
		Effort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("read model selection: %w", err)
	}
	// A removed model falls back to the platform default after an upgrade.
	if _, err := m.state.selection(saved.Model, saved.Effort); err != nil {
		return nil
	}
	return m.state.setActiveModel(saved.Model, saved.Effort)
}

func (c *modelState) initializeSelection() error {
	if err := c.validateModelCatalog(); err != nil {
		return err
	}
	if strings.TrimSpace(c.active.Ref) == "" && len(c.providers) > 0 {
		c.active.Ref = modelRef(c.providers[0].ProviderName, c.providers[0].ModelList[0].ModelName)
	}
	if c.active.Ref == "" {
		c.active = ResolvedModel{}
		c.compaction = c.active
		return nil
	}
	return c.setActiveModel(c.active.Ref)
}

func (c *modelState) selection(ref string, effort ...string) (ResolvedModel, error) {
	resolved, err := c.resolveModel(ref)
	if err != nil {
		return ResolvedModel{}, err
	}
	if len(effort) > 0 {
		resolved.ReasoningEffort = effort[0]
	}
	if err := ValidateEffort(resolved.UpstreamModel, resolved.ReasoningEffort); err != nil {
		return ResolvedModel{}, err
	}
	if err := validateResolvedBudget(resolved); err != nil {
		return ResolvedModel{}, err
	}
	return resolved, nil
}

func validateResolvedBudget(model ResolvedModel) error {
	if model.ContextWindow <= 0 || model.MaxOutputTokens <= 0 || model.MaxOutputTokens >= model.ContextWindow {
		return fmt.Errorf("invalid limit for %q: context and output must be positive and output smaller than context", model.Ref)
	}
	return nil
}

func (c *modelState) setActiveModel(ref string, effort ...string) error {
	main, err := c.selection(ref, effort...)
	if err != nil {
		return err
	}
	compaction := main
	if c.compactionRef != "" {
		compaction, err = c.selection(c.compactionRef)
		if err != nil {
			return fmt.Errorf("compaction_model: %w", err)
		}
	}
	if compaction.Ref == main.Ref && compaction.UpstreamModel == main.UpstreamModel {
		compaction.ReasoningEffort = main.ReasoningEffort
	}
	if err := validateResolvedBudget(compaction); err != nil {
		return err
	}
	if err := ValidateEffort(compaction.UpstreamModel, compaction.ReasoningEffort); err != nil {
		return err
	}
	c.active, c.compaction = main, compaction
	return nil
}

func (m *Manager) Resolve(ref string) (ResolvedModel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.selection(ref)
}

func (m *Manager) Active() ResolvedModel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.active
}

func (m *Manager) Compaction() ResolvedModel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.compaction
}

// SetActiveModel updates runtime selections without changing settings defaults.
func (m *Manager) SetActiveModel(ref string, effort ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate := m.state
	if err := candidate.setActiveModel(ref, effort...); err != nil {
		return err
	}
	dir := layout.Root(m.homeDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]string{"model": candidate.active.Ref, "reasoning_effort": candidate.active.ReasoningEffort})
	if err != nil {
		return err
	}
	if err := config.WriteSettingsAtomic(layout.ModelSelection(m.homeDir), data); err != nil {
		return err
	}
	m.state = candidate
	return nil
}

// ProviderInfo deliberately omits credentials and endpoints from catalog reads.
type ProviderInfo struct {
	ProviderName string
	ModelList    []ModelInfo
}

type ModelInfo struct {
	Ref              string
	ModelName        string
	UpstreamModel    string
	ReasoningEffort  string
	ReasoningEfforts []string
}

func (m *Manager) List() []ProviderInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ProviderInfo, 0, len(m.state.providers))
	for _, p := range m.state.providers {
		info := ProviderInfo{ProviderName: p.ProviderName, ModelList: make([]ModelInfo, 0, len(p.ModelList))}
		for _, model := range p.ModelList {
			upstream := model.UpstreamModel
			if upstream == "" {
				upstream = model.ModelName
			}
			info.ModelList = append(info.ModelList, ModelInfo{Ref: modelRef(p.ProviderName, model.ModelName), ModelName: model.ModelName,
				UpstreamModel: model.UpstreamModel, ReasoningEffort: model.ReasoningEffort, ReasoningEfforts: ReasoningEfforts(upstream)})
		}
		result = append(result, info)
	}
	return result
}
