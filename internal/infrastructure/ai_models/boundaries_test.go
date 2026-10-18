package ai_models

import (
	"encoding/json"
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

func TestSelectionSurvivesRestartAndCatalogRemoval(t *testing.T) {
	conf := fixtureConf(t, []byte(`[{"provider_name":"p","openai_api_key":"secret","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"default"},{"model_name":"other"}]}]`))
	home := t.TempDir()
	first, err := New(home, conf)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.SetActiveModel("p:other"); err != nil {
		t.Fatal(err)
	}
	second, err := New(home, conf)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.LoadSelection(); err != nil {
		t.Fatal(err)
	}
	if second.Active().Ref != "p:other" {
		t.Fatal("user selection lost")
	}
	conf.ProviderList[0].ModelList = conf.ProviderList[0].ModelList[:1]
	third, err := New(home, conf)
	if err != nil {
		t.Fatal(err)
	}
	if err = third.LoadSelection(); err != nil {
		t.Fatal(err)
	}
	if third.Active().Ref != "p:default" {
		t.Fatal("removed model did not fall back")
	}
}
