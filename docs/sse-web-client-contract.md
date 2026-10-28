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

首页在查询成功且列表为空时，自动调用创建接口创建 `default`，然后选中该项目。
查询失败时不会创建；创建返回 409 时重新查询并复用已存在的 `default`。

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

新建时 `title` 为空字符串，之后可通过重命名接口修改，前端不要依赖自动生成标题。
项目没有会话时，前端展示临时“新对话”，首条消息发送时才创建真实会话。
点击“新会话”则立即调用创建接口并选中返回的会话，允许创建多个空会话。

### 重命名 Session

```http
PATCH /api/sessions/{session_id}?user_id={UUID}
Content-Type: application/json

{"title":"排查登录问题"}
```

`user_id` 可省略；提供时必须与 `X-USERID` 一致。名称去除首尾空白后须为 1–120 个字符。
成功返回 200 和更新后的 Session DTO；非法参数返回 400，不存在或不属于当前用户返回 404。
proxy 在宿主侧更新标题和更新时间，不启动沙箱、不修改会话消息或上下文。
前端列表对长名称使用省略号，悬停显示完整名称。

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
  "has_more": true,
  "can_resume": true
}
```

字段展示规则：

- `can_resume` 表示完整会话的当前持久化工作集尚有未收束的用户轮次，与本页分页范围无关。判定与 `/resume` 一致：存在 user，且末条不是无工具调用、正常结束（`finish_reason` 为空或 `stop`）的 assistant。它不代表当前没有其他请求占用会话，点击恢复时仍由后端检查并发锁。
- 前端使用最新一页的 `can_resume`，历史加载成功且当前未执行时在列表底部展示“继续执行”；不依赖刷新后会丢失的内存错误状态。错误栏已有恢复按钮时不重复展示，服务端明确不允许重试的错误也不添加按钮。
- `system` 消息不会返回。
- `user` 返回 `content`。
- `assistant` 只返回 `content` 和 `reasoning_content`。
- `tool` 只返回 `tool_summary`，不会返回工具执行结果。
- JSON 中带 `omitempty` 的空字段可能不存在，前端应使用空字符串作为默认值。

## 用户配置与引导版本（proxy）

`GET /api/user-config` 使用 `X-USERID` 确定用户，由 proxy 读取该用户的数据库，不启动沙箱。响应设置 `Cache-Control: no-store`：

```json
{
  "user_guide_version": "20261027.1",
  "user_guide_seen_version": null
}
```

`user_guide_version` 是构建注入的只读版本，未记录已读版本时返回 `null`。前端首次加载等待配置，然后比较两个字段，不同则展示引导。旧的已读 Cookie 不再使用。

`PUT /api/user-config` 只更新请求中出现的字段，成功返回 `204`，重复提交相同值无副作用：

```json
{"user_guide_seen_version": "20261027.1"}
```

当前仅允许写入 `user_guide_seen_version`，值必须是非空字符串且不超过 64 字符；未知/只读字段或非法类型返回 `400`。提交版本与当前版本不同时返回 `409` / `GUIDE_VERSION_CHANGED`，不写入。关闭、跳过、开始体验及 Escape 均提交实际展示的版本；保存失败显示重试提示，版本冲突提示刷新页面，不阻止进入主页面。

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

## 6. 提交聊天任务

`POST /chat` 请求体沿用 `{ "session_id": "...", "task": "..." }`，成功返回 HTTP 200 JSON：

```json
{"task_id":"执行任务 UUID","task_status":"waiting"}
```

`POST /api/sessions/{session_id}/resume` 请求体为 `{ "task_id": "上一次失败任务 UUID" }`，创建新任务后返回同样结构；旧任务保持 fail。旧任务必须仍是该 session 最后任务，且当前工作集 can_resume=true。

无任务记录的旧会话可以省略 task_id；一旦有任务记录就必须携带匹配 ID。不同用户分别使用自己的 sessions.db；同项目最多一个 waiting/running 任务。

提交接口不再返回 SSE，不再由 proxy 注入 prepare 帧。前端在提交期间自行显示沙箱准备状态。提交返回的 waiting 是创建时快照，不代表任务在浏览器收到响应时仍在等待。

状态冲突返回 HTTP 409，例如：

```json
{"code":"PROJECT_BUSY","message":"项目正在执行任务，请稍后刷新会话","refresh_after_ms":2000}
```

SESSION_BUSY、TASK_CHANGED、NOTHING_TO_RESUME 也要求前端重新获取会话状态，不自动重发输入。

## 7. 历史与 SSE 订阅

历史接口在原有消息、分页、can_resume 上增加：

```json
{
  "task_id":"最后任务 UUID",
  "task_status":"running",
  "task_error_code":"",
  "task_error_message":""
}
```

没有任务时 task_id/task_status 为 null；空错误字段可以省略。最新历史页和任务信息在同一 DB 事务中读取，旧分页不能覆盖当前任务状态。

`GET /api/tasks/{task_id}/events` 返回 SSE，允许订阅 session 最后一个任务的 waiting/running/success/fail 状态。每个连接独立从任务起点重放，不与其他页面抢消息。

内容帧示例：

```text
event: reasoning
data: {"task_id":"...","event_id":2,"message_seq":4,"delta":"正在分析…"}

event: message
data: {"task_id":"...","event_id":3,"message_seq":4,"delta":"回复内容"}

event: tool_call
data: {"task_id":"...","event_id":4,"message_seq":5,"info":"执行工具…"}

```

start 帧携带 session_id、task_id、event_id；没有对应内容消息时 message_seq 为 0。tool_call 保留可选的 window_token/context_window 快照。

message_seq 在 session 内递增，在 LLM/工具执行前持久预留；未提交的预留号允许留下空洞。event_id 在 task 内递增，重连重复事件须去重。

结束统一使用 DB 驱动的控制事件：

```text
event: task_state
data: {"task_id":"...","task_status":"fail","error_code":"TASK_FAILED","error_message":"执行失败原因","action":"refresh_session"}

```

后端每 2 秒检查 DB 终态。补齐 final_event_id 之前的输出后发送 task_state；不依赖 MQ 中存在 done/error 帧。状态变化、事件缺口或订阅故障发送 refresh_required 后关闭连接。

## 8. 前端恢复与去重

打开页面、刷新和断线重连时：先拉取最新历史，清空临时 SSE 内容和游标，再根据状态订阅。waiting/running 自动订阅；success 展示历史；fail 显示失败原因和可用按钮。无任务记录的旧会话保留 can_resume 判断。

- event.message_seq 小于当前最后消息序号：丢弃。
- 相等且最后消息来自完整历史：丢弃所有该消息增量。
- 相等且最后消息来自 SSE：按任务内 event_id 去重，重复事件也丢弃。
- 更大：创建新的流式消息，并推进游标。
- start/task_state/refresh_required 不按消息序号过滤。

收到终态后刷新历史、上下文统计和会话列表。收到状态冲突后重新拉取数据，不强制浏览器整页刷新，不自动调用 resume。网络中断只重连订阅，不取消执行。

未提交输入保存在按 user_id/session_id 隔离的 localStorage 中，包含 text、发送前最后已保存消息的 base_message_seq，以及接收成功后补充的 task_id。只有确认对应 user 消息已保存才清理。没有活跃任务、历史仍未越过基线时，在本地用户消息旁提供“重试发送”；can_resume 对应的是继续已保存的用户轮次。

## 9. 主动停止与服务生命周期

`POST /api/tasks/{task_id}/cancel` 请求停止后台任务。任务真正退出前仍占用项目；退出后状态为 fail，error_code=USER_STOPPED。断开 SSE 不调用该接口。

cloud 任务使用服务 context，与提交请求解耦；waiting/running 和收尾过程计入 /internal/activity，防止关闭页面后被空闲回收。cloud 重启在接受请求前把其遗留活跃任务标为 fail（SERVICE_RESTARTED）；proxy 单独重启不改变任务状态。

实现编排、MQ 存储、恢复边界和本轮限制见 [异步聊天任务设计](async-chat-task-design.md)。

### 本轮变更文件预览与下载

历史消息的 `upsert_files` 列出该轮新增 / 修改的文件。前端将 PNG、JPEG、GIF、WebP、BMP
展示为缩略图，进入可见区域才请求原图；悬停或点击可放大。文件总数支持悬停展开和点击固定列表，
列表包含图片，悬停或点击文件名时请求预览，每个文件均可单独下载。

- `GET /api/sessions/{session_id}/chats/{chat_id}/file?path=...`：预览当前文件；文本最多 256 KiB，图片最多 10 MiB。
- 同一接口增加 `download=1`：完整下载该文件（最多 100 MiB），不受文本预览截断或预览格式限制。
- `GET /api/sessions/{session_id}/chats/{chat_id}/files.zip`：批量下载，仍限制 200 个文件、总计 100 MiB。

所有读取都限定于该用户、该轮文件清单和项目目录。下载均返回 `Content-Disposition: attachment`；
ZIP 名为 `会话名_yyyyMMdd_HHmm.zip`，时间采用下载时的上海时间，空标题使用“未命名会话”，
名称中的非法字符会替换，过长名称会截短。前端使用响应头提供的文件名。
预览和下载均读取文件当前版本，不是历史快照；这些接口不会唤醒沙箱。
