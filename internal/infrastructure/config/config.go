package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/spf13/viper"
)

type ModelConfig struct {
	ModelName     string `mapstructure:"MODEL_NAME"`
	UpstreamModel string `mapstructure:"-"`
}

type ProviderConfig struct {
	OpenaiApiKey  string        `mapstructure:"OPENAI_API_KEY"`
	OpenaiBaseUrl string        `mapstructure:"OPENAI_BASE_URL"`
	ProviderName  string        `mapstructure:"PROVIDER_NAME"`
	ModelList     []ModelConfig `mapstructure:"MODEL_LIST"`
}

type ResolvedModel struct {
	Ref           string
	ProviderName  string
	ModelName     string
	UpstreamModel string
	OpenaiApiKey  string
	OpenaiBaseUrl string
}

type envAndFileConf struct {
	UserMemoryExecutable     string `mapstructure:"USER_MEMORY_EXECUTABLE"`
	UserMemoryConcurrency    int    `mapstructure:"USER_MEMORY_CONCURRENCY"`
	UserMemoryTimeoutSeconds int    `mapstructure:"USER_MEMORY_TIMEOUT_SECONDS"`

	EmbeddingModel  string           `mapstructure:"EMBEDDING_MODEL"`
	CompactionModel string           `mapstructure:"COMPACTION_MODEL"`
	Model           string           `mapstructure:"MODEL"`
	ProviderList    []ProviderConfig `mapstructure:"PROVIDER_LIST"`

	// Openai* 是由 Model 解析出的当前运行时有效配置，不直接从配置文件反序列化。
	OpenaiApiKey  string `mapstructure:"-"`
	OpenaiBaseUrl string `mapstructure:"-"`
	OpenaiModel   string `mapstructure:"-"`

	EmbedOpenaiApiKey               string `mapstructure:"-"`
	EmbedOpenaiBaseUrl              string `mapstructure:"-"`
	EmbedOpenaiModel                string `mapstructure:"-"`
	OpenaiContextWindow             int    `mapstructure:"OPENAI_CONTEXT_WINDOW"`
	OpenaiMaxOutputTokens           int    `mapstructure:"OPENAI_MAX_OUTPUT_TOKENS"`
	CompactionOpenaiApiKey          string `mapstructure:"-"`
	CompactionOpenaiBaseUrl         string `mapstructure:"-"`
	CompactionOpenaiModel           string `mapstructure:"-"`
	CompactionOpenaiContextWindow   int    `mapstructure:"COMPACTION_OPENAI_CONTEXT_WINDOW"`
	CompactionOpenaiMaxOutputTokens int    `mapstructure:"COMPACTION_OPENAI_MAX_OUTPUT_TOKENS"`
	LlmRouterAddr                   string `mapstructure:"LLM_ROUTER_ADDR"`
	// LlmRouterURL 是进程启动后写入的实际本地端点，不从环境或配置文件读取。
	LlmRouterURL string `mapstructure:"-"`
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
			return ResolvedModel{
				Ref:           ref,
				ProviderName:  providerName,
				ModelName:     modelName,
				UpstreamModel: upstreamModel,
				OpenaiApiKey:  provider.OpenaiApiKey,
				OpenaiBaseUrl: provider.OpenaiBaseUrl,
			}, nil
		}
		return ResolvedModel{}, fmt.Errorf("model %q is not configured for provider %q", modelName, providerName)
	}
	return ResolvedModel{}, fmt.Errorf("provider %q is not configured", providerName)
}

func (c *envAndFileConf) validateModelCatalog() error {
	providers := make(map[string]struct{}, len(c.ProviderList))
	for _, provider := range c.ProviderList {
		if !validCatalogName(provider.ProviderName) {
			return fmt.Errorf("invalid PROVIDER_NAME %q", provider.ProviderName)
		}
		if provider.ProviderName == envProviderName &&
			(len(provider.ModelList) != 1 || provider.ModelList[0].ModelName != envModelName ||
				provider.ModelList[0].UpstreamModel == "") {
			return fmt.Errorf("PROVIDER_NAME %q is reserved for environment configuration", envProviderName)
		}
		if _, exists := providers[provider.ProviderName]; exists {
			return fmt.Errorf("duplicate PROVIDER_NAME %q", provider.ProviderName)
		}
		providers[provider.ProviderName] = struct{}{}
		if strings.TrimSpace(provider.OpenaiApiKey) == "" || strings.TrimSpace(provider.OpenaiBaseUrl) == "" {
			return fmt.Errorf("provider %q requires OPENAI_API_KEY and OPENAI_BASE_URL", provider.ProviderName)
		}
		if len(provider.ModelList) == 0 {
			return fmt.Errorf("provider %q requires at least one model", provider.ProviderName)
		}
		models := make(map[string]struct{}, len(provider.ModelList))
		for _, model := range provider.ModelList {
			if !validCatalogName(model.ModelName) {
				return fmt.Errorf("invalid MODEL_NAME %q for provider %q", model.ModelName, provider.ProviderName)
			}
			if _, exists := models[model.ModelName]; exists {
				return fmt.Errorf("duplicate MODEL_NAME %q for provider %q", model.ModelName, provider.ProviderName)
			}
			models[model.ModelName] = struct{}{}
		}
	}
	if len(c.ProviderList) == 0 {
		return errors.New("PROVIDER_LIST must contain at least one provider")
	}
	_, err := c.resolveModel(c.Model)
	return err
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
	return nil
}

// resolveAuxiliaryModel 从模型目录解析文件引用，再逐项应用非空环境变量。
// 完整的环境配置无需依赖文件引用；未配置压缩模型时继承主模型。
func (c *envAndFileConf) resolveAuxiliaryModel(key, ref, prefix string, fallback ResolvedModel) (ResolvedModel, error) {
	apiKey := strings.TrimSpace(os.Getenv(prefix + "API_KEY"))
	baseURL := strings.TrimSpace(os.Getenv(prefix + "BASE_URL"))
	model := strings.TrimSpace(os.Getenv(prefix + "MODEL_NAME"))
	resolved := fallback
	if ref != "" && (apiKey == "" || baseURL == "" || model == "") {
		var err error
		resolved, err = c.resolveModel(ref)
		if err != nil {
			return ResolvedModel{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	if apiKey != "" {
		resolved.OpenaiApiKey = apiKey
	}
	if baseURL != "" {
		resolved.OpenaiBaseUrl = baseURL
	}
	if model != "" {
		resolved.UpstreamModel = model
	}
	return resolved, nil
}

// ResolveModel 将 provider:model 引用解析为创建 provider/client 所需的运行时配置。
func ResolveModel(ref string) (ResolvedModel, error) { return EnvAndFileConf.resolveModel(ref) }

// ModelRefs 返回按 provider、model 名排序的全部合法引用，供 CLI 补全使用。
func ModelRefs() []string {
	refs := make([]string, 0)
	for _, provider := range EnvAndFileConf.ProviderList {
		for _, model := range provider.ModelList {
			refs = append(refs, modelRef(provider.ProviderName, model.ModelName))
		}
	}
	sort.Strings(refs)
	return refs
}

// SetActiveModel 更新当前进程使用的模型引用及其派生连接参数，不写回配置文件。
func SetActiveModel(ref string) error { return EnvAndFileConf.setActiveModel(ref) }

type cliConf struct {
	KB               string `mapstructure:"kb"`
	VectorDimensions int    `mapstructure:"vector-dim"`
	Oneshot          bool   `mapstructure:"oneshot"`
	Evaluate         bool   `mapstructure:"evaluate"`
	SSE              bool   `mapstructure:"sse"`
	QA               bool   `mapstructure:"qa"`
	Addr             string `mapstructure:"addr"`
	WorkDir          string `mapstructure:"workdir"`
	Task             string `mapstructure:"task"`
	TaskFile         string `mapstructure:"task-file"`
	Session          string `mapstructure:"session"`
	EvalSession      string `mapstructure:"eval_session"`
	Plan             bool   `mapstructure:"plan"`
}

// DefaultSSEAddr 是 sse server 模式的缺省监听地址：仅绑定本地回环，因为
// Agent 具备 bash / 写文件能力，默认不对外暴露；需要对外时以 -addr 覆盖。
const DefaultSSEAddr = "127.0.0.1:8080"

var CliConf cliConf

var Cli = viper.New()

func ParseEnvAndFile() error {
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

	EnvOrFile.SetDefault("OPENAI_CONTEXT_WINDOW", DefaultContextWindow)
	EnvOrFile.SetDefault("OPENAI_MAX_OUTPUT_TOKENS", DefaultMaxOutputTokens)
	EnvOrFile.SetDefault("LLM_ROUTER_ADDR", DefaultLLMRouterAddr)
	EnvOrFile.SetDefault("USER_MEMORY_CONCURRENCY", 1)
	EnvOrFile.SetDefault("USER_MEMORY_TIMEOUT_SECONDS", 120)
	for _, key := range []string{"USER_MEMORY_EXECUTABLE", "USER_MEMORY_CONCURRENCY", "USER_MEMORY_TIMEOUT_SECONDS"} {
		_ = EnvOrFile.BindEnv(key, key)
	}
	EnvOrFile.BindEnv("OPENAI_CONTEXT_WINDOW", "OPENAI_CONTEXT_WINDOW")
	EnvOrFile.BindEnv("OPENAI_MAX_OUTPUT_TOKENS", "OPENAI_MAX_OUTPUT_TOKENS")
	EnvOrFile.BindEnv("COMPACTION_OPENAI_CONTEXT_WINDOW", "COMPACTION_OPENAI_CONTEXT_WINDOW")
	EnvOrFile.BindEnv("COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS")
	EnvOrFile.BindEnv("LLM_ROUTER_ADDR", "LLM_ROUTER_ADDR")
	EnvOrFile.SetEnvKeyReplacer(strings.NewReplacer("_", "_"))

	if err = EnvOrFile.Unmarshal(&EnvAndFileConf); err != nil {
		return err
	}

	// 完整的 OPENAI_* 三元组作为一个保留别名的临时 provider 追加到目录，并
	// 覆盖当前选择。部分设置不与文件配置拼接，避免凭据、端点和模型错配。
	envAPIKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	envBaseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	envUpstreamModel := strings.TrimSpace(os.Getenv("OPENAI_MODEL_NAME"))
	envValues := 0
	for _, value := range []string{envAPIKey, envBaseURL, envUpstreamModel} {
		if value != "" {
			envValues++
		}
	}
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
			OpenaiApiKey:  envAPIKey,
			OpenaiBaseUrl: envBaseURL,
			ModelList: []ModelConfig{{
				ModelName:     envModelName,
				UpstreamModel: envUpstreamModel,
			}},
		})
		EnvAndFileConf.Model = modelRef(envProviderName, envModelName)
	}
	if err := EnvAndFileConf.validateModelCatalog(); err != nil {
		return err
	}
	if err := EnvAndFileConf.setActiveModel(EnvAndFileConf.Model); err != nil {
		return err
	}
	embedding, err := EnvAndFileConf.resolveAuxiliaryModel("EMBEDDING_MODEL", EnvAndFileConf.EmbeddingModel, "OPENAI_EMBEDDING_", ResolvedModel{})
	if err != nil {
		return err
	}
	EnvAndFileConf.EmbedOpenaiApiKey = embedding.OpenaiApiKey
	EnvAndFileConf.EmbedOpenaiBaseUrl = embedding.OpenaiBaseUrl
	EnvAndFileConf.EmbedOpenaiModel = embedding.UpstreamModel
	mainModel, _ := EnvAndFileConf.resolveModel(EnvAndFileConf.Model)
	compaction, err := EnvAndFileConf.resolveAuxiliaryModel("COMPACTION_MODEL", EnvAndFileConf.CompactionModel, "OPENAI_COMPACTION_", mainModel)
	if err != nil {
		return err
	}
	EnvAndFileConf.CompactionOpenaiApiKey = compaction.OpenaiApiKey
	EnvAndFileConf.CompactionOpenaiBaseUrl = compaction.OpenaiBaseUrl
	EnvAndFileConf.CompactionOpenaiModel = compaction.UpstreamModel
	if EnvAndFileConf.CompactionOpenaiContextWindow == 0 {
		EnvAndFileConf.CompactionOpenaiContextWindow = EnvAndFileConf.OpenaiContextWindow
	}
	if EnvAndFileConf.CompactionOpenaiMaxOutputTokens == 0 {
		EnvAndFileConf.CompactionOpenaiMaxOutputTokens = EnvAndFileConf.OpenaiMaxOutputTokens
	}
	if EnvAndFileConf.OpenaiContextWindow <= 0 {
		return errors.New("openai_context_window must be positive")
	}
	if EnvAndFileConf.OpenaiMaxOutputTokens <= 0 ||
		EnvAndFileConf.OpenaiMaxOutputTokens >= EnvAndFileConf.OpenaiContextWindow {
		return errors.New("openai_max_output_tokens must be positive and smaller than openai_context_window")
	}
	if EnvAndFileConf.CompactionOpenaiContextWindow <= 0 {
		return errors.New("compaction_openai_context_window must be positive")
	}
	if EnvAndFileConf.CompactionOpenaiMaxOutputTokens <= 0 ||
		EnvAndFileConf.CompactionOpenaiMaxOutputTokens >= EnvAndFileConf.CompactionOpenaiContextWindow {
		return errors.New("compaction_openai_max_output_tokens must be positive and smaller than compaction_openai_context_window")
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
	oneshot := flag.Bool("oneshot", false, "one-shot mode: run a single task and print structured JSON to stdout")
	evaluate := flag.Bool("evaluate", false, "evaluate an existing agent session and print a structured report to stdout")
	sse := flag.Bool("sse", false, "sse server mode: serve HTTP POST /chat and stream ReAct events over SSE")
	qa := flag.Bool("qa", false, "knowledge-base question answering mode; combine with -sse to serve QA over SSE")
	addr := flag.String("addr", DefaultSSEAddr, "sse server listen address")
	kb := flag.String("kb", "", "absolute sqlite-vec database file path; required for -qa and for -sse when OPENAI_EMBEDDING_* is configured")
	vectorDimensions := flag.Int("vector-dim", 0, "user-memory vector dimensions; required for -sse when OPENAI_EMBEDDING_* is configured")
	workDir := flag.String("workdir", "", "working directory; required in one-shot and evaluate modes, defaults to cwd otherwise")
	task := flag.String("task", "", "one-shot task prompt text")
	taskFile := flag.String("task-file", "", "one-shot task prompt file path; takes precedence over -task")
	session := flag.String("session", "", "session id to resume; empty starts a new session")
	evalSession := flag.String("eval_session", "", "session id to evaluate; required in evaluate mode")
	plan := flag.Bool("plan", false, "enable plan mode")
	flag.Parse()

	Cli.Set("oneshot", *oneshot)
	Cli.Set("evaluate", *evaluate)
	Cli.Set("sse", *sse)
	Cli.Set("qa", *qa)
	Cli.Set("addr", *addr)
	Cli.Set("workdir", *workDir)
	Cli.Set("kb", *kb)
	Cli.Set("vector-dim", *vectorDimensions)
	Cli.Set("task", *task)
	Cli.Set("task-file", *taskFile)
	Cli.Set("session", *session)
	Cli.Set("eval_session", *evalSession)
	Cli.Set("plan", *plan)

	if err := Cli.Unmarshal(&CliConf); err != nil {
		return err
	}
	if CliConf.QA || (CliConf.SSE && EmbeddingEnvironmentReady()) {
		if err := ValidateKBPath(CliConf.KB); err != nil {
			return err
		}
	}
	// -sse -qa serves the regular QA service over HTTP. It uses the dimensions
	// recorded in the knowledge database and therefore must not inherit the
	// user-memory-only -vector-dim requirement from plain SSE mode.
	if CliConf.SSE && !CliConf.QA && EmbeddingEnvironmentReady() {
		return ValidateVectorDimensions(CliConf.VectorDimensions)
	}
	return nil
}

// EmbeddingEnvironmentReady reports whether SSE user memory is explicitly
// enabled through the complete embedding environment-variable triplet.
func EmbeddingEnvironmentReady() bool {
	for _, key := range []string{"OPENAI_EMBEDDING_MODEL_NAME", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_API_KEY"} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			return false
		}
	}
	return true
}

func ValidateVectorDimensions(dimensions int) error {
	if dimensions <= 0 || dimensions > 8192 {
		return errors.New("-vector-dim must be between 1 and 8192 when SSE embedding is enabled")
	}
	return nil
}

// ValidateKBPath checks configuration only. Disabled SSE memory does not require
// the database to exist; enabled consumers validate the actual file and schema.
func ValidateKBPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("-kb is required for -qa and SSE embedding; specify an absolute sqlite-vec database file path")
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return errors.New("-kb must be an absolute sqlite-vec database file path")
	}
	return nil
}
