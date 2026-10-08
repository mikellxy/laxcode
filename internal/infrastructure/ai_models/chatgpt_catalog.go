package ai_models

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

const ChatGPTProvider = "openai-chatgpt"

// SaveChatGPTModels imports the signed-in account catalog without storing any
// token in settings.json. Caller holds the model switcher's write lock.
func (m *Manager) SaveChatGPTModels(models []Model) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(models) == 0 {
		return errors.New("ChatGPT model catalog is empty")
	}
	provider := ProviderConfig{ProviderName: ChatGPTProvider, AuthType: "oauth", CredentialRef: CredentialRef, OpenaiBaseUrl: BaseURL}
	seen := map[string]bool{}
	for _, model := range models {
		if !validCatalogName(model.Slug) || seen[model.Slug] {
			return fmt.Errorf("invalid or duplicate ChatGPT model %q", model.Slug)
		}
		seen[model.Slug] = true
		effort := ""
		if len(ReasoningEfforts(model.Slug)) > 0 {
			effort = "medium"
		}
		provider.ModelList = append(provider.ModelList, ModelConfig{ModelName: model.Slug, DisplayName: model.DisplayName, ReasoningEffort: effort})
	}
	replace := func(providers []ProviderConfig) ([]ProviderConfig, error) {
		result := append([]ProviderConfig(nil), providers...)
		for i, p := range result {
			if p.ProviderName == ChatGPTProvider {
				if p.AuthType != "oauth" {
					return nil, fmt.Errorf("provider %q is already used by API-key auth", ChatGPTProvider)
				}
				updated := provider
				updated.ModelList = append([]ModelConfig(nil), provider.ModelList...)
				for j := range updated.ModelList {
					for _, previous := range p.ModelList {
						if previous.ModelName == updated.ModelList[j].ModelName {
							updated.ModelList[j].Limit = previous.Limit
							updated.ModelList[j].ReasoningEffort = previous.ReasoningEffort
							break
						}
					}
				}
				result[i] = updated
				return result, nil
			}
		}
		return append(result, provider), nil
	}
	candidate := m.state
	var err error
	candidate.providers, err = replace(candidate.providers)
	if err != nil {
		return err
	}
	if candidate.active.Ref == "" {
		candidate.active.Ref = modelRef(ChatGPTProvider, models[0].Slug)
	}
	if _, err := candidate.resolveModel(candidate.active.Ref); err != nil {
		candidate.active.Ref = modelRef(ChatGPTProvider, models[0].Slug)
	}
	if strings.HasPrefix(candidate.options.CompactionModel, ChatGPTProvider+":") {
		if _, err := candidate.resolveModel(candidate.options.CompactionModel); err != nil {
			return fmt.Errorf("configured compaction model is absent from the new catalog: %w", err)
		}
	}
	if err := candidate.initializeSelection(); err != nil {
		return err
	}
	// Importing another provider must not reset the selected API-key model's
	// runtime effort, because its router client is retained by the caller.
	if candidate.active.Ref == m.state.active.Ref && !strings.HasPrefix(candidate.active.Ref, ChatGPTProvider+":") {
		if err := candidate.setActiveModel(candidate.active.Ref, m.state.active.ReasoningEffort); err != nil {
			return err
		}
	}
	document, err := readSettings(layout.UserSettings(m.homeDir))
	if err != nil {
		return err
	}
	var providers []ProviderConfig
	if raw, ok := document["provider_list"]; ok {
		if err := json.Unmarshal(raw, &providers); err != nil {
			return err
		}
	}
	providers, err = replace(providers)
	if err != nil {
		return err
	}
	document["provider_list"], err = json.Marshal(providers)
	if err != nil {
		return err
	}
	// A persisted env_provider is intentionally not created by this import.
	if strings.HasPrefix(candidate.active.Ref, ChatGPTProvider+":") {
		document["model"], _ = json.Marshal(candidate.active.Ref)
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(layout.Root(m.homeDir), 0700); err != nil {
		return err
	}
	if err := writeSettingsAtomic(layout.UserSettings(m.homeDir), append(data, '\n')); err != nil {
		return err
	}
	m.state = candidate
	return nil
}
