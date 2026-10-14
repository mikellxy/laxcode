<div align="right">

**中文** | [English](./README_EN.md)

</div>

<p align="center">
  <span style="font-family: 'Arial Rounded MT Bold', 'Nunito', sans-serif; font-size: 24px; font-weight: 700; color: #8798E5;">Lax</span><span style="font-family: 'Arial Rounded MT Bold', 'Nunito', sans-serif; font-size: 24px; font-weight: 700; color: #58D2C2;">Code</span>
</p>

[![Tests](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml/badge.svg)](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml)

LaxCode 是一个用 Go 实现、通过 Web UI 使用的轻量 coding agent。需要 Go 版本 >= 1.26。
## 快速开始
```shell
git clone https://github.com/mikellxy/laxcode.git && cd laxcode
brew install pnpm
./web.sh
```
该命令默认会在 http://127.0.0.1:5173 启动 Web UI, 本机启动时还会用默认浏览器打开页面。 [Windows使用](./docs/windows_run_web.md)
  
<img src="./examples/laxcode_web.png">  
  
支持上报 trace 至 OTel 服务  
  
<img src="./examples/otel.png">

## 特性
- 会话存储引擎
  - SQLite 事务 + 乐观锁、上下文压缩in_memory消息分代原子化更新、崩溃恢复时进行 agent 循环完整性检测。 [设计文档](./docs/session-storage-engine.md)
- 效果评测
  - 提供 agent 循环效果评测配套工具，多维打分，输出人类可读报告。 [查看单任务评估样例](./docs/readpaged-maxbytes-fix-evaluation.md)
- 上下文压缩
  - 剪枝、上下文卸载、LLM 结构化摘要三层压缩。 [设计文档](./docs/context-compaction-design.md)
- 软沙箱防护 & 危险命令 human-in-the-loop 确认
- 可观测性
  - 上报 agent 循环 span 到您的 Otel 服务(SigNoz/Tempo/Jaeger...)。 [将 LaxCode Span 上报到 SigNoz](./docs/signoz-tracing.md)

## 功能导航

- [**Coding Agent Web**](#coding-agent-web)
- [**Agent 效果评估**](#agent-session-evaluation) — 基于完整 ReAct 日志评估一次任务的完成效果(LLM-as-a-Judge)
- [**架构**](#architecture) 

### 创建配置文件

[配置文件使用说明](./docs/settings.md)。

<a id="coding-agent-web"></a>

### Coding Agent Web
> [!TIP]
> 默认挂载工具：`grep` `glob` `read_file` `write_file` `edit_file` `bash` `read_artifact`

在 macOS 上启动 Web UI：

```shell
brew install pnpm
./web.sh
```

`web.sh` 会安装前端依赖、构建前端生产资源和两个 Go 程序，以随机本地端口启动 coding 后端，再启动固定于 `127.0.0.1:5173` 的静态 Web 服务，将 API 和 SSE 请求代理到后端。前端不启用热更新，修改源码后需重新运行脚本才会生效。脚本在启动后端前设置 `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` 和 `OTEL_SERVICE_NAME=laxcode`，通过 OTLP/HTTP 上报 trace。只有前后端均通过健康检查后才会打开默认浏览器；按 `Ctrl-C` 可同时停止两个服务。

Windows PowerShell 直接运行启动脚本：

```powershell
.\web.ps1
```

仓库中的 `bin/win/laxcode.exe` 与 `bin/win/laxcode-web.exe` 是预编译的 Windows x64 程序，后者已嵌入前端生产资源并反向代理到后端的随机端口。因此 Windows 用户无需安装 Go、Node.js、pnpm 或 GCC。脚本会在两个服务均就绪后打开默认浏览器，并在 `Ctrl-C` 后清理两个进程。

维护者更新 Windows 产物时，可在 macOS 安装 Go 和 `pnpm` 后执行 `make build-windows`。该目标会重新构建前端，并覆盖 `bin/win/` 中的两个 x64 程序。

新建会话时由页面传入工作目录，并与 `session_id`、`mode` 持久绑定。遇到需要确认的工具操作时，页面会暂停当前流并显示确认框。

后端默认启动 coding SSE 服务，支持 `-addr`、`-plan`，以及配套使用的 `-model`、`-openai_api_key`、`-openai_base_url` 启动参数。原 `-sse`、`-mode`、`-kb`、`-session` 参数已移除；会话通过 Web UI 创建和续接。已有 code 会话仍可使用，旧 RAG 会话不能作为 coding 会话续接；已有数据库中的遗留记忆表保留原数据。

<a id="agent-session-evaluation"></a>

### 评估 Coding Agent 任务

当您使用 LaxCode 完成一个任务后，可以在会话页面右上角点击“评估”，输入本次评估的 requirement。Web 后端会为原会话创建快照，并以独立的 LLM-as-a-Judge 会话异步评估任务完成效果。

```text
${workdir}/eval/${session_id}/history.jsonl
```

评估任务以 `queued`、`running`、`succeeded`、`failed` 状态保存在 SQLite 中，页面会自动轮询。完成后的 Markdown 报告写入 `${HOME}/.laxcode/eval/`，任务列表会显示 requirement、状态和报告文件路径。

评估过程不会续聊或修改被评估任务的 session；评估器使用 `eval_${session_id}_${date_time}` 独立 session 保存自己的消息和 token 统计。

<a id="architecture"></a>

### 架构

Go 后端按 DDD 分层组织，核心依赖方向为 `cmd → application → domain ← infrastructure`；Web 前端作为独立子工程。

```text
LaxCode/
├── cmd/                            # 程序入口与运行模式适配
│   ├── main/                       # 加载配置并启动 coding Web 后端
│   ├── agentasm/                   # 组合根：装配 Agent、工具、模型、会话与追踪
│   ├── run_sse/                    # Web 后端：SSE、异步评估、会话、审批与目录选择接口
│   └── web/                        # 内嵌前端资源及反向代理的独立 Web 程序
├── internal/
│   ├── application/                # 应用层：编排领域能力与完整用例
│   │   ├── reactservice/           # ReAct 推理循环、上下文压缩与子 Agent 调度
│   │   └── llm_router/             # 本地模型网关的 HTTP/SSE 编排
│   ├── domain/                     # 领域层：核心模型、规则与基础设施端口
│   │   ├── session/                # 会话聚合、请求上下文及仓储接口
│   │   ├── tools/                  # 工具注册表、内置工具行为与执行端口
│   │   ├── prompt/                 # 系统提示词、Skill 与 Plan Mode 组装
│   │   ├── compactor/              # 上下文压缩策略
│   │   ├── llmprovider/            # LLM 客户端接口
│   │   ├── llmrouter/              # 模型网关流式传输接口
│   │   ├── telemetry/              # Trace 名称、属性与观测语义
│   │   └── sharedkernel/           # 消息、工具、SSE 与 token 等共享类型
│   └── infrastructure/             # 基础设施层：领域端口的外部实现
│       ├── llmprovider/             # OpenAI Responses 协议适配
│       ├── ai_models/               # 模型目录、连接、OAuth 鉴权与上游流式 client
│       ├── sessionrepo/             # SQLite 会话、历史与评估任务仓储
│       ├── artifactstore/           # 会话产物的文件存储
│       ├── workfs/                  # 工作区文件读写实现
│       ├── shell/                   # Shell 执行、超时与进程管理
│       ├── ripgrep/                 # 文件搜索与内容检索适配
│       ├── skillrepo/               # 本地 Skill 扫描与加载
│       ├── skillstore/              # 全局 Skill 包的受限暂存与原子提交
│       ├── config/                  # 配置合并、模型目录结构与 MCP 配置
│       ├── layout/                  # 用户数据与会话磁盘布局
│       └── tracing/                 # OpenTelemetry、OTLP 与本地追踪实现
├── web/                             # React + TypeScript Web 客户端
│   └── src/
│       ├── api/                     # 后端接口与 SSE 客户端
│       ├── app/                     # 应用根组件与全局 Provider
│       ├── components/              # 聊天、模型选择等界面组件
│       ├── features/                # 聊天状态与身份等业务模块
│       ├── hooks/                   # 工作区等可复用 React Hooks
│       └── types/                   # 前端 API 与消息类型
```


### 云端原型

运行 `./cloud.sh` 构建并启动后端和 Web UI。Web 默认监听 `0.0.0.0:5173`，可用
`LAXCODE_CLOUD_PORT=5180 ./cloud.sh` 修改端口；后端只监听本机，并通过 Web 服务代理。
直接启动后端时使用 `./bin/laxcode -cloud`，该参数由现有 config 解析流程处理。

部署前在有网络的构建环境运行 `make build-cloud-bash-image`，生成
`laxcode-cloud-bash:local`（Linux，Go 1.26.6、Python 3、rg、Bash）。镜像需预装在
运行服务的 Docker daemon 上；可用 `./cloud.sh -cloud_bash_image="your-image:tag"`
指定其他预制镜像。服务必须以可访问 Docker 的非 root 账户运行，挂载文件按服务
账户的 UID/GID 读写。运行时禁止自动拉取镜像，Docker/镜像不可用时不会回退到宿主机 Bash。

cloud 主 Agent 和子 Agent 每次 Bash 调用都使用一个新容器：`--rm --network=none
--pull=never`，保留本次容器内的 `127.0.0.1` 回环访问。仅将项目 WorkDir 和用户
TempDir 挂载到容器相同的绝对路径；不挂载 UserRoot、agenthome 或 Docker socket。
容器根文件系统只读，删除全部 capabilities、禁止提权，限 2 CPU、1 GiB 内存、
256 个进程；容器 `/tmp` 使用 256 MiB tmpfs，HOME 为 `/tmp/agenthome`。
TMPDIR/TMP/TEMP 指向挂载的用户 tmp，Go 禁用依赖下载和自动工具链下载。
Go 的 GOPATH 位于用户 tmp/go，避免向镜像中的只读目录写入模块缓存。
超时、取消和 Runner.Close 显式清理容器；后台服务不会跨 Bash 调用保留，启动、
等待就绪、测试和结束服务必须在同一次调用内完成。挂载目录中的文件保留，其他
目录里的环境修改随容器删除。Skills 通过宿主机的 grep/glob/read_file 工具读取。
非零退出和执行异常在模型正文、压缩摘要中都附带离线提示：用容器内的本地实现，
不反复联网重试；受离线限制的验证应说明未验证项目、原因及用户自行验证命令。
每次调用最多捕获 32 MiB 合并日志，超出部分丢弃并明确提示，Docker 日志驱动关闭。
容器内命令由 Docker 提供访问隔离，本地及未接入沙箱的 Bash 保留静态风险检查。

云端项目只输入名称（1–120 个英文字母、数字、中横线、下划线；`agenthome`、`tmp`
为保留名称），同一用户的重名项目返回 409。首次创建项目初始化
`~/laxcode/{user_id}/agenthome/.laxcode`、用户 SQLite 与技能模板；项目工作目录是
`~/laxcode/{user_id}/{project_name}`。默认 API Key 模型配置与 skills 从服务账户的
`.laxcode` 复制，OAuth 凭据、历史和 MCP 配置不会复制；后续用户配置互相独立。
可用 `./cloud.sh -conf_copy_dir="/srv/laxcode-defaults"` 指定初始化来源；该目录直接包含
`settings.json` 和 `skills/`，不再追加 `.laxcode`。相对路径按后端启动目录解析；
未指定时使用服务账户的 `~/.laxcode`。已有用户不会被重新复制或覆盖。

浏览器继续用访客 UUID cookie 分流。直接调用 API 时可传 `X-Laxcode-User-ID` UUID
请求头；原有项目/session 接口中的 `user_id` 仍保留，多个来源的身份必须一致。
`GET /api/config` 返回 cloud 标记。OAuth、目录选择、人工审批和共享 MCP 在此模式下关闭。
前端工作目录及悬停提示只显示 `/{project_name}`。主 Agent 和子 Agent 的系统提示词
要求回答中的项目路径隐藏宿主机前缀；工具调用与服务端请求继续使用真实工作目录。

文件工具的相对路径从项目 workdir 解析，允许访问整个用户根，越界直接返回工具错误。
每用户共用 `tmp`，仅子进程设置 HOME、TMPDIR、TMP、TEMP，服务进程环境不受请求影响。
云端 grep/glob 通过受限文件句柄操作，跳过符号链接与 `.git`，当前未应用 `.gitignore`。

云端会记录每次 chat 通过 `write_file`、`edit_file` 写入的项目文件，在最后一条
assistant 消息后展示新增/修改清单。重复写入与 resume 合并到同一清单，文件路径
显示为 `/{project_name}/...`。点击「打包下载 ZIP」下载文件当前版本，保留项目目录
结构；文件缺失或越界会报错。单次下载最多 200 个文件、未压缩总大小 100 MiB。
配置目录、临时目录及 Bash/脚本生成的文件不自动收录，不保存历史文件快照。
新增 `chat_upsert_files` 表由启动时自动迁移创建，删除 session 时清理其清单。

访客 UUID 仍不是登录鉴权，Docker Bash 隔离不能替代真实用户认证；云端部署仍需
在入口完成用户身份验证。Bash/脚本产生的文件不会自动加入聊天文件清单。
