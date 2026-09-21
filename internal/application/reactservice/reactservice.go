package reactservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

type ReActService struct {
	Session *session.Session
	// SessRepo 是会话持久化端口：加载与落盘由本服务（application 层）编排，
	// 聚合只做内存内的状态演化，不持有仓储。
	SessRepo  session.SessionRepository
	LLMClient llmprovider.LLMClient
	// ContextSummaryLLMClient 只在确定性本地压缩无法达到目标时调用。
	// 它不参与正常 ReAct 生成，且摘要请求不携带业务工具定义。
	ContextSummaryLLMClient llmprovider.LLMClient
	ToolRegistry            tools.Registry
	Artifacts               tools.ArtifactStore
	ReActEventConsumerF     func(reactEvent *ReactEvent)
	// tracer 是 chat 及其子 span 的追踪注入点，经构造注入；nil 缺省
	// noop，不产生任何观测输出。类型经 telemetry 别名持有，本包不直接
	// 依赖 OTel（span 的开启与收尾均走 telemetry 辅助函数）。
	tracer telemetry.Tracer
	// promptEnricher 是 chat 根 span 内、消息落盘前执行的可选查询增强器。
	// QA 模式注入向量化与知识库召回实现；普通模式保持 nil。
	promptEnricher PromptEnricher
}

var (
	ErrInvalidContextBudget  = errors.New("reactservice: invalid model context budget")
	ErrContextTargetNotReach = errors.New("reactservice: context compaction target cannot be reached")
	ErrPersistRequestContext = errors.New("reactservice: persist request context")
	// ErrNothingToResume 表示会话尾部已经收束，或尚无用户消息。调用方应拒绝
	// resume，避免在没有待完成输入时让模型重复生成。
	ErrNothingToResume = errors.New("reactservice: no interrupted chat to resume")
)

const (
	ReActEventTypeChunk          = "chunk"
	ReActEventTypeToolCall       = "tool_call"
	ReActEventTypeRecovery       = "recovery"
	ReActEventTypeHumanInTheLoop = "human_in_the_loop"
	contextTriggerPercent        = 80
	contextTargetPercent         = 60
	recoveryToolResultPrompt     = "上一次工具调用未获得可确认的结果；它可能尚未执行，也可能已经执行但结果未被保存。请先检查当前状态，再决定是否重试。"
)

type ReactEvent struct {
	Type             string
	Content          string                    // 工具执行提示或人工确认说明
	ChunkEvent       *sharedkernel.StreamChunk // LLM 流式增量，仅 chunk 事件携带
	HumanConfirmChan chan<- string             // 人工确认回复通道，仅 human_in_the_loop 事件携带
}

// PromptEnricher 在 chat 根 span 内把用户输入扩充为最终模型提示词。
// 实现可创建 query-embedding、vector-retrieval 等子 span。
type PromptEnricher interface {
	Enrich(ctx context.Context, query string) (string, error)
}

func NewReActService(sess *session.Session,
	sessRepo session.SessionRepository,
	llmClient llmprovider.LLMClient,
	contextSummaryLLMClient llmprovider.LLMClient,
	toolRegistry tools.Registry,
	reActEventConsumerF func(reactEvent *ReactEvent),
	tracer telemetry.Tracer,
	artifactStores ...tools.ArtifactStore) *ReActService {
	if reActEventConsumerF == nil {
		reActEventConsumerF = func(*ReactEvent) {}
	}
	r := &ReActService{
		Session:                 sess,
		SessRepo:                sessRepo,
		LLMClient:               llmClient,
		ContextSummaryLLMClient: contextSummaryLLMClient,
		ToolRegistry:            toolRegistry,
		ReActEventConsumerF:     reActEventConsumerF,
		tracer:                  telemetry.OrNoop(tracer),
	}
	// ArtifactStore 与数据库会话仓储相互独立；子服务绑定自己的 session ID。
	if len(artifactStores) > 0 && artifactStores[0] != nil {
		store := artifactStores[0]
		r.Artifacts = store
		toolRegistry.Register(tools.NewReadArtifactTool(store, sess.ID))
	}
	return r
}

// SetPromptEnricher 配置 chat 开始后、用户消息落盘前运行的可选提示词增强器。
// 应仅在服务对外可见前由组合根调用，不应在并发 Chat 期间修改。
func (r *ReActService) SetPromptEnricher(enricher PromptEnricher) {
	r.promptEnricher = enricher
}

// ReplaceLLMClient replaces the main generation client between Chat calls.
// Callers must not invoke it while a Chat is in progress.
func (r *ReActService) ReplaceLLMClient(client llmprovider.LLMClient) {
	r.LLMClient = client
}

// requestHumanConfirmation 向交互前端发出一次人工确认请求，并等待回复或取消。
// channel 由 ReActService 创建并持有；前端只获得发送端，不应关闭。容量为 1，
// 避免取消与用户提交同时发生时让前端发送 goroutine 永久阻塞。
// 当前尚无风险检测调用此能力，待策略确定后在相应边界接入。
func (r *ReActService) requestHumanConfirmation(ctx context.Context, content string) (string, error) {
	confirmChan := make(chan string, 1)
	r.ReActEventConsumerF(&ReactEvent{
		Type:             ReActEventTypeHumanInTheLoop,
		Content:          content,
		HumanConfirmChan: confirmChan,
	})
	select {
	case confirmation := <-confirmChan:
		return confirmation, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// InitSession 从数据库恢复最新工作集。
func (r *ReActService) InitSession(ctx context.Context) error {
	snapshot, err := r.SessRepo.GetRequestContext(ctx, r.Session.ID)
	if err != nil {
		return fmt.Errorf("%w: load request context: %w", ErrPersistRequestContext, err)
	}
	return r.Session.Restore(snapshot)
}

// InitSysPrompt 将本次系统提示词和账目一起提交到工作集快照。
func (r *ReActService) InitSysPrompt(ctx context.Context, p string) error {
	candidate := r.Session.Clone()
	isFirst := len(candidate.Messages) == 0
	sysMsg := candidate.UpsertSysMessage(p)
	if isFirst {
		return r.commitCreatedMessage(ctx, candidate, sysMsg, sysMsg)
	}
	return r.commitUpdatedMessage(ctx, candidate, sysMsg)
}

// Chat 先为数据库中恢复出的未完成 ReAct 补齐缺失的 tool result；随后立即
// 追加本次用户消息，让模型在同一次后续推理中综合旧工具结果与用户的新要求。
func (r *ReActService) Chat(ctx context.Context, p string) (
	msg *sharedkernel.Message, err error,
) {
	ctx = telemetry.ContextWithSessionID(ctx, r.Session.ID)
	agentRole := telemetry.AgentRoleFromContext(ctx)
	if agentRole == "" {
		agentRole = telemetry.AgentRoleMain
	}
	ctx, chatSpan := telemetry.Start(ctx, r.tracer, telemetry.SpanChat,
		telemetry.AttrSessionID.String(r.Session.ID),
		telemetry.AttrAgentRole.String(agentRole),
	)
	startedAt := time.Now()
	defer func() {
		if msg != nil {
			chatSpan.SetAttributes(
				telemetry.AttrFinishReason.String(msg.FinishReason),
			)
		}
		telemetry.CloseSpan(chatSpan,
			telemetry.WithErr(err),
			telemetry.WithTimeCostMs(time.Since(startedAt).Milliseconds()),
		)
	}()

	if r.promptEnricher != nil {
		p, err = r.promptEnricher.Enrich(ctx, p)
		if err != nil {
			return nil, err
		}
	}
	if err = r.recoverBeforeChat(ctx); err != nil {
		return nil, fmt.Errorf("recover previous chat: %w", err)
	}
	userMsg := r.Session.BuildUserMessage(p)
	candidate, err := r.Session.WithAppendedMessage(&userMsg)
	if err != nil {
		return nil, err
	}
	if err = r.commitCreatedMessage(ctx, candidate, userMsg, userMsg); err != nil {
		return nil, err
	}
	return r.think(ctx)
}

// Resume 恢复已经持久化 user message、但尚未以无工具调用 assistant 收束的
// 对话。它不会创建新的 user message，供断流/生成错误后的显式重试入口使用。
func (r *ReActService) Resume(ctx context.Context) (*sharedkernel.Message, error) {
	if !needsRecovery(r.Session.Messages) {
		return nil, ErrNothingToResume
	}
	ctx = telemetry.ContextWithSessionID(ctx, r.Session.ID)
	if err := r.recoverBeforeChat(ctx); err != nil {
		return nil, fmt.Errorf("recover previous chat: %w", err)
	}
	return r.think(ctx)
}

// needsRecovery 只以已提交工作集判断是否存在未收束的一轮：至少有一条 user，
// 且尾部不是无工具调用的最终 assistant。
func needsRecovery(messages []sharedkernel.Message) bool {
	if !hasUserMessage(messages) || len(messages) == 0 {
		return false
	}
	tail := messages[len(messages)-1]
	return tail.Role != sharedkernel.RoleAssistant || len(tail.ToolCalls) != 0
}

// recoverBeforeChat 直接从消息尾部推导上次执行是否收束；若未收束，只补齐
// 最近一次工具调用中未持久化的 tool result，无需额外的活跃对话状态字段。
func (r *ReActService) recoverBeforeChat(ctx context.Context) error {
	if !needsRecovery(r.Session.Messages) {
		return nil
	}
	r.ReActEventConsumerF(&ReactEvent{
		Type:    ReActEventTypeRecovery,
		Content: "检测到上一次对话未完成，正在恢复后继续处理本次输入。",
	})

	for _, call := range missingToolResults(r.Session.Messages) {
		toolMsg := &sharedkernel.Message{
			Role:       sharedkernel.RoleTool,
			ToolCallID: call.ID,
			Content:    recoveryToolResultPrompt,
		}
		if err := r.handleTurnMsg(ctx, toolMsg); err != nil {
			return err
		}
	}
	return nil
}

func hasUserMessage(messages []sharedkernel.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sharedkernel.RoleUser {
			return true
		}
	}
	return false
}

// missingToolResults 只检查最近一个 tool-call assistant。ReAct 在进入下一次
// 模型调用前会持久化该组全部 tool result，因此恢复时最多只有这个调用组未闭合。
func missingToolResults(messages []sharedkernel.Message) []sharedkernel.ToolCall {
	callIndex := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sharedkernel.RoleAssistant && len(messages[i].ToolCalls) > 0 {
			callIndex = i
			break
		}
	}
	if callIndex < 0 {
		return nil
	}

	completed := make(map[string]struct{})
	for i := callIndex + 1; i < len(messages); i++ {
		if messages[i].Role == sharedkernel.RoleTool {
			completed[messages[i].ToolCallID] = struct{}{}
		}
	}
	var missing []sharedkernel.ToolCall
	for _, call := range messages[callIndex].ToolCalls {
		if _, ok := completed[call.ID]; !ok {
			missing = append(missing, call)
		}
	}
	return missing
}

func (r *ReActService) think(ctx context.Context) (*sharedkernel.Message, error) {
	turnCnt := 0
	for {
		turnCnt++

		// 每轮固定一份工具定义：精确计数与随后的生成请求必须
		// 序列化同一份 tools，不能让 registry map 的遍历顺序在两次读取间漂移。
		toolDefs := r.ToolRegistry.GetAvailableTools()
		if err := r.compactContext(ctx, toolDefs); err != nil {
			return nil, err
		}

		llmStart := time.Now()
		llmCtx, llmSpan := telemetry.Start(ctx, r.tracer, telemetry.SpanLLMGenerate,
			telemetry.AttrTurnSeq.Int(turnCnt),
		)
		msg, err := r.LLMClient.GenerateStream(llmCtx, r.Session.Messages, toolDefs, func(chunkEvent sharedkernel.StreamChunk) {
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeChunk, ChunkEvent: &chunkEvent})
		})
		if msg != nil {
			llmSpan.SetAttributes(
				telemetry.AttrInputTokens.Int(msg.TokenUsed.TokenInput),
				telemetry.AttrOutputTokens.Int(msg.TokenUsed.TokenOutput),
				telemetry.AttrToolCallCount.Int(len(msg.ToolCalls)),
				telemetry.AttrFinishReason.String(msg.FinishReason),
			)
		}
		telemetry.CloseSpan(llmSpan,
			telemetry.WithTimeCostMs(time.Since(llmStart).Milliseconds()),
			telemetry.WithErr(err),
		)
		if err != nil {
			return msg, err
		}
		if err := r.handleTurnMsg(ctx, msg); err != nil {
			return nil, err
		}

		// 无工具调用，推理循环完成
		if len(msg.ToolCalls) == 0 {
			return msg, nil
		}
		toolCtx := telemetry.ContextWithTurnSeq(ctx, turnCnt)

		for _, tc := range msg.ToolCalls {
			info := r.ToolRegistry.BeforeExecInfo(&tc)
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeToolCall, Content: info})

			result := r.ToolRegistry.Execute(toolCtx, &tc)
			toolMsg := tools.ToolResultAsMsg(result)
			toolMsg.DisplayContent = info
			if err := r.handleTurnMsg(ctx, toolMsg); err != nil {
				return nil, err
			}
		}
	}
}

// handleTurnMsg 先在候选中赋予稳定标识，再原子提交历史与工作集；成功后内存
// 才切换。JSONL 冷备失败不会使数据库提交失败。
func (r *ReActService) handleTurnMsg(ctx context.Context, msg *sharedkernel.Message) error {
	candidate, err := r.Session.WithAppendedMessage(msg)
	if err != nil {
		return err
	}
	return r.commitCreatedMessage(ctx, candidate, *msg, *msg)
}

func (r *ReActService) commitCreatedMessage(ctx context.Context, candidate *session.Session, original, memory sharedkernel.Message) error {
	revision, err := r.SessRepo.CommitCreateMessage(ctx, r.Session.ID, candidate.RequestContext, original, memory)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPersistRequestContext, err)
	}
	candidate.Revision = revision
	*r.Session = *candidate
	return nil
}

func (r *ReActService) commitUpdatedMessage(ctx context.Context, candidate *session.Session, memory sharedkernel.Message) error {
	revision, err := r.SessRepo.CommitUpdateMessage(ctx, r.Session.ID, candidate.RequestContext, memory)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPersistRequestContext, err)
	}
	candidate.Revision = revision
	*r.Session = *candidate
	return nil
}

func (r *ReActService) commitNextMemoryGeneration(ctx context.Context, candidate *session.Session) error {
	revision, err := r.SessRepo.CommitNextMemoryGeneration(ctx, r.Session.ID, candidate.RequestContext)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPersistRequestContext, err)
	}
	candidate.Revision = revision
	*r.Session = *candidate
	return nil
}
