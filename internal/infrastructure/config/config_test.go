package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProviderDeepCopy(t *testing.T) {
	original := Provider{
		ProviderName: "provider", AuthType: "api_key", OpenaiApiKey: "key",
		OpenaiBaseUrl: "https://example.com/v1", CredentialRef: "credential",
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

func TestLoadMergesFileAndCLI(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{"model":"p:file","compaction_model":"p:summary","llm_router_addr":"127.0.0.1:18080","llm_router_url":"https://ignored.example","addr":"ignored","plan":true,"provider_list":[{"provider_name":"p","openai_api_key":"file-key","openai_base_url":"https://file.example/v1","model_list":[{"model_name":"file","limit":{"context":128000,"output":8192}},{"model_name":"summary"}]}]}`)
	conf, err := Load(home, []string{"-addr=127.0.0.1:9000", "-plan", "-model=cli-model", "-openai_api_key=cli-key", "-openai_base_url=https://cli.example/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if conf.Addr != "127.0.0.1:9000" || !conf.Plan || conf.Model != "env:cli-model" || conf.CompactionModel != "p:summary" || conf.LlmRouterAddr != "127.0.0.1:18080" || conf.LlmRouterURL != "" {
		t.Fatalf("merged config: %+v", conf)
	}
	if len(conf.ProviderList) != 2 || conf.ProviderList[0].OpenaiApiKey != "file-key" || conf.ProviderList[0].ModelList[0].Limit.Context != 128000 {
		t.Fatal("file provider was overwritten")
	}
	cli := conf.ProviderList[1]
	if cli.ProviderName != CLIProviderName || cli.OpenaiApiKey != "cli-key" || cli.OpenaiBaseUrl != "https://cli.example/v1" || cli.ModelList[0].ModelName != "cli-model" || *cli.ModelList[0].Limit != DefaultModelLimit() {
		t.Fatalf("CLI provider: %+v", cli)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".laxcode", "settings.json"))
	if strings.Contains(string(data), "cli-key") || strings.Contains(string(data), "cli-model") {
		t.Fatal("CLI configuration was persisted")
	}
	fileOnly, err := Load(home, nil)
	if err != nil || fileOnly.Model != "p:file" || fileOnly.Addr != DefaultSSEAddr || fileOnly.Plan {
		t.Fatalf("default CLI overwrote file or startup defaults: %+v, %v", fileOnly, err)
	}
	if *fileOnly.ProviderList[0].ModelList[1].Limit != DefaultModelLimit() {
		t.Fatal("missing model limit was not normalized")
	}
}

func TestCLIRequiresCompleteModelCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"-model=chat"}, {"-openai_api_key=key"}, {"-openai_base_url=https://example.com/v1"},
		{"-model=chat", "-openai_api_key=key"}, {"-model=chat", "-openai_base_url=https://example.com/v1"},
		{"-openai_api_key=key", "-openai_base_url=https://example.com/v1"},
	} {
		if _, err := Load(t.TempDir(), args); err == nil {
			t.Fatalf("accepted incomplete credentials: %v", args)
		}
	}
}

func TestParseCLIRejectsRemovedFlagsAndPositionalArguments(t *testing.T) {
	for _, arg := range []string{"-sse", "-mode=code", "-kb=x", "-session=x", "-token-budget=1000", "unexpected"} {
		if _, err := Load(t.TempDir(), []string{arg}); err == nil {
			t.Fatalf("accepted %q", arg)
		}
	}
}

func TestLoadIgnoresRetiredModelEnvironmentAndGlobalLimits(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{"model":"p:chat","compaction_model":"p:summary","openai_context_window":1,"openai_max_output_tokens":999999,"compaction_openai_context_window":-1,"compaction_openai_max_output_tokens":999999,"provider_list":[{"provider_name":"p","openai_api_key":"file-key","openai_base_url":"https://file.example/v1","model_list":[{"model_name":"chat"},{"model_name":"summary","limit":{"context":128000,"output":8192}}]}]}`)
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL_NAME", "OPENAI_CONTEXT_WINDOW", "OPENAI_MAX_OUTPUT_TOKENS", "OPENAI_COMPACTION_API_KEY", "OPENAI_COMPACTION_BASE_URL", "OPENAI_COMPACTION_MODEL_NAME", "COMPACTION_OPENAI_CONTEXT_WINDOW", "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "COMPACTION_MODEL", "LLM_ROUTER_ADDR"} {
		t.Setenv(key, "ignored-invalid-value")
	}
	conf, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if conf.Model != "p:chat" || conf.CompactionModel != "p:summary" || conf.LlmRouterAddr != DefaultLLMRouterAddr || len(conf.ProviderList) != 1 || *conf.ProviderList[0].ModelList[0].Limit != DefaultModelLimit() || conf.ProviderList[0].ModelList[1].Limit.Context != 128000 {
		t.Fatalf("retired settings were applied: %+v", conf)
	}
}

func TestLoadRejectsInvalidModelsAndJSONStructure(t *testing.T) {
	for _, content := range []string{
		`null`, `[]`, `{"model":`,
		`{"provider_list":[{"provider_name":"env","model_list":[]}]}`,
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

func TestLoadAppliesEnvFileWithoutModelOverrides(t *testing.T) {
	t.Setenv("LAXCODE_ENVCONF_PROBE", "inherited")
	t.Setenv("OPENAI_MODEL_NAME", "inherited-model")
	home := writeEnvConf(t, "LAXCODE_ENVCONF_PROBE=from-file\nOPENAI_MODEL_NAME=ignored-model\n")
	writeSettings(t, home, `{"model":"p:chat","provider_list":[{"provider_name":"p","openai_api_key":"key","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"chat"}]}]}`)
	conf, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("LAXCODE_ENVCONF_PROBE") != "from-file" || os.Getenv("OPENAI_MODEL_NAME") != "ignored-model" || conf.Model != "p:chat" || len(conf.ProviderList) != 1 {
		t.Fatal("env.conf injection or file model selection changed")
	}
}

func boolPtr(b bool) *bool { return &b }

// TestParseMCPServers 验证 mcp_servers 段的解析：stdio 字段、环境变量、
// enabled 缺省为 true / 显式 false、url 条目可解析。
func TestParseMCPServers(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model": "example:chat",
		"provider_list": [{
			"provider_name": "example",
			"openai_api_key": "sk-file-key",
			"openai_base_url": "https://file.example.com/v1",
			"model_list": [{"model_name": "chat"}]
		}],
		"mcp_servers": {
			"filesystem": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
				"env": ["NODE_ENV=production"]
			},
			"paused": {
				"command": "uvx",
				"args": ["mcp-server-fetch"],
				"enabled": false
			},
			"remote": {
				"url": "https://mcp.example.com/mcp",
				"headers": {"Authorization": "Bearer test-token"}
			}
		}
	}`)
	if err := parseTestConfig(); err != nil {
		t.Fatalf("Load settings: %v", err)
	}
	fs := Current.MCPServers["filesystem"]
	if fs.Command != "npx" || len(fs.Args) != 3 || fs.Args[1] != "@modelcontextprotocol/server-filesystem" {
		t.Errorf("filesystem conf = %+v", fs)
	}
	if len(fs.Env) != 1 || fs.Env[0] != "NODE_ENV=production" {
		t.Errorf("filesystem env = %v, want case-preserved K=V entries", fs.Env)
	}
	if !fs.IsEnabled() {
		t.Error("unset enabled must default to enabled")
	}
	if paused := Current.MCPServers["paused"]; paused.IsEnabled() {
		t.Error("enabled=false must disable the server")
	}
	if remote := Current.MCPServers["remote"]; remote.URL != "https://mcp.example.com/mcp" ||
		headerValue(remote.Headers, "Authorization") != "Bearer test-token" {
		t.Errorf("url entry must parse, got %+v", remote)
	}
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

// TestParseMCPServersCamelCase 兼容 MCP 客户端生态通用的 mcpServers 顶层键。
func TestParseMCPServersCamelCase(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{
		"model": "example:chat",
		"provider_list": [{
			"provider_name": "example",
			"openai_api_key": "sk-file-key",
			"openai_base_url": "https://file.example.com/v1",
			"model_list": [{"model_name": "chat"}]
		}],
		"mcpServers": {
			"ov-mcp-server": {
				"url": "https://mcp.example.com/mcp",
				"headers": {"Authorization": "Bearer test-token"}
			}
		}
	}`)
	if err := parseTestConfig(); err != nil {
		t.Fatalf("Load settings: %v", err)
	}
	remote := Current.MCPServers["ov-mcp-server"]
	if remote.URL != "https://mcp.example.com/mcp" ||
		headerValue(remote.Headers, "Authorization") != "Bearer test-token" {
		t.Fatalf("camelCase mcpServers entry = %+v", remote)
	}
}

// TestValidateMCPServers 覆盖结构校验：键合法性、已启用条目必须恰好声明
// 一种传输形态；disabled 条目整体豁免。
func TestValidateMCPServers(t *testing.T) {
	cases := []struct {
		name    string
		servers map[string]MCPServerConf
		wantErr string
	}{
		{"empty", nil, ""},
		{"stdio ok", map[string]MCPServerConf{"fs": {Command: "npx"}}, ""},
		{"http ok", map[string]MCPServerConf{"remote": {
			URL: "https://x/mcp", Headers: map[string]string{"Authorization": "Bearer token"},
		}}, ""},
		{
			"both command and url",
			map[string]MCPServerConf{"x": {Command: "npx", URL: "https://x"}},
			"exactly one",
		},
		{"neither command nor url", map[string]MCPServerConf{"x": {}}, "exactly one"},
		{"bad url scheme", map[string]MCPServerConf{"x": {URL: "ftp://x/mcp"}}, "invalid HTTP url"},
		{"headers need url", map[string]MCPServerConf{"x": {
			Command: "mcp-server", Headers: map[string]string{"Authorization": "x"},
		}}, "headers require url"},
		{"invalid header name", map[string]MCPServerConf{"x": {
			URL: "https://x/mcp", Headers: map[string]string{"Bad Header": "x"},
		}}, "invalid HTTP header name"},
		{"header newline", map[string]MCPServerConf{"x": {
			URL: "https://x/mcp", Headers: map[string]string{"Authorization": "x\ny"},
		}}, "contains a newline"},
		{"transport header reserved", map[string]MCPServerConf{"x": {
			URL: "https://x/mcp", Headers: map[string]string{"Content-Type": "text/plain"},
		}}, "managed by the MCP transport"},
		{"blank key", map[string]MCPServerConf{" ": {Command: "npx"}}, "invalid mcp_servers key"},
		{
			"disabled exempt",
			map[string]MCPServerConf{"x": {Command: "npx", URL: "https://x", Enabled: boolPtr(false)}},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Conf{MCPServers: tc.servers}
			err := c.validateMCPServers()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
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

func TestCloudFlagAndSettingsDoNotApplyUserEnvironment(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{}`)
	if err := os.WriteFile(filepath.Join(home, ".laxcode", "env.conf"), []byte("LAXCODE_CLOUD_ENV_PROBE=user-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAXCODE_CLOUD_ENV_PROBE", "server-value")
	conf, err := LoadSettings(home, []string{"-cloud", "-addr=127.0.0.1:0"})
	if err != nil || !conf.Cloud || conf.Addr != "127.0.0.1:0" {
		t.Fatalf("cloud flags: %+v %v", conf, err)
	}
	if os.Getenv("LAXCODE_CLOUD_ENV_PROBE") != "server-value" {
		t.Fatal("user env changed process environment")
	}
}

func TestConfCopyDirFlag(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(t.TempDir(), "configuration source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{source, relative} {
		conf, err := LoadSettings(home, []string{"-cloud", "-conf_copy_dir=" + input})
		if err != nil || conf.ConfCopyDir != source {
			t.Fatalf("source %q: dir=%q err=%v", input, conf.ConfCopyDir, err)
		}
	}
	conf, err := LoadSettings(home, []string{"-cloud"})
	if err != nil || conf.ConfCopyDir != "" {
		t.Fatalf("default copy dir: %+v %v", conf, err)
	}
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{file, filepath.Join(home, "missing")} {
		if _, err := LoadSettings(home, []string{"-cloud", "-conf_copy_dir=" + invalid}); err == nil {
			t.Fatalf("accepted invalid source %q", invalid)
		}
	}
}
