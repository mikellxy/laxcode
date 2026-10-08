package ai_models

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
	ReasoningEffort string
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
	in.ReasoningEffort = strings.TrimSpace(in.ReasoningEffort)
	if err := ValidateEffort(in.Model, in.ReasoningEffort); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidModelConfig, err)
	}
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
func (m *Manager) AddModelToSettings(input AddModelInput) (ModelConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := input.normalizeAndValidate(); err != nil {
		return ModelConfig{}, err
	}
	model := ModelConfig{
		ReasoningEffort: input.ReasoningEffort,
		ModelName:       input.Model,
		Limit:           &ModelLimit{Context: input.ContextWindow, Output: input.MaxOutputTokens},
	}

	runtimeProviders, err := appendModelToProviders(m.state.providers, input, model)
	if err != nil {
		return ModelConfig{}, err
	}
	runtimeCandidate := m.state
	runtimeCandidate.providers = runtimeProviders
	// 此前没有任何模型（延迟配置场景）：新模型自动成为活跃模型，维持
	// 「目录非空 ⟹ 活跃模型可解析」的校验不变式。
	activated := strings.TrimSpace(runtimeCandidate.active.Ref) == ""
	if activated {
		runtimeCandidate.active.Ref = modelRef(input.Provider, model.ModelName)
	}
	if err := runtimeCandidate.initializeSelection(); err != nil {
		return ModelConfig{}, fmt.Errorf("%w: %v", ErrInvalidModelConfig, err)
	}

	if !activated {
		if err := runtimeCandidate.setActiveModel(m.state.active.Ref, m.state.active.ReasoningEffort); err != nil {
			return ModelConfig{}, fmt.Errorf("%w: %v", ErrInvalidModelConfig, err)
		}
	}
	settingsPath := layout.UserSettings(m.homeDir)
	document, err := readSettings(settingsPath)
	if err != nil {
		return ModelConfig{}, err
	}
	var diskProviders []ProviderConfig
	if providerJSON, ok := document["provider_list"]; ok {
		if err := json.Unmarshal(providerJSON, &diskProviders); err != nil {
			return ModelConfig{}, fmt.Errorf("parse settings provider_list: %w", err)
		}
	} // 文件存在但缺 provider_list 键时按空目录处理
	diskProviders, err = appendModelToProviders(diskProviders, input, model)
	if err != nil {
		return ModelConfig{}, err
	}
	document["provider_list"], err = json.Marshal(diskProviders)
	if err != nil {
		return ModelConfig{}, fmt.Errorf("encode provider list: %w", err)
	}
	if activated {
		document["model"], _ = json.Marshal(runtimeCandidate.active.Ref)
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return ModelConfig{}, fmt.Errorf("encode settings: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return ModelConfig{}, fmt.Errorf("create settings dir: %w", err)
	}
	if err := writeSettingsAtomic(settingsPath, encoded); err != nil {
		return ModelConfig{}, err
	}
	m.state = runtimeCandidate
	// The returned configuration must not expose a pointer into the live catalog.
	limit := *model.Limit
	model.Limit = &limit
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
	return provider.AuthType != "oauth" && strings.TrimSpace(provider.OpenaiApiKey) == input.APIKey &&
		strings.TrimRight(strings.TrimSpace(provider.OpenaiBaseUrl), "/") == strings.TrimRight(input.BaseURL, "/")
}

// readSettings retains unrelated JSON fields and accepts legacy uppercase model
// keys. Writes normalize only the keys owned by this package.
func readSettings(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}
	if document == nil {
		return nil, errors.New("settings must be a JSON object")
	}
	for key, value := range document {
		for _, canonical := range []string{"provider_list", "model"} {
			if key != canonical && strings.EqualFold(key, canonical) {
				if _, exists := document[canonical]; !exists {
					document[canonical] = value
				}
				delete(document, key)
			}
		}
	}
	return document, nil
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
