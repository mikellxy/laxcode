# 宿主机部署

固定 Linux amd64 单实例部署。宿主机运行 Docker、OpenShell 0.1.4 gateway、laxcode-manager 和 laxcode-proxy；每用户沙箱运行 laxcode-cloud。manager/proxy 由 `deploy.py` 直接启动，不安装 systemd 服务。Docker 和 Caddy 由基础环境管理。

## 基础环境

使用已经准备好的 LaxCode ECS 基础镜像：Python 3、Docker Engine（systemd driver、cgroup v2）、OpenShell CLI/gateway **0.1.4**，以及已挂载的 `/sys/fs/bpf`。Docker 使用 `/var/run/docker.sock`，默认 `cgroup-parent` 必须为 `laxcode.slice`；部署以 root 执行，部署进程和 gateway/manager 自身位于该 slice 之外。

预装以下 Linux amd64 镜像，不在每次应用发布中搬运：

- `laxcode-tools:go1.26.6-v2`
- `ghcr.io/nvidia/openshell/supervisor:0.1.4`
- `ghcr.io/nvidia/openshell/sandbox:0.1.4`

需要重新制作基础镜像时，可在有网络的构建机执行 `sh deploy/build.sh --prepare-images`，再用 docker save/load 搬运。该命令固定构建/拉取 Linux amd64 镜像。工具链包含 Go 1.26.6、Node 22、Python、Bash、rg、jq、中文字体和 excalidraw-canvas。应用部署使用预装工具镜像离线 COPY 新 cloud 二进制。

OpenShell workload 初始化需要隔离网络内的回环自检。manager 会核验其网络隔离和 cgroup 后允许这段内部通信，不放开 supervisor 访问宿主回环、私网或元数据目标。

此流程不修改 dockerd 配置，不安装 manager service，也不自动迁移旧 systemd 安装。重装基础系统后直接使用；存在旧 manager service 的机器需先完成基础环境迁移。

## 构建和 CI 制品

```sh
# 首次准备配置；已有真实配置时不要覆盖
cp deploy/defaults/settings.example.json deploy/defaults/settings.json
cp deploy/admin.example.json deploy/admin.local.json
# 编辑真实模型配置和 API Key，或由 CI 注入该文件
# 编辑 admin.local.json，设置管理员账号和至少 12 字节的密码
cd deploy
sh build.sh
```

本机 macOS、Linux 和 CI 均固定交叉编译 **Linux amd64**，无需设置 TARGETOS/TARGETARCH。构建需要 Go 1.26.6、Node/pnpm、Python 3，以及依赖下载能力或已准备的缓存。

引导页版本由 `deploy/guide-version.txt` 管理（1–64 个字母、数字、`.`、`_` 或 `-`）。需要让用户重新查看引导时，修改该版本并发布；`build.sh` 通过 `-ldflags "-X main.guideVersion=..."` 注入 proxy。普通发布保持版本不变。直接 `go run` / `go build` 的开发默认版本为 `dev`。

### 只读管理后台

`/` 保持前台页面，`/admin` 显示管理员登录页；已有有效登录则直接显示管理页。后台依次展示用户 UUID、项目、会话名称/创建时间和分页历史消息，附件仅显示文件名。本期不提供用户删除、聊天发送、文件预览或下载。

`deploy/admin.local.json` 已加入 `.gitignore`，由构建脚本读取，不能使用示例密码。`build_admin.py` 使用随机盐和 PBKDF2-HMAC-SHA256（600000 次）生成密码校验值，通过 `-ldflags -X` 注入 proxy；明文账号密码文件不会复制到发布包或前端资源。修改凭据须重新编译部署。二进制包含密码校验值，仍应限制发布包访问权限。

登录接口为 `POST /admin/api/login`，请求 JSON 为 `{"username":"…","password":"…"}`，返回 `token` 和 `expires_in`。其余后台接口必须携带 `Authorization: Bearer <token>`：

- `GET /admin/api/me`：校验当前登录。
- `GET /admin/api/users?limit=100&after_user_id=…`：用户目录列表，返回 `users`、`has_more`、`next_after_user_id`。
- `GET /admin/api/users/{uid}/projects`：项目列表。
- `GET /admin/api/users/{uid}/sessions?project_id=…&limit=50&before_session_id=…`：会话分页。
- `GET /admin/api/users/{uid}/sessions/{sid}/messages?limit=50&before_seq=…`：历史消息分页。

JWT 有效期 2 小时，签名密钥每次 proxy 启动时重新生成，因此重启后需重新登录。token 仅存于浏览器当前标签页的 `sessionStorage`，退出时清除；退出不撤销已签发 token 的服务端有效期。登录采用全局限速（突发 5 次，每 3 秒补充一次），不信任客户端转发 IP 请求头。所有后台 API 响应禁止缓存，现有前台接口与 `/internal/store` 的身份机制不变。使用 HTTPS 对外提供登录页面。

Caddy 若已经统一 `reverse_proxy 127.0.0.1:5173`，无需新增规则。Vite 开发服务器已代理 `/admin/api`；直接 `go run` / `go build` 未注入凭据时后台接口返回 503，前台继续可用。需要本地后台调试时，先运行 `pnpm --dir web build`，再从仓库根运行 `python3 deploy/build_admin.py deploy/admin.local.json /tmp/laxcode-proxy dev`，按正常 proxy 参数启动该二进制。

已读版本通过 `/api/user-config` 保存在各用户现有 `sessions.db` 的 `user_config` 表中，首次打开数据库时自动建表，无需清库或重建沙箱。旧的 `laxcode_guide_seen` Cookie 不再参与判断，因此首次升级此功能时用户会再看到一次引导。

唯一发布制品为仓库根目录的 **`release/laxcode.tar.gz`**，权限 0600。整个 `/release/` 和真实 `deploy/defaults/settings.json` 被 Git 忽略。仓库不再使用根目录 `bin/`；压缩包内的 `bin/` 只用于组织三个部署程序。tar 仅写文件条目，目录由解压时自动创建，避免 `--unlink-first` 尝试移除已有非空目录。

```text
release/laxcode.tar.gz
└── laxcode/
    ├── deploy.py
    ├── reset_quota.py         # 维护时停止服务并清理配额 BPF pins
    ├── bin/laxcode-proxy       # 内嵌前端
    ├── bin/laxcode-cloud       # 沙箱程序
    ├── bin/laxcode-manager     # 宿主配额服务
    ├── settings.json          # 真实凭据，仅宿主注册 provider 使用
    ├── network.json
    ├── skills/
    ├── Dockerfile
    ├── configure.py
    └── manifest.json          # 架构、依赖版本、文件 SHA-256
```

CI 下载代码、注入配置后执行上述构建，再将 `release/laxcode.tar.gz` 上传 TOS，使用 `laxcode.{uuid}.tar.gz` 作为对象名。UUID 和上传由流水线负责；build/deploy 不管理远端版本。包内含真实凭据，TOS 使用私有访问。

## 安装与覆盖更新

在 ECS 上执行以下命令；`TOS_URL` 填本次制品的可下载地址（例如私有对象的预签名 URL）：

```sh
mkdir -p /root/laxcode_deploy
cd /root/laxcode_deploy
curl --fail --location "$TOS_URL" -o laxcode.tar.gz.download &&
  mv -f laxcode.tar.gz.download laxcode.tar.gz &&
  tar --unlink-first -xzf laxcode.tar.gz &&
  cd laxcode &&
  python3 deploy.py
```

临时下载文件仅防止中断时留下半个包，不备份旧压缩包。**直接解压到固定 `laxcode/` 目录**，不创建临时发布目录、不移动发布目录。`--unlink-first` 在写入前移除同名旧文件，避免向正在执行的二进制原地写入。解压成功后立即执行部署；这不是无中断发布，解压到重启之间旧服务可能读取新文件，不要并行执行多次下载/解压/部署。

```sh
cd /root/laxcode_deploy/laxcode
python3 deploy.py          # 默认 restart，也可显式传 restart
python3 deploy.py status   # manager、gateway、proxy PID
python3 deploy.py stop     # 删除应用沙箱，停止三个后台进程
```

部署脚本执行：

1. 校验新包文件、宿主架构、依赖镜像、Docker 默认父组和 bpffs。
2. 停止 proxy；已有 gateway 时先删除本应用沙箱；停止旧 manager。
3. 在原目录清理新 manifest 未列出的旧文件和旧生成文件，保留包内文件。
4. 首次生成 `state/manager.json`，之后复用；启动新 manager 并等待 guard 与 Docker 对账就绪。
5. 确保专用 gateway 可用，清理剩余旧沙箱，再生成配置。冷启动也先安装 guard 才启动 gateway。
6. 离线构建固定 **`laxcode-cloud:current`**，删除旧应用镜像，注册 provider，启动 proxy 并检查健康状态。

仅删除带 `laxcode-app=laxcode` 标签的应用沙箱。gateway 已运行时复用；`stop` 会一并停止 gateway。删除沙箱会终止正在执行的任务，用户数据和当天配额保留。

不创建 `.previous` 或镜像版本标签，不备份、不自动回滚。构建成功后按旧镜像 ID 清理（包含上次中断遗留的应用标签镜像），不删除新镜像、不做全局 prune；Docker 共享层和构建缓存可继续复用。失败后检查日志、修复原因并重新运行；需要旧代码时从 TOS 下载旧包再覆盖部署，**不要恢复旧配额 DB 或手工删除 BPF pins**。

## 规则或 ABI 变更后的维护清理

若 manager 因旧的 eBPF 规则或 ABI 不兼容而无法启动，发布包提供专用脚本：

```sh
cd /root/laxcode_deploy/laxcode
python3 reset_quota.py
python3 deploy.py
```

以 root 执行；脚本会停止应用并删除应用沙箱，终止正在执行的任务。它持有部署锁和 manager 锁，确认父 cgroup 无进程，先清空账本的恢复标记，再删除该服务的 pinned links 和 Maps。DB、用户文件、累计授权保留；下次启动保守占用旧的未确认授权，不退回当天额度。失败可排除原因后重跑，不自动重启服务，也不会自动清空账本。

本版本使用 BPF ABI 3 / 账本 schema 3；不兼容 pins 仍需维护清理。此次 epoch 重构按旧数据已清空的前提仅修改新建表结构，不提供旧账本自动迁移；部署前由操作者准备新表。路径配置注意事项见 [manager 运维说明](../laxcode-manager/README.md#维护时清理-ebpf-挂载)。

## 持久目录和启动行为

| 路径 | 用途 |
| --- | --- |
| `/root/laxcode_deploy/laxcode/` | 当前程序和生成配置，脚本清理其中非制品文件，不存放用户文件 |
| `/root/laxcode_deploy/state/` | PID、日志、部署锁、manager 配置、gateway 数据库/密钥及 XDG 配置 |
| `/root/laxcode_deploy/network.json` | 可选的机器级域名扩展，覆盖包内默认配置 |
| `/root/laxcode/` | 用户工作目录及 proxy 内部存储密钥 |
| `/var/lib/laxcode-manager/` | 配额 SQLite 数据库、WAL/SHM、单实例锁 |
| `/sys/fs/bpf/laxcode-manager/` | 当前开机周期的 pinned Maps/links |

日志为 `state/manager.log`、`state/gateway.log`、`state/proxy.log`。Unix Socket 位于 `/run/laxcode-manager/control.sock`。模型公开目录是 `laxcode/catalog/settings.json`；`laxcode/image/` 仅为不含真实密钥的 Docker build context，构建后删除，不是临时发布目录。

SSH 退出不停止后台服务。**ECS 重启后需手动执行 `python3 deploy.py`，进程异常退出也不会自动拉起。** Docker/Caddy 自启不等于应用自启。保持 OpenShell 0.1.4 的容器默认无自动重启策略，不手工改成 always/unless-stopped，避免沙箱在 guard 安装前启动。manager 退出不会卸载本次开机的 guard，但不再补充授权；重启接管保留账本。

proxy 监听 `0.0.0.0:5173`，gateway 仅监听 `127.0.0.1:18080`（健康端口 18081）。公网入口使用基础环境中的 Caddy `https://laxcode.cn`；安全组/宿主防火墙禁止公网直接访问 5173、80，Caddy 拒绝 `/internal/store`。内测版尚无登录验证，`X-USERID` UUID 是路由标识。

## 公网域名配置

构建默认配置为 `deploy/defaults/network.json`。ECS 可在 **`/root/laxcode_deploy/network.json`** 配置跨发布保留的扩展列表；该文件在 `laxcode/` 外，不会被覆盖解压或清理：

```json
{"additional_domains": ["example.com", "rust-lang.org"]}
```

固定开放三段及以上域名 `**.*.*`，以及 github.com、golang.org、go.dev、goproxy.cn、npmjs.org、pypi.org、pythonhosted.org。列表只补充小写域名，不填 URL、端口、IP 或通配符。修改后在 `laxcode/` 执行 `python3 deploy.py`，通过重建沙箱生效，不重置当天额度。

公网范围限 HTTP(S) 80/443，eBPF 仍阻断非公网地址；不承诺任意网站或任意协议。manager 的日额度及控制对端配置位于 `state/manager.json`，详见 [配置说明](../laxcode-manager/README.md#两个配置入口)。

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

proxy 首次打开一个用户的数据接口时，将发布包 `catalog/settings.json` 中的平台配置原子写入用户的 `agenthome/.laxcode/settings.json`；每次应用发布后，该用户首次访问时同步最新配置，再启动沙箱。沙箱中以发布目录 `catalog/settings.json` 为可信源，单独只读挂载到 `/workplace/agenthome/.laxcode/settings.json`，cloud 从 HOME 默认路径加载。挂载源位于用户工作目录之外，避免用户修改目录结构后篡改下次启动的配置。不要在保留运行中沙箱的情况下单独修改平台配置；正式更新使用 `python3 deploy.py`，先删除旧沙箱再启用新发布。

配置中的 API Key 是合法环境变量名。真实密钥经 configure.py 注册给专用 OpenShell gateway，启动沙箱时由 OpenShell 注入占位凭据，cloud 使用 `os.Getenv` 获取占位凭据。proxy 从发布包 `skills/` 首次原子初始化用户 Skills 目录，以后保留用户修改和删除。用户模型选择独立保存在 `model-selection.json`，同步平台配置时不覆盖。

沙箱镜像仅包含 cloud 可执行程序和固定基础工具链，不包含平台配置或默认 Skills，也不执行配置或 Skills 的复制操作。

含真实密钥的源配置、发布包、当前发布目录、gateway 状态仅供部署用户访问。构建镜像的 context 不包含真实 settings.json。


## 数据接口与沙箱执行

`GET /api/network-usage?day=YYYY-MM-DD` 返回公网流量配额（省略 day 为北京时间今天），不启动沙箱。Linux 执行前验证 manager；不可用返回 503，额度耗尽返回 429，已开始 SSE 时发送对应错误事件。文件和历史接口保持可用。

只有 `POST /chat`、`POST /api/sessions/{session_id}/resume` 和 `POST /api/evaluations` 会启动或唤醒沙箱。项目、会话、历史、上下文用量、性能统计、模型选择、Skills、文件预览/下载、评估列表和报告由 proxy 直接处理，查询与轮询不会刷新沙箱空闲计时。

共享 `workspace` 模块复用现有模型目录、SQLite 仓储及路径规则。SQLite 仍位于用户的 `agenthome/.laxcode/sessions/sessions.db`，但仅 proxy 打开数据库；cloud 通过内部存储接口访问同一仓储。这样避免 macOS 与 Docker Desktop Linux 跨内核共享 SQLite WAL/文件锁。JSONL 备份、trace、报告和项目产物继续保存在原目录。

部署生成的 `store-url` 使用宿主私网地址和 5173 端口；UDP 路由探测不发送数据，不要求目标能访问互联网。OpenShell 策略仅允许 `/usr/local/bin/laxcode-cloud` 向该地址的 `/internal/store` 发送 POST，并固定目标 IP `/32`。proxy 需监听该私网地址（默认 `0.0.0.0:5173`）；公网入口仍使用 Caddy，安全组不要对公网开放 5173。宿主私网地址变化后需重新执行部署以更新策略。

内部存储按用户校验独立凭据，由 `~/laxcode/.proxy-store-key` 派生并在沙箱启动时注入；该文件不挂载进沙箱，proxy 重启时必须保留。普通 `X-USERID` 请求不能调用内部存储接口。

模型切换立即保存 `agenthome/.laxcode/model-selection.json`，已运行任务继续使用其模型快照，之后的新任务读取最新选择。删除会话时，proxy 在用户生命周期锁下检查正在执行的请求和后台评估；繁忙或状态未知时返回 409，不唤醒沙箱。

跨 Docker Desktop 的存储回归可执行：

```bash
LAXCODE_DOCKER_SQLITE_TEST=1 go test ./workspace -run TestStorageAcrossDockerMount -v
```

该测试使用临时目录和一次性容器，需要预装 `laxcode-tools:go1.26.6-v2`。

### 聊天事件持久化

cloud 内嵌 NATS JetStream，通过进程内连接使用，不新增监听端口或独立服务。事件保存在每用户持久工作目录 `agenthome/.laxcode/task-events`，随现有 `/workplace` bind mount 保留；升级仍需先结束旧 cloud，不能让两个进程同时打开同一目录。新任务启动和 cloud 重启时清理较老任务事件；本轮没有定时过期策略。任务元数据继续写入 proxy 管理的每用户 `sessions.db`。`-max-chats` 现在限制并发提交请求；后台执行通过项目级活跃任务唯一索引限制同项目并发。
