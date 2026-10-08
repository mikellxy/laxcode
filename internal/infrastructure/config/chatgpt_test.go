package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/chatgpt"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func TestChatGPTCatalogPersistsAndResolvesAfterRestart(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := ParseEnvAndFile(); err != nil {
		t.Fatal(err)
	}
	models := []chatgpt.Model{{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol"}, {Slug: "another-model", DisplayName: "Another"}}
	if err := SaveChatGPTModels(home, models); err != nil {
		t.Fatal(err)
	}
	assertResolved := func() {
		t.Helper()
		resolved, err := ResolveModel("openai-chatgpt:gpt-6.1-sol")
		if err != nil || resolved.AuthType != "oauth" || resolved.CredentialRef != chatgpt.CredentialRef || resolved.OpenaiApiKey != "" || resolved.OpenaiBaseUrl != chatgpt.BaseURL || resolved.ReasoningEffort != "medium" {
			t.Fatalf("OAuth resolution=%+v err=%v", resolved, err)
		}
		if EnvAndFileConf.CompactionAuthType != "oauth" || EnvAndFileConf.CompactionCredentialRef != chatgpt.CredentialRef {
			t.Fatal("compaction did not inherit OAuth")
		}
	}
	assertResolved()
	data, err := os.ReadFile(layout.UserSettings(home))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"access_token", "refresh_token", "id_token"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("settings contain a token field")
		}
	}
	if err := ParseEnvAndFile(); err != nil {
		t.Fatal(err)
	}
	assertResolved()
	before := string(data)
	if err := SaveChatGPTModels(home, []chatgpt.Model{{Slug: "bad:model"}}); err == nil {
		t.Fatal("invalid model accepted")
	}
	data, _ = os.ReadFile(layout.UserSettings(home))
	if string(data) != before {
		t.Fatal("rejected catalog changed settings")
	}
	// Reconnect imports the catalog once and preserves other settings/providers.
	var document map[string]json.RawMessage
	_ = json.Unmarshal(data, &document)
	document["custom"] = json.RawMessage(`true`)
	data, _ = json.Marshal(document)
	if err := os.WriteFile(layout.UserSettings(home), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveChatGPTModels(home, models); err != nil {
		t.Fatal(err)
	}
	EnvAndFileConf.ProviderList[0].ModelList[0].Limit = &ModelLimit{Context: 300000, Output: 12000}
	EnvAndFileConf.ProviderList[0].ModelList[0].ReasoningEffort = "high"
	if err := SaveChatGPTModels(home, models); err != nil {
		t.Fatal(err)
	}
	if EnvAndFileConf.ProviderList[0].ModelList[0].Limit.Context != 300000 || EnvAndFileConf.ProviderList[0].ModelList[0].ReasoningEffort != "high" {
		t.Fatal("reconnect discarded configured model defaults")
	}
	data, _ = os.ReadFile(layout.UserSettings(home))
	if !strings.Contains(string(data), `"custom": true`) || len(EnvAndFileConf.ProviderList) != 1 {
		t.Fatal("reconnect duplicated provider or lost settings")
	}
}

func TestOAuthCatalogRejectsUnsafeEndpointAndUnsupportedEffort(t *testing.T) {
	for _, change := range []string{"endpoint", "credentials", "effort"} {
		t.Run(change, func(t *testing.T) {
			c := envAndFileConf{Model: "p:gpt-6.1-sol", ProviderList: []ProviderConfig{{ProviderName: "p", AuthType: "oauth", CredentialRef: chatgpt.CredentialRef, OpenaiBaseUrl: chatgpt.BaseURL, ModelList: []ModelConfig{{ModelName: "gpt-6.1-sol"}}}}}
			switch change {
			case "endpoint":
				c.ProviderList[0].OpenaiBaseUrl = "https://evil.example/v1"
			case "credentials":
				c.ProviderList[0].OpenaiApiKey = "key"
			case "effort":
				c.ProviderList[0].ModelList[0].ReasoningEffort = "ultra"
			}
			if err := c.validateModelCatalog(); err == nil {
				t.Fatal("unsafe OAuth configuration accepted")
			}
		})
	}
}
