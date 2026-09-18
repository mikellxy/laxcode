# laxcode-断点恢复实现方式

## 简介

[laxcode-断点恢复实现方式]会话里没有独立的"活跃对话状态"字段，上次执行是否收束直接从消息尾部推导：尾部是纯文本 assistant（无工具调用）即已收束；否则恢复。恢复的动作只有一个——为最近一次工具调用中未持久化的 tool result 补上占位结果，然后立即追加本次用户消息，让模型在同一次推理中综合旧工具结果与新要求。[laxcode-断点恢复实现方式]

[laxcode-断点恢复实现方式]恢复逻辑由 recoverBeforeChat 在每次 Chat 开头执行。missingToolResults 只检查最近一个带工具调用的 assistant：ReAct 循环在进入下一次模型调用前会把该组 tool result 全部落盘，因此未闭合的最多只有这一组；它收集该组中缺少结果的 ToolCall，逐条生成 role=tool 的占位消息（内容是固定的恢复提示，提醒模型先检查状态再决定是否重试），经 handleTurnMsg 走正常候选提交路径补进历史。[laxcode-断点恢复实现方式]

[laxcode-断点恢复实现方式]重启后的完整链路：InitSession 经仓储读取最新工作集并 Restore 进聚合，随后 recoverBeforeChat 推导收束状态并补齐缺口，Chat 继续推进。占位结果如实描述不确定性（可能未执行、也可能已执行但未保存），不做隐式重放，判断权留给模型。[laxcode-断点恢复实现方式]

## 关键代码入口
[laxcode-断点恢复实现方式]
- internal/application/reactservice/reactservice.go:ReActService.Chat
- internal/application/reactservice/reactservice.go:ReActService.recoverBeforeChat
- internal/application/reactservice/reactservice.go:missingToolResults
- internal/application/reactservice/reactservice.go:ReActService.handleTurnMsg
- internal/application/reactservice/reactservice.go:ReActService.InitSession
- internal/domain/session/request_context.go:Session.Restore
- internal/domain/session/repository.go:SessionRepository.GetRequestContext
[laxcode-断点恢复实现方式]
