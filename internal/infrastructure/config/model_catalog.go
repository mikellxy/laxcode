package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

var (
	ErrInvalidModelConfig          = errors.New("invalid model configuration")
	ErrModelAlreadyExists          = errors.New("model already exists")
	ErrProviderCredentialsConflict = errors.New("provider credentials conflict")
)

// AddModelInput 是 Web 管理接口新增模型所需的最小配置。API key 与 base URL
// 属于 provider 级配置；同一 provider 下的所有模型共享二者。
type AddModelInput struct {
	Provider        string
	Model           string
	APIKey          string
	BaseURL         string
	ContextWindow   int
	MaxOutputTokens int
}

func (in *AddModelInput) normalizeAndValidate() error {
	in.Provider = strings.TrimSpace(in.Provider)
	in.Model = strings.TrimSpace(in.Model)
	in.APIKey = strings.TrimSpace(in.APIKey)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	if !validCatalogName(in.Provider) || in.Provider == envProviderName {
		return fmt.Errorf("%w: invalid or reserved provider name", ErrInvalidModelConfig)
	}
	if !validCatalogName(in.Model) {
		return fmt.Errorf("%w: invalid model name", ErrInvalidModelConfig)
	}
	if in.APIKey == "" || in.BaseURL == "" {
		return fmt.Errorf("%w: api key and base URL are required", ErrInvalidModelConfig)
	}
	parsed, err := url.ParseRequestURI(in.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%w: base URL must be an absolute HTTP(S) URL", ErrInvalidModelConfig)
	}
	if in.ContextWindow <= 0 || in.MaxOutputTokens <= 0 || in.MaxOutputTokens >= in.ContextWindow {
		return fmt.Errorf("%w: token limits must be positive and output must be smaller than context", ErrInvalidModelConfig)
	}
	return nil
}

// AddModelToSettings 将模型原子写入 ${home}/.laxcode/settings.json，并在写入
// 成功后更新当前进程的模型目录。调用方必须与模型切换共用同一把写锁。
func AddModelToSettings(homeDir string, input AddModelInput) (ModelConfig, error) {
	if err := input.normalizeAndValidate(); err != nil {
		return ModelConfig{}, err
	}
	model := ModelConfig{
		ModelName: input.Model,
		Limit:     &ModelLimit{Context: input.ContextWindow, Output: input.MaxOutputTokens},
	}

	runtimeProviders, err := appendModelToProviders(EnvAndFileConf.ProviderList, input, model)
	if err != nil {
		return ModelConfig{}, err
	}
	runtimeCandidate := EnvAndFileConf
	runtimeCandidate.ProviderList = runtimeProviders
	if err := runtimeCandidate.validateModelCatalog(); err != nil {
		return ModelConfig{}, fmt.Errorf("%w: %v", ErrInvalidModelConfig, err)
	}

	settingsPath := layout.UserSettings(homeDir)
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return ModelConfig{}, fmt.Errorf("read settings: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return ModelConfig{}, fmt.Errorf("parse settings: %w", err)
	}
	var diskProviders []ProviderConfig
	providerJSON, ok := document["provider_list"]
	if !ok {
		return ModelConfig{}, fmt.Errorf("parse settings: provider_list is required")
	}
	if err := json.Unmarshal(providerJSON, &diskProviders); err != nil {
		return ModelConfig{}, fmt.Errorf("parse settings provider_list: %w", err)
	}
	diskProviders, err = appendModelToProviders(diskProviders, input, model)
	if err != nil {
		return ModelConfig{}, err
	}
	document["provider_list"], err = json.Marshal(diskProviders)
	if err != nil {
		return ModelConfig{}, fmt.Errorf("encode provider list: %w", err)
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return ModelConfig{}, fmt.Errorf("encode settings: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeSettingsAtomic(settingsPath, encoded); err != nil {
		return ModelConfig{}, err
	}
	EnvAndFileConf.ProviderList = runtimeProviders
	return model, nil
}

func appendModelToProviders(providers []ProviderConfig, input AddModelInput, model ModelConfig) ([]ProviderConfig, error) {
	result := make([]ProviderConfig, len(providers))
	copy(result, providers)
	for i := range result {
		provider := &result[i]
		if provider.ProviderName != input.Provider {
			continue
		}
		if !credentialsEqual(provider, input) {
			return nil, fmt.Errorf("%w: provider %q already uses different credentials or base URL", ErrProviderCredentialsConflict, input.Provider)
		}
		for _, existing := range provider.ModelList {
			if existing.ModelName == input.Model {
				return nil, fmt.Errorf("%w: %s:%s", ErrModelAlreadyExists, input.Provider, input.Model)
			}
		}
		provider.ModelList = append(append([]ModelConfig(nil), provider.ModelList...), model)
		return result, nil
	}
	result = append(result, ProviderConfig{
		ProviderName: input.Provider, OpenaiApiKey: input.APIKey, OpenaiBaseUrl: input.BaseURL,
		ModelList: []ModelConfig{model},
	})
	return result, nil
}

func credentialsEqual(provider *ProviderConfig, input AddModelInput) bool {
	return strings.TrimSpace(provider.OpenaiApiKey) == input.APIKey &&
		strings.TrimRight(strings.TrimSpace(provider.OpenaiBaseUrl), "/") == strings.TrimRight(input.BaseURL, "/")
}

func writeSettingsAtomic(path string, content []byte) (err error) {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err = temp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary settings: %w", err)
	}
	if _, err = temp.Write(content); err != nil {
		return fmt.Errorf("write temporary settings: %w", err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary settings: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close temporary settings: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	if dirHandle, openErr := os.Open(dir); openErr == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}
