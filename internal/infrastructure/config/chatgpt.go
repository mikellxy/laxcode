package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mikellxy/laxcode/internal/infrastructure/chatgpt"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

const ChatGPTProvider = "openai-chatgpt"

// SaveChatGPTModels imports the signed-in account catalog without storing any
// token in settings.json. Caller holds the model switcher's write lock.
func SaveChatGPTModels(homeDir string, models []chatgpt.Model) error {
	if len(models) == 0 {
		return errors.New("ChatGPT model catalog is empty")
	}
	provider := ProviderConfig{ProviderName: ChatGPTProvider, AuthType: "oauth", CredentialRef: chatgpt.CredentialRef, OpenaiBaseUrl: chatgpt.BaseURL}
	seen := map[string]bool{}
	for _, model := range models {
		if !validCatalogName(model.Slug) || seen[model.Slug] {
			return fmt.Errorf("invalid or duplicate ChatGPT model %q", model.Slug)
		}
		seen[model.Slug] = true
		effort := ""
		if len(chatgpt.ReasoningEfforts(model.Slug)) > 0 {
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
	candidate := EnvAndFileConf
	var err error
	candidate.ProviderList, err = replace(candidate.ProviderList)
	if err != nil {
		return err
	}
	if candidate.Model == "" {
		candidate.Model = modelRef(ChatGPTProvider, models[0].Slug)
	}
	if _, err := candidate.resolveModel(candidate.Model); err != nil {
		candidate.Model = modelRef(ChatGPTProvider, models[0].Slug)
	}
	if strings.HasPrefix(candidate.CompactionModel, ChatGPTProvider+":") && candidate.compactionConfigured {
		if _, err := candidate.resolveModel(candidate.CompactionModel); err != nil {
			return fmt.Errorf("configured compaction model is absent from the new catalog: %w", err)
		}
	}
	if err := candidate.validateModelCatalog(); err != nil {
		return err
	}
	document := map[string]json.RawMessage{}
	data, err := os.ReadFile(layout.UserSettings(homeDir))
	if err == nil {
		if err := json.Unmarshal(data, &document); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if document == nil {
		return errors.New("settings must be a JSON object")
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
	if strings.HasPrefix(candidate.Model, ChatGPTProvider+":") {
		document["model"], _ = json.Marshal(candidate.Model)
	}
	data, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(layout.Root(homeDir), 0700); err != nil {
		return err
	}
	if err := writeSettingsAtomic(layout.UserSettings(homeDir), append(data, '\n')); err != nil {
		return err
	}
	EnvAndFileConf.ProviderList = candidate.ProviderList
	return EnvAndFileConf.setActiveModel(candidate.Model)
}
