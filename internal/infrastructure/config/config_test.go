package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProviderDeepCopy(t *testing.T) {
	original := Provider{
		ProviderName: "provider", AuthType: "api_key", OpenaiApiKey: "key",
		OpenaiBaseUrl: "https://example.com/v1",
		ModelList: []Model{
			{ModelName: "alias", UpstreamModel: "actual", ReasoningEffort: "medium", Limit: &ModelLimit{Context: 1000, Output: 100}},
			{ModelName: "no-limit"},
		},
	}
	cloned := original.DeepCopy()
	if !reflect.DeepEqual(cloned, original) {
		t.Fatal("deep copy changed provider values")
	}
	if &cloned.ModelList[0] == &original.ModelList[0] || cloned.ModelList[0].Limit == original.ModelList[0].Limit {
		t.Fatal("deep copy shares model storage")
	}
	cloned.ProviderName = "changed"
	cloned.ModelList[0].ModelName = "changed"
	cloned.ModelList[0].Limit.Context = 2000
	cloned.ModelList[0].Limit.Output = 200
	cloned.ModelList[1].ModelName = "changed"
	if original.ProviderName != "provider" || original.ModelList[0].ModelName != "alias" ||
		*original.ModelList[0].Limit != (ModelLimit{Context: 1000, Output: 100}) || original.ModelList[1].ModelName != "no-limit" {
		t.Fatal("copy mutations changed the original provider")
	}
	original.ModelList[0].Limit.Context = 3000
	if cloned.ModelList[0].Limit.Context != 2000 {
		t.Fatal("original mutation changed the copy")
	}

	for _, models := range [][]Model{nil, {}} {
		provider := Provider{ModelList: models}
		if !reflect.DeepEqual(provider.DeepCopy(), provider) {
			t.Fatal("deep copy changed a nil or empty model list")
		}
	}
}

func swapConfigGlobals(t *testing.T) {
	t.Helper()
	previous := Current
	Current = Conf{}
	t.Cleanup(func() { Current = previous })
}

func writeSettings(t *testing.T, home, content string) {
	t.Helper()
	var doc map[string]any
	if json.Unmarshal([]byte(content), &doc) == nil {
		for key, value := range doc {
			if strings.EqualFold(key, "provider_list") {
				if providers, ok := value.([]any); ok {
					for i, v := range providers {
						if p, ok := v.(map[string]any); ok {
							if key, ok := p["openai_api_key"].(string); ok {
								env := fmt.Sprintf("TEST_PROVIDER_%d", i)
								t.Setenv(env, key)
								p["openai_api_key"] = env
							}
						}
					}
				}
			}
		}
		if b, err := json.Marshal(doc); err == nil {
			content = string(b)
		}
	}
	dir := filepath.Join(home, ".laxcode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func parseTestConfig() error {
	conf, err := Load(os.Getenv("HOME"), nil)
	if err == nil {
		Current = conf
	}
	return err
}

func TestParseCLIRejectsRemovedFlagsAndPositionalArguments(t *testing.T) {
	for _, arg := range []string{"-sse", "-mode=code", "-kb=x", "-session=x", "-token-budget=1000", "-settings_dir=/etc/laxcode", "unexpected"} {
		if _, err := Load(t.TempDir(), []string{arg}); err == nil {
			t.Fatalf("accepted %q", arg)
		}
	}
}

func TestLoadIgnoresRetiredModelEnvironmentAndGlobalLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"model":"p:chat","compaction_model":"p:summary","openai_context_window":1,"openai_max_output_tokens":999999,"compaction_openai_context_window":-1,"compaction_openai_max_output_tokens":999999,"provider_list":[{"provider_name":"p","openai_api_key":"file-key","openai_base_url":"https://file.example/v1","model_list":[{"model_name":"chat"},{"model_name":"summary","limit":{"context":128000,"output":8192}}]}]}`)
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL_NAME", "OPENAI_CONTEXT_WINDOW", "OPENAI_MAX_OUTPUT_TOKENS", "OPENAI_COMPACTION_API_KEY", "OPENAI_COMPACTION_BASE_URL", "OPENAI_COMPACTION_MODEL_NAME", "COMPACTION_OPENAI_CONTEXT_WINDOW", "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "COMPACTION_MODEL", "LLM_ROUTER_ADDR"} {
		t.Setenv(key, "ignored-invalid-value")
	}
	conf, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if conf.Model != "p:chat" || conf.CompactionModel != "p:summary" || len(conf.ProviderList) != 1 || *conf.ProviderList[0].ModelList[0].Limit != DefaultModelLimit() || conf.ProviderList[0].ModelList[1].Limit.Context != 128000 {
		t.Fatalf("retired settings were applied: %+v", conf)
	}
}

func TestLoadRejectsInvalidModelsAndJSONStructure(t *testing.T) {
	for _, content := range []string{
		`null`, `[]`, `{"model":`,
		`{"provider_list":[{"provider_name":"p","model_list":"invalid"}]}`,
		`{"provider_list":[{"provider_name":"p","model_list":[{"model_name":"m","limit":{}}]}]}`,
		`{"provider_list":[{"provider_name":"p","model_list":[{"model_name":"m","limit":{"context":1000}}]}]}`,
		`{"provider_list":[{"provider_name":"p","model_list":[{"model_name":"m","limit":{"context":1000,"output":1000}}]}]}`,
	} {
		home := t.TempDir()
		writeSettings(t, home, content)
		if _, err := Load(home, nil); err == nil {
			t.Fatalf("accepted invalid settings: %s", content)
		}
	}
}

func TestLoadParsesTypedProvidersAndDefaultsIndependently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"MODEL":"CaseSensitive:alias","PROVIDER_LIST":[{"provider_name":"CaseSensitive","openai_api_key":"key","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"alias","upstream_model":"actual","reasoning_effort":"","limit":{"context":100000,"output":8000}},{"model_name":"plain"},{"model_name":"other"}]}]}`)
	conf, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	models := conf.ProviderList[0].ModelList
	if conf.Model != "CaseSensitive:alias" || models[0].UpstreamModel != "actual" || models[0].Limit.Context != 100000 {
		t.Fatalf("typed fields lost: %+v", conf)
	}
	models[1].Limit.Context = 1
	if models[2].Limit.Context != DefaultContextWindow {
		t.Fatal("default model limits share a pointer")
	}
}

func TestRetiredMemorySettingsDoNotBlockStartup(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"embedding_model":"missing:model","embedding_vec_dim":-1,"user_memory_executable":"missing"}`)
	t.Setenv("OPENAI_EMBEDDING_API_KEY", "unused")
	if err := parseTestConfig(); err != nil {
		t.Fatalf("retired configuration blocked Web startup: %v", err)
	}
}

func TestSettingsDoNotApplyUserEnvironment(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{}`)
	if err := os.WriteFile(filepath.Join(home, ".laxcode", "env.conf"), []byte("LAXCODE_CLOUD_ENV_PROBE=user-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAXCODE_CLOUD_ENV_PROBE", "server-value")
	conf, err := LoadSettings(home, []string{"-addr=127.0.0.1:0"})
	if err != nil || conf.Addr != "127.0.0.1:0" {
		t.Fatalf("cloud flags: %+v %v", conf, err)
	}
	if os.Getenv("LAXCODE_CLOUD_ENV_PROBE") != "server-value" {
		t.Fatal("user env changed process environment")
	}
}
