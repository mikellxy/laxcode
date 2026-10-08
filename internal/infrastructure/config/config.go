package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/spf13/viper"
)

// MCPServerConf 声明一个外部 MCP server。stdio 使用 command + args + env；
// Streamable HTTP 使用 url + headers。enabled 缺省为 true（写配置即启用），
// 设 false 可保留声明但暂停接入。
type MCPServerConf struct {
	Command string   `mapstructure:"command" json:"command"`
	Args    []string `mapstructure:"args" json:"args"`
	// Env 是追加给子进程的环境变量，K=V 字符串数组（对齐 exec.Cmd.Env
	// 与 docker 惯例）。不用对象形式：viper 会把嵌套 map 键统一小写，
	// 而环境变量名大小写敏感。
	Env     []string          `mapstructure:"env" json:"env"`
	URL     string            `mapstructure:"url" json:"url"`
	Headers map[string]string `mapstructure:"headers" json:"headers"`
	Enabled *bool             `mapstructure:"enabled" json:"enabled"`
}

// IsEnabled 报告该 server 是否应被接入；未声明 enabled 视为启用。
func (c MCPServerConf) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// envAndFileConf contains settings inputs, never resolved model connections.
type envAndFileConf struct {
	Model                           string                   `mapstructure:"model"`
	CompactionModel                 string                   `mapstructure:"compaction_model"`
	ProviderList                    json.RawMessage          `mapstructure:"-"`
	MCPServers                      map[string]MCPServerConf `mapstructure:"mcp_servers"`
	OpenaiContextWindow             int                      `mapstructure:"openai_context_window"`
	OpenaiMaxOutputTokens           int                      `mapstructure:"openai_max_output_tokens"`
	CompactionOpenaiContextWindow   int                      `mapstructure:"compaction_openai_context_window"`
	CompactionOpenaiMaxOutputTokens int                      `mapstructure:"compaction_openai_max_output_tokens"`
	LlmRouterAddr                   string                   `mapstructure:"llm_router_addr"`
	LlmRouterURL                    string                   `mapstructure:"-"`
}

const DefaultLLMRouterAddr = "127.0.0.1:0"

var EnvAndFileConf envAndFileConf
var EnvOrFile = viper.New()

type cliConf struct {
	Addr string `mapstructure:"addr"`
	Plan bool   `mapstructure:"plan"`
}

const DefaultSSEAddr = "127.0.0.1:8090"

var CliConf cliConf
var Cli = viper.New()

// validateMCPServers 校验 mcp_servers 段的结构不变式：键非空且不含空白
// （键会进入工具名命名空间），已启用的条目必须声明且仅声明一种传输形态
// ——stdio command 或 Streamable HTTP url。运行期故障（进程起不来、
// 握手失败）不在此校验：装配时对单个 server fail-open，启动期硬失败会把
// 第三方 server 的故障放大成整个服务不可用。
func (c *envAndFileConf) validateMCPServers() error {
	for name, server := range c.MCPServers {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \t\r\n") {
			return fmt.Errorf("invalid mcp_servers key %q: must be non-empty without whitespace", name)
		}
		if !server.IsEnabled() {
			continue
		}
		hasCommand := strings.TrimSpace(server.Command) != ""
		hasURL := strings.TrimSpace(server.URL) != ""
		if hasCommand == hasURL {
			return fmt.Errorf("mcp_servers %q must declare exactly one of command (stdio) or url", name)
		}
		if hasURL {
			parsed, err := url.Parse(server.URL)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("mcp_servers %q has invalid HTTP url %q", name, server.URL)
			}
		} else if len(server.Headers) > 0 {
			return fmt.Errorf("mcp_servers %q headers require url transport", name)
		}
		for header, value := range server.Headers {
			if !validHTTPHeaderName(header) {
				return fmt.Errorf("mcp_servers %q has invalid HTTP header name %q", name, header)
			}
			if strings.ContainsAny(value, "\r\n") {
				return fmt.Errorf("mcp_servers %q HTTP header %q contains a newline", name, header)
			}
			switch strings.ToLower(header) {
			case "accept", "content-type", "last-event-id", "mcp-session-id":
				return fmt.Errorf("mcp_servers %q HTTP header %q is managed by the MCP transport", name, header)
			}
		}
	}
	return nil
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}

func ParseEnvAndFile() error {
	// env.conf 是进程环境基线，先于其余配置注入：settings.json 与 OPENAI_*
	// 环境绑定读到的是注入后的环境，bash 工具与 MCP server 派生的子进程也
	// 一并继承。必须早于任何出站 HTTP 请求——net/http 的代理环境经
	// sync.Once 在首次请求时缓存，之后注入 HTTP(S)_PROXY 不再生效。
	if homeDir, err := os.UserHomeDir(); err == nil {
		if err := ApplyEnvFile(homeDir); err != nil {
			return err
		}
	}

	var filePath string
	homeDir, err := os.UserHomeDir()
	if err == nil {
		// 用户级配置路径统一由布局包拼装（${home}/.laxcode/settings.json）
		filePath = layout.UserSettings(homeDir)
	}

	if filePath != "" {
		EnvOrFile.SetConfigFile(filePath)
	}
	err = EnvOrFile.ReadInConfig()
	if err != nil {
		if errors.As(err, &viper.ConfigFileNotFoundError{}) || errors.Is(err, os.ErrNotExist) {
		} else {
			return err
		}
	}
	// 兼容 MCP 生态常见的 camelCase 顶层键 mcpServers；LaxCode 文档仍以
	// snake_case mcp_servers 为规范。两者同时存在时规范键优先。
	if EnvOrFile.InConfig("mcpServers") {
		EnvOrFile.RegisterAlias("mcp_servers", "mcpServers")
	}

	EnvOrFile.SetDefault("OPENAI_CONTEXT_WINDOW", 200_000)
	EnvOrFile.SetDefault("OPENAI_MAX_OUTPUT_TOKENS", 16_384)
	EnvOrFile.SetDefault("LLM_ROUTER_ADDR", DefaultLLMRouterAddr)
	EnvOrFile.BindEnv("OPENAI_CONTEXT_WINDOW", "OPENAI_CONTEXT_WINDOW")
	EnvOrFile.BindEnv("OPENAI_MAX_OUTPUT_TOKENS", "OPENAI_MAX_OUTPUT_TOKENS")
	EnvOrFile.BindEnv("COMPACTION_OPENAI_CONTEXT_WINDOW", "COMPACTION_OPENAI_CONTEXT_WINDOW")
	EnvOrFile.BindEnv("COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS")
	EnvOrFile.BindEnv("LLM_ROUTER_ADDR", "LLM_ROUTER_ADDR")
	EnvOrFile.BindEnv("COMPACTION_MODEL", "COMPACTION_MODEL")
	EnvOrFile.SetEnvKeyReplacer(strings.NewReplacer("_", "_"))

	EnvAndFileConf = envAndFileConf{}
	if err = EnvOrFile.Unmarshal(&EnvAndFileConf); err != nil {
		return err
	}

	// Preserve provider_list directly from the source JSON, before Viper converts
	// nested objects into maps. Concrete provider parsing belongs to ai_models.
	EnvAndFileConf.ProviderList = nil
	if filePath != "" {
		data, readErr := os.ReadFile(filePath)
		if readErr == nil {
			var document map[string]json.RawMessage
			if err := json.Unmarshal(data, &document); err != nil {
				return err
			}
			if document == nil {
				return errors.New("settings must be a JSON object")
			}
			for key, value := range document {
				if strings.EqualFold(key, "provider_list") {
					EnvAndFileConf.ProviderList = value
				}
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
	}
	return EnvAndFileConf.validateMCPServers()
}

// ParseCli 解析命令行参数到 CliConf：用标准库 flag 定义与 cliConf 字段
// 一一对应的参数（flag 名与 mapstructure tag 保持一致，Cli viper 方能按
// key 匹配），flag.Parse 后把各值写入 Cli 实例再 Unmarshal 到 CliConf，
// 与 ParseEnvAndFile 的 viper 装配风格对称。
//
// 与 ParseEnvAndFile 不同，本函数内含 flag.Parse 会消费 os.Args，须由 main
// 在启动早期显式调用，不宜放入包 init——否则 go test 的测试二进制会在
// testing 注册 -test.* 参数之前执行 flag.Parse，遇到 -test.v 等以“未定义
// 参数”直接退出（老 internal/config 亦是由 main 显式调用 Parse）。
func ParseCli() error {
	addr := flag.String("addr", DefaultSSEAddr, "Web backend listen address")
	plan := flag.Bool("plan", false, "enable plan mode")
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flag.Args())
	}
	Cli.Set("addr", *addr)
	Cli.Set("plan", *plan)
	if err := Cli.Unmarshal(&CliConf); err != nil {
		return err
	}
	return nil
}
