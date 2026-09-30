# 配置文件使用说明

配置模板：[settings.json](./settings.json)。将模板复制到 `~/.laxcode/settings.json`，再替换模型名、API Key 和服务地址：

```shell
mkdir -p ~/.laxcode
cp docs/settings.json ~/.laxcode/settings.json
```

配置键使用小写 `snake_case`。请填写实际支持 OpenAI 兼容接口的服务及模型。不要将填入真实密钥的文件提交到仓库。`settings.json` 是一个完整的 JSON 对象，下面的代码块按功能拆开说明。

---

## 主模型与模型目录

> [!TIP]
> `model` 选中主模型，用于处理 agent 循环，格式为 `provider_name:model_name`

```json
{
  "model": "example:chat",
  "provider_list": [
    {
      "provider_name": "example",
      "openai_api_key": "YOUR_API_KEY",
      "openai_base_url": "https://api.example.com/v1",
      "model_list": [
        { "model_name": "chat" },
        { "model_name": "embedding" },
        { "model_name": "summary" }
      ]
    }
  ]
}
```

`provider_name` 和同一 provider 内的 `model_name` 必须分别唯一。`model` 必须指向配置中的现有条目。

---

## 模型 Token 预算

> [!TIP]
> 主模型、向量化模型和压缩模型分别使用所选模型条目中的 `limit.context` 与 `limit.output`。

```json
{
  "provider_list": [
    {
      "provider_name": "example",
      "openai_api_key": "YOUR_API_KEY",
      "openai_base_url": "https://api.example.com/v1",
      "model_list": [
        {
          "model_name": "chat",
          "limit": { "context": 128000, "output": 8192 }
        },
        {
          "model_name": "embedding",
          "limit": { "context": 128000, "output": 8192 }
        },
        {
          "model_name": "summary",
          "limit": { "context": 128000, "output": 8192 }
        }
      ]
    }
  ]
}
```

`limit.context` 和 `limit.output` 都必须大于 0，且 `output` 必须小于 `context`。

---

## 向量化模型与 agentic RAG

> [!TIP]
> `embedding_model` 指定向量化模型；`embedding_vec_dim` 是向量维度。制作知识库和启动 agentic RAG 保持配置相同以获取最佳召回效果

```json
{
  "embedding_model": "example:embedding",
  "embedding_vec_dim": 1024
}
```

向量维度填写 JSON 整数，范围为 `1`～`8192`。

---

## 压缩模型

> [!TIP]
> `compaction_model` 指定用于上下文压缩的模型；不填时继承主模型。

```json
{
  "compaction_model": "example:summary"
}
```

---

## 本地路由器

> [!TIP]
> 路由器默认监听本机随机端口用于模型热切换

```json
{
  "llm_router_addr": "127.0.0.1:0"
}
```

---

## MCP server（外部工具生态）

> [!TIP]
> `mcp_servers` 声明外部 MCP（Model Context Protocol）server；code 模式（`-sse -mode code` 或交互 CLI）装配时接入其工具，工具以 `mcp__<server>__<tool>` 命名注册给模型

```json
{
  "mcp_servers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/absolute/path/to/allow"],
      "env": ["NODE_ENV=production"]
    }
  }
}
```

Streamable HTTP server 可通过 `url` 接入，并用 `headers` 传递 Bearer Token
等认证信息：

```json
{
  "mcp_servers": {
    "ov-mcp-server": {
      "url": "https://api.vikingdb.cn-beijing.volces.com/openviking/mcp",
      "headers": {
        "Authorization": "Bearer xx.xx"
      }
    }
  }
}
```

- 每个已启用条目必须声明且仅声明一种传输形态：`command`（stdio）或 `url`（Streamable HTTP）。
- `env` 是追加给子进程的环境变量，`K=V` 字符串数组（大小写敏感，后设置覆盖先设置）。
- `headers` 仅用于 HTTP server，并会注入握手、工具调用、SSE 接收与连接关闭请求；敏感 header 不会转发到跨源重定向地址。
- 顶层推荐使用 LaxCode 风格的 `mcp_servers`；同时兼容 MCP 生态常见的 `mcpServers` 写法。
- `enabled` 缺省为 true（写配置即启用）；设为 false 可保留声明但暂停接入。
- 单个 server 连接失败不会阻塞启动或会话：该 server 被跳过并在 stderr 告警（fail-open）。
- 注意：stdio MCP server 以子进程运行，其行为不受 laxcode 工作目录沙箱约束；HTTP MCP server 会收到配置的凭据和模型发起的工具调用。仅配置你信任的 server，且不要提交真实 Token。
