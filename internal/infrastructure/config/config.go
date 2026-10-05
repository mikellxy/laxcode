package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"unicode"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/spf13/viper"
)

// ModelLimit 是 model_list 条目里的模型级 token 预算（limit.context /
// limit.output）：声明后作为该模型 LLM client 的 bucket，覆盖全局窗口配置。
type ModelLimit struct {
	Context int `mapstructure:"context" json:"context"`
	Output  int `mapstructure:"output" json:"output"`
}

type ModelConfig struct {
	ModelName     string      `mapstructure:"model_name" json:"model_name"`
	UpstreamModel string      `mapstructure:"-" json:"upstream_model,omitempty"`
	Limit         *ModelLimit `mapstructure:"limit" json:"limit,omitempty"`
}

type ProviderConfig struct {
	OpenaiApiKey  string        `mapstructure:"openai_api_key" json:"openai_api_key"`
	OpenaiBaseUrl string        `mapstructure:"openai_base_url" json:"openai_base_url"`
	ProviderName  string        `mapstructure:"provider_name" json:"provider_name"`
	ModelList     []ModelConfig `mapstructure:"model_list" json:"model_list"`
}

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

type ResolvedModel struct {
	Ref           string
	ProviderName  string
	ModelName     string
	UpstreamModel string
	OpenaiApiKey  string
	OpenaiBaseUrl string
	// ContextWindow / MaxOutputTokens 是该模型的生效 token 预算：模型级
	// limit 优先，未声明时回退全局 openai_context_window /
	// openai_max_output_tokens。
	ContextWindow   int
	MaxOutputTokens int
	hasLimit        bool
}

// envAndFileConf 的 mapstructure tag 与 settings.json 的键一致（小写
// snake_case）；mapstructure 按大小写不敏感匹配，旧版大写键的配置文件仍可
// 解析。Openai* 派生字段不从文件读取（mapstructure:"-"），由 setActiveModel
// 按活跃模型维护。
type envAndFileConf struct {

	// 辅助模型使用 OPENAI_* 环境变量覆盖时，该引用是展示别名，
	// 不作为可切换的模型目录条目。
	CompactionModel string           `mapstructure:"compaction_model"`
	Model           string           `mapstructure:"model"`
	ProviderList    []ProviderConfig `mapstructure:"provider_list"`

	// MCPServers 声明外部 MCP（Model Context Protocol）server，code 模式
	// 装配时接入其工具（见 cmd/agentasm 与 internal/infrastructure/mcp）。
	// 键为 server 名，进入工具名命名空间（mcp__<server>__<tool>）。
	MCPServers map[string]MCPServerConf `mapstructure:"mcp_servers"`

	// Openai* 是由 Model 解析出的当前运行时有效配置，不直接从配置文件反序列化。
	OpenaiApiKey  string `mapstructure:"-"`
	OpenaiBaseUrl string `mapstructure:"-"`
	OpenaiModel   string `mapstructure:"-"`

	OpenaiContextWindow             int    `mapstructure:"openai_context_window"`
	OpenaiMaxOutputTokens           int    `mapstructure:"openai_max_output_tokens"`
	CompactionOpenaiApiKey          string `mapstructure:"-"`
	CompactionOpenaiBaseUrl         string `mapstructure:"-"`
	CompactionOpenaiModel           string `mapstructure:"-"`
	CompactionOpenaiContextWindow   int    `mapstructure:"compaction_openai_context_window"`
	CompactionOpenaiMaxOutputTokens int    `mapstructure:"compaction_openai_max_output_tokens"`
	LlmRouterAddr                   string `mapstructure:"llm_router_addr"`
	// LlmRouterURL 是进程启动后写入的实际本地端点，不从环境或配置文件读取。
	LlmRouterURL string `mapstructure:"-"`

	// compactionConfigured 记录压缩模型是否被显式配置（compaction_model 引用
	// 或 OPENAI_COMPACTION_* 环境变量）。未显式配置时压缩模型继承主模型，
	// setActiveModel 切换主模型后需同步重推导。
	compactionConfigured bool
	// rawCompactionContextWindow / rawCompactionMaxOutputTokens 是压缩窗口的
	// 原始配置值（文件/环境），在派生值覆盖前快照，供运行期重推导复用。
	rawCompactionContextWindow   int
	rawCompactionMaxOutputTokens int
}

const (
	// 兼容端点的 /models 响应不会标准化暴露 context window，
	// 因此给出保守默认值，并允许按实际部署显式配置。
	DefaultContextWindow   = 200_000
	DefaultMaxOutputTokens = 16_384
	// 端口 0 让操作系统分配空闲端口，避免多个 laxcode 进程互相冲突；如需稳定
	// 地址供外部客户端访问，可通过 LLM_ROUTER_ADDR 显式覆盖。
	DefaultLLMRouterAddr = "127.0.0.1:0"
)

var EnvAndFileConf envAndFileConf

var EnvOrFile = viper.New()

const (
	envProviderName = "env_provider"
	envModelName    = "env_model"
)

func modelRef(providerName, modelName string) string {
	return providerName + ":" + modelName
}

func validCatalogName(name string) bool {
	return name != "" && !strings.ContainsRune(name, ':') &&
		strings.IndexFunc(name, unicode.IsSpace) < 0
}

func (c *envAndFileConf) resolveModel(ref string) (ResolvedModel, error) {
	providerName, modelName, ok := strings.Cut(ref, ":")
	if !ok || !validCatalogName(providerName) || !validCatalogName(modelName) {
		return ResolvedModel{}, fmt.Errorf("invalid model reference %q; expected provider:model", ref)
	}
	for _, provider := range c.ProviderList {
		if provider.ProviderName != providerName {
			continue
		}
		for _, model := range provider.ModelList {
			if model.ModelName != modelName {
				continue
			}
			upstreamModel := model.UpstreamModel
			if upstreamModel == "" {
				upstreamModel = model.ModelName
			}
			resolved := ResolvedModel{
				Ref:             ref,
				ProviderName:    providerName,
				ModelName:       modelName,
				UpstreamModel:   upstreamModel,
				OpenaiApiKey:    provider.OpenaiApiKey,
				OpenaiBaseUrl:   provider.OpenaiBaseUrl,
				ContextWindow:   c.OpenaiContextWindow,
				MaxOutputTokens: c.OpenaiMaxOutputTokens,
			}
			// 模型级 limit 覆盖全局窗口；validateModelCatalog 保证 limit
			// 一旦声明则两项均合法，未声明（nil）时保持全局回退值。
			if model.Limit != nil {
				resolved.ContextWindow = model.Limit.Context
				resolved.MaxOutputTokens = model.Limit.Output
				resolved.hasLimit = true
			}
			// 显式环境变量优先于配置文件的模型级 limit。
			if strings.TrimSpace(os.Getenv("OPENAI_CONTEXT_WINDOW")) != "" {
				resolved.ContextWindow = c.OpenaiContextWindow
			}
			if strings.TrimSpace(os.Getenv("OPENAI_MAX_OUTPUT_TOKENS")) != "" {
				resolved.MaxOutputTokens = c.OpenaiMaxOutputTokens
			}
			return resolved, nil
		}
		return ResolvedModel{}, fmt.Errorf("model %q is not configured for provider %q", modelName, providerName)
	}
	return ResolvedModel{}, fmt.Errorf("provider %q is not configured", providerName)
}

func (c *envAndFileConf) validateModelCatalog() error {
	providers := make(map[string]struct{}, len(c.ProviderList))
	for _, provider := range c.ProviderList {
		if !validCatalogName(provider.ProviderName) {
			return fmt.Errorf("invalid provider_name %q", provider.ProviderName)
		}
		if provider.ProviderName == envProviderName &&
			(len(provider.ModelList) != 1 || provider.ModelList[0].ModelName != envModelName ||
				provider.ModelList[0].UpstreamModel == "") {
			return fmt.Errorf("provider_name %q is reserved for environment configuration", envProviderName)
		}
		if _, exists := providers[provider.ProviderName]; exists {
			return fmt.Errorf("duplicate provider_name %q", provider.ProviderName)
		}
		providers[provider.ProviderName] = struct{}{}
		if strings.TrimSpace(provider.OpenaiApiKey) == "" || strings.TrimSpace(provider.OpenaiBaseUrl) == "" {
			return fmt.Errorf("provider %q requires openai_api_key and openai_base_url", provider.ProviderName)
		}
		if len(provider.ModelList) == 0 {
			return fmt.Errorf("provider %q requires at least one model", provider.ProviderName)
		}
		models := make(map[string]struct{}, len(provider.ModelList))
		for _, model := range provider.ModelList {
			if !validCatalogName(model.ModelName) {
				return fmt.Errorf("invalid model_name %q for provider %q", model.ModelName, provider.ProviderName)
			}
			if model.Limit != nil &&
				(model.Limit.Context <= 0 || model.Limit.Output <= 0 || model.Limit.Output >= model.Limit.Context) {
				return fmt.Errorf("invalid limit for model %q of provider %q: context and output must be positive and output must be smaller than context",
					model.ModelName, provider.ProviderName)
			}
			if _, exists := models[model.ModelName]; exists {
				return fmt.Errorf("duplicate model_name %q for provider %q", model.ModelName, provider.ProviderName)
			}
			models[model.ModelName] = struct{}{}
		}
	}
	if len(c.ProviderList) == 0 {
		if c.Model != "" {
			return fmt.Errorf("model %q is set but provider_list is empty", c.Model)
		}
		// 空目录且未选择模型是合法的「未配置」状态：SSE 模式允许先启动进入
		// 页面，再经 POST /api/models 添加；是否要求必须配置由调用方按模式决定。
		return nil
	}
	_, err := c.resolveModel(c.Model)
	return err
}

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

func (c *envAndFileConf) setActiveModel(ref string) error {
	resolved, err := c.resolveModel(ref)
	if err != nil {
		return err
	}
	c.Model = resolved.Ref
	c.OpenaiApiKey = resolved.OpenaiApiKey
	c.OpenaiBaseUrl = resolved.OpenaiBaseUrl
	c.OpenaiModel = resolved.UpstreamModel
	// 压缩模型未显式配置时继承主模型；主模型切换（含延迟配置后的首次激
	// 活）后同步重推导，保证运行期装配读到与新主模型一致的压缩配置。
	if !c.compactionConfigured {
		c.CompactionModel = resolved.Ref
		c.CompactionOpenaiApiKey = resolved.OpenaiApiKey
		c.CompactionOpenaiBaseUrl = resolved.OpenaiBaseUrl
		c.CompactionOpenaiModel = resolved.UpstreamModel
		c.CompactionOpenaiContextWindow = effectiveAuxiliaryBudget(
			resolved.ContextWindow, c.rawCompactionContextWindow, resolved.hasLimit, "COMPACTION_OPENAI_CONTEXT_WINDOW")
		c.CompactionOpenaiMaxOutputTokens = effectiveAuxiliaryBudget(
			resolved.MaxOutputTokens, c.rawCompactionMaxOutputTokens, resolved.hasLimit, "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS")
	}
	return nil
}

type modelEnvironment struct {
	apiKey, baseURL, model string
}

func readModelEnvironment(prefix string) modelEnvironment {
	return modelEnvironment{
		apiKey:  strings.TrimSpace(os.Getenv(prefix + "API_KEY")),
		baseURL: strings.TrimSpace(os.Getenv(prefix + "BASE_URL")),
		model:   strings.TrimSpace(os.Getenv(prefix + "MODEL_NAME")),
	}
}

func (e modelEnvironment) count() int {
	count := 0
	for _, value := range []string{e.apiKey, e.baseURL, e.model} {
		if value != "" {
			count++
		}
	}
	return count
}

func (e modelEnvironment) apply(resolved *ResolvedModel) {
	if e.count() == 0 {
		return
	}
	resolved.Ref = modelRef(envProviderName, envModelName)
	if e.apiKey != "" {
		resolved.OpenaiApiKey = e.apiKey
	}
	if e.baseURL != "" {
		resolved.OpenaiBaseUrl = e.baseURL
	}
	if e.model != "" {
		resolved.UpstreamModel = e.model
	}
}

func effectiveAuxiliaryBudget(modelValue, configuredValue int, hasModelLimit bool, envKey string) int {
	if strings.TrimSpace(os.Getenv(envKey)) != "" {
		return configuredValue
	}
	if hasModelLimit {
		return modelValue
	}
	if configuredValue != 0 {
		return configuredValue
	}
	return modelValue
}

// resolveAuxiliaryModel 从模型目录解析文件引用，再逐项应用非空环境变量。
// 完整的环境配置无需依赖文件引用；未配置压缩模型时继承主模型。
func (c *envAndFileConf) resolveAuxiliaryModel(key, ref string, env modelEnvironment, fallback ResolvedModel) (ResolvedModel, error) {
	resolved := fallback
	if env.count() == 3 {
		resolved = ResolvedModel{
			ContextWindow: c.OpenaiContextWindow, MaxOutputTokens: c.OpenaiMaxOutputTokens,
		}
	} else if ref != "" {
		var err error
		resolved, err = c.resolveModel(ref)
		if err != nil {
			return ResolvedModel{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	if env.model != "" && env.model != resolved.UpstreamModel {
		// 模型名被环境变量替换后，原模型的 limit 不再适用。
		resolved.ContextWindow = c.OpenaiContextWindow
		resolved.MaxOutputTokens = c.OpenaiMaxOutputTokens
		resolved.hasLimit = false
	}
	env.apply(&resolved)
	return resolved, nil
}

// ResolveModel 将 provider:model 引用解析为创建 provider/client 所需的运行时配置。
func ResolveModel(ref string) (ResolvedModel, error) { return EnvAndFileConf.resolveModel(ref) }

// SetActiveModel 更新当前进程使用的模型引用及其派生连接参数，不写回配置文件。
func SetActiveModel(ref string) error { return EnvAndFileConf.setActiveModel(ref) }

// ActiveModelBudget 返回当前活跃主模型的 token 预算：模型级 limit 优先，未声明
// 时回退全局 openai_context_window / openai_max_output_tokens。目录在启动时已
// 校验，解析失败（仅可能出现在测试等手工构造的配置上）退回全局窗口配置。
func ActiveModelBudget() (contextWindow, maxOutputTokens int) {
	if resolved, err := EnvAndFileConf.resolveModel(EnvAndFileConf.Model); err == nil {
		return resolved.ContextWindow, resolved.MaxOutputTokens
	}
	return EnvAndFileConf.OpenaiContextWindow, EnvAndFileConf.OpenaiMaxOutputTokens
}

type cliConf struct {
	Addr        string `mapstructure:"addr"`
	Plan        bool   `mapstructure:"plan"`
	TokenBudget int    `mapstructure:"token-budget"`
}

// DefaultSSEAddr 是 sse server 模式的缺省监听地址：仅绑定本地回环，因为
// Agent 具备 bash / 写文件能力，默认不对外暴露；需要对外时以 -addr 覆盖。
const DefaultSSEAddr = "127.0.0.1:8090"

var CliConf cliConf

var Cli = viper.New()

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

	EnvOrFile.SetDefault("OPENAI_CONTEXT_WINDOW", DefaultContextWindow)
	EnvOrFile.SetDefault("OPENAI_MAX_OUTPUT_TOKENS", DefaultMaxOutputTokens)
	EnvOrFile.SetDefault("LLM_ROUTER_ADDR", DefaultLLMRouterAddr)
	EnvOrFile.BindEnv("OPENAI_CONTEXT_WINDOW", "OPENAI_CONTEXT_WINDOW")
	EnvOrFile.BindEnv("OPENAI_MAX_OUTPUT_TOKENS", "OPENAI_MAX_OUTPUT_TOKENS")
	EnvOrFile.BindEnv("COMPACTION_OPENAI_CONTEXT_WINDOW", "COMPACTION_OPENAI_CONTEXT_WINDOW")
	EnvOrFile.BindEnv("COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS")
	EnvOrFile.BindEnv("LLM_ROUTER_ADDR", "LLM_ROUTER_ADDR")
	EnvOrFile.BindEnv("COMPACTION_MODEL", "COMPACTION_MODEL")
	EnvOrFile.SetEnvKeyReplacer(strings.NewReplacer("_", "_"))

	if err = EnvOrFile.Unmarshal(&EnvAndFileConf); err != nil {
		return err
	}

	// 完整的 OPENAI_* 三元组作为一个保留别名的临时 provider 追加到目录，并
	// 覆盖当前选择。部分设置不与文件配置拼接，避免凭据、端点和模型错配。
	mainEnv := readModelEnvironment("OPENAI_")
	envValues := mainEnv.count()
	if envValues != 0 && envValues != 3 {
		return errors.New("OPENAI_API_KEY, OPENAI_BASE_URL and OPENAI_MODEL_NAME must be set together")
	}
	if envValues == 3 {
		for _, provider := range EnvAndFileConf.ProviderList {
			if provider.ProviderName == envProviderName {
				return fmt.Errorf("PROVIDER_NAME %q is reserved for environment configuration", envProviderName)
			}
		}
		EnvAndFileConf.ProviderList = append(EnvAndFileConf.ProviderList, ProviderConfig{
			ProviderName:  envProviderName,
			OpenaiApiKey:  mainEnv.apiKey,
			OpenaiBaseUrl: mainEnv.baseURL,
			ModelList: []ModelConfig{{
				ModelName:     envModelName,
				UpstreamModel: mainEnv.model,
			}},
		})
		EnvAndFileConf.Model = modelRef(envProviderName, envModelName)
	}
	// 在派生值覆盖前快照压缩模型的显式配置：compactionConfigured 决定
	// setActiveModel 是否随主模型重推导压缩模型；raw* 是压缩窗口的原始
	// 配置值，供重推导时复用（文件/环境里未配置时为零值）。
	EnvAndFileConf.compactionConfigured = strings.TrimSpace(EnvAndFileConf.CompactionModel) != "" ||
		readModelEnvironment("OPENAI_COMPACTION_").count() > 0
	EnvAndFileConf.rawCompactionContextWindow = EnvAndFileConf.CompactionOpenaiContextWindow
	EnvAndFileConf.rawCompactionMaxOutputTokens = EnvAndFileConf.CompactionOpenaiMaxOutputTokens
	// 目录非空但未选择模型时默认选中第一个条目：维持「目录非空 ⟹ 活跃模型
	// 可解析」的不变式，也让延迟配置（SSE 模式先添加模型）在重启后无需再
	// 手动选择。
	if len(EnvAndFileConf.ProviderList) > 0 && strings.TrimSpace(EnvAndFileConf.Model) == "" {
		first := EnvAndFileConf.ProviderList[0]
		EnvAndFileConf.Model = modelRef(first.ProviderName, first.ModelList[0].ModelName)
	}
	if err := EnvAndFileConf.validateModelCatalog(); err != nil {
		return err
	}
	if err := EnvAndFileConf.validateMCPServers(); err != nil {
		return err
	}
	// 目录可为空（SSE 模式的延迟配置状态）：无活跃模型时跳过激活，压缩模型
	// 的派生与校验一并推迟到添加并激活首个模型之后。
	if EnvAndFileConf.Model != "" {
		if err := EnvAndFileConf.setActiveModel(EnvAndFileConf.Model); err != nil {
			return err
		}
	}
	mainModel, _ := EnvAndFileConf.resolveModel(EnvAndFileConf.Model)
	compaction, err := EnvAndFileConf.resolveAuxiliaryModel(
		"COMPACTION_MODEL", EnvAndFileConf.CompactionModel,
		readModelEnvironment("OPENAI_COMPACTION_"), mainModel)
	if err != nil {
		return err
	}
	EnvAndFileConf.CompactionModel = compaction.Ref
	EnvAndFileConf.CompactionOpenaiApiKey = compaction.OpenaiApiKey
	EnvAndFileConf.CompactionOpenaiBaseUrl = compaction.OpenaiBaseUrl
	EnvAndFileConf.CompactionOpenaiModel = compaction.UpstreamModel
	EnvAndFileConf.CompactionOpenaiContextWindow = effectiveAuxiliaryBudget(
		compaction.ContextWindow, EnvAndFileConf.CompactionOpenaiContextWindow,
		compaction.hasLimit, "COMPACTION_OPENAI_CONTEXT_WINDOW")
	EnvAndFileConf.CompactionOpenaiMaxOutputTokens = effectiveAuxiliaryBudget(
		compaction.MaxOutputTokens, EnvAndFileConf.CompactionOpenaiMaxOutputTokens,
		compaction.hasLimit, "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS")
	if EnvAndFileConf.OpenaiContextWindow <= 0 {
		return errors.New("openai_context_window must be positive")
	}
	if EnvAndFileConf.OpenaiMaxOutputTokens <= 0 ||
		EnvAndFileConf.OpenaiMaxOutputTokens >= EnvAndFileConf.OpenaiContextWindow {
		return errors.New("openai_max_output_tokens must be positive and smaller than openai_context_window")
	}
	if EnvAndFileConf.Model != "" {
		if EnvAndFileConf.CompactionOpenaiContextWindow <= 0 {
			return errors.New("compaction_openai_context_window must be positive")
		}
		if EnvAndFileConf.CompactionOpenaiMaxOutputTokens <= 0 ||
			EnvAndFileConf.CompactionOpenaiMaxOutputTokens >= EnvAndFileConf.CompactionOpenaiContextWindow {
			return errors.New("compaction_openai_max_output_tokens must be positive and smaller than compaction_openai_context_window")
		}
	}
	if window, output := ActiveModelBudget(); window <= 0 || output <= 0 || output >= window {
		return errors.New("active model limit must have positive context and output smaller than context")
	}

	return nil
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
	tokenBudget := flag.Int("token-budget", 0, "token budget per session; 0 disables confirmation")
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flag.Args())
	}
	Cli.Set("addr", *addr)
	Cli.Set("plan", *plan)
	Cli.Set("token-budget", *tokenBudget)
	if err := Cli.Unmarshal(&CliConf); err != nil {
		return err
	}
	if CliConf.TokenBudget < 0 {
		return fmt.Errorf("-token-budget must be non-negative")
	}
	return nil
}
