package ai_models

import (
	"context"
	"encoding/json"
	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerStreamClientKeepsItsModelAfterSelectionChanges(t *testing.T) {
	swapConfigGlobals(t)
	received := make(chan map[string]any, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received <- body
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("factory lost provider credentials")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
	}))
	defer upstream.Close()
	raw, err := json.Marshal([]ProviderConfig{{ProviderName: "p", OpenaiApiKey: "secret", OpenaiBaseUrl: upstream.URL + "/v1", ModelList: []ModelConfig{{ModelName: "first"}, {ModelName: "second"}}}})
	if err != nil {
		t.Fatal(err)
	}
	models, err := New(t.TempDir(), raw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	first, err := models.StreamClient("p:first")
	if err != nil {
		t.Fatal(err)
	}
	if err := models.SetActiveModel("p:second"); err != nil {
		t.Fatal(err)
	}
	second, err := models.StreamClient("p:second")
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []domainrouter.StreamClient{first, second} {
		stream, err := client.GenerateStream(context.Background(), []byte(`{"model":"caller","input":[],"custom":{"enabled":true}}`))
		if err != nil {
			t.Fatal(err)
		}
		if !stream.Next() {
			t.Fatalf("missing stream event: %v", stream.Err())
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
	}
	firstRequest, secondRequest := <-received, <-received
	if firstRequest["model"] != "first" || secondRequest["model"] != "second" || firstRequest["custom"] == nil {
		t.Fatalf("factory requests: %+v %+v", firstRequest, secondRequest)
	}
}

func TestManagersAndCatalogViewsAreIndependent(t *testing.T) {
	swapConfigGlobals(t)
	raw := json.RawMessage(`[{"provider_name":"p","openai_api_key":"secret","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"gpt-6.1-sol","reasoning_effort":"medium"},{"model_name":"other"}]}]`)
	one, err := New(t.TempDir(), raw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	two, err := New(t.TempDir(), raw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := one.SetActiveModel("p:other"); err != nil {
		t.Fatal(err)
	}
	if two.Active().Ref != "p:gpt-6.1-sol" {
		t.Fatal("selection leaked between managers")
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
	models, err := New(home, raw, DefaultOptions())
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

func TestEmptyProviderAndPersistedEnvironmentProviderAreRejected(t *testing.T) {
	swapConfigGlobals(t)
	for _, raw := range []string{
		`[{"provider_name":"p","openai_api_key":"key","openai_base_url":"https://example.com/v1","model_list":[]}]`,
		`[{"provider_name":"env_provider","openai_api_key":"key","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"env_model","upstream_model":"hidden"}]}]`,
	} {
		if _, err := New(t.TempDir(), json.RawMessage(raw), DefaultOptions()); err == nil {
			t.Fatal("invalid directory accepted")
		}
	}
}

func TestOAuthImportKeepsSelectedAPIKeyModelEffort(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	raw := json.RawMessage(`[{"provider_name":"p","openai_api_key":"secret","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"gpt-6.1-sol","reasoning_effort":"medium"}]}]`)
	writeSettings(t, home, `{"PROVIDER_LIST":`+string(raw)+`,"MODEL":"p:gpt-6.1-sol","custom":true}`)
	models, err := New(home, raw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := models.SetActiveModel("p:gpt-6.1-sol", "high"); err != nil {
		t.Fatal(err)
	}
	if err := models.SaveChatGPTModels([]Model{{Slug: "gpt-6.1-sol"}}); err != nil {
		t.Fatal(err)
	}
	if models.Active().ReasoningEffort != "high" || models.Compaction().ReasoningEffort != "high" {
		t.Fatal("import reset active model effort")
	}
	data, err := os.ReadFile(filepath.Join(home, ".laxcode", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ProviderList []ProviderConfig `json:"provider_list"`
		Custom       bool             `json:"custom"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.ProviderList) != 2 || !doc.Custom || strings.Contains(string(data), "PROVIDER_LIST") {
		t.Fatalf("import lost legacy provider or unrelated settings: %s", data)
	}
}
