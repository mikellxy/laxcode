package ai_models

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func TestChatGPTCatalogPersistsAndResolvesAfterRestart(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	models := []Model{{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol"}, {Slug: "another-model", DisplayName: "Another"}}
	if err := testManager.SaveChatGPTModels(models); err != nil {
		t.Fatal(err)
	}
	assertResolved := func() {
		t.Helper()
		resolved, err := testManager.Resolve("openai-chatgpt:gpt-6.1-sol")
		if err != nil || resolved.AuthType != "oauth" || resolved.CredentialRef != CredentialRef || resolved.OpenaiApiKey != "" || resolved.OpenaiBaseUrl != BaseURL || resolved.ReasoningEffort != "medium" {
			t.Fatalf("OAuth resolution=%+v err=%v", resolved, err)
		}
		if testManager.state.compaction.AuthType != "oauth" || testManager.state.compaction.CredentialRef != CredentialRef {
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
	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	assertResolved()
	before := string(data)
	if err := testManager.SaveChatGPTModels([]Model{{Slug: "bad:model"}}); err == nil {
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
	if err := testManager.SaveChatGPTModels(models); err != nil {
		t.Fatal(err)
	}
	testManager.state.providers[0].ModelList[0].Limit = &ModelLimit{Context: 300000, Output: 12000}
	testManager.state.providers[0].ModelList[0].ReasoningEffort = "high"
	if err := testManager.SaveChatGPTModels(models); err != nil {
		t.Fatal(err)
	}
	if testManager.state.providers[0].ModelList[0].Limit.Context != 300000 || testManager.state.providers[0].ModelList[0].ReasoningEffort != "high" {
		t.Fatal("reconnect discarded configured model defaults")
	}
	data, _ = os.ReadFile(layout.UserSettings(home))
	if !strings.Contains(string(data), `"custom": true`) || len(testManager.state.providers) != 1 {
		t.Fatal("reconnect duplicated provider or lost settings")
	}
}

func TestOAuthCatalogRejectsUnsafeEndpointAndUnsupportedEffort(t *testing.T) {
	for _, change := range []string{"endpoint", "credentials", "effort"} {
		t.Run(change, func(t *testing.T) {
			c := modelState{active: ResolvedModel{Ref: "p:gpt-6.1-sol"}, providers: []ProviderConfig{{ProviderName: "p", AuthType: "oauth", CredentialRef: CredentialRef, OpenaiBaseUrl: BaseURL, ModelList: []ModelConfig{{ModelName: "gpt-6.1-sol"}}}}}
			switch change {
			case "endpoint":
				c.providers[0].OpenaiBaseUrl = "https://evil.example/v1"
			case "credentials":
				c.providers[0].OpenaiApiKey = "key"
			case "effort":
				c.providers[0].ModelList[0].ReasoningEffort = "ultra"
			}
			if err := c.validateModelCatalog(); err == nil {
				t.Fatal("unsafe OAuth configuration accepted")
			}
		})
	}
}
