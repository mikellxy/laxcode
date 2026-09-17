# laxcode-token账目实现方式

## 简介

[laxcode-token账目实现方式]会话维护两本账，口径不同不能混。TokenUsed 累计全程的 provider 实测用量：正常生成轮在 AppendMessage 里累计，只有 assistant 消息携带模型返回的实测值；上下文摘要调用的额外用量由压缩流程在候选上补记。WindowToken 记录"下一个完整请求"的窗口占用，反映当前工作集有多大。[laxcode-token账目实现方式]

[laxcode-token账目实现方式]窗口账目的维护分三处。每次 assistant 生成后，AppendMessage 用本轮实测输入覆盖 WindowToken——实测输入已包含系统提示词与当时全部历史，直接覆盖即可。压缩流程里每得到一次 provider 精确计数就调 ReconcileWindowInput 校正。替换系统提示词时，UpsertSysMessage 扣旧加新：内部估算值 sysToken 刻意不写进 Message.TokenUsed，那是实测计费口径，混入估算会污染落盘历史。[laxcode-token账目实现方式]

[laxcode-token账目实现方式]估算与实测分工明确。本地 EstimateTokenInt（tiktoken）只用于聚合内部的粗校正和压缩裁剪的节省估算；凡涉及触发判断和达标确认的计数，一律走 LLMClient.CountInputTokens，它按与 Generate 相同的序列化口径计算，包含消息、reasoning 项和工具定义。压缩编排里每次策略修改后都重新精确计数，未确认达标前不发送生成请求。[laxcode-token账目实现方式]

## 关键代码入口
[laxcode-token账目实现方式]
- internal/domain/sharedkernel/token.go:TokenStatistics
- internal/domain/sharedkernel/token.go:EstimateTokenInt
- internal/domain/session/session.go:Session.AppendMessage
- internal/domain/session/session.go:Session.UpsertSysMessage
- internal/domain/session/session.go:Session.refreshSysToken
- internal/domain/session/session.go:Session.ReconcileWindowInput
- internal/application/reactservice/context_compaction.go:ReActService.compactContext
- internal/domain/llmprovider/llm_client.go:LLMClient.CountInputTokens
- internal/infrastructure/llmprovider/openai_impl.go:OpenApiProvider.CountInputTokens
  [laxcode-token账目实现方式]
