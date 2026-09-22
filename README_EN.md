<div align="right">

[中文](./README.md) | **English**

</div>

<p align="center">
  <img src="./laxcode.png" alt="LaxCode" width="360" height="180">
</p>

[![Tests](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml/badge.svg)](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml)

LaxCode is a lightweight AI Agent implemented in Go.

## Feature Navigation

- [**Coding Agent CLI**](#coding-agent-cli)
- [**Agent Evaluation**](#agent-session-evaluation) — Evaluates a completed task from its full ReAct log using LLM-as-a-Judge
- [**Agentic QA**](#agentic-memory-qa) — Supports RAG user-memory recall and an SSE interaction page
- [**Agentic RAG QA**](#agentic-rag-qa) — Supports knowledge-base retrieval and an SSE interaction page

## Quick Start

<a id="coding-agent-cli"></a>

### Coding Agent CLI
> [!TIP]
> Default mounted tools: `grep` `glob` `read_file` `write_file` `edit_file` `bash` `read_artifact`

```shell
make build
./bin/laxcode
```
<img src="examples/laxcode_intro.gif" alt="LaxCode interactive terminal demo" width="960" style="max-width: 100%; height: 600px;">  

<a id="agent-session-evaluation"></a>

### Evaluate a Coding Agent Task

After completing a task with LaxCode, you can evaluate its outcome in an independent LLM-as-a-Judge session by specifying the task's original `workdir` and `session_id`. The evaluator reads the following immutable ReAct message log:

```text
${workdir}/.laxcode/.session/${session_id}/history.jsonl
```

Run the evaluation with the target task's `session_id` passed through `-eval_session`:

```shell
./bin/laxcode \
  -evaluate \
  -workdir=/path/to/project \
  -eval_session=88a74c78-a5c4-4602-bb1e-8e4a4ce0256b
```

The evaluation report is written to stdout as a single-line JSON object. Its fields include:

- `eval_session_id`: the session ID of the task being evaluated.
- `session_id`: the independent session ID created for the evaluator.
- `result`: a Markdown report containing evidence-backed scores for tool-call appropriateness, tool-call robustness, task planning, user-goal completion, and other dimensions.

The evaluator neither resumes nor modifies the target session. Its own messages and token statistics are stored in the independent evaluation session.

<a id="agentic-memory-qa"></a>

### Agentic QA (supports RAG user-memory recall and provides an SSE page)
> [!TIP]
> - No tools mounted by default
> - Asynchronously extracts user memory every three ReAct loops and persists it via chunk-vectorization
> - Vectorizes the user query to recall memories
> - Uses the specified sqlite-vec db

```shell
uv sync --project knowledge-pipeline --locked
```
```shell
make build
# When memory is enabled:
# -vector-dim should match the embedding model in use
# -kb sets the absolute path of the sqlite-vec db
mkdir -p /tmp/laxcode-example
./bin/laxcode -sse \
  -kb=/tmp/laxcode-example/kb.sqlite \
  -vector-dim=1024 \
  -workdir=/tmp/laxcode-example \
  -addr=127.0.0.1:8090
```
```shell
# In another terminal, start the React frontend (http://127.0.0.1:5173)
pnpm --dir web install --frozen-lockfile
pnpm --dir web dev

# or use npm
npm --prefix web install
npm --prefix web run dev
```

<a id="agentic-rag-qa"></a>

### Agentic RAG QA (provides an SSE page)

> [!TIP]
> - No tools mounted by default

#### Step 1: Build the knowledge base with the bundled tool

```shell
uv sync --project knowledge-pipeline --locked
mkdir -p /tmp/laxcode-qa
"$PWD/knowledge-pipeline/.venv/bin/laxcode-knowledge" \
  --target=knowledge \
  --doc=/absolute/path/to/knowledge.md \
  --db=/tmp/laxcode-qa/kb.sqlite
```

The splitter automatically reads `~/.laxcode/chunk_settings.json`. If the file is absent, it uses the default Markdown heading rule. See [`knowledge-pipeline/chunk_config.example.json`](./knowledge-pipeline/chunk_config.example.json) for the format.

#### Step 2: Set the knowledge base path and start the RAG server

```shell
make build
mkdir -p /tmp/laxcode-qa/workdir
./bin/laxcode -sse \
  -qa \
  -kb=/tmp/laxcode-qa/kb.sqlite \
  -workdir=/tmp/laxcode-qa/workdir \
  -addr=127.0.0.1:8090
```

#### Step 3: Start the web UI in another terminal

```shell
pnpm --dir web install --frozen-lockfile
pnpm --dir web dev

# or use npm
npm --prefix web install
npm --prefix web run dev
```

Open <http://127.0.0.1:5173>.
