# 云端部署

## 构建和启动

### 构建本机 proxy 与 Linux cloud 发布包，预装固定工具链和 OpenShell 运行镜像

- `deploy/build.sh`
- `deploy/Dockerfile`
- `deploy/Dockerfile.tools`

### 校验离线发布、替换应用、注册 provider，保持宿主机专用网关运行

- `deploy/deploy.sh`：`restart(base, state, env)`、`ensure_gateway(state, env)`、`start_proxy(base, state, env)`
- `laxcode-proxy/main.go`：`func run() error`

### 注册管理员模型凭据并生成只含环境变量引用的沙箱配置

- `deploy/configure.py`：`prepare(source)`、`generate(release)`、`register(release)`
- `internal/infrastructure/config/config.go`：`func LoadSettingsFromDir(configDir string, args []string) (Conf, error)`
- `internal/infrastructure/config/configs.go`：`func LoadProviderAPIKeys(providers []Provider) error`

## 身份与沙箱生命周期

### 浏览器统一携带访客 UUID 请求头，proxy 校验 X-USERID 后转发并实时刷新 SSE

- `web/src/api/client.ts`：`request(input: RequestInfo | URL, init?: RequestInit)`
- `web/src/features/identity/guest.ts`：`getOrCreateGuestID()`
- `laxcode-proxy/main.go`：`func (p *proxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request)`

### proxy 按用户加锁初始化目录，原子同步平台配置并仅首次初始化默认 Skills

- `laxcode-proxy/workplace.go`：`func initializeWorkplace(base, id string) (string, error)`
- `laxcode-proxy/main.go`：`func (p *proxyServer) prepareWorkspace(id string, u *userState) error`
- `workspace/api.go`：`func Open(root, id string, catalog Catalog, skills string) (*API, error)`
- `workspace/default_skills.go`：`func initializeDefaultSkills(source, dest string) error`
- `internal/infrastructure/config/settings.go`：`func WriteCatalogAtomic(path string, conf Conf) error`

### 沙箱从 HOME 加载平台配置，发布目录配置以单文件只读挂载覆盖用户路径

- `laxcode-proxy/openshell.go`：`func (o *openshellRuntime) Create(ctx context.Context, id, root string) error`
- `internal/infrastructure/config/config.go`：`func LoadSettings(homeDir string, args []string) (Conf, error)`

### 固定名称查询和创建沙箱，创建返回不确定时先查询结果，已停止沙箱使用 start

- `laxcode-proxy/main.go`：`func (p *proxyServer) ensure(ctx context.Context, id string, u *userState) (*url.URL, error)`
- `laxcode-proxy/openshell.go`：`func (o *openshellRuntime) Create(ctx context.Context, id, root string) error`
- `laxcode-proxy/openshell.go`：`func (o *openshellRuntime) Lookup(ctx context.Context, id string) (*sandboxState, error)`

### 通过 OpenShell Service URL 的 Host 路由到沙箱内 8080，空闲且无后台评估时停止沙箱

- `laxcode-proxy/openshell.go`：`func (o *openshellRuntime) Endpoint(ctx context.Context, name string) (*url.URL, error)`
- `laxcode-proxy/main.go`：`func (p *proxyServer) stopIdle(ctx context.Context)`
- `cmd/run_sse/cloud.go`：`func (c *cloudService) ServeHTTP(w http.ResponseWriter, r *http.Request)`

## 沙箱内应用

### cloud 持有单用户模型管理器和远程仓储，SQLite 仅由 proxy 打开

- `cmd/run_sse/cloud.go`：`func newCloudServiceWithRepository(home, id string, conf config.Conf, tracer telemetry.Tracer, repo sessionrepo.Repository) (*cloudService, error)`
- `cmd/run_sse/run_sse.go`：`func Run(codeInstance *CodeInstanceGuard)`
- `internal/infrastructure/sessionrepo/remote.go`：`func NewRemote(url, userID, token string) *Remote`
- `workspace/api.go`：`func (s *API) StoreHandler() http.Handler`

### 项目仅按名称创建于 workplace，项目和会话身份固定为沙箱用户

- `workspace/api.go`：`func ValidateProjectName(name string) error`
- `workspace/metadata.go`：`func (s *API) HandleCreateProject(w http.ResponseWriter, r *http.Request)`
- `workspace/metadata.go`：`func (s *API) HandleCreateSession(w http.ResponseWriter, r *http.Request)`

### 用户从平台目录选择模型，独立保存选择并在启动时恢复

- `internal/infrastructure/ai_models/manager.go`：`func (m *Manager) SetActiveModel(ref string, effort ...string) error`
- `internal/infrastructure/ai_models/manager.go`：`func (m *Manager) LoadSelection() error`
- `web/src/components/model-picker/ModelPicker.tsx`

### Bash 直接执行用户沙箱内 shell，取消、超时及调用结束时清理进程组

- `internal/infrastructure/shell/sandbox.go`：`func (r *SandboxRunner) Run(ctx context.Context, work, command string, timeout time.Duration) (out tools.ShellOutcome, err error)`
- `internal/infrastructure/shell/sandbox.go`：`func (r *SandboxRunner) Close() error`
- `cmd/agentasm/agentasm.go`：`func Assemble(ctx context.Context, in Input) (*Assembled, error)`

### 文件和 Skills 工具使用用户根句柄，阻止符号链接越界

- `internal/domain/tools/read_roots.go`：`func ValidateUserPath(userRoot, target string) error`
- `internal/infrastructure/workfs/workfs.go`：`func NewRoot(path string) (*FS, error)`
- `internal/infrastructure/skillstore/skillstore.go`：`func NewWithRoot(skillsRoot, userRoot string) *Store`
