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
- 日志：`state/proxy.log`、`state/gateway.log`。

内测版以 `X-USERID` UUID 标识用户，静态页面和 `/healthz` 无需身份；尚无登录验证，公网访问控制由部署方配置。部署脚本管理后台进程，本版不安装应用开机自启服务。

## 用户目录与密钥

```text
~/laxcode/{user_id}/                → /workplace
├── .initialized
├── agenthome/                     → HOME
│   └── .laxcode/
│       ├── skills/                # 首次从镜像复制，以后保留修改/删除
│       ├── model-selection.json   # 用户模型选择
│       └── ...                    # 会话数据库等
├── {project_name}/                # 会话 workdir
└── tmp/
```

平台配置直接从镜像只读的 `/etc/laxcode/settings.json` 加载，不再复制进用户 HOME；其中 API Key 是合法环境变量名。真实密钥经 configure.py 注册给专用 OpenShell gateway，启动沙箱时由 OpenShell 注入占位凭据，cloud 使用 `os.Getenv` 获取占位凭据。默认 Skills 位于镜像 `/opt/laxcode/skills/`，cloud 原子初始化用户 Skills 目录；已有用户目录（包括旧部署初始化过的目录）不覆盖。

含真实密钥的源配置、发布包、当前/上一发布目录、gateway 状态仅供部署用户访问。构建镜像的 context 不包含真实 settings.json。
