package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAddModelToSettingsPersistsAndUpdatesRuntimeCatalog(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model":"p:main",
		"custom_setting":{"preserved":true},
		"provider_list":[{
			"provider_name":"p",
			"openai_api_key":"secret",
			"openai_base_url":"https://example.com/v1",
			"model_list":[{"model_name":"main"}]
		}]
	}`)
	if err := ParseEnvAndFile(); err != nil {
		t.Fatal(err)
	}
	added, err := AddModelToSettings(home, AddModelInput{
		Provider: "p", Model: "new-model", APIKey: "secret", BaseURL: "https://example.com/v1/",
		ContextWindow: 200_000, MaxOutputTokens: 16_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.ModelName != "new-model" || added.Limit == nil || added.Limit.Context != 200_000 || added.Limit.Output != 16_000 {
		t.Fatalf("added model=%+v", added)
	}
	resolved, err := ResolveModel("p:new-model")
	if err != nil || resolved.ContextWindow != 200_000 || resolved.MaxOutputTokens != 16_000 {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	if EnvAndFileConf.Model != "p:main" {
		t.Fatalf("adding a model changed current model to %q", EnvAndFileConf.Model)
	}

	settingsPath := filepath.Join(home, ".laxcode", "settings.json")
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		CustomSetting map[string]bool  `json:"custom_setting"`
		ProviderList  []ProviderConfig `json:"provider_list"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	if !document.CustomSetting["preserved"] || len(document.ProviderList) != 1 || len(document.ProviderList[0].ModelList) != 2 {
		t.Fatalf("persisted settings=%s", content)
	}
	info, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings permissions=%#o, want 0600", info.Mode().Perm())
	}
}

func TestAddModelToSettingsRejectsDuplicateAndCredentialConflict(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, modelSettings("p", "main"))
	if err := ParseEnvAndFile(); err != nil {
		t.Fatal(err)
	}

	base := AddModelInput{Provider: "p", APIKey: "sk-file-key", BaseURL: "https://file.example.com/v1", ContextWindow: 100, MaxOutputTokens: 10}
	duplicate := base
	duplicate.Model = "main"
	if _, err := AddModelToSettings(home, duplicate); !errors.Is(err, ErrModelAlreadyExists) {
		t.Fatalf("duplicate error=%v", err)
	}
	conflict := base
	conflict.Model = "another"
	conflict.APIKey = "different"
	if _, err := AddModelToSettings(home, conflict); !errors.Is(err, ErrProviderCredentialsConflict) {
		t.Fatalf("credential conflict error=%v", err)
	}
}

func TestAddModelToSettingsCreatesProvider(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, modelSettings("p", "main"))
	if err := ParseEnvAndFile(); err != nil {
		t.Fatal(err)
	}
	_, err := AddModelToSettings(home, AddModelInput{
		Provider: "second", Model: "chat", APIKey: "secret-2", BaseURL: "https://second.example/v1",
		ContextWindow: 128_000, MaxOutputTokens: 8_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveModel("second:chat")
	if err != nil || resolved.OpenaiBaseUrl != "https://second.example/v1" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
}
