package ai_models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

func TestManagersAndCatalogViewsAreIndependent(t *testing.T) {
	swapConfigGlobals(t)
	raw := json.RawMessage(`[{"provider_name":"p","openai_api_key":"secret","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"gpt-6.1-sol","reasoning_effort":"medium"},{"model_name":"other"}]}]`)
	conf := fixtureConf(t, raw)
	one, err := New(t.TempDir(), conf)
	if err != nil {
		t.Fatal(err)
	}
	two, err := New(t.TempDir(), conf)
	if err != nil {
		t.Fatal(err)
	}
	if err := one.SetActiveModel("p:other"); err != nil {
		t.Fatal(err)
	}
	if two.Active().Ref != "p:gpt-6.1-sol" {
		t.Fatal("selection leaked between managers")
	}
	conf.ProviderList[0].OpenaiApiKey = "changed"
	conf.ProviderList[0].ModelList[0].ModelName = "changed"
	conf.ProviderList[0].ModelList[0].Limit.Context = 1
	resolved, err := two.Resolve("p:gpt-6.1-sol")
	if err != nil || resolved.OpenaiApiKey != "secret" || resolved.ContextWindow != config.DefaultContextWindow {
		t.Fatal("startup configuration mutated manager state")
	}
	view := two.List()
	view[0].ModelList[0].ModelName = "changed"
	view[0].ModelList[0].ReasoningEfforts[0] = "changed"
	fresh := two.List()
	if fresh[0].ModelList[0].ModelName != "gpt-6.1-sol" || fresh[0].ModelList[0].ReasoningEfforts[0] != "low" {
		t.Fatal("catalog view mutated manager state")
	}
	encoded, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "https://") {
		t.Fatal("catalog leaks connection credentials")
	}
}

func TestCatalogMutationPreservesEffortAndFailurePreservesState(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	raw := json.RawMessage(`[{"provider_name":"p","openai_api_key":"secret","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"gpt-6.1-sol","reasoning_effort":"medium"}]}]`)
	writeSettings(t, home, `{"provider_list":`+string(raw)+`}`)
	models, err := New(home, fixtureConf(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := models.SetActiveModel("p:gpt-6.1-sol", "high"); err != nil {
		t.Fatal(err)
	}
	input := AddModelInput{Provider: "p", Model: "other", APIKey: "secret", BaseURL: "https://example.com/v1", ContextWindow: 100000, MaxOutputTokens: 8000}
	added, err := models.AddModelToSettings(input)
	if err != nil {
		t.Fatal(err)
	}
	added.Limit.Context = 1
	resolved, err := models.Resolve("p:other")
	if err != nil || resolved.ContextWindow != 100000 {
		t.Fatal("returned model exposes live catalog limits")
	}
	if models.Active().ReasoningEffort != "high" || models.Compaction().ReasoningEffort != "high" {
		t.Fatal("adding another model reset runtime effort")
	}
	before := models.Active()
	path := filepath.Join(home, ".laxcode", "settings.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	input.Model = "failed"
	if _, err := models.AddModelToSettings(input); err == nil {
		t.Fatal("failed settings read was ignored")
	}
	if models.Active() != before || len(models.List()[0].ModelList) != 2 {
		t.Fatal("failed persistence changed runtime catalog")
	}
}

func TestEmptyProviderAndInvalidLimitAreRejected(t *testing.T) {
	swapConfigGlobals(t)
	for _, raw := range []string{
		`[{"provider_name":"p","openai_api_key":"key","openai_base_url":"https://example.com/v1","model_list":[]}]`,
		`[{"provider_name":"p","openai_api_key":"key","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"m","limit":{"context":1000,"output":1000}}]}]`,
	} {
		if _, err := New(t.TempDir(), fixtureConf(t, []byte(raw))); err == nil {
			t.Fatal("invalid directory accepted")
		}
	}
}
