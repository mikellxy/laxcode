# 宿主机部署

单实例部署：宿主机运行 Docker、OpenShell gateway 和 laxcode-proxy。proxy 内嵌前端，每个用户只有一个 OpenShell 沙箱，沙箱运行 Linux laxcode-cloud。应用发布不重启 gateway，不使用 Docker-in-Docker。

## 一次性准备机器

- macOS：Docker Desktop 4.92.0（已验证），开启 **Settings → Resources → Network → Enable host networking**，关闭与 host networking 不兼容的 Enhanced Container Isolation。
- Linux ECS：预装 Docker Engine、Python 3、OpenShell CLI 和 openshell-gateway **0.1.4**；本版使用 Docker driver，系统须满足该版本 OpenShell 的内核要求。
- OpenShell CLI、gateway、supervisor 和 sandbox runtime 版本一致。Docker 和这些可执行程序须在部署用户的 PATH 中。
- Docker 必须是本机 Unix socket；用户具有 Docker 访问权限。ECS 使用 root；macOS 使用当前用户。Linux 首次初始化用户目录时，proxy 将目录所有权设置为沙箱 UID/GID 1000。

在有网络的对应架构机器上，预先准备工具链和运行时镜像：

```sh
./deploy/build.sh --prepare-images
# 构建固定 laxcode-tools:go1.26.6-v1，拉取两个 OpenShell 0.1.4 镜像
```

将以下镜像预装进 ECS 系统镜像，或一次性 docker save/load 搬运：

- `laxcode-tools:go1.26.6-v1`
- `ghcr.io/nvidia/openshell/supervisor:0.1.4`
- `ghcr.io/nvidia/openshell/sandbox:0.1.4`

工具链包含 Go 1.26.6、Node 22、Python、Bash、rg、jq、中文字体和 excalidraw-canvas。制作机器镜像后固定这些镜像；改变工具链时升级基础镜像标签。应用部署只 COPY，不下载依赖。amd64 ECS 的预装镜像必须是 linux/amd64，不能直接复用 Mac arm64 镜像。

## 构建发布包

```sh
cp deploy/defaults/settings.example.json deploy/defaults/settings.json
# 编辑真实模型配置和 API Key；已有真实配置时不要覆盖
./deploy/build.sh
```

构建需要 Go 1.26.6、Node/pnpm、Python 3，以及构建依赖下载能力或已准备的缓存。`settings.json` 和 `laxcode.tar.gz` 均被 Git 忽略，压缩包权限为 0600。

本机运行默认生成本机 OS/CPU 的 proxy；cloud 始终是相同 CPU 的 Linux 二进制。Linux amd64 CI 默认产出 Linux amd64 包，也可以显式使用 `TARGETOS=linux TARGETARCH=amd64 ./deploy/build.sh`。部署时会检查目标 OS、CPU、依赖版本和文件 SHA-256。

```text
laxcode.tar.gz
├── bin/laxcode-proxy       # 内嵌前端，宿主机程序
├── bin/laxcode-cloud       # Linux 沙箱程序
├── settings.json           # 真实密钥，仅供宿主机注册 provider
├── skills/                 # 默认内置 Skills
├── Dockerfile              # FROM 固定工具镜像 + COPY
├── configure.py
└── manifest.json
```

## 安装与更新

```sh
mkdir -p ~/laxcode_deploy
cp deploy/deploy.sh deploy/laxcode.tar.gz ~/laxcode_deploy/
cd ~/laxcode_deploy
./deploy.sh restart
./deploy.sh status
# 停止 proxy 和应用沙箱，保留网关及用户数据
./deploy.sh stop
```

Linux 使用同样的流程，通过 scp 或 CI/CD 将文件放到目标目录。每次更新同时分发最新 deploy.sh 和 laxcode.tar.gz。

`restart` 获取部署锁，解压到临时目录，校验文件，生成不含真实密钥的独立 Docker build context，使用预装镜像构建，然后确保专用网关可用。之后停止 proxy，只删除带 `laxcode-app=laxcode` 标签的应用沙箱，替换发布目录，重新注册/更新 provider，启动 proxy 并检查健康状态。已有发布保留一份 `.previous`，正常异常时尝试恢复旧发布；用户工作目录始终不参与替换。突然断电或强制终止部署时可重新执行 restart。

沙箱删除会终止当时正在执行的会话任务，请在适当时段更新。空闲 30 分钟的沙箱平时只 stop，下次请求 start 原沙箱；应用更新才删除并重建。

- 应用入口：`http://localhost:5173`，proxy 监听 `0.0.0.0:5173`。
- 专用 gateway：`127.0.0.1:18080`，健康检查 `127.0.0.1:18081/health`；仅回环监听。
- 现有系统默认 OpenShell 网关不受影响；LaxCode 使用独立 XDG 配置、数据库和 Docker `sandbox_label=laxcode`。
- 运行状态、PID、日志、网关数据库/密钥：`~/laxcode_deploy/state/`。
- 当前发布：`~/laxcode_deploy/laxcode/`；不在这里保存用户文件。
- proxy 模型目录：当前发布中的 `catalog/settings.json`，仅含环境变量名，不含真实 API Key；临时镜像构建目录 `image/` 在构建后删除。
- 日志：`state/proxy.log`、`state/gateway.log`。

内测版以 `X-USERID` UUID 标识用户，静态页面和 `/healthz` 无需身份；尚无登录验证，公网访问控制由部署方配置。部署脚本管理后台进程，本版不安装应用开机自启服务。

## 用户目录与密钥

```text
~/laxcode/{user_id}/                → /workplace
├── .initialized
├── agenthome/                     → HOME
│   └── .laxcode/
│       ├── settings.json          # proxy 同步的平台配置，沙箱中单文件只读挂载
│       ├── skills/                # proxy 首次从发布包复制，以后保留修改/删除
│       ├── model-selection.json   # 用户模型选择
│       └── ...                    # 会话数据库等
├── {project_name}/                # 会话 workdir
└── tmp/
```

proxy 首次打开一个用户的数据接口时，将发布包 `catalog/settings.json` 中的平台配置原子写入用户的 `agenthome/.laxcode/settings.json`；每次应用发布后，该用户首次访问时同步最新配置，再启动沙箱。沙箱中以发布目录 `catalog/settings.json` 为可信源，单独只读挂载到 `/workplace/agenthome/.laxcode/settings.json`，cloud 从 HOME 默认路径加载。挂载源位于用户工作目录之外，避免用户修改目录结构后篡改下次启动的配置。不要在保留运行中沙箱的情况下单独修改平台配置；正式更新使用 `deploy.sh restart`，先删除旧沙箱再启用新发布。

配置中的 API Key 是合法环境变量名。真实密钥经 configure.py 注册给专用 OpenShell gateway，启动沙箱时由 OpenShell 注入占位凭据，cloud 使用 `os.Getenv` 获取占位凭据。proxy 从发布包 `skills/` 首次原子初始化用户 Skills 目录，以后保留用户修改和删除。用户模型选择独立保存在 `model-selection.json`，同步平台配置时不覆盖。

沙箱镜像仅包含 cloud 可执行程序和固定基础工具链，不包含平台配置或默认 Skills，也不执行配置或 Skills 的复制操作。

含真实密钥的源配置、发布包、当前/上一发布目录、gateway 状态仅供部署用户访问。构建镜像的 context 不包含真实 settings.json。


## 数据接口与沙箱执行

只有 `POST /chat`、`POST /api/sessions/{session_id}/resume` 和 `POST /api/evaluations` 会启动或唤醒沙箱。项目、会话、历史、上下文用量、性能统计、模型选择、Skills、文件预览/下载、评估列表和报告由 proxy 直接处理，查询与轮询不会刷新沙箱空闲计时。

共享 `workspace` 模块复用现有模型目录、SQLite 仓储及路径规则。SQLite 仍位于用户的 `agenthome/.laxcode/sessions/sessions.db`，但仅 proxy 打开数据库；cloud 通过内部存储接口访问同一仓储。这样避免 macOS 与 Docker Desktop Linux 跨内核共享 SQLite WAL/文件锁。JSONL 备份、trace、报告和项目产物继续保存在原目录。

部署生成的 `store-url` 使用宿主私网地址和 5173 端口；UDP 路由探测不发送数据，不要求目标能访问互联网。OpenShell 策略仅允许 `/usr/local/bin/laxcode-cloud` 向该地址的 `/internal/store` 发送 POST，并固定目标 IP `/32`。proxy 需监听该私网地址（默认 `0.0.0.0:5173`）；公网入口仍使用 Caddy，安全组不要对公网开放 5173。宿主私网地址变化后需重新执行部署以更新策略。

内部存储按用户校验独立凭据，由 `~/laxcode/.proxy-store-key` 派生并在沙箱启动时注入；该文件不挂载进沙箱，proxy 重启时必须保留。普通 `X-USERID` 请求不能调用内部存储接口。

模型切换立即保存 `agenthome/.laxcode/model-selection.json`，已运行任务继续使用其模型快照，之后的新任务读取最新选择。删除会话时，proxy 在用户生命周期锁下检查正在执行的请求和后台评估；繁忙或状态未知时返回 409，不唤醒沙箱。

跨 Docker Desktop 的存储回归可执行：

```bash
LAXCODE_DOCKER_SQLITE_TEST=1 go test ./workspace -run TestStorageAcrossDockerMount -v
```

该测试使用临时目录和一次性容器，需要预装 `laxcode-tools:go1.26.6-v1`。
