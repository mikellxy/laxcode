# Cloud Session 性能汇总

接口直接分析当前用户 Session 的本地 `tracing.log`，不写入数据库，也不返回原始
日志、提示词、工具参数或宿主机路径。仅 Cloud 模式可用。

Cloud 页面选中会话后，可点击左侧 Context 区块下的「性能监控」打开汇总弹窗。
弹窗按 Chat 展示总耗时、首次请求首输出、LLM 和工具调用统计；展开「请求明细」
可查看 send/resume 各自的耗时和 Trace ID。点击「刷新数据」重新读取日志，关闭
弹窗后取消尚未完成的请求；不自动轮询。未选择会话时按钮禁用，本地模式不显示。

```http
GET /api/sessions/{session_id}/performance?user_id={user_id}
```

也可以用 `X-Laxcode-User-ID` 请求头或已有访客 Cookie 提供用户身份，与现有
Cloud 接口一致；同时提供的身份必须一致。Session 必须存在于该用户的数据库中。

```sh
curl --fail-with-body \
  'http://127.0.0.1:5173/api/sessions/c113bd38-a82d-43f3-adcd-c90dca7bccb9/performance?user_id=94f4ba61-ba3d-416c-8b55-2ce315dbf7ca'
```

返回示例（耗时均为毫秒，最多保留三位小数）：

```json
{
  "session_id": "c113bd38-a82d-43f3-adcd-c90dca7bccb9",
  "chats": [
    {
      "chat_id": "44916348-6932-412a-810c-c7396f2cccf6",
      "total_ms": 409402.078,
      "llm_generate": {"count": 10, "total_ms": 399193.475, "avg_ms": 39919.348},
      "tools": {
        "bash": {"count": 4, "total_ms": 9394.174, "avg_ms": 2348.543}
      },
      "requests": [
        {
          "trace_id": "18b9fb7cbf7908fb3323ffe0b5b7215e",
          "request_id": "request-id",
          "operation": "send",
          "start_time": "2026-10-16T04:00:00Z",
          "end_time": "2026-10-16T04:06:49.402078Z",
          "status": "completed",
          "total_ms": 409402.078,
          "first_output_ms": 10357,
          "llm_generate": {"count": 10, "total_ms": 399193.475, "avg_ms": 39919.348},
          "tools": {
            "bash": {"count": 4, "total_ms": 9394.174, "avg_ms": 2348.543}
          }
        }
      ]
    }
  ],
  "partial": false,
  "invalid_line_count": 0,
  "incomplete_trace_count": 0
}
```

## 统计口径

- `chats` 按首次请求开始时间排序，`requests` 保留同一 Chat 的 send/resume 明细。
- `total_ms` 使用 Chat 根 span 的耗时；同一 Chat 多次请求时相加，不包含两次请求
  间等待用户操作的时间，也不重复累加嵌套 span。
- `first_output_ms` 是每次请求开始至首次非空推理或正文输出的耗时，沿用现有埋点。
  不包括工具参数输出；无首输出记录时返回 `null`，与 `0` 区分。
- `llm_generate` 包含同一 trace 下主 Agent 和子 Agent 的所有 `llm-generate` 调用，
  含失败调用。工具按 `laxcode.tool_name` 分组，Bash 耗时包含容器启动和清理开销。
- `count` 为调用次数，`total_ms` 为调用耗时之和，`avg_ms` 为总耗时除以总次数。
  Chat 层按所有调用加权，不对各次请求的平均值再次取平均。
- 子 Agent、并行或嵌套工具的耗时可能重叠，各项 `total_ms` 不应相加当作 Chat 耗时。
- `status` 为根 span 的执行状态（`completed` / `error`），不代表任务结果已通过验证。
- 相同 trace/span ID 的重复日志只统计一次；旧日志缺少 `duration_ms` 时使用起止时间。

## 日志不完整与错误

仅返回已有完整 Chat 根 span 的请求。执行中的请求可能只有子 span，其根 span
尚未落盘，此时不返回该请求，并增加 `incomplete_trace_count`。异常退出留下的
缺失根 span 也按此处理，不能仅凭此字段判定请求仍在执行。

损坏行（含写入一半的尾行）跳过并计入 `invalid_line_count`。出现损坏行或缺失根
span 时 `partial` 为 `true`；空日志返回空 `chats`。读取开始时固定日志长度，后续
追加的内容在下一次调用时统计。

| HTTP 状态 | 含义 |
| --- | --- |
| 400 | Session ID 无效，或用户身份来源冲突 |
| 403 | 本地模式，或日志路径/文件不允许读取 |
| 404 | 用户或 Session 不存在；本地日志不存在时错误码为 `TRACE_LOG_UNAVAILABLE` |
| 408 | 分析取消或超时（5 秒） |
| 413 | 日志超过 32 MiB、100,000 行，或单行达到扫描器的 1 MiB 上限 |
| 500 | 数据库或日志读取失败 |

服务使用 OTLP 时可能没有本地 tracing 日志，此接口不查询远端 Trace 存储，返回
`TRACE_LOG_UNAVAILABLE`。响应设置 `Cache-Control: no-store`。
