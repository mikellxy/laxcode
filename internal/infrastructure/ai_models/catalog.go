package ai_models

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

// ModelLimit 是 model_list 条目里的模型级 token 预算（limit.context /
// limit.output）：声明后作为该模型 LLM client 的 bucket，覆盖全局窗口配置。
type ModelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type ModelConfig struct {
	DisplayName     string      `json:"display_name,omitempty"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
	ModelName       string      `json:"model_name"`
	UpstreamModel   string      `json:"upstream_model,omitempty"`
	Limit           *ModelLimit `json:"limit,omitempty"`
}

type ProviderConfig struct {
	AuthType      string        `json:"auth_type,omitempty"`
	CredentialRef string        `json:"credential_ref,omitempty"`
	OpenaiApiKey  string        `json:"openai_api_key"`
	OpenaiBaseUrl string        `json:"openai_base_url"`
	ProviderName  string        `json:"provider_name"`
	ModelList     []ModelConfig `json:"model_list"`
}

type ResolvedModel struct {
	AuthType        string
	CredentialRef   string
	ReasoningEffort string
	Ref             string
	ProviderName    string
	ModelName       string
	UpstreamModel   string
	OpenaiApiKey    string
	OpenaiBaseUrl   string
	// ContextWindow / MaxOutputTokens 是该模型的生效 token 预算：模型级
	// limit 优先，未声明时回退全局 openai_context_window /
	// openai_max_output_tokens。
	ContextWindow   int
	MaxOutputTokens int
	hasLimit        bool
}

const (
	envProviderName = "env_provider"
	envModelName    = "env_model"
)

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
				AuthType: provider.AuthType, CredentialRef: provider.CredentialRef, ReasoningEffort: model.ReasoningEffort,
				Ref:             ref,
				ProviderName:    providerName,
				ModelName:       modelName,
				UpstreamModel:   upstreamModel,
				OpenaiApiKey:    provider.OpenaiApiKey,
				OpenaiBaseUrl:   provider.OpenaiBaseUrl,
				ContextWindow:   c.options.ContextWindow,
				MaxOutputTokens: c.options.MaxOutputTokens,
			}
			// 模型级 limit 覆盖全局窗口；validateModelCatalog 保证 limit
			// 一旦声明则两项均合法，未声明（nil）时保持全局回退值。
			if model.Limit != nil {
				resolved.ContextWindow = model.Limit.Context
				resolved.MaxOutputTokens = model.Limit.Output
				resolved.hasLimit = true
			}
			// 显式环境变量优先于配置文件的模型级 limit。
			if c.contextOverride {
				resolved.ContextWindow = c.options.ContextWindow
			}
			if c.outputOverride {
				resolved.MaxOutputTokens = c.options.MaxOutputTokens
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
		if provider.ProviderName == envProviderName &&
			(len(provider.ModelList) != 1 || provider.ModelList[0].ModelName != envModelName ||
				provider.ModelList[0].UpstreamModel == "") {
			return fmt.Errorf("provider_name %q is reserved for environment configuration", envProviderName)
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
		case "oauth":
			if provider.CredentialRef != CredentialRef || strings.TrimRight(provider.OpenaiBaseUrl, "/") != strings.TrimRight(BaseURL, "/") || provider.OpenaiApiKey != "" {
				return fmt.Errorf("provider %q requires a ChatGPT credential reference and the official endpoint", provider.ProviderName)
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
			if model.Limit != nil &&
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

type modelEnvironment struct {
	apiKey, baseURL, model string
}

func readModelEnvironment(prefix string) modelEnvironment {
	return modelEnvironment{
		apiKey:  strings.TrimSpace(os.Getenv(prefix + "API_KEY")),
		baseURL: strings.TrimSpace(os.Getenv(prefix + "BASE_URL")),
		model:   strings.TrimSpace(os.Getenv(prefix + "MODEL_NAME")),
	}
}

func (e modelEnvironment) count() int {
	count := 0
	for _, value := range []string{e.apiKey, e.baseURL, e.model} {
		if value != "" {
			count++
		}
	}
	return count
}

func (e modelEnvironment) apply(resolved *ResolvedModel) {
	if e.count() == 0 {
		return
	}
	resolved.Ref = modelRef(envProviderName, envModelName)
	// An explicit environment credential switches billing to API-key auth.
	if e.apiKey != "" || e.baseURL != "" {
		resolved.AuthType, resolved.CredentialRef = "", ""
	}
	if e.apiKey != "" {
		resolved.OpenaiApiKey = e.apiKey
	}
	if e.baseURL != "" {
		resolved.OpenaiBaseUrl = e.baseURL
	}
	if e.model != "" {
		resolved.UpstreamModel = e.model
	}
}

func effectiveAuxiliaryBudget(modelValue, configuredValue int, hasModelLimit bool, envOverride bool) int {
	if envOverride {
		return configuredValue
	}
	if hasModelLimit {
		return modelValue
	}
	if configuredValue != 0 {
		return configuredValue
	}
	return modelValue
}

// resolveAuxiliaryModel 从模型目录解析文件引用，再逐项应用非空环境变量。
// 完整的环境配置无需依赖文件引用；未配置压缩模型时继承主模型。
func (c *modelState) resolveAuxiliaryModel(key, ref string, env modelEnvironment, fallback ResolvedModel) (ResolvedModel, error) {
	resolved := fallback
	if env.count() == 3 {
		resolved = ResolvedModel{
			ContextWindow: c.options.ContextWindow, MaxOutputTokens: c.options.MaxOutputTokens,
		}
	} else if ref != "" {
		var err error
		resolved, err = c.resolveModel(ref)
		if err != nil {
			return ResolvedModel{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	if env.model != "" && env.model != resolved.UpstreamModel {
		// 模型名被环境变量替换后，原模型的 limit 不再适用。
		resolved.ContextWindow = c.options.ContextWindow
		resolved.MaxOutputTokens = c.options.MaxOutputTokens
		resolved.hasLimit = false
		resolved.ReasoningEffort = ""
	}
	env.apply(&resolved)
	return resolved, nil
}
