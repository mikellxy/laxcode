<div align="right">

[中文](./README.md) | **English**

</div>

<p align="center">
  <span style="font-family: 'Arial Rounded MT Bold', 'Nunito', sans-serif; font-size: 24px; font-weight: 700; color: #8798E5;">Lax</span><span style="font-family: 'Arial Rounded MT Bold', 'Nunito', sans-serif; font-size: 24px; font-weight: 700; color: #58D2C2;">Code</span>
</p>

[![Tests](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml/badge.svg)](https://github.com/mikellxy/laxcode-cli/actions/workflows/test.yml)

LaxCode is a lightweight coding agent implemented in Go with a Web UI. Requires Go >= 1.26.
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
- Evaluation
  - Bundled tooling for evaluating agent-loop effectiveness, with multi-dimensional scoring and human-readable reports. [View a single-task evaluation sample](./docs/readpaged-maxbytes-fix-evaluation.md)
- Context compaction
  - Three layers of compaction: pruning, context offloading, and LLM structured summarization. [Design doc](./docs/context-compaction-design.md)
- Soft sandbox protection & human-in-the-loop confirmation for dangerous commands
- Observability
  - Export agent-loop spans to your OTel service (SigNoz/Tempo/Jaeger...). [Export LaxCode spans to SigNoz](./docs/signoz-tracing.md)

## Feature Navigation

- [**Coding Agent Web**](#coding-agent-web)
- [**Agent Session Evaluation**](#agent-session-evaluation) — Evaluate how well a task was completed based on its full ReAct log (LLM-as-a-Judge)
- [**Architecture**](#architecture) 

### Creating the configuration file

[Configuration file usage](./docs/settings.md).

<a id="coding-agent-web"></a>

### Coding Agent Web
> [!TIP]
> Default mounted tools: `grep` `glob` `read_file` `write_file` `edit_file` `bash` `read_artifact`

Start the Web UI on macOS:

```shell
brew install pnpm
./web.sh
```

`web.sh` installs frontend dependencies, builds the Go program, starts the coding backend on a random local port, and then launches the Vite page pinned to `127.0.0.1:5173`. The default browser opens only after both the frontend and backend pass their health checks; press `Ctrl-C` to stop both services.

On Windows PowerShell, run the launcher directly:

```powershell
.\web.ps1
```

The repository includes prebuilt Windows x64 binaries at `bin/win/laxcode.exe` and `bin/win/laxcode-web.exe`. The latter embeds the production frontend assets and reverse-proxies to the backend's random port, so Windows users do not need Go, Node.js, pnpm, or GCC. The script opens the default browser once both services are ready and cleans up both processes on `Ctrl-C`.

To refresh the Windows artifacts, maintainers can install Go and `pnpm` on macOS and run `make build-windows`. This target rebuilds the frontend and replaces both x64 executables in `bin/win/`.

For manual startup, you can still run `./bin/laxcode -token-budget=100000`. When creating a session, the page supplies the working directory, which is persisted alongside the `session_id` and `mode`. The budget is tracked continuously per `session_id` within the current SSE server process, and a new baseline is established after the server restarts. When a threshold is reached or a dangerous Bash command is encountered, the page pauses the current stream and shows a confirmation dialog.

The backend starts as a coding SSE service by default. Startup options are `-addr`, `-plan`, and `-token-budget`; the former `-sse`, `-mode`, `-kb`, and `-session` flags have been removed. Create and resume sessions through the Web UI. Existing code sessions remain usable; old RAG sessions cannot be resumed as coding sessions. Retired memory tables in existing databases are left intact.

<a id="agent-session-evaluation"></a>

### Evaluating a Coding Agent Task

After completing a task with LaxCode, click **Evaluate** in the upper-right corner of the conversation and enter the evaluation requirement. The Web backend snapshots the source session and runs an independent LLM-as-a-Judge session asynchronously.

```text
${workdir}/eval/${session_id}/history.jsonl
```

Evaluation jobs are stored in SQLite with `queued`, `running`, `succeeded`, and `failed` states, and the page polls active jobs automatically. Completed Markdown reports are written under `${HOME}/.laxcode/eval/`; the task list shows each requirement, status, and report path.

The evaluation neither resumes nor modifies the source session. The evaluator stores its own messages and token statistics in an isolated `eval_${session_id}_${date_time}` session.

<a id="architecture"></a>

### Architecture

The Go backend is organized into DDD layers, with the core dependency direction `cmd → application → domain ← infrastructure`; the Web frontend operates as an independent sub-project.

```text
LaxCode/
├── cmd/                            # Program entrypoints and run-mode adapters
│   ├── main/                       # Loads configuration and starts the coding Web backend
│   ├── agentasm/                   # Composition root: assembles Agent, tools, models, sessions, and tracing
│   ├── run_sse/                    # Web backend: SSE, async evaluation, session, approval, and directory-selection APIs
│   └── web/                        # Standalone web program embedding frontend assets with a reverse proxy
├── internal/
│   ├── application/                # Application layer: orchestrates domain capabilities and full use cases
│   │   ├── reactservice/           # ReAct reasoning loop, context compaction, and sub-agent scheduling
│   │   └── llm_router/             # HTTP/SSE orchestration for the local model gateway
│   ├── domain/                     # Domain layer: core models, rules, and infrastructure ports
│   │   ├── session/                # Session aggregate, request context, and repository interfaces
│   │   ├── tools/                  # Tool registry, built-in tool behaviors, and execution ports
│   │   ├── prompt/                 # System prompt, Skill, and Plan Mode assembly
│   │   ├── compactor/              # Context compaction strategies
│   │   ├── llmprovider/            # LLM client interface
│   │   ├── llmrouter/              # Model gateway streaming interface
│   │   ├── telemetry/              # Trace names, attributes, and observability semantics
│   │   └── sharedkernel/           # Shared types for messages, tools, SSE, tokens, etc.
│   └── infrastructure/             # Infrastructure layer: external implementations of domain ports
│       ├── llmprovider/            # OpenAI Responses protocol adapter
│       ├── llmrouter/              # OpenAI-compatible streaming gateway adapter
│       ├── sessionrepo/            # SQLite session, history, and evaluation repositories
│       ├── artifactstore/          # File storage for session artifacts
│       ├── workfs/                 # Workspace file read/write implementation
│       ├── shell/                  # Shell execution, timeouts, and process management
│       ├── ripgrep/                # File search and content retrieval adapter
│       ├── skillrepo/              # Local Skill scanning and loading
│       ├── skillstore/             # Restricted staging and atomic commits for global Skills
│       ├── config/                 # Config loading and model catalog
│       ├── layout/                 # User data and session disk layout
│       └── tracing/                # OpenTelemetry, OTLP, and local tracing implementations
├── web/                            # React + TypeScript web client
│   └── src/
│       ├── api/                    # Backend APIs and SSE client
│       ├── app/                    # App root component and global providers
│       ├── components/             # UI components such as chat and model selection
│       ├── features/               # Business modules such as chat state and identity
│       ├── hooks/                  # Reusable React hooks (workspace, etc.)
│       └── types/                  # Frontend API and message types
```
