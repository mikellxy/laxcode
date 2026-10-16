package ai_models

import (
	"fmt"
	"strings"
	"unicode"
)

type ResolvedModel struct {
	ReasoningEffort string
	Ref             string
	ProviderName    string
	ModelName       string
	UpstreamModel   string
	OpenaiApiKey    string
	OpenaiBaseUrl   string
	// ContextWindow and MaxOutputTokens come from this model's limit.
	ContextWindow   int
	MaxOutputTokens int
}

func modelRef(providerName, modelName string) string {
	return providerName + ":" + modelName
}

func validCatalogName(name string) bool {
	return name != "" && !strings.ContainsRune(name, ':') &&
		strings.IndexFunc(name, unicode.IsSpace) < 0
}

func (c *modelState) resolveModel(ref string) (ResolvedModel, error) {
	providerName, modelName, ok := strings.Cut(ref, ":")
	if !ok || !validCatalogName(providerName) || !validCatalogName(modelName) {
		return ResolvedModel{}, fmt.Errorf("invalid model reference %q; expected provider:model", ref)
	}
	for _, provider := range c.providers {
		if provider.ProviderName != providerName {
			continue
		}
		for _, model := range provider.ModelList {
			if model.ModelName != modelName {
				continue
			}
			upstreamModel := model.UpstreamModel
			if upstreamModel == "" {
				upstreamModel = model.ModelName
			}
			resolved := ResolvedModel{
				ReasoningEffort: model.ReasoningEffort,
				Ref:             ref,
				ProviderName:    providerName,
				ModelName:       modelName,
				UpstreamModel:   upstreamModel,
				OpenaiApiKey:    provider.OpenaiApiKey,
				OpenaiBaseUrl:   provider.OpenaiBaseUrl,
				ContextWindow:   model.Limit.Context,
				MaxOutputTokens: model.Limit.Output,
			}
			return resolved, nil
		}
		return ResolvedModel{}, fmt.Errorf("model %q is not configured for provider %q", modelName, providerName)
	}
	return ResolvedModel{}, fmt.Errorf("provider %q is not configured", providerName)
}

func (c *modelState) validateModelCatalog() error {
	providers := make(map[string]struct{}, len(c.providers))
	for _, provider := range c.providers {
		if !validCatalogName(provider.ProviderName) {
			return fmt.Errorf("invalid provider_name %q", provider.ProviderName)
		}
		if _, exists := providers[provider.ProviderName]; exists {
			return fmt.Errorf("duplicate provider_name %q", provider.ProviderName)
		}
		providers[provider.ProviderName] = struct{}{}
		switch provider.AuthType {
		case "", "api_key":
			if strings.TrimSpace(provider.OpenaiApiKey) == "" || strings.TrimSpace(provider.OpenaiBaseUrl) == "" {
				return fmt.Errorf("provider %q requires openai_api_key and openai_base_url", provider.ProviderName)
			}

		default:
			return fmt.Errorf("unsupported auth_type %q", provider.AuthType)
		}
		if len(provider.ModelList) == 0 {
			return fmt.Errorf("provider %q requires at least one model", provider.ProviderName)
		}
		models := make(map[string]struct{}, len(provider.ModelList))
		for _, model := range provider.ModelList {
			upstream := model.UpstreamModel
			if upstream == "" {
				upstream = model.ModelName
			}
			if err := ValidateEffort(upstream, model.ReasoningEffort); err != nil {
				return err
			}
			if !validCatalogName(model.ModelName) {
				return fmt.Errorf("invalid model_name %q for provider %q", model.ModelName, provider.ProviderName)
			}
			if model.Limit == nil ||
				(model.Limit.Context <= 0 || model.Limit.Output <= 0 || model.Limit.Output >= model.Limit.Context) {
				return fmt.Errorf("invalid limit for model %q of provider %q: context and output must be positive and output must be smaller than context",
					model.ModelName, provider.ProviderName)
			}
			if _, exists := models[model.ModelName]; exists {
				return fmt.Errorf("duplicate model_name %q for provider %q", model.ModelName, provider.ProviderName)
			}
			models[model.ModelName] = struct{}{}
		}
	}
	return nil
}
