package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		"EMBEDDING_MODEL", "COMPACTION_MODEL", "EMBEDDING_VEC_DIM",
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

func TestParseEnvAndFileCorruptFileReturnsError(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"openai_api_key": `) // 非法 JSON

	if err := ParseEnvAndFile(); err == nil {
		t.Fatal("settings.json 非法时应返回错误")
	}
}

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
	swapCliGlobals(t, "-addr=127.0.0.1:9000", "-plan")
	if err := ParseCli(); err != nil {
		t.Fatal(err)
	}
	if CliConf.Addr != "127.0.0.1:9000" || !CliConf.Plan {
		t.Fatalf("config=%+v", CliConf)
	}
}

func TestParseCliDefaults(t *testing.T) {
	swapCliGlobals(t)
	if err := ParseCli(); err != nil {
		t.Fatal(err)
	}
	if CliConf.Addr != DefaultSSEAddr || CliConf.Plan {
		t.Fatalf("config=%+v", CliConf)
	}
}

func TestParseCliRejectsRemovedModesAndInvalidArguments(t *testing.T) {
	for _, arg := range []string{"-sse", "-mode=code", "-mode=rag", "-code", "-rag", "-kb=x", "-session=x", "-token-budget=1000", "unexpected"} {
		t.Run(arg, func(t *testing.T) {
			swapCliGlobals(t, arg)
			if err := ParseCli(); err == nil {
				t.Fatalf("accepted %q", arg)
			}
		})
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
	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	fs := EnvAndFileConf.MCPServers["filesystem"]
	if fs.Command != "npx" || len(fs.Args) != 3 || fs.Args[1] != "@modelcontextprotocol/server-filesystem" {
		t.Errorf("filesystem conf = %+v", fs)
	}
	if len(fs.Env) != 1 || fs.Env[0] != "NODE_ENV=production" {
		t.Errorf("filesystem env = %v, want case-preserved K=V entries", fs.Env)
	}
	if !fs.IsEnabled() {
		t.Error("unset enabled must default to enabled")
	}
	if paused := EnvAndFileConf.MCPServers["paused"]; paused.IsEnabled() {
		t.Error("enabled=false must disable the server")
	}
	if remote := EnvAndFileConf.MCPServers["remote"]; remote.URL != "https://mcp.example.com/mcp" ||
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
	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	remote := EnvAndFileConf.MCPServers["ov-mcp-server"]
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
			c := &envAndFileConf{MCPServers: tc.servers}
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
	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("retired configuration blocked Web startup: %v", err)
	}
}

func TestProviderListRemainsRawUntilModelInitialization(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// The config reader accepts JSON without interpreting provider-specific fields.
	raw := `[{ "provider_name": "CaseSensitive", "future_option": {"MixedCase": true}, "model_list": "invalid-for-ai-models" }]`
	writeSettings(t, home, `{"model":"CaseSensitive:model","provider_list":`+raw+`}`)
	if err := ParseEnvAndFile(); err != nil {
		t.Fatal(err)
	}
	if string(EnvAndFileConf.ProviderList) != raw || EnvAndFileConf.Model != "CaseSensitive:model" {
		t.Fatal("provider JSON or model reference was transformed")
	}
}
