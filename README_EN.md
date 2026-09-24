<div align="right">

[中文](./README.md) | **English**

</div>

<p align="center">
  <span style="font-family: 'Arial Rounded MT Bold', 'Nunito', sans-serif; font-size: 24px; font-weight: 700; color: #8798E5;">Lax</span><span style="font-family: 'Arial Rounded MT Bold', 'Nunito', sans-serif; font-size: 24px; font-weight: 700; color: #58D2C2;">Code</span>
</p>

[![Tests](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml/badge.svg)](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml)

LaxCode is a lightweight Agent implemented in Go. It supports two modes — coding agent and agentic RAG — and ships with a LangGraph-based knowledge base pipeline. Requires Go >= 1.26.
## Quick Start
```shell
git clone https://github.com/mikellxy/laxcode.git && cd laxcode
brew install pnpm
./web.sh
```
By default, this command starts the Web UI at http://127.0.0.1:5173 and opens the page in the default browser when started locally. [Windows usage](./docs/windows_run_web.md)
  
<img src="./examples/laxcode_web.png">  
  
Supports exporting traces to an OTel service  
  
<img src="./examples/otel.png">

## Features
- Session storage engine
  - SQLite transactions + optimistic locking, generation-based atomic updates of in-memory messages during context compaction, and agent-loop integrity checks on crash recovery. [Design doc](./docs/session-storage-engine.md)
- Agentic RAG
  - Ships a LangGraph-based knowledge base pipeline, using a chunk splitter with triple constraints on heading, chunk_size, and overlap_size ([knowledge-pipeline](./knowledge-pipeline/))
- Evaluation
  - Bundled tooling for evaluating agent-loop effectiveness, with multi-dimensional scoring and human-readable reports. [View a single-task evaluation sample](./docs/readpaged-maxbytes-fix-evaluation.md)
- Context compaction
  - Three layers of compaction: pruning, context offloading, and LLM structured summarization. [Design doc](./docs/context-compaction-design.md)
- Soft sandbox protection & human-in-the-loop confirmation for dangerous commands
- Observability
  - Export agent-loop spans to your OTel service (SigNoz/Tempo/Jaeger...). [Export LaxCode spans to SigNoz](./docs/signoz-tracing.md)

## Feature Navigation

- [**Coding Agent CLI**](#coding-agent-cli)
- [**Agent Session Evaluation**](#agent-session-evaluation) — Evaluate how well a task was completed based on its full ReAct log (LLM-as-a-Judge)
- [**Agentic RAG**](#agentic-rag-qa) — Knowledge-base retrieval with an SSE interaction page
- [**Architecture**](#architecture) 

### Creating the configuration file

[Configuration file usage](./docs/settings.md).

<a id="coding-agent-cli"></a>

### Coding Agent CLI
> [!TIP]
> Default mounted tools: `grep` `glob` `read_file` `write_file` `edit_file` `bash` `read_artifact`

```shell
make build
./bin/laxcode
```

In interactive mode, you can set a token budget for the current run, e.g. `./bin/laxcode -token-budget=100000`. When the combined input and output tokens reach thresholds such as 1x, 1.5x, or 2x the budget, LaxCode asks whether to continue before proceeding; enter `yes` to continue, or any other input to stop. When resuming an old session, historical usage is not counted toward the current budget.

Browser coding mode starts in two steps on macOS:

```shell
brew install pnpm
./web.sh
```

`web.sh` installs frontend dependencies, builds the Go program, starts `-sse -code` on a random local port, and then launches the Vite page pinned to `127.0.0.1:5173`. The default browser opens only after both the frontend and backend pass their health checks; press `Ctrl-C` to stop both services.

On Windows PowerShell, run the launcher directly:

```powershell
.\web.ps1
```

The repository includes prebuilt Windows x64 binaries at `bin/win/laxcode.exe` and `bin/win/laxcode-web.exe`. The latter embeds the production frontend assets and reverse-proxies to the backend's random port, so Windows users do not need Go, Node.js, pnpm, or GCC. The script opens the default browser once both services are ready and cleans up both processes on `Ctrl-C`.

To refresh the Windows artifacts, maintainers can install `pnpm`, `sqlite`, and `mingw-w64` on macOS and run `make build-windows`. This target rebuilds the frontend and replaces both x64 executables in `bin/win/`.

For manual startup, you can still run `./bin/laxcode -sse -code -token-budget=100000`. When creating a session, the page supplies the working directory, which is persisted alongside the `session_id`. This mode mounts the same coding tools as the CLI; the budget is tracked continuously per `session_id` within the current SSE server process, and a new baseline is established after the server restarts. When a threshold is reached or a dangerous Bash command is encountered, the page pauses the current stream and shows a confirmation dialog.
<img src="examples/laxcode_intro.gif" alt="LaxCode interactive terminal demo" width="960" style="max-width: 100%; height: 600px;">  

<a id="agent-session-evaluation"></a>

### Evaluating a Coding Agent Task

After completing a task with LaxCode, you can specify that task's original `workdir` and `session_id` to have LaxCode evaluate the outcome in an independent LLM-as-a-Judge session. Session data is stored uniformly under the user directory, and the evaluator reads the following immutable ReAct message log:

```text
${HOME}/.laxcode/sessions/${session_id}/history.jsonl
```

Run the evaluation with the target task's `session_id` passed through `-eval_session`:

```shell
./bin/laxcode \
  -evaluate \
  -workdir=/path/to/project \
  -eval_session=88a74c78-a5c4-4602-bb1e-8e4a4ce0256b
```

The evaluation report is printed to stdout as a single-line JSON object. It contains:

- `eval_session_id`: the session ID of the task being evaluated.
- `session_id`: the independent session ID created for this evaluator run.
- `result`: a Markdown evaluation report containing scores and evidence across dimensions such as tool-call appropriateness, tool-call robustness, task planning, and user-goal completion.

The evaluation neither resumes nor modifies the evaluated task's session; the evaluator's own messages and token statistics are stored in a separate session.

<a id="agentic-rag-qa"></a>

### Agentic RAG QA

> [!TIP]
> - No tools mounted by default

#### Step 1: Build the knowledge base

Follow the [knowledge-pipeline instructions](./knowledge-pipeline/README.md) to configure the embedding model and import documents.

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

<a id="architecture"></a>

### Architecture

The Go backend is organized into DDD layers, with the core dependency direction `cmd → application → domain ← infrastructure`; the web frontend and the knowledge ingestion pipeline operate as independent sub-projects.

```text
LaxCode/
├── cmd/                            # Program entrypoints and run-mode adapters
│   ├── main/                       # CLI main entry: loads config and dispatches run modes
│   ├── agentasm/                   # Composition root: assembles Agent, tools, models, sessions, and tracing
│   ├── run_cli/                    # Coding Agent interactive terminal mode
│   ├── run_evaluate/               # LLM-as-a-Judge task evaluation mode
│   ├── run_qa/                     # Agentic RAG command-line QA mode
│   ├── run_sse/                    # Web backend: SSE, session, approval, and directory-selection APIs
│   └── web/                        # Standalone web program embedding frontend assets with a reverse proxy
├── internal/
│   ├── application/                # Application layer: orchestrates domain capabilities and full use cases
│   │   ├── reactservice/           # ReAct reasoning loop, context compaction, and sub-agent scheduling
│   │   ├── qaservice/              # Knowledge-base retrieval-augmented QA flow
│   │   ├── usermemory/             # User memory recall, summarization, and async write flows
│   │   └── llm_router/             # HTTP/SSE orchestration for the local model gateway
│   ├── domain/                     # Domain layer: core models, rules, and infrastructure ports
│   │   ├── session/                # Session aggregate, request context, and repository interfaces
│   │   ├── tools/                  # Tool registry, built-in tool behaviors, and execution ports
│   │   ├── prompt/                 # System prompt, Skill, and Plan Mode assembly
│   │   ├── compactor/              # Context compaction strategies
│   │   ├── knowledgebase/          # Knowledge base, vector retrieval, and embedding interfaces
│   │   ├── llmprovider/            # LLM client interface
│   │   ├── llmrouter/              # Model gateway streaming interface
│   │   ├── telemetry/              # Trace names, attributes, and observability semantics
│   │   └── sharedkernel/           # Shared types for messages, tools, SSE, tokens, etc.
│   └── infrastructure/             # Infrastructure layer: external implementations of domain ports
│       ├── llmprovider/            # OpenAI Responses protocol adapter
│       ├── llmrouter/              # OpenAI-compatible streaming gateway adapter
│       ├── embedding/              # OpenAI-compatible embedding model adapter
│       ├── knowledgebase/          # SQLite + sqlite-vec knowledge base implementation
│       ├── sessionrepo/            # SQLite session, history, and memory-task repositories
│       ├── artifactstore/          # File storage for session artifacts
│       ├── workfs/                 # Workspace file read/write implementation
│       ├── shell/                  # Shell execution, timeouts, and process management
│       ├── ripgrep/                # File search and content retrieval adapter
│       ├── skillrepo/              # Local Skill scanning and loading
│       ├── memorypipeline/         # Process adapter for the Python memory ingestion pipeline
│       ├── config/                 # Config loading and model catalog
│       ├── layout/                 # User data and session disk layout
│       ├── cliprinter/             # Interactive terminal UI
│       └── tracing/                # OpenTelemetry, OTLP, and local tracing implementations
├── web/                            # React + TypeScript web client
│   └── src/
│       ├── api/                    # Backend APIs and SSE client
│       ├── app/                    # App root component and global providers
│       ├── components/             # UI components such as chat and model selection
│       ├── features/               # Business modules such as chat state and identity
│       ├── hooks/                  # Reusable React hooks (workspace, etc.)
│       └── types/                  # Frontend API and message types
├── knowledge-pipeline/             # Python/LangGraph knowledge and user-memory ingestion pipeline
│   ├── src/laxcode_knowledge/
│   │   ├── cmd/                    # CLI entrypoints
│   │   ├── models/                 # Config and data models
│   │   ├── node/                   # LangGraph processing nodes
│   │   ├── splitter/               # Document chunking strategies
│   │   ├── state/                  # Workflow state definitions
│   │   ├── store/                  # SQLite vector data writing
│   │   └── tools/                  # Pipeline tool abstractions and registry
│   └── tests/                      # Knowledge pipeline tests
```
