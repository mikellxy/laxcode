# POST /chat 对话接口

## 路由注册与服务生命周期

### 注册 POST /chat 路由，SSE 服务监听并在收到 SIGINT/SIGTERM 后等在途流收束

- `cmd/run_sse/run_sse.go`：`func run(router agentasm.RouterClientReplacer, codeInstance *CodeInstanceGuard, models *ai_models.Manager) error`

## 请求前置校验（进入 SSE 流之前，失败返回普通 JSON 状态码）

### 解析请求体并要求 session_id 与 task 必填，超限请求体直接拒绝

- `cmd/run_sse/handler.go`：`func (s *server) handleChat(w http.ResponseWriter, r *http.Request)`

### 同一 session_id 互斥串行，占用中立即返回 409 而非排队

- `cmd/run_sse/handler.go`：`func (s *sessionLocks) TryLock(id string) (func(), bool)`

### 校验会话存在、code 模式匹配且模型已配置，缺失模型提前返回 MODEL_REQUIRED

- `cmd/run_sse/handler.go`：`func (s *server) handleChat(w http.ResponseWriter, r *http.Request)`
- `cmd/run_sse/handler.go`：`func (s *server) modelMissing() bool`

## SSE 流建立与事件帧下发

### 发送 text/event-stream 响应头后进入流阶段，此后失败只走 error 帧且状态码不可回退

- `cmd/run_sse/sse.go`：`func newSSEWriter(w http.ResponseWriter, f http.Flusher) *sseWriter`

### 将 ReAct 事件转成 reasoning、message、tool_call 帧按序下发

- `cmd/run_sse/sse.go`：`func newEventConsumer(sw *sseWriter) func(*reactservice.ReactEvent)`

### 审批类事件注册 approval 记录并下发 approval_required 帧

- `cmd/run_sse/handler.go`：`func (s *server) eventConsumer(sw *sseWriter, sessionID *string, requestID string) func(*reactservice.ReactEvent)`
- `cmd/run_sse/approval.go`：`func (b *approvalBroker) register(sessionID, requestID string, reply chan<- string) string`

## 每请求装配与对话执行

### switcher 读锁持有期间按请求装配 ReActService，失败发 AGENT_ASSEMBLY_FAILED 帧并 Cleanup

- `cmd/run_sse/tracing.go`：`func (s *server) assembleAgent(t *chatTrace, in agentasm.Input) (assembled *agentasm.Assembled, err error)`
- `cmd/agentasm/agentasm.go`：`func Assemble(ctx context.Context, in Input) (*Assembled, error)`

### 补齐未完成 ReAct 的 tool result、追加 user 消息并执行一轮推理循环

- `internal/application/reactservice/reactservice.go`：`func (r *ReActService) Chat(ctx context.Context, p string) (msg *sharedkernel.Message, err error)`

### 发送 start 帧与 done 帧，done 携带最终消息与会话统计

- `cmd/run_sse/handler.go`：`func doneData(assembled *agentasm.Assembled, msg *sharedkernel.Message) DoneData`

## 错误处理与重试语义

### 危险命令被用户拒绝时终止本轮对话并发 CHAT_STOPPED 错误帧

- `cmd/run_sse/handler.go`：`func (s *server) handleChat(w http.ResponseWriter, r *http.Request)`
- `internal/application/reactservice/reactservice.go`：`ErrDangerousCommandDeclined`

### Chat 失败后按 user 消息数是否增加区分 resume 与 resend 重试动作

- `cmd/run_sse/handler.go`：`func countMessagesByRole(messages []sharedkernel.Message, role string) int`

### 建立请求级追踪并把 chat_id 传播给装配阶段与日志

- `cmd/run_sse/tracing.go`：`func (s *server) startChatTrace(ctx context.Context, sessionID, operation, chatID string, startedAt time.Time) *chatTrace`
