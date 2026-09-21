<div align="right">

[中文](./README.md) | **English**

</div>

# LaxCode

[![Tests](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml/badge.svg)](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml)

LaxCode is a lightweight AI Agent implemented in Go.

## Quick Start
* Use the coding agent CLI
  * Default mounted tools: `grep` `glob` `read_file` `write_file` `edit_file` `bash` `read_artifact`
```shell
export OPENAI_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_BASE_URL=https://api.openai.com/v1     # any OpenAI-compatible endpoint
export OPENAI_MODEL=gpt-4o-mini

make build
./bin/laxcode
```
<img src="examples/laxcode_intro.gif" alt="LaxCode interactive terminal demo" width="960" style="max-width: 100%; height: 600px;">  

* Use the SSE agentic QA service
  * No tools mounted by default
  * Asynchronously extracts user memory every three ReAct loops
  * Supports RAG recall of user memory across all sessions
  * Stores user-memory chunks and vectors in a specified sqlite-vec db file
```shell
# Specify the embedding model & initialize the chunk-vectorization pipeline
export OPENAI_EMBEDDING_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_EMBEDDING_BASE_URL=https://api.openai.com/v1 # any OpenAI-compatible endpoint
export OPENAI_EMBEDDING_MODEL_NAME=text-embedding-3-small
uv sync --project knowledge-pipeline --locked
export USER_MEMORY_EXECUTABLE="$PWD/knowledge-pipeline/.venv/bin/laxcode-knowledge"
# Optional: the SSE service can still answer questions without user-memory extraction and recall
```
```shell
export OPENAI_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_BASE_URL=https://api.openai.com/v1     # any OpenAI-compatible endpoint
export OPENAI_MODEL_NAME=gpt-4o-mini

make build
# When memory is enabled:
# -vector-dim should match the embedding model in use
# -kb sets the absolute path of the sqlite-vec db file; laxcode runs migrations automatically
mkdir -p /tmp/laxcode-example
./bin/laxcode -sse \
  -kb=/tmp/laxcode-example/kb.sqlite \
  -vector-dim=1024 \
  -workdir=/tmp/laxcode-example \
  -addr=127.0.0.1:8080
```
```shell
# In another terminal, start the React frontend (http://127.0.0.1:5173)
pnpm --dir web install --frozen-lockfile
pnpm --dir web dev

# or use npm
npm --prefix web install
npm --prefix web run dev
```

* Use the agentic RAG QA service
  * No tools mounted by default
  * step-1: Use the project's knowledge-pipeline tool to chunk-vectorize knowledge documents and persist them into the specified sqlite-vec db file
  * step-2: Start the laxcode agentic RAG QA service to experience knowledge recall
```shell
# Configure the embedding model used by the knowledge base
export OPENAI_EMBEDDING_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_EMBEDDING_BASE_URL=https://api.openai.com/v1 # any OpenAI-compatible endpoint
export OPENAI_EMBEDDING_MODEL_NAME=text-embedding-3-small

# Install the Python pipeline and write knowledge documents into the specified sqlite-vec db
uv sync --project knowledge-pipeline --locked
mkdir -p /tmp/laxcode-qa
"$PWD/knowledge-pipeline/.venv/bin/laxcode-knowledge" \
  --target=knowledge \
  --doc=/absolute/path/to/knowledge.md \
  --db=/tmp/laxcode-qa/kb.sqlite
```
```shell
# QA queries must use the same embedding model as indexing
export OPENAI_EMBEDDING_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_EMBEDDING_BASE_URL=https://api.openai.com/v1 # any OpenAI-compatible endpoint
export OPENAI_EMBEDDING_MODEL_NAME=text-embedding-3-small

export OPENAI_API_KEY=sk-xxxxxxxxxxxxxxxx
export OPENAI_BASE_URL=https://api.openai.com/v1     # any OpenAI-compatible endpoint
export OPENAI_MODEL_NAME=gpt-4o-mini

make build
mkdir -p /tmp/laxcode-qa/workdir
./bin/laxcode -qa \
  -kb=/tmp/laxcode-qa/kb.sqlite \
  -workdir=/tmp/laxcode-qa/workdir
```
