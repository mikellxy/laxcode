# LaxCode Cloud

[English](./README_EN.md)

LaxCode 是用 Go 和 React 实现、通过 Web UI 使用的 coding agent。
`cloud` 分支专用于云端部署；本地模式由独立分支维护。

## 快速开始

开发机需要 Go >= 1.26.6、pnpm、Docker，以及已安装的 rg。以具有 Docker 访问权限的普通账户运行：

```sh
make build-cloud-bash-image
./cloud.sh
```

浏览器访问 `http://127.0.0.1:5173`。首次创建项目时初始化用户工作区；在模型设置中
填写 API Key 和 Base URL 后即可开始聊天。也可按 [配置说明](./docs/settings.md)
预设新用户的模型与 Skills。

<img src="./examples/laxcode_web.png">

## 功能

- 按访客 UUID 隔离项目、模型、会话与 Skills；SQLite 保存会话和请求上下文。
- 主 Agent 与子 Agent 使用离线 Docker Bash，文件工具限制在当前用户根目录内。
- 三层上下文压缩：剪枝、工具输出归档、结构化摘要。
- 会话评估、性能监控、文件预览与 ZIP 下载。
- OpenTelemetry/OTLP 与文件追踪；[SigNoz 配置](./docs/signoz-tracing.md)。

## 架构

依赖方向为 `cmd → application → domain ← infrastructure`。
`cmd/main` 启动服务，`cmd/run_sse` 按用户分流，`cmd/agentasm` 按请求装配 Agent；
`internal/application/reactservice` 编排推理循环，`web/src` 提供 React 客户端。
模型客户端直接连接用户配置的上游 Responses API。

会话评估会创建独立快照和评估任务，不修改被评估会话；完成后可下载完整 Markdown 报告。
模型配置与会话数据的备份方法见 [部署说明](./deploy/cloud/README.md)。

## 云端运行与部署

前端首次访问时展示两页上手引导：性能监控入口及汇总、Skill 调用与产物下载。
关闭、跳过或完成引导后，在当前浏览器写入有效期一年的 Cookie，后续直接进入主页面。
引导完全在前端运行，进入主页面后才发起工作区接口请求；截图按可用屏幕空间完整缩放。

构建 NAS 的 Linux amd64 离线部署包：在安装 Go、Node/pnpm、Docker 的开发机上执行
`make build-cloud`。产物位于 `nas/`，包括两个程序、内嵌前端、Bash 镜像 tar、
启动脚本、构建信息和 SHA256 校验文件。已有 `nas/defaults/` 配置与 Skills 会保留；
首次构建生成空配置，需自行放入模型配置与预置 Skills。部署步骤见 `nas/README.md`。

运行 `./cloud.sh` 构建并启动后端和 Web UI。Web 默认监听 `0.0.0.0:5173`，可用
`LAXCODE_CLOUD_PORT=5180 ./cloud.sh` 修改端口；后端只监听本机，并通过 Web 服务代理。
直接启动后端使用 `./bin/laxcode`，本分支始终运行云端服务，无需模式参数。

部署前在有网络的构建环境运行 `make build-cloud-bash-image`，生成
`laxcode-cloud-bash:local`（Linux，Go 1.26.6、Python 3、rg、jq、Bash）。镜像需预装在
运行服务的 Docker daemon 上；可用 `./cloud.sh -cloud_bash_image="your-image:tag"`
指定其他预制镜像。服务必须以可访问 Docker 的非 root 账户运行，挂载文件按服务
账户的 UID/GID 读写。运行时禁止自动拉取镜像，Docker/镜像不可用时不会回退到宿主机 Bash。

Cloud 模式全局最多同时处理 2 个 Chat/Resume 请求，名额覆盖整段 SSE 流和文件统计收尾。
超出上限直接返回 HTTP 429（`SERVER_BUSY`），不排队；前端可稍后手动重试。

可通过 `GET /api/sessions/{session_id}/performance?user_id={user_id}` 汇总本地 tracing
日志，查看各轮 Chat/Resume 的总耗时、首输出耗时，以及 LLM 和各工具的平均耗时。
统计口径及调用方式见 [Session 性能汇总接口](./docs/session-performance.md)。

cloud 主 Agent 和子 Agent 每次 Bash 调用都使用一个新容器：`--rm --network=none
--pull=never`，保留本次容器内的 `127.0.0.1` 回环访问。项目 WorkDir 和用户
TempDir 挂载到容器相同的绝对路径，权限为可读写；另将
`agenthome/.laxcode/skills` 以相同绝对路径只读挂载，允许读取和执行包内脚本、资源。
不挂载整个 UserRoot、agenthome、其他配置与会话数据或 Docker socket。
容器根文件系统只读，删除全部 capabilities、禁止提权，限 2 CPU、1 GiB 内存、
256 个进程；容器 `/tmp` 使用 256 MiB tmpfs，HOME 为 `/tmp/agenthome`。
TMPDIR/TMP/TEMP 指向挂载的用户 tmp，Go 禁用依赖下载和自动工具链下载。
Go 的 GOPATH 位于用户 tmp/go，避免向镜像中的只读目录写入模块缓存。
超时、取消和 Runner.Close 显式清理容器；后台服务不会跨 Bash 调用保留，启动、
等待就绪、测试和结束服务必须在同一次调用内完成。挂载目录中的文件保留，其他
目录里的环境修改随容器删除。Skills 定义优先通过宿主机的 grep/glob/read_file
工具读取；包内脚本可通过 Bash 的真实绝对路径执行，生成文件和缓存必须写入
项目或 `$TMPDIR`。HOME 仍为容器临时目录，不能用 `$HOME` 拼接 Skills 路径。
只读挂载保留原有文件权限，直接执行脚本需要可执行位，也可用 bash/python3
解释器读取脚本；依赖需要镜像预装，网络限制不变。
非零退出和执行异常在模型正文、压缩摘要中都附带离线提示：用容器内的本地实现，
不反复联网重试；受离线限制的验证应说明未验证项目、原因及用户自行验证命令。
每次调用最多捕获 32 MiB 合并日志，超出部分丢弃并明确提示，Docker 日志驱动关闭。
容器内命令由 Docker 提供访问隔离；缺少 Docker 沙箱能力时直接拒绝 Bash 执行。

云端项目只输入名称（1–120 个英文字母、数字、中横线、下划线；`agenthome`、`tmp`
为保留名称），同一用户的重名项目返回 409。首次创建项目初始化
`~/laxcode/{user_id}/agenthome/.laxcode`、用户 SQLite 与技能模板；项目工作目录是
`~/laxcode/{user_id}/{project_name}`。默认 API Key 模型配置与 skills 从服务账户的
`.laxcode` 读取。宿主机模板允许保留明文 API Key，复制时将 Key 按
`user_id:provider:model` 写入宿主机的 `~/.laxcode/configs.db`（覆盖已有记录），
用户 agenthome 下的 settings 不包含 Key。OAuth 凭据、历史和 MCP 配置不会复制；
后续用户配置互相独立，已有用户 JSON 中的旧 Key 会在加载时迁移。
可用 `./cloud.sh -conf_copy_dir="/srv/laxcode-defaults"` 指定初始化来源；该目录直接包含
`settings.json` 和 `skills/`，不再追加 `.laxcode`。相对路径按后端启动目录解析；
未指定时使用服务账户的 `~/.laxcode`。已有用户不会被重新复制或覆盖。

浏览器继续用访客 UUID cookie 分流。直接调用 API 时可传 `X-Laxcode-User-ID` UUID
请求头；原有项目/session 接口中的 `user_id` 仍保留，多个来源的身份必须一致。
`GET /api/config` 返回 cloud 标记。模型接入使用 API Key；项目通过名称创建。
前端工作目录及悬停提示只显示 `/{project_name}`。主 Agent 和子 Agent 的系统提示词
要求回答中的项目路径隐藏宿主机前缀；工具调用与服务端请求继续使用真实工作目录。

文件工具的相对路径从项目 workdir 解析，允许访问整个用户根，越界直接返回工具错误。
每用户共用 `tmp`，仅子进程设置 HOME、TMPDIR、TMP、TEMP，服务进程环境不受请求影响。
云端 grep/glob 通过受限文件句柄操作，跳过符号链接与 `.git`，当前未应用 `.gitignore`。

云端在每次 Chat/Resume 前后扫描项目文件，记录本轮新增和修改的产物，在最后一条
assistant 消息后展示新增/修改清单。重复写入与 resume 合并到同一清单，文件路径
显示为 `/{project_name}/...`。点击「打包下载 ZIP」下载文件当前版本，保留项目目录
结构；文件缺失或越界会报错。单次下载最多 200 个文件、未压缩总大小 100 MiB。
配置目录、临时目录、Git 元数据及常见依赖/缓存目录不收录；Bash/脚本生成的项目文件也会收录，不保存历史文件快照。
新增 `chat_upsert_files` 表由启动时自动迁移创建，删除 session 时清理其清单。

访客 UUID 仍不是登录鉴权，Docker Bash 隔离不能替代真实用户认证；云端部署仍需
在入口完成用户身份验证。
