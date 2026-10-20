# SSE Web 客户端接口契约与交互流程

本文档描述 React Web 客户端接入 `cmd/run_sse` 服务时使用的接口、SSE 事件和推荐交互流程。

## 0. 前端工程目录约定

React 前端与 Go 后端放在同一个仓库中，前端根目录固定为 `web/`：

```text
LaxCode/
├── cmd/
├── internal/
├── docs/
│   └── sse-web-client-contract.md
├── web/
│   ├── public/                     # 不经构建处理的静态资源
│   ├── src/
│   │   ├── api/
│   │   │   ├── client.ts           # HTTP 基础封装和统一错误类型
│   │   │   ├── sessions.ts         # 新建、分页查询 Session
│   │   │   ├── history.ts          # 历史消息分页查询
│   │   │   └── chat-stream.ts      # POST /chat 与 SSE 帧解析
│   │   ├── components/
│   │   │   ├── session-tabs/       # Session 标签、切换和新建按钮
│   │   │   ├── message-list/       # 历史区、滚动锚点、消息分组和加载更早消息
│   │   │   ├── message-item/       # user/assistant 消息展示
│   │   │   ├── tool-message-block/ # 同一 assistant 轮次的 tool 消息标签块（可展开/折叠）
│   │   │   ├── reasoning-panel/    # reasoning_content 折叠区
│   │   │   └── chat-composer/      # 输入框、发送和取消按钮
│   │   ├── features/
│   │   │   ├── identity/           # 游客 UUID Cookie
│   │   │   ├── sessions/           # Session 查询、选择和分页状态
│   │   │   └── chat/               # 流式消息 reducer 与对账流程
│   │   ├── hooks/                   # 页面级复用 hooks
│   │   ├── types/
│   │   │   ├── api.ts              # 后端 DTO 和 SSE 事件类型
│   │   │   └── chat.ts             # 统一 ChatMessage ViewModel
│   │   ├── app/                     # QueryClient、全局 Provider 和 App
│   │   ├── main.tsx
│   │   └── index.css
│   ├── index.html
│   ├── package.json
│   ├── tsconfig.json
│   ├── vite.config.ts
│   └── eslint.config.js
├── go.mod
└── Makefile
```

目录职责约束：

- `api/` 只负责网络协议和 DTO，不持有 React UI 状态。
- `types/api.ts` 与本文档中的 JSON 契约保持一致；不要让组件直接依赖原始 SSE 帧。
- `features/chat/` 将历史 DTO 和 SSE 事件转换为统一的 `ChatMessage`。
- `components/` 只通过 props 或 feature hooks 使用数据，不直接调用 `fetch`。
- Session 列表和历史分页属于服务端状态；当前 SSE 流属于临时客户端状态。
- 测试文件放在对应模块旁边，统一使用 `*.test.ts` 或 `*.test.tsx`，不另建按技术类型划分的大型测试目录。

### 本地开发

Vite 开发服务器使用 `5173`，通过代理访问 Go SSE 服务，避免开发环境 CORS 和 Cookie 跨域问题：

```ts
// web/vite.config.ts
import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "LAXCODE_");
  const backend = env.LAXCODE_PROXY_TARGET || "http://127.0.0.1:8090";

  return {
    plugins: [react()],
    server: {
      host: "127.0.0.1",
      port: 5173,
      strictPort: true,
      proxy: {
        "/api": backend,
        "/chat": backend,
        "/healthz": backend,
      },
    },
  };
});
```

`cloud.sh` 构建前端和两个 Go 程序，以 `127.0.0.1:0` 启动后端，
从 `.laxcode/sse-code.instance` 读取实际地址，再通过 `laxcode-web` 代理 API 和 SSE。

前端 API 地址始终使用相对路径，例如 `/api/sessions` 和 `/chat`，不要在组件中硬编码主机或端口。

### 构建与生产部署

```bash
cd web
npm run build
```

构建产物固定输出到 `web/dist/`，通过 Go `embed` 嵌入 Web 程序。
`make build-cloud` 生成 Linux amd64 离线部署包；`web/dist/` 不提交到 Git。

## 1. 客户端身份

未登录用户由前端生成 UUID，并长期保存在 Cookie 中。只要 Cookie 未被清除，同一浏览器配置会继续使用同一个游客 ID。

```ts
const COOKIE_NAME = "laxcode_guest_id";

export function getOrCreateGuestID(): string {
  const prefix = `${COOKIE_NAME}=`;
  const stored = document.cookie
    .split("; ")
    .find((item) => item.startsWith(prefix))
    ?.slice(prefix.length);
  if (stored) return decodeURIComponent(stored);

  const id = crypto.randomUUID();
  document.cookie = [
    `${COOKIE_NAME}=${encodeURIComponent(id)}`,
    "Path=/",
    "Max-Age=31536000",
    "SameSite=Lax",
    location.protocol === "https:" ? "Secure" : "",
  ].filter(Boolean).join("; ");
  return id;
}
```

`user_id` 和新建 Session 返回的 `session_id` 都是 UUID。

所有业务接口必须携带 `X-USERID` UUID 请求头。proxy 将请求转发到该用户的独立沙箱，cloud 校验身份并只访问该用户数据库；UUID 仍不是登录凭据。

## 2. 前端统一消息模型

历史接口 DTO 和 SSE 增量事件不是同一种结构。建议在进入 React 渲染层之前统一成 ViewModel：

```ts
export type ChatMessage = {
  id: string;
  seq?: number;
  role: "user" | "assistant" | "tool";
  content: string;
  reasoningContent?: string;
  toolSummary?: string;
  createdAt: string;
  status: "streaming" | "completed" | "error";
};
```

历史数据属于服务端状态，推荐使用 TanStack Query 管理；当前 SSE 流属于临时状态，可以使用 `useReducer` 或 Zustand 管理。

## 3. 项目与 Session

### 新建项目

```http
POST /api/projects
Content-Type: application/json
```

```json
{
  "user_id": "11111111-1111-4111-8111-111111111111",
  "name": "LaxCode"
}
```

服务端在当前用户根下创建项目目录；名称限制为 1–120 个英文字母、数字、中横线或下划线，
`agenthome` 与 `tmp` 为保留名称，重名返回 409。成功返回 `201 Created`，响应包含
`project_id`、`user_id`、`name`、`work_dir`、`created_at` 和 `updated_at`。

### 查询项目

```http
GET /api/projects?user_id={UUID}
```

响应为 `{"projects": [...]}`，项目按更新时间倒序排列。

### 新建 Session

### 请求

```http
POST /api/sessions
Content-Type: application/json
```

```json
{
  "user_id": "11111111-1111-4111-8111-111111111111",
  "project_id": "5ec09acd-c646-40cd-b364-ccfbe182fa09"
}
```

`user_id` 必须是合法 UUID，`project_id` 必须属于该用户。Session 的 `work_dir`
由服务端从项目读取，不接受客户端覆盖。请求体包含未知字段时返回 `400`。

### 成功响应

状态码：`201 Created`

```json
{
  "session_id": "97e310f4-b757-427a-92f4-d2d88956d54a",
  "user_id": "11111111-1111-4111-8111-111111111111",
  "project_id": "5ec09acd-c646-40cd-b364-ccfbe182fa09",
  "title": "",
  "work_dir": "/absolute/path/to/LaxCode",
  "created_at": "2026-09-20T01:02:03Z",
  "updated_at": "2026-09-20T01:02:03Z"
}
```

`title` 当前固定为空字符串，前端不要依赖自动生成标题。

## 4. 查询 Session 列表

### 请求

```http
GET /api/sessions?user_id={UUID}&project_id={PROJECT_ID}&limit=20
```

继续加载更早的 Session：

```http
GET /api/sessions?user_id={UUID}&project_id={PROJECT_ID}&limit=20&before_session_id={上一页的next_before_session_id}
```

- `user_id`：兼容字段，可省略；提供时必须与 `X-USERID` 一致。
- `project_id`：必填，只返回该项目下的 Session。
- `limit`：可选，默认 `20`，范围 `1..100`。
- `before_session_id`：可选，排他游标。
- 结果按 `updated_at DESC, session_id DESC` 排列，即最近活动的 Session 在前。

### 响应

```json
{
  "sessions": [
    {
      "session_id": "97e310f4-b757-427a-92f4-d2d88956d54a",
      "user_id": "11111111-1111-4111-8111-111111111111",
      "project_id": "5ec09acd-c646-40cd-b364-ccfbe182fa09",
      "title": "",
      "work_dir": "/absolute/path/to/LaxCode",
      "created_at": "2026-09-20T01:02:03Z",
      "updated_at": "2026-09-20T01:05:03Z"
    }
  ],
  "next_before_session_id": "97e310f4-b757-427a-92f4-d2d88956d54a",
  "has_more": true
}
```

`has_more=false` 时不再请求下一页，且 `next_before_session_id` 可能不存在。

### 删除 Session

```http
DELETE /api/sessions/{session_id}?user_id={UUID}
```

`session_id` 和 `user_id` 都必须是 UUID。只能删除属于该用户的会话；成功返回
`204 No Content`，不存在或不属于该用户时返回 `404`。

删除覆盖 `request_contexts`、该会话所有 original / memory generation 消息，
以及整个会话目录（history 冷备、artifact、trace 日志和规划文件）。文件清理失败
时回滚数据库删除并返回 `500`。项目及工作目录中的代码文件保持不变；独立的
已结束评估记录、报告和项目内评估快照仍保留。

聊天执行中、正在创建评估或有排队/执行中的评估任务时返回 `409`，等待完成后
再删除。前端在删除成功后清理历史与上下文缓存；删除选中会话后选中同项目下的
其他会话，若没有剩余会话则展示空聊天页，不自动创建真实 Session。

## 5. 查询历史消息

### 请求

```http
GET /api/sessions/{session_id}/messages?limit=50
```

向上滚动加载更早消息：

```http
GET /api/sessions/{session_id}/messages?limit=50&before_seq={上一页的next_before_seq}
```

- `limit`：可选，默认 `50`，范围 `1..100`。
- `before_seq`：可选，必须是正整数，是排他游标。
- Session 不存在时返回 `404`。
- 响应中的消息按 `seq` 正序排列。
- 第一页是最新的一页；更早页面应插入现有消息数组头部，并保持滚动位置。

### 响应

```json
{
  "messages": [
    {
      "seq": 8,
      "role": "user",
      "content": "运行测试",
      "created_at": "2026-09-20T01:02:03Z"
    },
    {
      "seq": 9,
      "role": "assistant",
      "content": "我先检查项目。",
      "reasoning_content": "需要确认测试入口。",
      "created_at": "2026-09-20T01:02:04Z"
    },
    {
      "seq": 10,
      "role": "tool",
      "tool_summary": "bash(go test ./...)",
      "created_at": "2026-09-20T01:02:05Z"
    }
  ],
  "next_before_seq": 8,
  "has_more": true
}
```

字段展示规则：

- `system` 消息不会返回。
- `user` 返回 `content`。
- `assistant` 只返回 `content` 和 `reasoning_content`。
- `tool` 只返回 `tool_summary`，不会返回工具执行结果。
- JSON 中带 `omitempty` 的空字段可能不存在，前端应使用空字符串作为默认值。

## 5.1 平台模型目录与用户选择

`GET /api/models` 返回管理员预置的当前模型和可切换目录，不返回 API Key 或 Base URL。
`POST /api/model` 通过 `provider`、`model` 和可选 `reasoning_effort` 切换；选择持久化到当前用户目录。
`POST /api/models` 已移除（返回 405），用户不能新增模型或凭据。平台模型目录由部署配置生成，只读挂载到每个沙箱。

## 5.2 异步评估任务

创建当前 coding session 的评估任务：

```http
POST /api/evaluations
Content-Type: application/json
```

```json
{
  "session_id": "97e310f4-b757-427a-92f4-d2d88956d54a",
  "requirement": "重点检查测试覆盖和用户验收条件"
}
```

请求仅传 `session_id` 和 `requirement`，工作目录从源 Session 读取，独立 Judge
Session 也使用该工作目录。后端先获取源 Session 锁，再获取项目锁；源会话或项目忙、
已有 `queued/running` 评估时返回 `409`，不创建 job。同一源 Session 同时只运行一个评估任务。

创建 `queued` job 后，在请求内把源会话目录快照到 `${work_dir}/eval/${session_id}`，
并完成独立 Judge Session 的装配；Session ID 由会话层生成。初始化失败或请求取消时，
将 job 标记为 `failed` 并返回错误。初始化成功、确认请求未取消并登记后台任务后，
返回 `202 Accepted` 和 `queued` job；前端在等待响应期间显示“评估任务初始化中…”。
源 Session 和项目锁仅持有到初始化完成，后台模型执行使用服务级 context，
把最终 Markdown 报告写入 `${HOME}/.laxcode/eval/`。

查询当前源 Session 的全部评估任务：

```http
GET /api/evaluations?session_id={session_id}
```

```json
{
  "jobs": [{
    "id": "eval_97e310f4-b757-427a-92f4-d2d88956d54a_20260927_101530_000000000",
    "source_session_id": "97e310f4-b757-427a-92f4-d2d88956d54a",
    "requirement": "重点检查测试覆盖和用户验收条件",
    "status": "succeeded",
    "report_path": "/home/user/.laxcode/eval/eval_97e310f4-b757-427a-92f4-d2d88956d54a_20260927_101530_000000000.md",
    "created_at": "2026-09-27T10:15:30Z",
    "updated_at": "2026-09-27T10:18:00Z",
    "completed_at": "2026-09-27T10:18:00Z"
  }]
}
```

状态为 `queued | running | succeeded | failed`。响应省略 `report_path` 字段，并隐藏 `error` 中的
UserRoot 前缀。前端按 `status == succeeded` 显示下载按钮，不依赖报告路径；
仅在存在活跃状态时轮询。

下载完整评估报告：

```http
GET /api/sessions/{session_id}/evaluations/{job_id}/report
```

Cloud 用户身份必须通过 `X-USERID` 请求头提供。浏览器从访客 Cookie 读取 UUID 并设置请求头。
旧接口中的 `user_id` 可省略，提供时必须与请求头一致。后端在当前用户数据库中校验源 Session、评估任务和归属关系，
仅允许下载 `succeeded` 任务；根据 Job ID 在服务端拼接固定报告目录和文件名，
不接受客户端文件路径，也不使用数据库中的绝对 `report_path` 读取文件。

成功返回 `text/markdown; charset=utf-8`，设置 `Content-Disposition: attachment`
及文件名 `{job_id}.md`、`Cache-Control: no-store`、`X-Content-Type-Options: nosniff`。
报告完整传输，不应用文件预览接口的 256 KiB 截断限制。通过 `os.Root` 限制读取
范围，仅允许普通文件，拒绝报告文件符号链接及目录越界。

源 Session、评估任务或报告文件不存在时返回 `404`；任务未成功时返回 `409`；
非法 ID 返回 `400`；目录越界或文件不允许读取时返回 `403`。前端显示下载中及
失败提示，出错时不会把 JSON 错误正文保存成 Markdown。

## 6. 发起对话和接收 SSE

### 请求

```http
POST /chat
Content-Type: application/json
Accept: text/event-stream
```

```json
{
  "session_id": "97e310f4-b757-427a-92f4-d2d88956d54a",
  "task": "运行项目测试"
}
```

Web 前端先创建项目并展示空聊天页，延迟到用户首次发送消息时调用
`POST /api/sessions`，再使用返回的 `session_id` 发起对话。`/chat` 要求
`session_id` 必填；空聊天页不显示 session_id，也不请求会话历史或上下文。
模型切换接口不依赖 Session，空聊天页仍可切换服务当前使用的模型。

这是 POST SSE，不能直接使用浏览器原生 `EventSource`。使用 `fetch` 读取 `Response.body`，或使用支持 POST 的 SSE 客户端库。SSE 帧可能跨多个网络 chunk，不能把每个 `ReadableStream` chunk 当作一条完整事件。

### 流开始前的 HTTP 错误

- proxy 身份校验失败、请求体读取失败或超过 1 MiB 时返回 `400`。
- proxy 全局对话并发达到上限时返回 `429`，保留 `SERVER_BUSY` 和 `retry_action`。
- `500`：服务端不支持流式响应等启动错误。

收到响应后应先检查 HTTP 状态和 `Content-Type`。进入 `text/event-stream` 后，后续业务错误通过 `event: error` 表达，HTTP 状态仍然是 `200`。
proxy 发送 `prepare` 后，启动失败和沙箱返回的 HTTP 错误（包括 `400/409`）均转换为
SSE `error`，保留上游 `code`、`message` 和 `retry_action`。proxy 启动失败使用
`SANDBOX_UNAVAILABLE`，对 `/chat` 提示 `resend`，对 `/resume` 提示 `resume`；
未知上游响应使用 `UPSTREAM_ERROR`。直连沙箱时，流开始前仍可能收到原始 HTTP 错误。

### SSE 事件

#### prepare

```text
event: prepare
data: {}
```

proxy 在用户身份校验、全局并发限流和有界读取请求体后立即发送并 Flush，
早于等待用户生命周期锁、启动沙箱和转发请求。前端显示“正在进入您的专属安全沙箱…”，
使用循环三点动效；不创建聊天消息，不写入历史、SQLite 或浏览器持久化存储。

#### start

```text
event: start
data: {"session_id":"97e310f4-b757-427a-92f4-d2d88956d54a"}
```

装配完成后发送。显式创建 Session 时应与请求中的 ID 一致。
前端只把等待提示改为“已进入您的专属安全沙箱，正在准备回复…”，保持同一个动效。
收到首个包含非空白内容的 `reasoning` / `message`，或收到 `tool_call` 时收起提示；
`error`、取消、断流和 `done` 也会收起提示并停止动效。该状态只保留在当前页面的会话内存中。

#### reasoning

```text
event: reasoning
data: {"delta":"先检查测试入口"}
```

将 `delta` 追加到当前 assistant 消息的 `reasoningContent`。UI 可默认折叠，但流式更新期间仍需持续保存内容。

#### message

```text
event: message
data: {"delta":"测试已经通过。"}
```

将 `delta` 追加到当前 assistant 消息的 `content`。

#### tool_call

```text
event: tool_call
data: {"info":"bash(go test ./...)"}
```

新增一条临时 tool 消息，将 `info` 映射到 `toolSummary`。后端执行完成后，同一摘要会持久化并由历史接口返回。

#### done

```text
event: done
data: {
  "session_id": "97e310f4-b757-427a-92f4-d2d88956d54a",
  "result": "测试已经通过。",
  "token_used": {
    "token_input": 100,
    "token_output": 20
  },
  "window_token": {
    "token_input": 80,
    "token_output": 20
  }
}
```

`result` 是本轮最终 assistant 结果。前端已通过 `message` 增量构建正文时，不要再次追加 `result`；可以用它校验或覆盖最终正文。

#### error

```text
event: error
data: {
  "code":"CHAT_FAILED",
  "message":"error description",
  "retry_action":"resume"
}
```

标记当前流失败并停止等待。失败后仍应刷新历史，因为 user 消息或部分执行消息可能已经持久化。

- `code` 是稳定的业务错误码，前端不得解析 `message` 判断行为。
- `retry_action=resume` 表示本轮 user 消息已经持久化，应调用恢复接口。
- `retry_action=resend` 表示本轮 user 消息尚未持久化，应使用原 task 重新调用 `/chat`。
- `retry_action` 缺失表示错误不可自动重试，只展示错误信息。

当前业务错误码包括 `INVALID_REQUEST`、`SESSION_BUSY`、
`AGENT_ASSEMBLY_FAILED`、`CHAT_FAILED`、`RESUME_FAILED`、
`NOTHING_TO_RESUME` 和 `INTERNAL_ERROR`。

## 6.1 恢复未完成对话

当错误帧返回 `retry_action=resume` 时调用：

```http
POST /api/sessions/{session_id}/resume
Accept: text/event-stream
```

请求不携带 task。后端从已持久化工作集执行 `recoverBeforeChat`，随后继续 ReAct
推理，不会追加新的 user 消息。响应复用 `/chat` 的 `start`、`reasoning`、
`message`、`tool_call`、`done` 和 `error` 事件。

如果会话没有待恢复的对话，返回 SSE 错误：

```text
event: error
data: {"code":"NOTHING_TO_RESUME","message":"reactservice: no interrupted chat to resume"}
```

前端收到该错误后不应继续自动重试，避免已经收束的会话重复生成。

## 7. 实时消息归并规则

一次用户请求可能产生以下持久化序列：

```text
user
assistant(reasoning/content + tool calls)
tool
assistant(reasoning/content + tool calls)
tool
assistant(final)
```

推荐 reducer 行为：

1. 发送请求前，立即插入一条 `status=completed` 的本地 user 消息。
2. 收到首个 `reasoning` 或 `message` 时，创建 `status=streaming` 的 assistant 消息。
3. 收到 `tool_call` 时，将当前 assistant 消息标记为 `completed`（reasoning 已结束），保留该消息并在其后插入 tool 消息；SSE 整体仍保持运行。
4. tool 之后再次收到 `reasoning` 或 `message` 时，创建下一条 assistant 消息，不要继续追加到工具调用前的 assistant 消息。
5. 收到 `done` 时，将最后一条 streaming assistant 标记为 completed。
6. 收到 `error` 时，将 streaming assistant 标记为 error。

SSE 没有服务端 `seq` 和 `created_at`，因此实时消息使用临时 ID 和浏览器时间。

## 8. 完成后的服务端对账

历史数据和实时数据建议分开保存：

```ts
const visibleMessages = [...historyMessages, ...streamingMessages];
```

收到 `done` 或 `error` 后：

1. 暂时保留 streamingMessages，避免界面闪烁。
2. 重新请求当前 Session 的最新历史页。
3. 历史请求成功后清空 streamingMessages。
4. 使 Session 列表缓存失效，使刚对话的 Session 回到列表顶部。

不要直接使用正文去重；相同正文可以合法出现多次。以历史接口返回的 `seq` 作为持久化消息唯一标识，临时消息使用独立 UUID。

## 9. 页面完整交互流程

```text
页面启动
  → 读取或创建游客 UUID Cookie
  → GET /api/projects?user_id=...
  → 对每个项目 GET /api/sessions?user_id=...&project_id=...
  → 有 Session 时选中已有会话，读取历史并渲染
  → 项目中没有 Session 时展示空聊天页

用户发送问题
  → 若当前为空聊天页，先 POST /api/sessions（携带 project_id）
  → 创建成功后选中真实 Session；创建失败保留原消息并提供重试
  → 本地插入 user 消息并禁用重复提交
  → POST /chat，fetch 读取 SSE
  → reasoning/message/tool_call 增量更新临时消息
  → done/error 结束当前流
  → 刷新历史消息和 Session 列表
  → 服务端历史到达后移除临时消息

用户向上滚动
  → has_more=true 时携带 next_before_seq 请求更早历史
  → 将返回消息插到列表头部
  → 恢复加载前的视觉滚动位置

用户新建项目
  → POST /api/directory-picker
  → 在 Go 服务所在的设备（Mac / Windows）上选择目录
  → 输入项目名称
  → POST /api/projects
  → 刷新项目列表并选中新项目，展示空聊天页

用户在项目中新建会话
  → 展示该项目的空聊天页，等待用户输入
  → 首次发送时才创建真实 Session

用户删除会话
  → 确认后 DELETE /api/sessions/{id}?user_id=...
  → 清理列表、历史和上下文缓存
  → 删除选中会话时选择同项目的其他会话；没有剩余会话则展示空聊天页
```

## 10. 取消与并发

- 每个 Session 同时只允许一轮 `/chat`；并发请求返回 `409`。
- 前端发送期间应禁用该 Session 的发送按钮，但其他 Session 可以并行对话。
- 使用 `AbortController` 可以中断浏览器请求；连接断开会取消服务端当前请求。
- 切换 Session 时可以让后台流继续并按 Session 保存状态，也可以显式 abort；两种策略必须保持一致。
- `done` 或 `error` 后必须释放发送中状态，避免输入框永久锁定。
