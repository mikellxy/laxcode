<div align="right">

**中文** | [English](./README_EN.md)

</div>

<p align="center">
  <img src="./laxcode.jpg" alt="LaxCode" width="360" height="360">
</p>

[![Tests](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml/badge.svg)](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml)

LaxCode 是一个用 Go 实现的轻量 AI Agent。

## 功能导航

- [**Coding Agent CLI**](#coding-agent-cli) — 支持文件检索、编辑和命令执行的终端智能体
- [**Agent 效果评估**](#agent-session-evaluation) — 基于完整 ReAct 日志评估一次任务的完成效果(LLM-as-a-Judge)
- [**Agentic 问答**](#agentic-memory-qa) — 支持 RAG 用户记忆召回与 SSE 交互页面
- [**Agentic RAG 问答**](#agentic-rag-qa) — 支持知识库检索与 SSE 交互页面

## 快速开始

<a id="coding-agent-cli"></a>

### Coding Agent CLI
> [!TIP]
> 默认挂载工具：`grep` `glob` `read_file` `write_file` `edit_file` `bash` `read_artifact`

```shell
export OPENAI_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_BASE_URL=https://api.openai.com/v1     # 任意 OpenAI 兼容端点
export OPENAI_MODEL=gpt-4o-mini

make build
./bin/laxcode
```
<img src="examples/laxcode_intro.gif" alt="LaxCode 终端交互演示" width="960" style="max-width: 100%; height: 600px;">  

<a id="agent-session-evaluation"></a>

### 评估 Coding Agent 任务

当您使用 LaxCode 完成一个任务后，可以指定该任务原有的 `workdir` 和 `session_id`，让 LaxCode 以独立的 LLM-as-a-Judge 会话评估这次任务的完成效果。评估器会读取以下不可变 ReAct 消息日志：

```text
${workdir}/.laxcode/.session/${session_id}/history.jsonl
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

<a id="agentic-memory-qa"></a>

### Agentic 问答(支持 RAG 用户记忆召回,提供SSE页面)
> [!TIP]
> - 默认不挂载工具
> - 异步从每三轮 ReAct 循环提取用户记忆，进行 chunk-vectorization 持久化
> - 用户 query 向量化召回记忆
> - 使用指定的 sqlite-vec db

```shell
# 指定向量化模型 & 初始化 chunk-vectorization python pipeline
export OPENAI_EMBEDDING_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_EMBEDDING_BASE_URL=https://api.openai.com/v1 # 任意 OpenAI 兼容端点
export OPENAI_EMBEDDING_MODEL_NAME=text-embedding-3-small
uv sync --project knowledge-pipeline --locked
export USER_MEMORY_EXECUTABLE="$PWD/knowledge-pipeline/.venv/bin/laxcode-knowledge"
# 非必须，可以正常启动sse服务进行问答，并跳过用户记忆提取和召回
```
```shell
export OPENAI_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_BASE_URL=https://api.openai.com/v1     # 任意 OpenAI 兼容端点
export OPENAI_MODEL_NAME=gpt-4o-mini

make build
# 激活记忆功能时
# -vector-dim 根据使用的向量化模型设置
# -kb 设置 sqlite-vec db 绝对路径
mkdir -p /tmp/laxcode-example
./bin/laxcode -sse \
  -kb=/tmp/laxcode-example/kb.sqlite \
  -vector-dim=1024 \
  -workdir=/tmp/laxcode-example \
  -addr=127.0.0.1:8080
```
```shell
# 另开一个终端，启动 React 前端（http://127.0.0.1:5173）
pnpm --dir web install --frozen-lockfile
pnpm --dir web dev

# 或使用 npm
npm --prefix web install
npm --prefix web run dev
```

<a id="agentic-rag-qa"></a>

### Agentic RAG 问答(提供SSE页面)
> [!TIP]
> - 默认不挂载工具
> - step-1: 使用项目的 knowledge-pipeline 工具进行知识文档 chunk-vectorization，并持久化到指定的 sqlite-vec db
> - step-2: 启动 laxcode agentic RAG QA，体验 RAG 知识库

```shell
# 配置知识库使用的向量化模型
export OPENAI_EMBEDDING_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_EMBEDDING_BASE_URL=https://api.openai.com/v1 # 任意 OpenAI 兼容端点
export OPENAI_EMBEDDING_MODEL_NAME=text-embedding-3-small

# 安装 Python pipeline，并将知识文档写入指定的 sqlite-vec db
uv sync --project knowledge-pipeline --locked
mkdir -p /tmp/laxcode-qa
"$PWD/knowledge-pipeline/.venv/bin/laxcode-knowledge" \
  --target=knowledge \
  --doc=/absolute/path/to/knowledge.md \
  --db=/tmp/laxcode-qa/kb.sqlite
```
```shell
# QA 查询必须使用与建库相同的向量化模型
export OPENAI_EMBEDDING_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_EMBEDDING_BASE_URL=https://api.openai.com/v1 # 任意 OpenAI 兼容端点
export OPENAI_EMBEDDING_MODEL_NAME=text-embedding-3-small

export OPENAI_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_BASE_URL=https://api.openai.com/v1     # 任意 OpenAI 兼容端点
export OPENAI_MODEL_NAME=gpt-4o-mini

make build
mkdir -p /tmp/laxcode-qa/workdir
./bin/laxcode -sse \
  -qa \
  -kb=/tmp/laxcode-qa/kb.sqlite \
  -workdir=/tmp/laxcode-qa/workdir \
  -vector-dim=1024 \
  -addr=127.0.0.1:8080
```
```shell
# 另开一个终端，启动 React 前端（http://127.0.0.1:5173）
pnpm --dir web install --frozen-lockfile
pnpm --dir web dev

# 或使用 npm
npm --prefix web install
npm --prefix web run dev
```
