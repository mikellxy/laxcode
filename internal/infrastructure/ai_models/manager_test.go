package ai_models

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

var testManager *Manager

func parseTestSettings() error {
	conf, err := config.Load(os.Getenv("HOME"), nil)
	if err != nil {
		return err
	}
	testManager, err = New(os.Getenv("HOME"), conf)
	return err
}

func activeModelBudget() (int, int) {
	model := testManager.Active()
	return model.ContextWindow, model.MaxOutputTokens
}
func swapConfigGlobals(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	previous := testManager
	t.Cleanup(func() { testManager = previous })
}

func modelSettings(provider, model string) string {
	return fmt.Sprintf(`{
		"model": %q,
		"provider_list": [{
			"provider_name": %q,
			"openai_api_key": "sk-file-key",
			"openai_base_url": "https://file.example.com/v1",
			"model_list": [{"model_name": %q}]
		}]
	}`, provider+":"+model, provider, model)
}

func writeSettings(t *testing.T, home string, content string) {
	t.Helper()
	var doc map[string]any
	if json.Unmarshal([]byte(content), &doc) == nil {
		if providers, ok := doc["provider_list"].([]any); ok {
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
		if b, err := json.Marshal(doc); err == nil {
			content = string(b)
		}
	}
	dir := filepath.Join(home, ".laxcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func TestParseProviderModelCatalog(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model": "openai:gpt-4.1",
		"provider_list": [
			{"provider_name":"openai","openai_api_key":"sk-o","openai_base_url":"https://o.example/v1","model_list":[{"model_name":"gpt-4o"},{"model_name":"gpt-4.1"}]},
			{"provider_name":"deepseek","openai_api_key":"sk-d","openai_base_url":"https://d.example/v1","model_list":[{"model_name":"chat"}]}
		]
	}`)

	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	if testManager.state.active.OpenaiApiKey != "sk-o" || testManager.state.active.UpstreamModel != "gpt-4.1" {
		t.Fatalf("当前模型派生配置错误：%+v", testManager.state)
	}
	if err := testManager.SetActiveModel("deepseek:chat"); err != nil {
		t.Fatal(err)
	}
	if testManager.state.active.OpenaiBaseUrl != "https://d.example/v1" || testManager.state.active.UpstreamModel != "chat" {
		t.Fatalf("切换后配置错误：%+v", testManager.state)
	}
	if _, err := testManager.Resolve("deepseek:gpt-4.1"); err == nil {
		t.Fatal("不得跨 provider 组合模型")
	}
}

func TestParseProviderModelCatalogRejectsDuplicateNames(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model":"p:m",
		"provider_list":[
			{"provider_name":"p","openai_api_key":"k","openai_base_url":"https://a","model_list":[{"model_name":"m"}]},
			{"provider_name":"p","openai_api_key":"k","openai_base_url":"https://b","model_list":[{"model_name":"m"}]}
		]
	}`)
	if err := parseTestSettings(); err == nil {
		t.Fatal("重复 PROVIDER_NAME 应报错")
	}
}

func TestResolveModelBudgetPrefersModelLimit(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model": "p:limited",
		"provider_list": [{
			"provider_name": "p",
			"openai_api_key": "sk-p",
			"openai_base_url": "https://p.example/v1",
			"model_list": [
				{"model_name": "limited", "limit": {"context": 1048576, "output": 131072}},
				{"model_name": "plain"}
			]
		}]
	}`)
	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	limited, err := testManager.Resolve("p:limited")
	if err != nil {
		t.Fatal(err)
	}
	if limited.ContextWindow != 1_048_576 || limited.MaxOutputTokens != 131_072 {
		t.Fatalf("模型级 limit 未生效：%+v", limited)
	}
	plain, err := testManager.Resolve("p:plain")
	if err != nil {
		t.Fatal(err)
	}
	if plain.ContextWindow != config.DefaultContextWindow || plain.MaxOutputTokens != config.DefaultMaxOutputTokens {
		t.Fatalf("未声明 limit 应回退配置加载时补齐的模型 limit：%+v", plain)
	}
	if window, output := activeModelBudget(); window != 1_048_576 || output != 131_072 {
		t.Fatalf("ActiveModelBudget 应取活跃模型的 limit：%d/%d", window, output)
	}
	// 切回未声明 limit 的模型应使用其默认 limit，而非残留上一个模型的 limit。
	if err := testManager.SetActiveModel("p:plain"); err != nil {
		t.Fatal(err)
	}
	if window, output := activeModelBudget(); window != config.DefaultContextWindow || output != config.DefaultMaxOutputTokens {
		t.Fatalf("切换后预算未回退配置加载时补齐的模型 limit：%d/%d", window, output)
	}
}

func TestMainAndCompactionModelsUseOwnLimits(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model":"p:main", "compaction_model":"p:summary",
		"openai_context_window":200000, "openai_max_output_tokens":16000,
		"compaction_openai_context_window":300000, "compaction_openai_max_output_tokens":20000,
		"provider_list":[{"provider_name":"p", "openai_api_key":"key", "openai_base_url":"https://example.com/v1",
			"model_list":[
				{"model_name":"main", "limit":{"context":100000,"output":8000}},
				{"model_name":"summary", "limit":{"context":120000,"output":12000}}
			]}]
	}`)
	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	if window, output := activeModelBudget(); window != 100000 || output != 8000 {
		t.Fatalf("main budget = %d/%d", window, output)
	}
	if testManager.state.compaction.ContextWindow != 120000 || testManager.state.compaction.MaxOutputTokens != 12000 {
		t.Fatalf("compaction budget = %d/%d", testManager.state.compaction.ContextWindow, testManager.state.compaction.MaxOutputTokens)
	}
}

func TestAuxiliaryLimitsFallBackToModelDefaults(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model":"p:main", "compaction_model":"p:summary",
		"provider_list":[{"provider_name":"p", "openai_api_key":"key", "openai_base_url":"https://example.com/v1",
			"model_list":[{"model_name":"main"},{"model_name":"summary"}]}]
	}`)
	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	if window, output := activeModelBudget(); window != config.DefaultContextWindow || output != config.DefaultMaxOutputTokens {
		t.Fatalf("main budget = %d/%d", window, output)
	}
	if testManager.state.compaction.ContextWindow != config.DefaultContextWindow || testManager.state.compaction.MaxOutputTokens != config.DefaultMaxOutputTokens {
		t.Fatalf("auxiliary budgets did not fall back to model defaults: %+v", testManager.state)
	}
}

func TestParseProviderModelCatalogRejectsInvalidLimit(t *testing.T) {
	for _, tc := range []struct{ name, limit string }{
		{"zero context", `{"context": 0, "output": 100}`},
		{"zero output", `{"context": 1000, "output": 0}`},
		{"output not smaller than context", `{"context": 1000, "output": 1000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			swapConfigGlobals(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			writeSettings(t, home, fmt.Sprintf(`{
				"model": "p:m",
				"provider_list": [{
					"provider_name": "p",
					"openai_api_key": "k",
					"openai_base_url": "https://a.example/v1",
					"model_list": [{"model_name": "m", "limit": %s}]
				}]
			}`, tc.limit))
			if err := parseTestSettings(); err == nil {
				t.Fatal("非法 limit 应报错")
			}
		})
	}
}

func TestManagerAllowsEmptyCatalog(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := parseTestSettings(); err != nil {
		t.Fatalf("Load settings: %v", err)
	}
	if len(testManager.state.providers) != 0 || testManager.state.active.Ref != "" {
		t.Fatalf("expected unconfigured state, got model=%q providers=%d", testManager.state.active.Ref, len(testManager.state.providers))
	}

}

// TestManagerDefaultsToFirstModel 目录非空但未选择模型时默认选中第一个
// 条目，维持「目录非空 ⟹ 活跃模型可解析」的不变式。
func TestManagerDefaultsToFirstModel(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"provider_list":[{
		"provider_name":"p",
		"openai_api_key":"sk-file-key",
		"openai_base_url":"https://file.example.com/v1",
		"model_list":[{"model_name":"main"},{"model_name":"second"}]
	}]}`)
	if err := parseTestSettings(); err != nil {
		t.Fatalf("Load settings: %v", err)
	}
	if testManager.state.active.Ref != "p:main" || testManager.state.active.UpstreamModel != "main" {
		t.Fatalf("default model=%q upstream=%q", testManager.state.active.Ref, testManager.state.active.UpstreamModel)
	}
	if testManager.state.compaction.Ref != "p:main" || testManager.state.compaction.UpstreamModel != "main" {
		t.Fatalf("compaction should inherit the active model: %+v", testManager.state)
	}
}

// TestSetActiveModelReDerivesInheritedCompaction 运行期切换主模型时，未显式
// 配置的压缩模型随新主模型重推导；显式配置的压缩模型保持不变。
func TestSetActiveModelReDerivesInheritedCompaction(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"provider_list":[{
		"provider_name":"p",
		"openai_api_key":"sk-p",
		"openai_base_url":"https://p.example.com/v1",
		"model_list":[{"model_name":"main","limit":{"context":100000,"output":8000}}]
	},{
		"provider_name":"q",
		"openai_api_key":"sk-q",
		"openai_base_url":"https://q.example.com/v1",
		"model_list":[{"model_name":"other"}]
	}]}`)
	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	if err := testManager.SetActiveModel("q:other"); err != nil {
		t.Fatal(err)
	}
	if testManager.state.compaction.Ref != "q:other" || testManager.state.compaction.UpstreamModel != "other" ||
		testManager.state.compaction.OpenaiApiKey != "sk-q" {
		t.Fatalf("compaction should follow the new main model: %+v", testManager.state)
	}
	// 新主模型无模型级 limit，压缩窗口回退模型默认 limit。
	if testManager.state.compaction.ContextWindow != config.DefaultContextWindow || testManager.state.compaction.MaxOutputTokens != config.DefaultMaxOutputTokens {
		t.Fatalf("compaction budget should fall back to model defaults: %+v", testManager.state)
	}
}

func fixtureConf(t *testing.T, raw []byte) config.Conf {
	t.Helper()
	var providers []config.Provider
	if err := json.Unmarshal(raw, &providers); err != nil {
		t.Fatal(err)
	}
	return providersConf(providers)
}

func providersConf(providers []config.Provider) config.Conf {
	for i := range providers {
		for j := range providers[i].ModelList {
			model := &providers[i].ModelList[j]
			if model.Limit == nil {
				limit := config.DefaultModelLimit()
				model.Limit = &limit
			}
		}
	}
	return config.Conf{ProviderList: providers}
}

func TestRootedPreferencesCannotFollowHostSymlink(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "agenthome")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, home, modelSettings("p", "a"))
	conf, err := config.Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewRooted(home, root, conf)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	outside := t.TempDir()
	if err := os.Rename(filepath.Join(home, ".laxcode"), filepath.Join(home, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".laxcode")); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetActiveModel("p:a"); err == nil {
		t.Fatal("preference write escaped user root")
	}
	if _, err := os.Stat(filepath.Join(outside, "model-selection.json")); !os.IsNotExist(err) {
		t.Fatal("wrote outside user root")
	}
}
