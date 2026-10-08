package ai_models

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/spf13/viper"
)

var testManager *Manager

func parseTestSettings() error {
	if err := config.ParseEnvAndFile(); err != nil {
		return err
	}
	c := config.EnvAndFileConf
	var err error
	testManager, err = New(os.Getenv("HOME"), c.ProviderList, Options{
		Model: c.Model, CompactionModel: c.CompactionModel,
		ContextWindow: c.OpenaiContextWindow, MaxOutputTokens: c.OpenaiMaxOutputTokens,
		CompactionContextWindow: c.CompactionOpenaiContextWindow, CompactionMaxOutputTokens: c.CompactionOpenaiMaxOutputTokens,
	})
	return err
}

func activeModelBudget() (int, int) {
	m := testManager.Active()
	return m.ContextWindow, m.MaxOutputTokens
}

func swapConfigGlobals(t *testing.T) {
	t.Helper()
	previousManager := testManager
	prevViper := config.EnvOrFile
	prevConf := config.EnvAndFileConf
	config.EnvOrFile = viper.New()
	config.EnvAndFileConf = prevConf
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL_NAME",
		"OPENAI_EMBEDDING_API_KEY", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_MODEL_NAME",
		"EMBEDDING_MODEL", "COMPACTION_MODEL", "EMBEDDING_VEC_DIM",
		"OPENAI_COMPACTION_API_KEY", "OPENAI_COMPACTION_BASE_URL", "OPENAI_COMPACTION_MODEL_NAME",
		"OPENAI_CONTEXT_WINDOW", "OPENAI_MAX_OUTPUT_TOKENS", "COMPACTION_OPENAI_CONTEXT_WINDOW", "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "LLM_ROUTER_ADDR"} {
		t.Setenv(key, "")
	}
	t.Cleanup(func() {
		testManager = previousManager
		config.EnvOrFile = prevViper
		config.EnvAndFileConf = prevConf
	})
}

func setEnvModel(t *testing.T, model string) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "sk-env-key")
	t.Setenv("OPENAI_BASE_URL", "https://env.example.com/v1")
	t.Setenv("OPENAI_MODEL_NAME", model)
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
	dir := filepath.Join(home, ".laxcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func TestParseEnvAndFileFromEnv(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	setEnvModel(t, "gpt-env")

	if err := parseTestSettings(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if testManager.state.active.OpenaiApiKey != "sk-env-key" {
		t.Errorf("api key 应从环境读取，实际 %q", testManager.state.active.OpenaiApiKey)
	}
	if testManager.state.active.OpenaiBaseUrl != "https://env.example.com/v1" ||
		testManager.state.active.UpstreamModel != "gpt-env" {
		t.Errorf("base url/model 应从环境读取，实际 %+v", testManager.state)
	}
	if testManager.state.options.ContextWindow != DefaultContextWindow ||
		testManager.state.options.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Errorf("context budget defaults not applied: %+v", testManager.state)
	}
	if config.EnvAndFileConf.LlmRouterAddr != config.DefaultLLMRouterAddr {
		t.Errorf("llm router default addr = %q", config.EnvAndFileConf.LlmRouterAddr)
	}
	if testManager.state.active.Ref != "env_provider:env_model" {
		t.Errorf("环境模型引用=%q", testManager.state.active.Ref)
	}
	resolved, err := testManager.Resolve("env_provider:env_model")
	if err != nil || resolved.UpstreamModel != "gpt-env" {
		t.Fatalf("环境模型解析=%+v, %v", resolved, err)
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
	if plain.ContextWindow != DefaultContextWindow || plain.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf("未声明 limit 应回退全局窗口配置：%+v", plain)
	}
	if window, output := activeModelBudget(); window != 1_048_576 || output != 131_072 {
		t.Fatalf("ActiveModelBudget 应取活跃模型的 limit：%d/%d", window, output)
	}
	// 切回未声明 limit 的模型应回退全局窗口，而非残留上一个模型的 limit。
	if err := testManager.SetActiveModel("p:plain"); err != nil {
		t.Fatal(err)
	}
	if window, output := activeModelBudget(); window != DefaultContextWindow || output != DefaultMaxOutputTokens {
		t.Fatalf("切换后预算未回退全局窗口配置：%d/%d", window, output)
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

func TestAuxiliaryLimitsFallBackToGlobalDefaults(t *testing.T) {
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
	if window, output := activeModelBudget(); window != DefaultContextWindow || output != DefaultMaxOutputTokens {
		t.Fatalf("main budget = %d/%d", window, output)
	}
	if testManager.state.compaction.ContextWindow != DefaultContextWindow || testManager.state.compaction.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf("auxiliary budgets did not fall back to global defaults: %+v", testManager.state)
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

func TestParseEnvAndFileEnvOverridesFile(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, modelSettings("file-provider", "file-model"))
	setEnvModel(t, "gpt-env")

	if err := parseTestSettings(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if testManager.state.active.OpenaiApiKey != "sk-env-key" || testManager.state.active.UpstreamModel != "gpt-env" {
		t.Errorf("环境变量应覆盖文件配置，实际 %+v", testManager.state)
	}
	if testManager.state.active.Ref != "env_provider:env_model" || len(testManager.state.providers) != 2 {
		t.Errorf("环境 provider 应追加并成为当前模型，实际 %+v", testManager.state)
	}
}

func TestParseEnvAndFileRejectsPartialEnvProvider(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, modelSettings("file-provider", "file-model"))
	t.Setenv("OPENAI_API_KEY", "sk-1")
	if err := parseTestSettings(); err == nil {
		t.Fatal("部分 OPENAI_* 环境变量应报错")
	}
}

func TestParseEnvAndFileContextBudgetFromEnv(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	setEnvModel(t, "gpt-env")
	t.Setenv("OPENAI_CONTEXT_WINDOW", "1000000")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "32768")
	if err := parseTestSettings(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if testManager.state.options.ContextWindow != 1_000_000 || testManager.state.options.MaxOutputTokens != 32_768 {
		t.Fatalf("context budget env was not applied: %+v", testManager.state)
	}
}

func TestParseEnvAndFileCompactionProviderOverridesAndFallbacks(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	setEnvModel(t, "main-model")
	t.Setenv("OPENAI_CONTEXT_WINDOW", "100000")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "10000")
	t.Setenv("OPENAI_COMPACTION_MODEL_NAME", "summary-model")
	t.Setenv("COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "2000")

	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	if testManager.state.compaction.OpenaiApiKey != "sk-env-key" ||
		testManager.state.compaction.OpenaiBaseUrl != "https://env.example.com/v1" ||
		testManager.state.compaction.UpstreamModel != "summary-model" ||
		testManager.state.compaction.ContextWindow != 100000 ||
		testManager.state.compaction.MaxOutputTokens != 2000 {
		t.Fatalf("unexpected compaction provider config: %+v", testManager.state)
	}
}

func TestParseEnvAndFileRejectsInvalidContextBudget(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	setEnvModel(t, "gpt-env")
	t.Setenv("OPENAI_CONTEXT_WINDOW", "1000")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "1000")
	if err := parseTestSettings(); err == nil {
		t.Fatal("max output equal to context window must fail")
	}
}

func TestCompactionModelSources(t *testing.T) {
	for _, scenario := range []string{"file", "reference env", "partial env", "full env", "invalid reference", "unset"} {
		t.Run(scenario, func(t *testing.T) {
			swapConfigGlobals(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			ref := "aux:small"
			if scenario == "invalid reference" || scenario == "full env" {
				ref = "missing:model"
			}
			if scenario == "unset" {
				ref = ""
			}
			writeSettings(t, home, fmt.Sprintf(`{
					"model":"main:chat", %q:%q,
					"provider_list":[
						{"provider_name":"main","openai_api_key":"main-key","openai_base_url":"https://main.example/v1","model_list":[{"model_name":"chat"}]},
						{"provider_name":"aux","openai_api_key":"aux-key","openai_base_url":"https://aux.example/v1","model_list":[{"model_name":"small"}]}
					]
				}`, "compaction_model", ref))
			want := []string{"aux-key", "https://aux.example/v1", "small"}
			if scenario == "reference env" {
				t.Setenv("COMPACTION_MODEL", "main:chat")
				want = []string{"main-key", "https://main.example/v1", "chat"}
			}
			if scenario == "partial env" || scenario == "full env" {
				t.Setenv("OPENAI_COMPACTION_MODEL_NAME", "env-model")
				want[2] = "env-model"
			}
			if scenario == "full env" {
				t.Setenv("OPENAI_COMPACTION_API_KEY", "env-key")
				t.Setenv("OPENAI_COMPACTION_BASE_URL", "https://env.example/v1")
				want[0], want[1] = "env-key", "https://env.example/v1"
			}
			if scenario == "unset" {
				want = []string{"main-key", "https://main.example/v1", "chat"}
			}
			err := parseTestSettings()
			if scenario == "invalid reference" {
				if err == nil {
					t.Fatal("expected invalid model reference error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := []string{testManager.state.compaction.OpenaiApiKey, testManager.state.compaction.OpenaiBaseUrl, testManager.state.compaction.UpstreamModel}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			wantRef := "aux:small"
			if scenario == "reference env" {
				wantRef = "main:chat"
			}
			if scenario == "partial env" || scenario == "full env" {
				wantRef = modelRef(envProviderName, envModelName)
			}
			if scenario == "unset" {
				wantRef = "main:chat"
			}
			gotRef := testManager.state.compaction.Ref
			if gotRef != wantRef {
				t.Fatalf("display ref = %q, want %q", gotRef, wantRef)
			}
		})
	}
}

func TestEnvironmentBudgetsOverrideFileModelLimits(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model":"p:main", "compaction_model":"p:summary",
		"provider_list":[{"provider_name":"p", "openai_api_key":"key", "openai_base_url":"https://example.com/v1",
			"model_list":[
				{"model_name":"main", "limit":{"context":100000,"output":8000}},
				{"model_name":"summary", "limit":{"context":120000,"output":12000}}
			]}]
	}`)
	t.Setenv("OPENAI_CONTEXT_WINDOW", "300000")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "30000")
	t.Setenv("COMPACTION_OPENAI_CONTEXT_WINDOW", "400000")
	t.Setenv("COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "40000")
	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	if window, output := activeModelBudget(); window != 300000 || output != 30000 {
		t.Fatalf("main budget = %d/%d", window, output)
	}
	if testManager.state.compaction.ContextWindow != 400000 || testManager.state.compaction.MaxOutputTokens != 40000 {
		t.Fatalf("environment budget overrides were not applied: %+v", testManager.state)
	}
}

func TestCompleteEnvironmentModelsShareDisplayAlias(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct{ prefix, model string }{
		{"OPENAI_", "chat"},
		{"OPENAI_COMPACTION_", "summary"},
	} {
		t.Setenv(tc.prefix+"API_KEY", "key")
		t.Setenv(tc.prefix+"BASE_URL", "https://example.com/v1")
		t.Setenv(tc.prefix+"MODEL_NAME", tc.model)
	}
	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	alias := modelRef(envProviderName, envModelName)
	if testManager.state.active.Ref != alias || testManager.state.compaction.Ref != alias {
		t.Fatalf("environment display refs = %q, %q", testManager.state.active.Ref, testManager.state.compaction.Ref)
	}
	if testManager.state.active.UpstreamModel != "chat" || testManager.state.compaction.UpstreamModel != "summary" {
		t.Fatalf("upstream model names were mixed: %+v", testManager.state)
	}
}

func TestEnvironmentModelNameDoesNotReuseFileModelLimit(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model":"p:main", "compaction_model":"p:summary",
		"provider_list":[{"provider_name":"p", "openai_api_key":"key", "openai_base_url":"https://example.com/v1",
			"model_list":[
				{"model_name":"main"},
				{"model_name":"summary", "limit":{"context":120000,"output":12000}}
			]}]
	}`)
	t.Setenv("OPENAI_COMPACTION_MODEL_NAME", "env-summary")
	if err := parseTestSettings(); err != nil {
		t.Fatal(err)
	}
	if testManager.state.compaction.ContextWindow != DefaultContextWindow || testManager.state.compaction.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf("overridden models reused file limits: %+v", testManager.state)
	}
}

// TestParseEnvAndFileAllowsEmptyCatalog 验证 SSE 延迟配置：零配置（无文件、
// 无环境变量）启动不报错，目录与活跃模型均为空，全局默认值仍生效。
func TestParseEnvAndFileAllowsEmptyCatalog(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := parseTestSettings(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if len(testManager.state.providers) != 0 || testManager.state.active.Ref != "" {
		t.Fatalf("expected unconfigured state, got model=%q providers=%d", testManager.state.active.Ref, len(testManager.state.providers))
	}
	if testManager.state.options.ContextWindow != DefaultContextWindow || testManager.state.options.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf("global defaults should still apply: %+v", testManager.state)
	}
}

// TestParseEnvAndFileDefaultsToFirstModel 目录非空但未选择模型时默认选中第一个
// 条目，维持「目录非空 ⟹ 活跃模型可解析」的不变式。
func TestParseEnvAndFileDefaultsToFirstModel(t *testing.T) {
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
		t.Fatalf("ParseEnvAndFile: %v", err)
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
	// 新主模型无模型级 limit，压缩窗口回退全局默认。
	if testManager.state.compaction.ContextWindow != DefaultContextWindow || testManager.state.compaction.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf("compaction budget should fall back to global defaults: %+v", testManager.state)
	}
}
