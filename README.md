<div align="right">

**中文** | [English](./README_EN.md)

</div>

<p align="center">
  <img src="./laxcode.png" alt="LaxCode" width="400" height="180">
</p>

[![Tests](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml/badge.svg)](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml)

LaxCode 是一个用 Go 实现的轻量 AI Agent。

## 特性
- 会话存储引擎
  - SQLite 事务 + 乐观锁、上下文压缩in_memory消息分代原子化更新、崩溃恢复时进行 agent 循环完整性检测
  - [会话存储引擎设计文档](./docs/session-storage-engine.md)
- Agentic RAG
  - 提供基于 LangGraph 的知识库制作配套 pipeline，使用标题、chunk_size、overlap_size 三重约束的 chunk splitter（[knowledge-pipeline](./knowledge-pipeline/)）
- 效果评测
  - 提供 agent 循环效果评测配套工具，多维打分，输出人类可读报告
  - [查看单任务评估样例](./docs/readpaged-maxbytes-fix-evaluation.md)
- 上下文压缩
  - 剪枝、上下文卸载、LLM 结构化摘要三层压缩
  - [上下文压缩设计文档](./docs/context-compaction-design.md)
- Token 预算
  - 主模型、压缩模型、向量化模型可独立配置上下文窗口，支持热切换
- Agent 运行保护
  - 连续 3 次相同工具名与参数的调用会中断本轮推理；连续 5 次使用同一工具会向模型注入自查提醒
  - 使用 Bash AST 检查命令；删除、提权、覆盖重定向等风险操作须由用户输入 `yes` 后执行
  - 命令检查属于静态规则，不能推断任意外部程序或脚本的全部副作用
- 可观测性
  - 上报 agent 循环 span 到您的 Otel 服务(SigNoz/Tempo/Jaeger...)
  - [将 LaxCode Span 上报到 SigNoz](./docs/signoz-tracing.md)

## 功能导航

- [**Coding Agent CLI**](#coding-agent-cli)
- [**Agent 效果评估**](#agent-session-evaluation) — 基于完整 ReAct 日志评估一次任务的完成效果(LLM-as-a-Judge)
- [**Agentic RAG**](#agentic-rag-qa) — 支持知识库检索与 SSE 交互页面

## 快速开始
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

Windows PowerShell 使用 Node.js LTS 安装 pnpm，然后运行对等的启动脚本：

```powershell
winget install OpenJS.NodeJS.LTS
npx get-pnpm
.\web.ps1
```

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
