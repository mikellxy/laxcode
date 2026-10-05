# 将 LaxCode Span 上报到 SigNoz

LaxCode 内置 OpenTelemetry OTLP/HTTP exporter。配置 SigNoz 的 OTLP 地址后，
LaxCode 会将 `chat`、`llm-generate` 和 `tool-exec` 等 span 批量上报到 SigNoz，并在 Traces 页面展示完整调用瀑布流。

## 1. 使用 Docker 部署 SigNoz

以下是 SigNoz 官方当前推荐的单机 Docker Compose 部署方式。运行前请确保已安装
Docker Engine 20.10+ 和 Docker Compose v2，并为 Docker 分配至少 4 GB 内存。

安装 Foundry CLI：

```shell
curl -fsSL https://signoz.io/foundry.sh | bash
```

创建 `casting.yaml`：

```yaml
apiVersion: v1alpha1
kind: Installation
metadata:
  name: signoz
spec:
  deployment:
    flavor: compose
    mode: docker
```

部署 SigNoz：

```shell
foundryctl cast -f casting.yaml
docker ps
```

部署完成后，默认使用以下端口：

| 端口 | 用途 |
| --- | --- |
| `8080` | SigNoz Web UI |
| `4317` | OTLP/gRPC 数据接收 |
| `4318` | OTLP/HTTP 数据接收，LaxCode 使用此端口 |

浏览器打开 <http://localhost:8080> 完成首次初始化。若 SigNoz 部署在远程主机，
请开放 `8080` 和 `4318` 端口，并将下文的 `localhost` 替换为该主机的 IP 或域名。

> SigNoz v0.130.0 起，仓库中旧的 `deploy/` Docker Compose 安装方式已弃用；
> 新部署建议使用 Foundry。已有旧部署可继续接收 LaxCode 上报的数据。

## 2. 配置 LaxCode

SigNoz 与 LaxCode 二进制运行在同一台主机时，在启动 LaxCode 前设置：

```shell
export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
export OTEL_SERVICE_NAME="laxcode"

./bin/laxcode
```

其中：

- `OTEL_EXPORTER_OTLP_ENDPOINT` 是启用远端上报的必需变量。请填写基础地址，
  不要附加 `/v1/traces`，OpenTelemetry SDK 会自动添加该路径。
- `OTEL_SERVICE_NAME` 可选，默认值为 `laxcode`；它也是 SigNoz 中用于筛选服务的名称。
- 自托管 SigNoz 默认不需要 ingestion key，因此无需配置
  `OTEL_EXPORTER_OTLP_HEADERS`。
- LaxCode 已固定使用 OTLP/HTTP exporter，无需额外设置
  `OTEL_EXPORTER_OTLP_PROTOCOL`。

可通过资源属性标记版本和运行环境：

```shell
export OTEL_RESOURCE_ATTRIBUTES="service.version=dev,deployment.environment.name=local"
```

LaxCode 根据 `OTEL_EXPORTER_OTLP_ENDPOINT` 判断是否启用远端 exporter，因而不要只设置
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`。未设置通用 endpoint 时，span 会继续写入：

```text
${HOME}/.laxcode/sessions/${session_id}/log/tracing.log
```

请求追踪层级如下，`/chat` 与 `/resume` 共用：

```text
chat
├─ agent-assemble
└─ react
   ├─ context-prepare
   ├─ llm-generate
   ├─ tool-exec
   └─ …
```

`chat` 从 HTTP handler 入口计时，包含请求校验、装配、ReAct 和清理。
`agent-assemble` 覆盖技能/MCP 加载、会话恢复和系统提示词初始化；每轮
`context-prepare` 覆盖工具定义准备、token 计数和必要的上下文压缩。

`agent-assemble` 的阶段耗时直接记录为属性，不额外创建子 span：

| 属性 | 范围 |
| --- | --- |
| `laxcode.assemble.repo_init_ms` | 打开 SQLite、设置连接和 schema 检查/迁移 |
| `laxcode.assemble.skills_load_ms` | 技能发现、读取与解析 |
| `laxcode.assemble.mcp_register_ms` | 后端请求使用共享 MCP 连接，只注册工具 |
| `laxcode.assemble.mcp_connect_ms` | 未注入共享连接的独立装配：连接 MCP server、获取和注册工具 |
| `laxcode.assemble.session_restore_ms` | 从数据库读取工作集并恢复会话 |
| `laxcode.assemble.sys_prompt_init_ms` | 构造系统提示词并持久化工作集 |

单位为毫秒，保留小数以区分不足 1ms 的步骤。阶段失败时也会记录已耗时间；
尚未执行或不适用的阶段不填写。阶段耗时不包含失败后的资源清理，也未覆盖
全部对象构造等零散步骤，因此其和不要求等于 span 总耗时。

每个 MCP server 的连接尝试会在 `log/laxcode.log` 写入一条 JSON 日志，
`msg` 为 `mcp_connect`，包含 `chat_id`、`server_name`、`duration_ms`、
`tool_count` 和 `status`（`connected`、`failed` 或 `no_tools`）。失败时还包含
`error`；有有效追踪上下文时附带 `trace_id` 和 `span_id`。耗时覆盖连接与工具
发现，成功但没有工具的连接随后会被关闭。后端在服务启动时建立连接，请求
只复用连接；启动日志的 `chat_id` 为空串，不属于某次 chat。独立装配时，
日志沿用调用上下文的 chat ID。

后端启动连接按 server 名检索，独立装配可按 chat ID 检索：

```shell
jq -c 'select(.msg == "mcp_connect" and .server_name == "ov-mcp-server")' log/laxcode.log
jq -c 'select(.msg == "mcp_connect" and .chat_id == "你的-chat-id")' log/laxcode.log
```

根 span 的 `laxcode.operation` 为 `send` 或 `resume`，`laxcode.request_id`
每次请求独立生成。`laxcode.chat_id` 只在根 span 上记录，并随 user message
写入 SQLite 和 JSONL；resume 沿用该轮输入已有的 ID，不新增 user message。
没有 chat ID 的旧会话恢复时不补造 ID。

根 span 和每个 `llm-generate` 都在首个非空 reasoning/text delta 到达时记录
`first-output` 事件。根 span 的 `laxcode.first_output_ms` 表示入口到首输出的
时间，生成 span 的 `laxcode.ttft_ms` 表示该次模型调用到首输出的时间。
SSE 响应头、start 帧、空 delta 和工具参数不计入；没有输出的调用不填写这两个
耗时属性。这里测量后端收到输出的时间，不包含浏览器接收延迟。

span 总耗时直接使用起止时间，移除了重复的 `laxcode.time_cost_ms` 属性。
OTLP exporter 在服务启动时创建，服务退出时统一 flush/关闭；本地 filetrace
仍按会话落盘，在请求根 span 结束后关闭。

## 3. Docker 网络地址

如果 LaxCode 也运行在容器中，容器内的 `localhost` 指向 LaxCode 容器自身，
需要根据部署方式修改 endpoint：

```shell
# LaxCode 与 SigNoz Collector 位于同一个 Docker network
export OTEL_EXPORTER_OTLP_ENDPOINT="http://otel-collector:4318"

# LaxCode 容器访问宿主机上的 SigNoz（Docker Desktop）
export OTEL_EXPORTER_OTLP_ENDPOINT="http://host.docker.internal:4318"

# SigNoz 位于另一台主机
export OTEL_EXPORTER_OTLP_ENDPOINT="http://<signoz-host>:4318"
```

同一 Docker network 场景中的 `otel-collector` 是示例服务名，请替换为实际的
SigNoz OpenTelemetry Collector Compose 服务名。

## 4. 查看调用瀑布流

启动 LaxCode 并完成至少一次对话，然后：

1. 打开 SigNoz Web UI。
2. 进入 **Traces** 页面并刷新。
3. 使用 `service.name = laxcode` 筛选调用链。
4. 打开一条 trace，查看以 `chat` 为根节点的 span 瀑布流及耗时、token、工具名称等属性。

如果没有看到数据，请先确认 `docker ps` 中 Collector 正常运行、`4318` 已发布，
并检查 LaxCode 进程读取到的配置：

```shell
echo "$OTEL_EXPORTER_OTLP_ENDPOINT"
echo "$OTEL_SERVICE_NAME"
```

官方资料：

- [SigNoz Docker 自托管安装](https://signoz.io/docs/install/docker/)
- [SigNoz 自托管数据接入](https://signoz.io/docs/ingestion/self-hosted/overview/)
- [SigNoz Go OpenTelemetry 接入](https://signoz.io/docs/instrumentation/opentelemetry-golang/)
