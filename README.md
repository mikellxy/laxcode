<div align="right">

**中文** | [English](./README_EN.md)

</div>

# LaxCode

[![Tests](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml/badge.svg)](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml)

LaxCode 是一个用 Go 实现的轻量 AI Agent。

## 快速开始
* 使用 coding agent cli
  * 默认挂载工具：`grep` `glob` `read_file` `write_file` `edit_file` `bash` `read_artifact`
```shell
export OPENAI_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_BASE_URL=https://api.openai.com/v1     # 任意 OpenAI 兼容端点
export OPENAI_MODEL=gpt-4o-mini

make build
./bin/laxcode
```
<img src="examples/laxcode_intro.gif" alt="LaxCode 终端交互演示" width="960" style="max-width: 100%; height: 600px;">  

* 使用 see agentic 问答服务
  * 默认不挂载工具
  * 支持每三轮 ReAct 循环异步提取用户记忆
  * 所有会话中均支持 RAG 召回用户记忆
  * 使用指定的 sqlite-vec db文件存储用户记忆 chunk 和向量
```shell
# 指定向量化模型 & 初始化 chunk-vectorization pipeline
export OPENAI_EMBEDDING_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_EMBEDDING_BASE_URL=https://api.openai.com/v1 # 任意 OpenAI 兼容端点
export OPENAI_EMBEDDING_MODEL_NAME=text-embedding-3-small
uv sync --project knowledge-pipeline --locked
export USER_MEMORY_EXECUTABLE="$PWD/knowledge-pipeline/.venv/bin/laxcode-knowledge"
# 非必须，可以正常启动sse服务进行问答，不执行用户记忆提取和召回
```
```shell
export OPENAI_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_BASE_URL=https://api.openai.com/v1     # 任意 OpenAI 兼容端点
export OPENAI_MODEL_NAME=gpt-4o-mini

make build
# 激活记忆功能时
# -vector-dim 根据使用的向量化模型设置
# -kb 设置 sqlite-vec db文件的绝度路径，laxcode 自动进行 migration
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

* 使用 agentic RAG 问答服务
  * 默认不挂载工具
  * step-1: 使用项目的 knowledge-pipeline 工具进行知识文档 chunk-vectorization，并持久化到指定的 sqlite-vec db 文件
  * step-2: 启动 laxcode agentic RAG qa 服务，体验知识召回
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
./bin/laxcode -qa \
  -kb=/tmp/laxcode-qa/kb.sqlite \
  -workdir=/tmp/laxcode-qa/workdir
```
