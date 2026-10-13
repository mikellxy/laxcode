# 上下文压缩

## 配置来源

### 启动时合并配置文件和命令行参数，保存全局配置

- `internal/infrastructure/config/config.go`：`func Parse() error`
- `internal/infrastructure/config/config.go`：`func Load(homeDir string, args []string) (Conf, error)`

### 加载进程环境变量文件，为模型连接和子进程提供代理等环境配置

- `internal/infrastructure/config/envconf.go`：`func ApplyEnvFile(homeDir string) error`

### 从用户目录的配置文件解析模型目录、主模型和压缩模型选择

- `internal/infrastructure/config/config.go`：`func parseFile(homeDir string) (fileConf, error)`

### 将命令行模型参数合并为临时 provider，并覆盖主模型选择

- `internal/infrastructure/config/config.go`：`func parseCLI(args []string) (cliConf, error)`
- `internal/infrastructure/config/config.go`：`func merge(file fileConf, cli cliConf) (Conf, error)`

### 为缺省模型预算补默认值，校验模型目录和窗口限制

- `internal/infrastructure/config/config.go`：`func DefaultModelLimit() ModelLimit`
- `internal/infrastructure/config/config.go`：`func merge(file fileConf, cli cliConf) (Conf, error)`
- `internal/infrastructure/ai_models/catalog.go`：`func (c *modelState) validateModelCatalog() error`

### 解析主模型和摘要模型，未指定摘要模型时继承主模型配置

- `internal/infrastructure/ai_models/manager.go`：`func (c *modelState) initializeSelection() error`
- `internal/infrastructure/ai_models/catalog.go`：`func (c *modelState) resolveModel(ref string) (ResolvedModel, error)`
- `internal/infrastructure/ai_models/manager.go`：`func (c *modelState) setActiveModel(ref string, effort ...string) error`

### 模型切换时更新运行时选择，使后续装配采用新的模型和预算

- `cmd/agentasm/model_switcher.go`：`func (s *ModelSwitcher) SwitchModelLocked(ref string, effort ...string) error`
- `internal/infrastructure/ai_models/manager.go`：`func (m *Manager) SetActiveModel(ref string, effort ...string) error`

## 依赖装配

### 从启动配置创建共享模型管理器，并传入请求的服务装配入口

- `cmd/agentasm/model_switcher.go`：`func NewModels(homeDir string) (*ai_models.Manager, error)`
- `cmd/run_sse/tracing.go`：`func (s *server) assembleAgent(t *chatTrace, in agentasm.Input) (assembled *agentasm.Assembled, err error)`
- `cmd/agentasm/agentasm.go`：`func Assemble(ctx context.Context, in Input) (*Assembled, error)`

### 按用户主目录创建会话及 SQLite 仓储，确定上下文的持久化位置

- `cmd/agentasm/core.go`：`func assembleCore(ctx context.Context, workDir, explicitHome, sessionID string, consumer func(*reactservice.ReactEvent), withArtifacts bool, tracer telemetry.Tracer, models *ai_models.Manager) (*coreAssembly, error)`
- `cmd/agentasm/agentasm.go`：`func resolveHomeDir(explicit string) (string, error)`
- `internal/infrastructure/sessionrepo/sqlite.go`：`func NewSqliteSessionRepo(dbPath, historyRoot string) (*SqliteSessionRepo, error)`

### 根据运行模式创建归档存储，并注册读取归档原文的工具

- `cmd/agentasm/core.go`：`func assembleCore(ctx context.Context, workDir, explicitHome, sessionID string, consumer func(*reactservice.ReactEvent), withArtifacts bool, tracer telemetry.Tracer, models *ai_models.Manager) (*coreAssembly, error)`
- `cmd/agentasm/agentasm.go`：`func Assemble(ctx context.Context, in Input) (*Assembled, error)`
- `internal/domain/tools/read_artifact.go`：`func NewReadArtifactTool(store ArtifactStore, sessionID string) *ReadArtifactTool`

### 按当前主模型创建客户端，注入预算、认证和流式路由器连接

- `cmd/agentasm/agentasm.go`：`func newMainProvider(homeDir string, models *ai_models.Manager) *llmprovider.OpenApiProvider`
- `internal/infrastructure/llmprovider/openai_impl.go`：`func NewOpenApiProviderWithStreamGateway(apiKey, baseURL, model, streamGatewayURL string, budgetValues ...int) *OpenApiProvider`
- `internal/infrastructure/llmprovider/openai_impl.go`：`func (p *OpenApiProvider) WithChatGPT(homeDir, credentialRef string) *OpenApiProvider`

### 按压缩模型创建独立摘要客户端，使用该模型自己的预算和连接

- `cmd/agentasm/core.go`：`func assembleCore(ctx context.Context, workDir, explicitHome, sessionID string, consumer func(*reactservice.ReactEvent), withArtifacts bool, tracer telemetry.Tracer, models *ai_models.Manager) (*coreAssembly, error)`
- `internal/infrastructure/llmprovider/openai_impl.go`：`func NewOpenApiProvider(apiKey, baseURL, model string, budgetValues ...int) *OpenApiProvider`

### 将会话、仓储、模型客户端、工具注册表和归档存储注入 ReActService

- `internal/application/reactservice/reactservice.go`：`func NewReActService(sess *session.Session, sessRepo session.SessionRepository, llmClient llmprovider.LLMClient, contextSummaryLLMClient llmprovider.LLMClient, toolRegistry tools.Registry, reActEventConsumerF func(reactEvent *ReactEvent), tracer telemetry.Tracer, artifactStores ...tools.ArtifactStore) *ReActService`

### 恢复最新请求上下文，再创建或替换系统提示词

- `internal/application/reactservice/reactservice.go`：`func (r *ReActService) InitSession(ctx context.Context) error`
- `internal/infrastructure/sessionrepo/sqlite.go`：`func (r *SqliteSessionRepo) GetRequestContext(ctx context.Context, id string) (session.RequestContext, error)`
- `internal/application/reactservice/reactservice.go`：`func (r *ReActService) InitSysPrompt(ctx context.Context, p string) error`

## 压缩流程

### 每次模型生成前固定一份工具定义，供压缩计数和生成请求共同使用

- `internal/application/reactservice/reactservice.go`：`func (r *ReActService) think(ctx context.Context) (*sharedkernel.Message, error)`

### 读取主模型预算，校验窗口和输出预留并计算可用输入空间

- `internal/application/reactservice/context_compaction.go`：`func (r *ReActService) compactContext(ctx context.Context, toolDefs []sharedkernel.ToolDefinition) (retErr error)`
- `internal/infrastructure/llmprovider/openai_impl.go`：`func (p *OpenApiProvider) ContextBudget() domainllm.ContextBudget`
- `internal/domain/llmprovider/llm_client.go`：`func (b ContextBudget) MaxInputTokens() int`

### 按完整请求统计输入 token，远端计数失败或使用 OAuth 时采用本地估算

- `internal/infrastructure/llmprovider/openai_impl.go`：`func (p *OpenApiProvider) CountInputTokens(ctx context.Context, msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition) (int, error)`
- `internal/infrastructure/llmprovider/openai_impl.go`：`func (p *OpenApiProvider) buildResponseParams(msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition) responses.ResponseNewParams`
- `internal/infrastructure/llmprovider/local_token_count.go`：`func (p *OpenApiProvider) countInputTokensLocal(msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition) (int, error)`

### 输入达到固定触发阈值时开始压缩，否则只校正当前窗口统计

- `internal/application/reactservice/context_compaction.go`：`func (r *ReActService) compactContext(ctx context.Context, toolDefs []sharedkernel.ToolDefinition) (retErr error)`
- `internal/domain/session/session.go`：`func (s *Session) ReconcileWindowInput(inputTokens int)`

### 深复制候选上下文，使压缩失败时保留当前会话状态

- `internal/domain/session/request_context.go`：`func (s *Session) Clone() *Session`

### 保留最近三个工具调用组及其后续消息，并扩展边界以维持调用和结果配对

- `internal/domain/compactor/compactor.go`：`func ProtectedStart(msgs []sharedkernel.Message) int`
- `internal/domain/compactor/compactor.go`：`func collectToolCallGroups(msgs []sharedkernel.Message) []toolCallGroup`

### 筛选较早的大体积工具输出，先保存原文再附加归档引用

- `internal/domain/compactor/compactor.go`：`func ArtifactCandidates(msgs []sharedkernel.Message) []int`
- `internal/application/reactservice/context_compaction.go`：`func (r *ReActService) compactLocally(ctx context.Context, run *compactionRun, artifactCandidates []int) error`
- `internal/infrastructure/artifactstore/fs.go`：`func (s *FileStore) PutArtifact(ctx context.Context, sessionID, content string) (sharedkernel.ArtifactRef, error)`

### 在归档之后、本地裁剪之前复制旧历史，作为摘要模型的输入来源

- `internal/application/reactservice/context_compaction.go`：`func (r *ReActService) compactLocally(ctx context.Context, run *compactionRun, artifactCandidates []int) error`

### 将已归档的旧工具输出替换为简短结果和可按需读取的原文引用

- `internal/domain/compactor/compactor.go`：`func (simpleStrategy) Compress(msgs []sharedkernel.Message, minTokenSavings int) ([]sharedkernel.Message, int, error)`
- `internal/domain/compactor/compactor.go`：`func clearedToolOutput(msg sharedkernel.Message) string`

### 清除保留范围之前的 reasoning 文本，保留加密 reasoning 数据

- `internal/domain/compactor/compactor.go`：`func (simpleStrategy) Compress(msgs []sharedkernel.Message, minTokenSavings int) ([]sharedkernel.Message, int, error)`

### 截断较早的 assistant 正文中间部分，保留首尾内容

- `internal/domain/compactor/compactor.go`：`func (simpleStrategy) Compress(msgs []sharedkernel.Message, minTokenSavings int) ([]sharedkernel.Message, int, error)`
- `internal/domain/compactor/compactor.go`：`func truncateMiddleRunes(content string, limit int) (string, bool)`

### 每轮本地裁剪后重新计数，达到目标或无法继续降低占用时结束本地阶段

- `internal/domain/session/session.go`：`func (s *Session) Compact(strategy Compactor, minTokenSavings int) (int, error)`
- `internal/application/reactservice/context_compaction.go`：`func (r *ReActService) compactLocally(ctx context.Context, run *compactionRun, artifactCandidates []int) error`

### 本地裁剪未达目标时，扣除系统提示词和保留内容以计算摘要可用空间

- `internal/application/reactservice/context_compaction.go`：`func (r *ReActService) compactWithSummary(ctx context.Context, run *compactionRun) error`
- `internal/domain/compactor/summary.go`：`func MergeSummary(msgs []sharedkernel.Message, protectedStart int, content string) ([]sharedkernel.Message, error)`

### 构造无业务工具的摘要请求，并按摘要模型自己的预算校验输入大小

- `internal/application/reactservice/context_summary.go`：`func (r *ReActService) generateContextSummary(ctx context.Context, source []sharedkernel.Message, previous string, maxTokens int) (string, sharedkernel.TokenStatistics, error)`

### 调用摘要模型生成历史摘要，拒绝空响应和意外的工具调用

- `internal/application/reactservice/context_summary.go`：`func (r *ReActService) generateContextSummary(ctx context.Context, source []sharedkernel.Message, previous string, maxTokens int) (string, sharedkernel.TokenStatistics, error)`

### 校验结构化摘要并规范化 JSON，拒绝未知字段、尾随内容和空摘要

- `internal/application/reactservice/context_summary.go`：`func normalizeContextSummary(content string) (string, error)`

### 用一条摘要替换旧历史，保留系统提示词、近期消息及原始来源序号

- `internal/domain/compactor/summary.go`：`func MergeSummary(msgs []sharedkernel.Message, protectedStart int, content string) ([]sharedkernel.Message, error)`

### 合并后按主模型重新计数，仍超目标时最多再压缩一次已有摘要

- `internal/application/reactservice/context_compaction.go`：`func (r *ReActService) compactWithSummary(ctx context.Context, run *compactionRun) error`
- `internal/application/reactservice/context_summary.go`：`func (r *ReActService) generateContextSummary(ctx context.Context, source []sharedkernel.Message, previous string, maxTokens int) (string, sharedkernel.TokenStatistics, error)`

### 压缩达标后校正窗口统计，并推进一个上下文版本

- `internal/domain/session/session.go`：`func (s *Session) ReconcileWindowInput(inputTokens int)`
- `internal/domain/session/request_context.go`：`func (s *Session) AdvanceMemoryGeneration() error`

### 在数据库事务中提交新版本，成功后才采用候选上下文

- `internal/application/reactservice/reactservice.go`：`func (r *ReActService) commitNextMemoryGeneration(ctx context.Context, candidate *session.Session) error`
- `internal/infrastructure/sessionrepo/sqlite.go`：`func (r *SqliteSessionRepo) CommitNextMemoryGeneration(ctx context.Context, id string, snapshot session.RequestContext) (uint64, error)`

### 记录压缩结果，成功后继续主模型生成，失败则停止本轮生成

- `internal/application/reactservice/context_compaction.go`：`func (r *ReActService) compactContext(ctx context.Context, toolDefs []sharedkernel.ToolDefinition) (retErr error)`
- `internal/application/reactservice/reactservice.go`：`func (r *ReActService) think(ctx context.Context) (*sharedkernel.Message, error)`

### 按当前会话分页读取已归档原文，校验归档标识和内容摘要

- `internal/domain/tools/read_artifact.go`：`func (t *ReadArtifactTool) Execute(ctx context.Context, args json.RawMessage) (string, error)`
- `internal/infrastructure/artifactstore/fs.go`：`func (s *FileStore) ReadArtifact(ctx context.Context, sessionID, id string, offset, limit int) (tools.ArtifactPage, error)`
