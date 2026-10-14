package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
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

// Conf is the merged startup configuration. Runtime model selections belong to ai_models.
type Conf struct {
	Model           string
	CompactionModel string
	ProviderList    []Provider
	MCPServers      map[string]MCPServerConf
	LlmRouterAddr   string
	LlmRouterURL    string
	Addr            string
	Plan            bool
	Cloud           bool
}

type Provider struct {
	ProviderName  string  `json:"provider_name" mapstructure:"provider_name"`
	AuthType      string  `json:"auth_type,omitempty" mapstructure:"auth_type"`
	OpenaiApiKey  string  `json:"openai_api_key,omitempty" mapstructure:"openai_api_key"`
	OpenaiBaseUrl string  `json:"openai_base_url" mapstructure:"openai_base_url"`
	CredentialRef string  `json:"credential_ref,omitempty" mapstructure:"credential_ref"`
	ModelList     []Model `json:"model_list" mapstructure:"model_list"`
}

// DeepCopy returns a provider with an independent model list and model limits.
func (p Provider) DeepCopy() Provider {
	cloned := p
	if p.ModelList != nil {
		cloned.ModelList = make([]Model, len(p.ModelList))
		copy(cloned.ModelList, p.ModelList)
		for i := range cloned.ModelList {
			if limit := cloned.ModelList[i].Limit; limit != nil {
				limitCopy := *limit
				cloned.ModelList[i].Limit = &limitCopy
			}
		}
	}
	return cloned
}

type Model struct {
	ModelName       string      `json:"model_name" mapstructure:"model_name"`
	UpstreamModel   string      `json:"upstream_model,omitempty" mapstructure:"upstream_model"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty" mapstructure:"reasoning_effort"`
	Limit           *ModelLimit `json:"limit,omitempty" mapstructure:"limit"`
}

type ModelLimit struct {
	Context int `json:"context" mapstructure:"context"`
	Output  int `json:"output" mapstructure:"output"`
}

type fileConf struct {
	Model           string                   `mapstructure:"model"`
	CompactionModel string                   `mapstructure:"compaction_model"`
	ProviderList    []Provider               `mapstructure:"provider_list"`
	MCPServers      map[string]MCPServerConf `mapstructure:"mcp_servers"`
	LlmRouterAddr   string                   `mapstructure:"llm_router_addr"`
	LlmRouterURL    string                   `mapstructure:"-"`
}

type cliConf struct {
	Addr          string `mapstructure:"addr"`
	Plan          bool   `mapstructure:"plan"`
	Cloud         bool   `mapstructure:"cloud"`
	ModelName     string `mapstructure:"model"`
	OpenaiApiKey  string `mapstructure:"openai_api_key"`
	OpenaiBaseUrl string `mapstructure:"openai_base_url"`
}

const (
	DefaultLLMRouterAddr   = "127.0.0.1:0"
	DefaultSSEAddr         = "127.0.0.1:8090"
	DefaultContextWindow   = 200_000
	DefaultMaxOutputTokens = 16_384
	CLIProviderName        = "env"
)

var Current Conf

func DefaultModelLimit() ModelLimit {
	return ModelLimit{Context: DefaultContextWindow, Output: DefaultMaxOutputTokens}
}

// Parse loads settings and CLI arguments before any outbound requests.
func Parse() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	conf, err := Load(homeDir, os.Args[1:])
	if err != nil {
		return err
	}
	Current = conf
	return nil
}

func Load(homeDir string, args []string) (Conf, error) {
	if err := ApplyEnvFile(homeDir); err != nil {
		return Conf{}, err
	}
	return LoadSettings(homeDir, args)
}

// LoadSettings reads settings and flags without changing process environment.
func LoadSettings(homeDir string, args []string) (Conf, error) {
	file, err := parseFile(homeDir)
	if err != nil {
		return Conf{}, err
	}
	cli, err := parseCLI(args)
	if err != nil {
		return Conf{}, err
	}
	return merge(file, cli)
}

func parseFile(homeDir string) (fileConf, error) {
	data, err := os.ReadFile(layout.UserSettings(homeDir))
	if errors.Is(err, os.ErrNotExist) {
		return fileConf{}, nil
	}
	if err != nil {
		return fileConf{}, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return fileConf{}, errors.New("settings must be a JSON object")
	}
	reader := viper.New()
	reader.SetConfigType("json")
	if err := reader.ReadConfig(bytes.NewReader(data)); err != nil {
		return fileConf{}, err
	}
	if !reader.InConfig("mcp_servers") && reader.InConfig("mcpServers") {
		reader.RegisterAlias("mcp_servers", "mcpServers")
	}
	var file fileConf
	if err := reader.Unmarshal(&file); err != nil {
		return fileConf{}, err
	}
	return file, nil
}

func parseCLI(args []string) (cliConf, error) {
	var cli cliConf
	flags := flag.NewFlagSet("laxcode", flag.ContinueOnError)
	// The caller reports parse errors; avoid printing the same diagnostic twice.
	flags.SetOutput(io.Discard)
	flags.Usage = func() {
		flags.SetOutput(os.Stderr)
		fmt.Fprintln(os.Stderr, "Usage of laxcode:")
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	flags.StringVar(&cli.Addr, "addr", DefaultSSEAddr, "Web backend listen address")
	flags.BoolVar(&cli.Plan, "plan", false, "enable plan mode")
	flags.BoolVar(&cli.Cloud, "cloud", false, "enable cloud prototype with per-user workspaces")
	flags.StringVar(&cli.ModelName, "model", "", "upstream model name (requires API key and base URL)")
	flags.StringVar(&cli.OpenaiApiKey, "openai_api_key", "", "API key for the CLI model")
	flags.StringVar(&cli.OpenaiBaseUrl, "openai_base_url", "", "base URL for the CLI model")
	if err := flags.Parse(args); err != nil {
		return cliConf{}, err
	}
	if flags.NArg() != 0 {
		return cliConf{}, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	return cli, nil
}

func merge(file fileConf, cli cliConf) (Conf, error) {
	conf := Conf{LlmRouterAddr: DefaultLLMRouterAddr, Addr: DefaultSSEAddr}
	conf.Model, conf.CompactionModel = file.Model, file.CompactionModel
	conf.ProviderList, conf.MCPServers = file.ProviderList, file.MCPServers
	if file.LlmRouterAddr != "" {
		conf.LlmRouterAddr = file.LlmRouterAddr
	}
	conf.Addr, conf.Plan, conf.Cloud = cli.Addr, cli.Plan, cli.Cloud
	for _, provider := range conf.ProviderList {
		if provider.ProviderName == CLIProviderName {
			return Conf{}, fmt.Errorf("provider_name %q is reserved for CLI configuration", CLIProviderName)
		}
	}
	cli.ModelName = strings.TrimSpace(cli.ModelName)
	cli.OpenaiApiKey = strings.TrimSpace(cli.OpenaiApiKey)
	cli.OpenaiBaseUrl = strings.TrimSpace(cli.OpenaiBaseUrl)
	supplied := 0
	for _, value := range []string{cli.ModelName, cli.OpenaiApiKey, cli.OpenaiBaseUrl} {
		if value != "" {
			supplied++
		}
	}
	if supplied != 0 && supplied != 3 {
		return Conf{}, errors.New("-model, -openai_api_key and -openai_base_url must be set together")
	}
	if supplied == 3 {
		limit := DefaultModelLimit()
		conf.ProviderList = append(conf.ProviderList, Provider{ProviderName: CLIProviderName, AuthType: "api_key",
			OpenaiApiKey: cli.OpenaiApiKey, OpenaiBaseUrl: cli.OpenaiBaseUrl,
			ModelList: []Model{{ModelName: cli.ModelName, Limit: &limit}},
		})
		conf.Model = CLIProviderName + ":" + cli.ModelName
	}
	for i := range conf.ProviderList {
		for j := range conf.ProviderList[i].ModelList {
			model := &conf.ProviderList[i].ModelList[j]
			if model.Limit == nil {
				limit := DefaultModelLimit()
				model.Limit = &limit
			}
			if model.Limit.Context <= 0 || model.Limit.Output <= 0 || model.Limit.Output >= model.Limit.Context {
				return Conf{}, fmt.Errorf("invalid limit for %s:%s: context and output must be positive and output smaller than context", conf.ProviderList[i].ProviderName, model.ModelName)
			}
		}
	}
	if err := conf.validateMCPServers(); err != nil {
		return Conf{}, err
	}
	return conf, nil
}

// validateMCPServers 校验 mcp_servers 段的结构不变式：键非空且不含空白
// （键会进入工具名命名空间），已启用的条目必须声明且仅声明一种传输形态
// ——stdio command 或 Streamable HTTP url。运行期故障（进程起不来、
// 握手失败）不在此校验：装配时对单个 server fail-open，启动期硬失败会把
// 第三方 server 的故障放大成整个服务不可用。
func (c *Conf) validateMCPServers() error {
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
