package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/viper"
)

// swapConfigGlobals 隔离 ParseEnvAndFile 依赖的包级全局，测试结束恢复。
func swapConfigGlobals(t *testing.T) {
	t.Helper()
	prevViper := EnvOrFile
	prevConf := EnvAndFileConf
	EnvOrFile = viper.New()
	EnvAndFileConf = envAndFileConf{}
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL_NAME",
		"OPENAI_EMBEDDING_API_KEY", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_MODEL_NAME",
		"OPENAI_COMPACTION_API_KEY", "OPENAI_COMPACTION_BASE_URL", "OPENAI_COMPACTION_MODEL_NAME",
		"OPENAI_CONTEXT_WINDOW", "OPENAI_MAX_OUTPUT_TOKENS", "COMPACTION_OPENAI_CONTEXT_WINDOW", "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "LLM_ROUTER_ADDR"} {
		t.Setenv(key, "")
	}
	t.Cleanup(func() {
		EnvOrFile = prevViper
		EnvAndFileConf = prevConf
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
		"MODEL": %q,
		"PROVIDER_LIST": [{
			"PROVIDER_NAME": %q,
			"OPENAI_API_KEY": "sk-file-key",
			"OPENAI_BASE_URL": "https://file.example.com/v1",
			"MODEL_LIST": [{"MODEL_NAME": %q}]
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
	t.Setenv("OPENAI_EMBEDDING_API_KEY", "embed-key")
	t.Setenv("OPENAI_EMBEDDING_BASE_URL", "https://embed.example.com/v1")
	t.Setenv("OPENAI_EMBEDDING_MODEL_NAME", "embed-model")

	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if EnvAndFileConf.OpenaiApiKey != "sk-env-key" {
		t.Errorf("api key 应从环境读取，实际 %q", EnvAndFileConf.OpenaiApiKey)
	}
	if EnvAndFileConf.OpenaiBaseUrl != "https://env.example.com/v1" ||
		EnvAndFileConf.OpenaiModel != "gpt-env" {
		t.Errorf("base url/model 应从环境读取，实际 %+v", EnvAndFileConf)
	}
	if EnvAndFileConf.EmbedOpenaiApiKey != "embed-key" ||
		EnvAndFileConf.EmbedOpenaiBaseUrl != "https://embed.example.com/v1" ||
		EnvAndFileConf.EmbedOpenaiModel != "embed-model" {
		t.Errorf("embedding config should come from OPENAI_EMBEDDING_* env vars: %+v", EnvAndFileConf)
	}
	if EnvAndFileConf.OpenaiContextWindow != DefaultContextWindow ||
		EnvAndFileConf.OpenaiMaxOutputTokens != DefaultMaxOutputTokens {
		t.Errorf("context budget defaults not applied: %+v", EnvAndFileConf)
	}
	if EnvAndFileConf.LlmRouterAddr != DefaultLLMRouterAddr {
		t.Errorf("llm router default addr = %q", EnvAndFileConf.LlmRouterAddr)
	}
	if EnvAndFileConf.Model != "env_provider:env_model" {
		t.Errorf("环境模型引用=%q", EnvAndFileConf.Model)
	}
	resolved, err := ResolveModel("env_provider:env_model")
	if err != nil || resolved.UpstreamModel != "gpt-env" {
		t.Fatalf("环境模型解析=%+v, %v", resolved, err)
	}
}

func TestParseProviderModelCatalog(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"MODEL": "openai:gpt-4.1",
		"PROVIDER_LIST": [
			{"PROVIDER_NAME":"openai","OPENAI_API_KEY":"sk-o","OPENAI_BASE_URL":"https://o.example/v1","MODEL_LIST":[{"MODEL_NAME":"gpt-4o"},{"MODEL_NAME":"gpt-4.1"}]},
			{"PROVIDER_NAME":"deepseek","OPENAI_API_KEY":"sk-d","OPENAI_BASE_URL":"https://d.example/v1","MODEL_LIST":[{"MODEL_NAME":"chat"}]}
		]
	}`)

	if err := ParseEnvAndFile(); err != nil {
		t.Fatal(err)
	}
	if EnvAndFileConf.OpenaiApiKey != "sk-o" || EnvAndFileConf.OpenaiModel != "gpt-4.1" {
		t.Fatalf("当前模型派生配置错误：%+v", EnvAndFileConf)
	}
	wantRefs := []string{"deepseek:chat", "openai:gpt-4.1", "openai:gpt-4o"}
	if got := ModelRefs(); !reflect.DeepEqual(got, wantRefs) {
		t.Fatalf("ModelRefs=%v, want %v", got, wantRefs)
	}
	if err := SetActiveModel("deepseek:chat"); err != nil {
		t.Fatal(err)
	}
	if EnvAndFileConf.OpenaiBaseUrl != "https://d.example/v1" || EnvAndFileConf.OpenaiModel != "chat" {
		t.Fatalf("切换后配置错误：%+v", EnvAndFileConf)
	}
	if _, err := ResolveModel("deepseek:gpt-4.1"); err == nil {
		t.Fatal("不得跨 provider 组合模型")
	}
}

func TestParseProviderModelCatalogRejectsDuplicateNames(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"MODEL":"p:m",
		"PROVIDER_LIST":[
			{"PROVIDER_NAME":"p","OPENAI_API_KEY":"k","OPENAI_BASE_URL":"https://a","MODEL_LIST":[{"MODEL_NAME":"m"}]},
			{"PROVIDER_NAME":"p","OPENAI_API_KEY":"k","OPENAI_BASE_URL":"https://b","MODEL_LIST":[{"MODEL_NAME":"m"}]}
		]
	}`)
	if err := ParseEnvAndFile(); err == nil {
		t.Fatal("重复 PROVIDER_NAME 应报错")
	}
}

func TestParseEnvAndFileLLMRouterAddrFromEnv(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	setEnvModel(t, "gpt-env")
	t.Setenv("LLM_ROUTER_ADDR", "127.0.0.1:18080")

	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if EnvAndFileConf.LlmRouterAddr != "127.0.0.1:18080" {
		t.Fatalf("llm router addr = %q", EnvAndFileConf.LlmRouterAddr)
	}
}

func TestParseEnvAndFileEnvOverridesFile(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, modelSettings("file-provider", "file-model"))
	setEnvModel(t, "gpt-env")

	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if EnvAndFileConf.OpenaiApiKey != "sk-env-key" || EnvAndFileConf.OpenaiModel != "gpt-env" {
		t.Errorf("环境变量应覆盖文件配置，实际 %+v", EnvAndFileConf)
	}
	if EnvAndFileConf.Model != "env_provider:env_model" || len(EnvAndFileConf.ProviderList) != 2 {
		t.Errorf("环境 provider 应追加并成为当前模型，实际 %+v", EnvAndFileConf)
	}
}

func TestParseEnvAndFileRejectsPartialEnvProvider(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, modelSettings("file-provider", "file-model"))
	t.Setenv("OPENAI_API_KEY", "sk-1")
	if err := ParseEnvAndFile(); err == nil {
		t.Fatal("部分 OPENAI_* 环境变量应报错")
	}
}

func TestParseEnvAndFileCorruptFileReturnsError(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"openai_api_key": `) // 非法 JSON

	if err := ParseEnvAndFile(); err == nil {
		t.Fatal("settings.json 非法时应返回错误")
	}
}

func TestParseEnvAndFileContextBudgetFromEnv(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	setEnvModel(t, "gpt-env")
	t.Setenv("OPENAI_CONTEXT_WINDOW", "1000000")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "32768")
	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if EnvAndFileConf.OpenaiContextWindow != 1_000_000 || EnvAndFileConf.OpenaiMaxOutputTokens != 32_768 {
		t.Fatalf("context budget env was not applied: %+v", EnvAndFileConf)
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

	if err := ParseEnvAndFile(); err != nil {
		t.Fatal(err)
	}
	if EnvAndFileConf.CompactionOpenaiApiKey != "sk-env-key" ||
		EnvAndFileConf.CompactionOpenaiBaseUrl != "https://env.example.com/v1" ||
		EnvAndFileConf.CompactionOpenaiModel != "summary-model" ||
		EnvAndFileConf.CompactionOpenaiContextWindow != 100000 ||
		EnvAndFileConf.CompactionOpenaiMaxOutputTokens != 2000 {
		t.Fatalf("unexpected compaction provider config: %+v", EnvAndFileConf)
	}
}

func TestParseEnvAndFileRejectsInvalidContextBudget(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	setEnvModel(t, "gpt-env")
	t.Setenv("OPENAI_CONTEXT_WINDOW", "1000")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "1000")
	if err := ParseEnvAndFile(); err == nil {
		t.Fatal("max output equal to context window must fail")
	}
}

// swapCliGlobals 隔离 ParseCli 依赖的包级全局（flag 集合、os.Args、Cli viper）。
func swapCliGlobals(t *testing.T, args ...string) {
	t.Helper()
	prevArgs := os.Args
	prevFlagCmd := flag.CommandLine
	prevCli := Cli
	prevConf := CliConf

	flag.CommandLine = flag.NewFlagSet("config-test", flag.ContinueOnError)
	os.Args = append([]string{"config.test"}, args...)
	Cli = viper.New()
	CliConf = cliConf{}

	t.Cleanup(func() {
		os.Args = prevArgs
		flag.CommandLine = prevFlagCmd
		Cli = prevCli
		CliConf = prevConf
	})
}

func TestParseCli(t *testing.T) {
	swapCliGlobals(t,
		"-oneshot=true",
		"-sse=true",
		"-qa=true",
		"-kb", filepath.Join(t.TempDir(), "vectors.sqlite"),
		"-vector-dim", "1024",
		"-addr", ":9000",
		"-workdir", "/tmp/proj",
		"-task", "do something",
		"-task-file", "/tmp/t.txt",
		"-session", "sess-9",
		"-plan=true",
	)

	if err := ParseCli(); err != nil {
		t.Fatalf("ParseCli: %v", err)
	}
	if !CliConf.Oneshot {
		t.Error("oneshot 应为 true")
	}
	if CliConf.WorkDir != "/tmp/proj" || CliConf.Task != "do something" ||
		CliConf.TaskFile != "/tmp/t.txt" || CliConf.Session != "sess-9" {
		t.Errorf("字符串参数解析不符：%+v", CliConf)
	}
	if !CliConf.Plan {
		t.Error("plan 应为 true")
	}
	if !CliConf.SSE {
		t.Error("sse 应为 true")
	}
	if !CliConf.QA {
		t.Error("qa 应为 true")
	}
	if CliConf.Addr != ":9000" {
		t.Errorf("addr 应为 :9000，实际 %q", CliConf.Addr)
	}
	if CliConf.VectorDimensions != 1024 {
		t.Errorf("vector dimensions 应为 1024，实际 %d", CliConf.VectorDimensions)
	}
}

func TestParseCliDefaults(t *testing.T) {
	swapCliGlobals(t)
	if err := ParseCli(); err != nil {
		t.Fatalf("ParseCli with no args: %v", err)
	}
	if CliConf.Oneshot || CliConf.Plan || CliConf.SSE || CliConf.QA {
		t.Errorf("缺省布尔参数应全为 false，实际 %+v", CliConf)
	}
	if CliConf.WorkDir != "" || CliConf.Task != "" || CliConf.Session != "" {
		t.Errorf("缺省字符串参数应为空，实际 %+v", CliConf)
	}
	if CliConf.Addr != DefaultSSEAddr {
		t.Errorf("缺省 addr 应为 %q，实际 %q", DefaultSSEAddr, CliConf.Addr)
	}
}

func TestParseCliCombinedSSEQADoesNotRequireUserMemoryVectorDimensions(t *testing.T) {
	for _, key := range []string{"OPENAI_EMBEDDING_MODEL_NAME", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_API_KEY"} {
		t.Setenv(key, "configured")
	}
	swapCliGlobals(t,
		"-sse=true",
		"-qa=true",
		"-kb", filepath.Join(t.TempDir(), "kb.sqlite"),
	)

	if err := ParseCli(); err != nil {
		t.Fatalf("combined SSE QA should not require -vector-dim: %v", err)
	}
}

func TestAuxiliaryModelSources(t *testing.T) {
	for _, kind := range []string{"EMBEDDING", "COMPACTION"} {
		for _, scenario := range []string{"file", "partial env", "full env", "invalid reference", "unset"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
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
					"MODEL":"main:chat", %q:%q,
					"PROVIDER_LIST":[
						{"PROVIDER_NAME":"main","OPENAI_API_KEY":"main-key","OPENAI_BASE_URL":"https://main.example/v1","MODEL_LIST":[{"MODEL_NAME":"chat"}]},
						{"PROVIDER_NAME":"aux","OPENAI_API_KEY":"aux-key","OPENAI_BASE_URL":"https://aux.example/v1","MODEL_LIST":[{"MODEL_NAME":"small"}]}
					]
				}`, kind+"_MODEL", ref))
				want := []string{"aux-key", "https://aux.example/v1", "small"}
				if scenario == "partial env" || scenario == "full env" {
					t.Setenv("OPENAI_"+kind+"_MODEL_NAME", "env-model")
					want[2] = "env-model"
				}
				if scenario == "full env" {
					t.Setenv("OPENAI_"+kind+"_API_KEY", "env-key")
					t.Setenv("OPENAI_"+kind+"_BASE_URL", "https://env.example/v1")
					want[0], want[1] = "env-key", "https://env.example/v1"
				}
				if scenario == "unset" {
					want = []string{"", "", ""}
					if kind == "COMPACTION" {
						want = []string{"main-key", "https://main.example/v1", "chat"}
					}
				}
				err := ParseEnvAndFile()
				if scenario == "invalid reference" {
					if err == nil {
						t.Fatal("expected invalid model reference error")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got := []string{EnvAndFileConf.EmbedOpenaiApiKey, EnvAndFileConf.EmbedOpenaiBaseUrl, EnvAndFileConf.EmbedOpenaiModel}
				if kind == "COMPACTION" {
					got = []string{EnvAndFileConf.CompactionOpenaiApiKey, EnvAndFileConf.CompactionOpenaiBaseUrl, EnvAndFileConf.CompactionOpenaiModel}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %v, want %v", got, want)
				}
			})
		}
	}
}

func TestKnowledgeBaseFlagForSSEAndQA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "external vectors.sqlite")
	for _, mode := range []string{"-sse", "-qa"} {
		for _, tc := range []struct {
			name, value string
			valid       bool
		}{
			{"absolute", path, true}, {"missing", "", false}, {"relative", "kb/memory.sqlite", false}, {"home shorthand", "~/kb.sqlite", false},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				if mode == "-sse" {
					for _, key := range []string{"OPENAI_EMBEDDING_MODEL_NAME", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_API_KEY"} {
						t.Setenv(key, "configured")
					}
				}
				args := []string{mode, "-kb=" + tc.value}
				if mode == "-sse" {
					args = append(args, "-vector-dim=1024")
				}
				swapCliGlobals(t, args...)
				// Legacy configuration must not supply a missing -kb.
				t.Setenv("USER_MEMORY_DB", path)
				err := ParseCli()
				if (err == nil) != tc.valid {
					t.Fatalf("ParseCli error=%v, valid=%v", err, tc.valid)
				}
				if tc.valid && CliConf.KB != path {
					t.Fatalf("kb=%q", CliConf.KB)
				}
			})
		}
	}
}

func TestSSERequiresValidVectorDimensions(t *testing.T) {
	db := filepath.Join(t.TempDir(), "vectors.sqlite")
	for _, key := range []string{"OPENAI_EMBEDDING_MODEL_NAME", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_API_KEY"} {
		t.Setenv(key, "configured")
	}
	for _, value := range []string{"", "0", "-1", "8193"} {
		t.Run(value, func(t *testing.T) {
			args := []string{"-sse", "-kb=" + db}
			if value != "" {
				args = append(args, "-vector-dim="+value)
			}
			swapCliGlobals(t, args...)
			if err := ParseCli(); err == nil {
				t.Fatalf("accepted vector dimensions %q", value)
			}
		})
	}
}

func TestSSEAllowsMissingMemoryFlagsWhenEmbeddingIsDisabled(t *testing.T) {
	keys := []string{"OPENAI_EMBEDDING_MODEL_NAME", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_API_KEY"}
	for _, key := range keys {
		t.Setenv(key, "configured")
	}
	for _, missing := range keys {
		t.Run(missing, func(t *testing.T) {
			t.Setenv(missing, "")
			swapCliGlobals(t, "-sse")
			if err := ParseCli(); err != nil {
				t.Fatalf("disabled embedding unexpectedly requires memory flags: %v", err)
			}
			if CliConf.KB != "" {
				t.Fatalf("kb = %q", CliConf.KB)
			}
			if CliConf.VectorDimensions != 0 {
				t.Fatalf("vector dimensions = %d", CliConf.VectorDimensions)
			}
		})
	}
}
