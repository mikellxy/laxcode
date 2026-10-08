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
        { "model_name": "summary" }
      ]
    }
  ]
}
```

`provider_name` 和同一 provider 内的 `model_name` 必须分别唯一。`model` 必须指向配置中的现有条目。

## 使用 ChatGPT 订阅

在 Web 页面点击模型旁的齿轮，将认证方式切换为「ChatGPT 订阅」，再点击
**Continue with ChatGPT**。在浏览器中完成登录和订阅使用授权后，后端会导入
此账户可用的模型，前端通过原有模型列表和切换接口进行选择。API Key 模型
仍可同时保留并切换。当前保存一个 ChatGPT 账户连接；再次登录用于续授权。

授权回调监听本机 `127.0.0.1` 的随机空闲端口，不占用 Codex 的固定登录端口。
登录有效期为 10 分钟；关闭添加窗口会取消待完成的登录。浏览器和后端应在
同一台电脑运行，因为授权回调使用本机地址。

登录自动生成的 provider 配置形如：

```json
{
  "provider_name": "openai-chatgpt",
  "auth_type": "oauth",
  "credential_ref": "chatgpt-main",
  "openai_base_url": "https://api.openai.com/v1/",
  "model_list": [
    { "model_name": "gpt-6.1-sol", "display_name": "GPT-6.1 Sol", "reasoning_effort": "medium" }
  ]
}
```

模型清单以登录账户返回的目录为准。支持的模型会显示独立的「思考强度」
选择器；目前 GPT-6.1 Sol 可选 `low`、`medium`、`high`、`xhigh`、`max`。
其他模型保留上游默认强度。模型条目的 `reasoning_effort` 保存默认值，页面
选择只影响当前进程；切换模型会采用目标模型的默认值，正在进行的对话结束
后才生效。未指定压缩模型时，它同时继承主模型的认证方式和思考强度。

OAuth 凭证单独保存在 `~/.laxcode/auth.json`，原子写入（Unix 权限 `0600`）。
后端在请求前刷新即将过期的 access token，并串行保存轮换后的
refresh token；凭证不通过模型列表返回，也不写进 `settings.json`。

此路线使用公共 Responses API 的流式请求。`limit.output` 仍用于本地上下文
预算，OAuth 请求不会发送不受支持的 `max_output_tokens`，因此它不是远端
输出硬上限。Token 计数使用本地估算；上下文摘要也通过 OAuth 流式请求聚合。
会话数据库会自动增加加密 reasoning 字段，以保存工具续轮需要的 opaque 状态。

符合资格的 Plus 用户可授权使用订阅额度；可用性取决于账户、工作区和政策，
Plus 的五小时额度在使用该订阅的应用间共享。额度或权限错误会直接返回，
不会自动改用 API Key 计费。详见
[官方接入文档](https://developers.openai.com/siwc/token-sharing-open-source) 和
[订阅用量说明](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)。

---

## 模型 Token 预算

> [!TIP]
> 主模型和压缩模型分别使用所选模型条目中的 `limit.context` 与 `limit.output`。

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
> `mcp_servers` 声明外部 MCP（Model Context Protocol）server；coding agent装配时接入其工具，工具以 `mcp__<server>__<tool>` 命名注册给模型

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
- 后端在启动时连接 MCP server，多个 chat 共享连接，各 Agent 使用独立工具注册表；请求结束不会关闭连接，服务退出时统一关闭。
- 单个 server 连接失败不会导致启动或会话失败：该 server 被跳过并在 stderr 告警（fail-open）。启动仍需等待连接尝试完成。
- 配置在服务启动时读取，修改后需要重启。暂不自动重连；启动时连接失败或运行中连接断开后，重启服务恢复。
- 注意：stdio MCP server 以子进程运行，其行为不受 laxcode 工作目录沙箱约束；HTTP MCP server 会收到配置的凭据和模型发起的工具调用。仅配置你信任的 server，且不要提交真实 Token。
