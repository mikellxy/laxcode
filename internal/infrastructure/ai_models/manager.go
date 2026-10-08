// Package ai_models owns the model catalog, connection construction and OAuth.
package ai_models

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
)

const (
	DefaultContextWindow   = 200_000
	DefaultMaxOutputTokens = 16_384
)

// Options contains startup selections and raw fallback limits, not derived values.
type Options struct {
	Model                     string
	CompactionModel           string
	ContextWindow             int
	MaxOutputTokens           int
	CompactionContextWindow   int
	CompactionMaxOutputTokens int
}

func DefaultOptions() Options {
	return Options{ContextWindow: DefaultContextWindow, MaxOutputTokens: DefaultMaxOutputTokens}
}

// Manager is shared by the composition root and model management endpoints.
// ModelSwitcher coordinates publishing selections with replacing the router client.
// ReActService receives only LLMClient instances with their own limit snapshots.
type Manager struct {
	mu      sync.RWMutex
	homeDir string
	state   modelState
}

type modelState struct {
	options                   Options
	providers                 []ProviderConfig
	active                    ResolvedModel
	compaction                ResolvedModel
	compactionEnv             modelEnvironment
	contextOverride           bool
	outputOverride            bool
	compactionContextOverride bool
	compactionOutputOverride  bool
}

// New parses provider_list once. An empty catalog is valid for Web onboarding.
// Environment-backed models are runtime-only and never persisted in provider_list.
func New(homeDir string, providerList json.RawMessage, opts Options) (*Manager, error) {
	for key, value := range map[string]*int{
		"OPENAI_CONTEXT_WINDOW":               &opts.ContextWindow,
		"OPENAI_MAX_OUTPUT_TOKENS":            &opts.MaxOutputTokens,
		"COMPACTION_OPENAI_CONTEXT_WINDOW":    &opts.CompactionContextWindow,
		"COMPACTION_OPENAI_MAX_OUTPUT_TOKENS": &opts.CompactionMaxOutputTokens,
	} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("%s must be an integer: %w", key, err)
			}
			*value = parsed
		}
	}
	if ref := os.Getenv("COMPACTION_MODEL"); ref != "" {
		opts.CompactionModel = ref
	}
	s := modelState{options: opts, active: ResolvedModel{Ref: opts.Model},
		compactionEnv:             readModelEnvironment("OPENAI_COMPACTION_"),
		contextOverride:           strings.TrimSpace(os.Getenv("OPENAI_CONTEXT_WINDOW")) != "",
		outputOverride:            strings.TrimSpace(os.Getenv("OPENAI_MAX_OUTPUT_TOKENS")) != "",
		compactionContextOverride: strings.TrimSpace(os.Getenv("COMPACTION_OPENAI_CONTEXT_WINDOW")) != "",
		compactionOutputOverride:  strings.TrimSpace(os.Getenv("COMPACTION_OPENAI_MAX_OUTPUT_TOKENS")) != "",
	}
	if len(providerList) > 0 {
		if err := json.Unmarshal(providerList, &s.providers); err != nil {
			return nil, fmt.Errorf("parse provider_list: %w", err)
		}
	}
	for _, p := range s.providers {
		if p.ProviderName == envProviderName {
			return nil, fmt.Errorf("provider_name %q is reserved for environment configuration", envProviderName)
		}
	}
	mainEnv := readModelEnvironment("OPENAI_")
	if n := mainEnv.count(); n != 0 && n != 3 {
		return nil, errors.New("OPENAI_API_KEY, OPENAI_BASE_URL and OPENAI_MODEL_NAME must be set together")
	}
	if mainEnv.count() == 3 {
		s.providers = append(s.providers, ProviderConfig{
			ProviderName: envProviderName, OpenaiApiKey: mainEnv.apiKey, OpenaiBaseUrl: mainEnv.baseURL,
			ModelList: []ModelConfig{{ModelName: envModelName, UpstreamModel: mainEnv.model}},
		})
		s.active.Ref = modelRef(envProviderName, envModelName)
	}
	if err := s.initializeSelection(); err != nil {
		return nil, err
	}
	return &Manager{homeDir: homeDir, state: s}, nil
}

func (c *modelState) initializeSelection() error {
	if err := c.validateModelCatalog(); err != nil {
		return err
	}
	if c.options.ContextWindow <= 0 || c.options.MaxOutputTokens <= 0 || c.options.MaxOutputTokens >= c.options.ContextWindow {
		return errors.New("openai context window must be positive and max output tokens smaller than context window")
	}
	if strings.TrimSpace(c.active.Ref) == "" && len(c.providers) > 0 {
		c.active.Ref = modelRef(c.providers[0].ProviderName, c.providers[0].ModelList[0].ModelName)
	}
	if c.active.Ref == "" {
		c.active = ResolvedModel{ContextWindow: c.options.ContextWindow, MaxOutputTokens: c.options.MaxOutputTokens}
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
	compaction, err := c.resolveAuxiliaryModel("COMPACTION_MODEL", c.options.CompactionModel,
		c.compactionEnv, main)
	if err != nil {
		return err
	}
	compaction.ContextWindow = effectiveAuxiliaryBudget(compaction.ContextWindow, c.options.CompactionContextWindow, compaction.hasLimit, c.compactionContextOverride)
	compaction.MaxOutputTokens = effectiveAuxiliaryBudget(compaction.MaxOutputTokens, c.options.CompactionMaxOutputTokens, compaction.hasLimit, c.compactionOutputOverride)
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
	return m.state.setActiveModel(ref, effort...)
}

// StreamClient constructs a client without changing the current selection.
// Token refresh is performed by the OAuth transport when a request is sent.
func (m *Manager) StreamClient(ref string, effort ...string) (domainrouter.StreamClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	resolved, err := m.state.selection(ref, effort...)
	if err != nil {
		return nil, err
	}
	if resolved.AuthType == "oauth" {
		return newChatGPTStreamClient(m.homeDir, resolved.CredentialRef, resolved.UpstreamModel).WithReasoningEffort(resolved.ReasoningEffort), nil
	}
	return newOpenAIStreamClient(resolved.OpenaiApiKey, resolved.OpenaiBaseUrl, resolved.UpstreamModel).WithReasoningEffort(resolved.ReasoningEffort), nil
}

// ProviderInfo deliberately omits credentials and endpoints from catalog reads.
type ProviderInfo struct {
	ProviderName string
	AuthType     string
	ModelList    []ModelInfo
}

type ModelInfo struct {
	Ref              string
	ModelName        string
	UpstreamModel    string
	DisplayName      string
	ReasoningEffort  string
	ReasoningEfforts []string
}

func (m *Manager) List() []ProviderInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ProviderInfo, 0, len(m.state.providers))
	for _, p := range m.state.providers {
		info := ProviderInfo{ProviderName: p.ProviderName, AuthType: p.AuthType, ModelList: make([]ModelInfo, 0, len(p.ModelList))}
		for _, model := range p.ModelList {
			upstream := model.UpstreamModel
			if upstream == "" {
				upstream = model.ModelName
			}
			info.ModelList = append(info.ModelList, ModelInfo{Ref: modelRef(p.ProviderName, model.ModelName), ModelName: model.ModelName,
				UpstreamModel: model.UpstreamModel, DisplayName: model.DisplayName, ReasoningEffort: model.ReasoningEffort, ReasoningEfforts: ReasoningEfforts(upstream)})
		}
		result = append(result, info)
	}
	return result
}
