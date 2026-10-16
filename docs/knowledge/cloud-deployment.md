# 云端部署原型验证

## 启动与 Web 代理

### 构建后端和内嵌 Web UI，通过 LAXCODE_CLOUD_PORT 指定 Web 端口并等待健康检查

- `cloud.sh`

### 解析云端启动参数，后端默认运行用户分流服务

- `internal/infrastructure/config/config.go`：`func parseCLI(args []string) (cliConf, error)`
- `cmd/main/main.go`：`func main()`

### SSE 服务持有用户分流处理器和可选共享追踪器

- `cmd/run_sse/run_sse.go`：`func run(codeInstance *CodeInstanceGuard) error`

### Web 服务默认监听所有网卡，向本机后端代理 API、chat 和健康检查请求

- `cmd/web/main.go`：`func run() error`
- `cmd/web/main.go`：`func newHandler(assets fs.FS, backend *url.URL) http.Handler`

## 用户身份与请求分流

### 浏览器创建并复用 laxcode_guest_id UUID cookie 作为访客身份

- `web/src/features/identity/guest.ts`：`export function getOrCreateGuestID(): string`

### 合并请求头、查询参数、cookie 和项目或会话创建请求体中的 UUID，拒绝缺失或冲突的用户身份

- `cmd/run_sse/cloud.go`：`func cloudUserID(w http.ResponseWriter, r *http.Request) (string, error)`

### 无需身份即可读取 cloud 标记和健康状态，云端请求拒绝目录选择、OAuth 和人工审批接口

- `cmd/run_sse/cloud.go`：`func (c *cloudUsers) ServeHTTP(w http.ResponseWriter, r *http.Request)`
- `cmd/run_sse/run_sse.go`：`func (s *server) routes() *http.ServeMux`

### 尚未创建项目的用户查询项目和模型时返回空列表，其他工作区请求返回 404

- `cmd/run_sse/cloud.go`：`func (c *cloudUsers) ServeHTTP(w http.ResponseWriter, r *http.Request)`
- `cmd/run_sse/cloud.go`：`func (c *cloudUsers) user(id string, create bool) (*cloudUser, error)`

## 用户初始化与项目创建

### 首次创建项目时在服务账户主目录下初始化 laxcode/{user_id}/agenthome 和用户共用 tmp

- `cmd/run_sse/cloud.go`：`func (c *cloudUsers) user(id string, create bool) (*cloudUser, error)`
- `cmd/run_sse/cloud.go`：`func initializeCloudHome(serviceHome, id, copyDir string) error`

### 首次初始化从 -conf_copy_dir 指定目录复制 API Key 配置和技能模板，未指定时使用服务账户的 .laxcode，通过临时目录及标记发布 agenthome

- `cmd/run_sse/cloud.go`：`func initializeCloudHome(serviceHome, id, copyDir string) error`
- `cmd/run_sse/cloud.go`：`func copyCloudSkills(source, destination string) error`
- `internal/infrastructure/config/config.go`：`func LoadSettingsFromDir(configDir string, args []string) (Conf, error)`

### 按用户加载 settings 而不修改服务进程环境，缓存独立模型管理器、SQLite 仓储和 HTTP 服务

- `internal/infrastructure/config/config.go`：`func LoadSettings(homeDir string, args []string) (Conf, error)`
- `cmd/run_sse/cloud.go`：`func (c *cloudUsers) user(id string, create bool) (*cloudUser, error)`

### 项目名称限制为 1–120 个英文字母、数字、中横线或下划线，并拒绝 agenthome 和 tmp 保留名称

- `cmd/run_sse/cloud.go`：`func validateCloudProjectName(name string) error`

### 串行创建用户根下的项目目录，重名返回 409，项目数据库写入失败时移除新建目录

- `cmd/run_sse/handler.go`：`func (s *server) handleCreateProject(w http.ResponseWriter, r *http.Request)`

### 创建会话时校验项目归属，并从项目记录继承工作目录

- `cmd/run_sse/handler.go`：`func (s *server) handleCreateSession(w http.ResponseWriter, r *http.Request)`

## 用户模型与 Agent 装配

### 向用户 settings 添加 API Key 模型，缺少当前模型时自动激活新增模型

- `cmd/run_sse/handler.go`：`func (s *server) handleAddModel(w http.ResponseWriter, r *http.Request)`

### 模型切换只更新当前用户的模型选择，装配客户端时直连上游

- `cmd/agentasm/model_switcher.go`：`func NewModelSwitcher(models *ai_models.Manager) *ModelSwitcher`
- `cmd/agentasm/model_switcher.go`：`func (s *ModelSwitcher) SwitchModelLocked(ref string, effort ...string) error`
- `cmd/agentasm/agentasm.go`：`func newMainProvider(models *ai_models.Manager) *llmprovider.OpenApiProvider`

### Chat 和 Resume 装配注入用户根、临时目录及用户仓储

- `cmd/run_sse/handler.go`：`func (s *server) handleChat(w http.ResponseWriter, r *http.Request)`
- `cmd/run_sse/handler.go`：`func (s *server) handleResume(w http.ResponseWriter, r *http.Request)`

### 评估 Agent 装配沿用当前用户的模型、仓储、用户根和临时目录

- `cmd/run_sse/evaluation.go`：`func (s *server) runEvaluation(ctx context.Context, job evaluation.Job) error`

### 云端读取会话上下文前校验会话存在于当前用户仓储

- `cmd/run_sse/handler.go`：`func (s *server) handleSessionContext(w http.ResponseWriter, r *http.Request)`

### 装配前校验工作目录、HOME 和临时目录位于用户根内，注册受限工具并向提示词注入用户文件范围

- `cmd/agentasm/agentasm.go`：`func Assemble(ctx context.Context, in Input) (*Assembled, error)`
- `internal/domain/tools/read_roots.go`：`func ValidateUserPath(userRoot, target string) error`

### 复用 HTTP 服务注入的用户仓储，Agent Cleanup 只关闭自行创建的仓储

- `cmd/agentasm/core.go`：`func assembleCore(ctx context.Context, workDir, explicitHome, sessionID string, consumer func(*reactservice.ReactEvent), withArtifacts bool, tracer telemetry.Tracer, models *ai_models.Manager, suppliedRepo session.SessionRepository, userRoot string) (*coreAssembly, error)`

### 子 Agent 校验工作目录并继承用户根、技能、文件访问限制和临时目录，单独创建 Shell 执行器

- `internal/application/reactservice/subagent.go`：`func (s *SubAgent) Execute(ctx context.Context, args json.RawMessage) (string, error)`

## 文件访问与 Bash 执行限制

### 相对文件路径从项目 workdir 解析，允许访问整个用户根，写入前校验符号链接指向

- `internal/domain/tools/read_roots.go`：`func resolveUserTarget(workDir, userRoot, input string) (string, string, error)`
- `internal/domain/tools/read_roots.go`：`func resolveWriteTarget(workDir, userRoot, input string) (target, root string, err error)`
- `internal/domain/tools/read_roots.go`：`func ValidateUserPath(userRoot, target string) error`

### 云端文件读取、写入和编辑越界时直接返回工具错误，不通过人工审批授权放行

- `internal/domain/tools/read_file.go`：`func (r *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error)`
- `internal/domain/tools/write_file.go`：`func (w *WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error)`
- `internal/domain/tools/edit_file.go`：`func (e *EditFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error)`

### 文件系统适配器持有 os.Root 句柄，在实际读写时阻止符号链接越界

- `internal/infrastructure/workfs/workfs.go`：`func NewRoot(path string) (*FS, error)`
- `internal/infrastructure/workfs/workfs.go`：`func (f FS) OpenRead(absPath string) (io.ReadCloser, error)`
- `internal/infrastructure/workfs/workfs.go`：`func (f FS) ReadFile(absPath string) ([]byte, error)`
- `internal/infrastructure/workfs/workfs.go`：`func (f FS) WriteFile(absPath string, content []byte) error`

### 云端 grep 和 glob 遍历跳过符号链接、非普通文件及 .git，当前不应用 .gitignore

- `internal/infrastructure/ripgrep/root.go`：`func (r *RootRunner) files(ctx context.Context, cwd, glob string, visit func(string, string) error) error`

### grep 通过 os.Root 打开文件并经标准输入交给 rg，glob 在返回文件路径前再次校验

- `internal/infrastructure/ripgrep/root.go`：`func (r *RootRunner) Search(ctx context.Context, cwd, file, expression, include string, limit int) (tools.GrepSearchResult, error)`
- `internal/infrastructure/ripgrep/root.go`：`func (r *RootRunner) Glob(ctx context.Context, cwd, glob string, limit int) (tools.GlobSearchResult, error)`

### Bash 调度只接受具备沙箱能力的执行器，每次调用使用离线 Docker 容器

- `internal/application/reactservice/reactservice.go`：`func (r *ReActService) executeToolCall(ctx context.Context, call *sharedkernel.ToolCall) (*sharedkernel.ToolResult, error)`
- `internal/infrastructure/shell/docker.go`：`func (r *DockerRunner) Run(ctx context.Context, workDir, command string, timeout time.Duration) (outcome tools.ShellOutcome, runErr error)`

## 用户技能与工具输出归档

### 在用户根限制下发现 agenthome 中的技能并创建和更新技能包

- `internal/infrastructure/skillrepo/skillrepo.go`：`func (s Source) listRooted(skillsRoot string) []prompt.SkillFile`
- `internal/application/reactservice/reactservice.go`：`func (r *ReActService) executeToolCall(ctx context.Context, call *sharedkernel.ToolCall) (*sharedkernel.ToolResult, error)`

### 技能存储通过用户根打开技能目录，防止技能目录重定向到用户范围之外

- `internal/infrastructure/skillstore/skillstore.go`：`func NewWithRoot(skillsRoot, userRoot string) *Store`
- `internal/infrastructure/skillstore/skillstore.go`：`func (s *Store) openRoot() (*os.Root, error)`

### 工具输出归档在用户根内通过临时文件原子写入，读取时限制到当前会话的 artifacts 目录

- `internal/infrastructure/artifactstore/fs.go`：`func (s *FileStore) putRooted(sessionID, content string, ref sharedkernel.ArtifactRef) (sharedkernel.ArtifactRef, error)`
- `internal/infrastructure/artifactstore/fs.go`：`func (s *FileStore) artifactRoot(sessionID string) (*os.Root, error)`

## Web 云端交互与退出清理

### Web 以项目名称创建工作区并刷新模型列表

- `web/src/app/App.tsx`：`export function App()`
- `web/src/hooks/use-workspace.ts`：`export function useWorkspace(userID: string)`
- `web/src/api/projects.ts`：`export const createProject = (userID: string, name: string) =>`
- `web/src/components/project-onboarding/ProjectOnboarding.tsx`：`export function ProjectOnboarding({ creating, onCreate }: Props)`

### 云端添加模型界面只展示 API Key 和 Base URL 接入方式

- `web/src/components/model-picker/AddModelDialog.tsx`：`export function AddModelDialog({ saving, error, onCancel, onSubmit }: Props)`

### 服务退出时停止每用户评估并关闭仓储，启动脚本退出时回收后端和 Web 子进程

- `cmd/run_sse/cloud.go`：`func (c *cloudUsers) Close()`
- `cloud.sh`：`cleanup()`
