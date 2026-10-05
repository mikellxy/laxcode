// Package telemetry 是 DDD 架构下领域层的可观测性词汇包：集中定义 span
// 名与属性键常量、OTel API 类型的别名（Tracer/Span/KeyValue，见 trace.go）、
// session_id / agent_role 的 ctx 传播、span 开启与关闭辅助、noop 缺省。
//
// 依赖约定：本包只依赖 OpenTelemetry **API**（go.opentelemetry.io/otel，
// 厂商中立的稳定抽象），不包含任何上报后端、装配或资源生命周期逻辑。
// domain / application / cmd 的埋点方一律经本包使用追踪能力，**不得**直接
// import go.opentelemetry.io/ 下的任何包：否则「领域只依赖 telemetry 一个
// 观测词汇包」就是空话，换观测方案时仍要回头改领域代码。全仓对 OTel 的
// import 因此只落在本包与 infrastructure/tracing 两处，可被 grep 直接校验。
//
// 观测语义约定（chat 根调用树、laxcode.* 业务属性、
// gen_ai.* token 属性）沉淀于此。真正的装配——TracerProvider/上报后端的
// 选择与进程退出 Shutdown——由 internal/infrastructure/tracing（含 filetrace）
// 负责，那是唯一允许同时依赖 OTel API 与具体实现的层。
package telemetry

import "go.opentelemetry.io/otel/attribute"

// 请求以 chat 为根，装配与 react 为其子 span；每轮准备、生成和工具执行
// 归属 react，子 Agent 的 react 归属启动它的 tool-exec。
const (
	SpanChat           = "chat"
	SpanAgentAssemble  = "agent-assemble"
	SpanReact          = "react"
	SpanContextPrepare = "context-prepare"
	SpanLLMGenerate    = "llm-generate"
	SpanToolExec       = "tool-exec"
	EventFirstOutput   = "first-output"
)

// laxcode 自有概念的业务属性键
const (
	AttrSessionID     attribute.Key = "laxcode.session_id"
	AttrChatID        attribute.Key = "laxcode.chat_id"
	AttrToolName      attribute.Key = "laxcode.tool_name"
	AttrAgentRole     attribute.Key = "laxcode.agent_role"
	AttrTurnSeq       attribute.Key = "laxcode.loop_seq"
	AttrToolCallCount attribute.Key = "laxcode.tool_call_count"
	AttrRequestID     attribute.Key = "laxcode.request_id"
	AttrOperation     attribute.Key = "laxcode.operation"
	AttrFirstOutputMs attribute.Key = "laxcode.first_output_ms"
	AttrTTFTMs        attribute.Key = "laxcode.ttft_ms"
	AttrFinishReason  attribute.Key = "laxcode.finish_reason"
)

// AttrAgentRole 的取值
const (
	AgentRoleMain = "main"
	AgentRoleSub  = "sub"
)

// token 用量属性键：键名对齐 OTel GenAI 语义约定（experimental），
// 使兼容后端可自动识别；以字面量常量定义而不引入 semconv 包，
// 约定演进时在此单点修改。
const (
	AttrInputTokens  attribute.Key = "gen_ai.usage.input_tokens"
	AttrOutputTokens attribute.Key = "gen_ai.usage.output_tokens"
)
