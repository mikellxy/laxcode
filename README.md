<div align="right">

**中文** | [English](./README_EN.md)

</div>

<p align="center">
  <span style="font-family: 'Arial Rounded MT Bold', 'Nunito', sans-serif; font-size: 24px; font-weight: 700; color: #8798E5;">Lax</span><span style="font-family: 'Arial Rounded MT Bold', 'Nunito', sans-serif; font-size: 24px; font-weight: 700; color: #58D2C2;">Code</span>
</p>

[![Tests](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml/badge.svg)](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml)

LaxCode 是一个用 Go 实现的轻量 Agent。支持 coding agent 和 agentic RAG 两种模式，配套提供基于 LangGraph 的知识库制作工作流。需要 Go 版本 >= 1.26。
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
- Agentic RAG
  - 提供基于 LangGraph 的知识库制作配套 pipeline，使用标题、chunk_size、overlap_size 三重约束的 chunk splitter（[knowledge-pipeline](./knowledge-pipeline/)）
- 效果评测
  - 提供 agent 循环效果评测配套工具，多维打分，输出人类可读报告。 [查看单任务评估样例](./docs/readpaged-maxbytes-fix-evaluation.md)
- 上下文压缩
  - 剪枝、上下文卸载、LLM 结构化摘要三层压缩。 [设计文档](./docs/context-compaction-design.md)
- 软沙箱防护 & 危险命令 human-in-the-loop 确认
- 可观测性
  - 上报 agent 循环 span 到您的 Otel 服务(SigNoz/Tempo/Jaeger...)。 [将 LaxCode Span 上报到 SigNoz](./docs/signoz-tracing.md)

## 功能导航

- [**Coding Agent CLI**](#coding-agent-cli)
- [**Agent 效果评估**](#agent-session-evaluation) — 基于完整 ReAct 日志评估一次任务的完成效果(LLM-as-a-Judge)
- [**Agentic RAG**](#agentic-rag-qa) — 支持知识库检索与 SSE 交互页面
- [**架构**](#architecture) 

### 创建配置文件

[配置文件使用说明](./docs/settings.md)。

<a id="coding-agent-cli"></a>

### Coding Agent CLI
> [!TIP]
> 默认挂载工具：`grep` `glob` `read_file` `write_file` `edit_file` `bash` `read_artifact`

```shell
make build
./bin/laxcode
```

交互模式可设置本次运行的 token 预算，例如 `./bin/laxcode -token-budget=100000`。输入与输出 token 合计达到预算的 1、1.5、2 倍等阈值时，继续执行前会询问是否继续；输入 `yes` 继续，其他输入停止。续接旧会话时，历史用量不计入本次预算。

浏览器代码模式可在 macOS 上两步启动：

```shell
brew install pnpm
./web.sh
```

`web.sh` 会安装前端依赖、构建 Go 程序，以随机本地端口启动 `-sse -code`，再启动固定于 `127.0.0.1:5173` 的 Vite 页面。只有前后端均通过健康检查后才会打开默认浏览器；按 `Ctrl-C` 可同时停止两个服务。

Windows PowerShell 直接运行启动脚本：

```powershell
.\web.ps1
```

仓库中的 `bin/win/laxcode.exe` 与 `bin/win/laxcode-web.exe` 是预编译的 Windows x64 程序，后者已嵌入前端生产资源并反向代理到后端的随机端口。因此 Windows 用户无需安装 Go、Node.js、pnpm 或 GCC。脚本会在两个服务均就绪后打开默认浏览器，并在 `Ctrl-C` 后清理两个进程。

维护者更新 Windows 产物时，可在 macOS 安装 `pnpm`、`sqlite` 和 `mingw-w64` 后执行 `make build-windows`。该目标会重新构建前端，并覆盖 `bin/win/` 中的两个 x64 程序。

手动启动时仍可使用 `./bin/laxcode -sse -code -token-budget=100000`。新建会话时由页面传入工作目录，并与 `session_id` 持久绑定。该模式挂载与 CLI 相同的代码工具；预算按当前 SSE 服务进程中的 `session_id` 连续计算，服务重启后重新建立基线。达到阈值或遇到危险 Bash 命令时，页面会暂停当前流并显示确认框。
<img src="examples/laxcode_intro.gif" alt="LaxCode 终端交互演示" width="960" style="max-width: 100%; height: 600px;">  

<a id="agent-session-evaluation"></a>

### 评估 Coding Agent 任务

当您使用 LaxCode 完成一个任务后，可以指定该任务原有的 `workdir` 和 `session_id`，让 LaxCode 以独立的 LLM-as-a-Judge 会话评估这次任务的完成效果。会话数据统一保存在用户目录，评估器会读取以下不可变 ReAct 消息日志：

```text
${HOME}/.laxcode/sessions/${session_id}/history.jsonl
```

运行评估时，通过 `-eval_session` 传入待评估任务的 `session_id`：

```shell
./bin/laxcode \
  -evaluate \
  -workdir=/path/to/project \
  -eval_session=88a74c78-a5c4-4602-bb1e-8e4a4ce0256b
```

评估报告会从 stdout 以单行 JSON 输出。其中：

- `eval_session_id` 是被评估任务的 session ID。
- `session_id` 是本次评估器新建的独立 session ID。
- `result` 是 Markdown 格式的评估报告，包含工具调用合理性、工具调用健壮性、任务规划能力和用户目标完成度等维度的评分与证据。

评估过程不会续聊或修改被评估任务的 session；评估器自己的消息和 token 统计保存在独立 session 中。

<a id="agentic-rag-qa"></a>

### Agentic RAG 问答

> [!TIP]
> - 默认不挂载工具

#### Step 1：制作知识库

按照 [knowledge-pipeline 使用说明](./knowledge-pipeline/README.md)配置向量模型并导入文档。

#### Step 2：指定知识库路径并启动 RAG 服务

```shell
make build
mkdir -p /tmp/laxcode-qa/workdir
./bin/laxcode -sse \
  -qa \
  -kb=/tmp/laxcode-qa/kb.sqlite \
  -workdir=/tmp/laxcode-qa/workdir \
  -addr=127.0.0.1:8090
```

#### Step 3：在另一终端启动 Web 界面

```shell
pnpm --dir web install --frozen-lockfile
pnpm --dir web dev

# 或使用 npm
npm --prefix web install
npm --prefix web run dev
```

打开 <http://127.0.0.1:5173>。

<a id="architecture"></a>

### 架构

Go 后端按 DDD 分层组织，核心依赖方向为 `cmd → application → domain ← infrastructure`；Web 前端和知识库导入管线作为独立子工程协作。

```text
LaxCode/
├── cmd/                            # 程序入口与运行模式适配
│   ├── main/                       # CLI 主入口：加载配置并分流各运行模式
│   ├── agentasm/                   # 组合根：装配 Agent、工具、模型、会话与追踪
│   ├── run_cli/                    # Coding Agent 交互式终端模式
│   ├── run_evaluate/               # LLM-as-a-Judge 任务评估模式
│   ├── run_sse/                    # Web 后端：SSE、QA、会话、审批与目录选择接口
│   └── web/                        # 内嵌前端资源及反向代理的独立 Web 程序
├── internal/
│   ├── application/                # 应用层：编排领域能力与完整用例
│   │   ├── reactservice/           # ReAct 推理循环、上下文压缩与子 Agent 调度
│   │   ├── qaservice/              # 知识库检索增强问答流程
│   │   ├── usermemory/             # 用户记忆召回、摘要与异步写入流程
│   │   └── llm_router/             # 本地模型网关的 HTTP/SSE 编排
│   ├── domain/                     # 领域层：核心模型、规则与基础设施端口
│   │   ├── session/                # 会话聚合、请求上下文及仓储接口
│   │   ├── tools/                  # 工具注册表、内置工具行为与执行端口
│   │   ├── prompt/                 # 系统提示词、Skill 与 Plan Mode 组装
│   │   ├── compactor/              # 上下文压缩策略
│   │   ├── knowledgebase/          # 知识库、向量检索与嵌入接口
│   │   ├── llmprovider/            # LLM 客户端接口
│   │   ├── llmrouter/              # 模型网关流式传输接口
│   │   ├── telemetry/              # Trace 名称、属性与观测语义
│   │   └── sharedkernel/           # 消息、工具、SSE 与 token 等共享类型
│   └── infrastructure/             # 基础设施层：领域端口的外部实现
│       ├── llmprovider/             # OpenAI Responses 协议适配
│       ├── llmrouter/               # OpenAI 兼容流式网关适配
│       ├── embedding/               # OpenAI 兼容向量模型适配
│       ├── knowledgebase/           # SQLite + sqlite-vec 知识库实现
│       ├── sessionrepo/             # SQLite 会话、历史与记忆任务仓储
│       ├── artifactstore/           # 会话产物的文件存储
│       ├── workfs/                  # 工作区文件读写实现
│       ├── shell/                   # Shell 执行、超时与进程管理
│       ├── ripgrep/                 # 文件搜索与内容检索适配
│       ├── skillrepo/               # 本地 Skill 扫描与加载
│       ├── skillstore/              # 全局 Skill 包的受限暂存与原子提交
│       ├── memorypipeline/          # Python 记忆入库管线的进程适配
│       ├── config/                  # 配置加载与模型目录
│       ├── layout/                  # 用户数据与会话磁盘布局
│       ├── cliprinter/              # 交互式终端界面
│       └── tracing/                 # OpenTelemetry、OTLP 与本地追踪实现
├── web/                             # React + TypeScript Web 客户端
│   └── src/
│       ├── api/                     # 后端接口与 SSE 客户端
│       ├── app/                     # 应用根组件与全局 Provider
│       ├── components/              # 聊天、模型选择等界面组件
│       ├── features/                # 聊天状态与身份等业务模块
│       ├── hooks/                   # 工作区等可复用 React Hooks
│       └── types/                   # 前端 API 与消息类型
├── knowledge-pipeline/              # Python/LangGraph 知识与用户记忆导入管线
│   ├── src/laxcode_knowledge/
│   │   ├── cmd/                     # 命令行入口
│   │   ├── models/                  # 配置与数据模型
│   │   ├── node/                    # LangGraph 处理节点
│   │   ├── splitter/                # 文档分块策略
│   │   ├── state/                   # 工作流状态定义
│   │   ├── store/                   # SQLite 向量数据写入
│   │   └── tools/                   # 管线工具抽象与注册
│   └── tests/                       # 知识管线测试
```
