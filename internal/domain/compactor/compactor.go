// Package compactor 提供 agent 运行时上下文的纯内存压缩策略。
// 触发时机、provider 精确 token 计数和模型窗口预算由 application 层
// 编排；本包只负责在给定的最小节省目标下选择要裁剪的内容。
package compactor

import (
	"fmt"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const (
	oldToolOutputTokenThreshold = 50
	recentContentRuneLimit      = 1000
	KeepRecentToolCallGroups    = 3
)

// simpleStrategy 是默认的确定性轻压缩策略，无状态且可并发使用。
type simpleStrategy struct{}

// SimpleCompactor 是默认压缩策略。
var SimpleCompactor simpleStrategy

type toolCallGroup struct {
	assistantIdx int
	resultIdxs   []int
	complete     bool
}

// Compress 至少尝试节省 minTokenSavings 个 token。它返回独立的消息
// 切片，不修改调用方的原始历史。saved 是本地估算值，只用于决定
// 下一个裁剪动作；最终是否达标必须由 provider 重新精确计数确认。
//
// 裁剪顺序：召回 chunks → 已归档的旧工具输出 → 旧 reasoning → 旧 assistant 正文。
// 召回 chunks（记忆与知识库片段）平时随历史 append-only 保留以维持模型
// 前缀缓存命中，只在压缩时回收保护区之前消息上的片段；保护区内的当前轮
// 上下文保持完整。不足三个调用组时保护区覆盖全部消息，此时 chunks 的
// 保护区退化为最后一条 user 消息（当前轮），更早历史上的片段一律回收。
// 其余动作只裁剪保护区之前的消息：最近三个调用组起点至末尾的整个区间
// 不裁剪。工具调用消息和所有对应结果消息始终保留，从而不破坏
// function_call/function_call_output 配对。
func (simpleStrategy) Compress(msgs []sharedkernel.Message, minTokenSavings int) ([]sharedkernel.Message, int, error) {
	if minTokenSavings < 0 {
		return nil, 0, fmt.Errorf("compactor: min token savings must not be negative")
	}
	out := sharedkernel.CloneMessages(msgs)
	if minTokenSavings == 0 || len(out) == 0 {
		return out, 0, nil
	}

	groups := collectToolCallGroups(out)
	protected := ProtectedStart(out)
	saved := 0
	reached := func() bool { return saved >= minTokenSavings }

	// 回收保护区之前消息上挂载的召回片段；片段可由后续对话重新召回，
	// 是可再生上下文，优先于会话正文被清理。
	chunkBound := protected
	if chunkBound == 0 {
		// 无足够调用组时保护区覆盖全部消息；chunks 的“当前轮”锚定
		// 最后一条 user 消息，否则无工具会话永远无处回收片段。
		chunkBound = lastUserIndex(out)
	}
	for i := 0; i < chunkBound; i++ {
		if len(out[i].MemoryChunks) == 0 && len(out[i].RAGChunks) == 0 {
			continue
		}
		for _, chunk := range out[i].MemoryChunks {
			saved += sharedkernel.EstimateTokenInt(chunk.Content)
		}
		for _, chunk := range out[i].RAGChunks {
			saved += sharedkernel.EstimateTokenInt(chunk.Content)
		}
		out[i].MemoryChunks = nil
		out[i].RAGChunks = nil
		if reached() {
			return out, saved, nil
		}
	}

	// 以调用组为原子单位处理：同一 assistant turn 发出的并行工具
	// 结果一起保留或一起清理，不会出现“只留最后一个结果”。
	for _, group := range groups {
		if group.assistantIdx >= protected {
			break
		}
		for _, idx := range group.resultIdxs {
			if out[idx].Artifact == nil || sharedkernel.EstimateTokenInt(out[idx].Content) <= oldToolOutputTokenThreshold {
				continue
			}
			placeholder := clearedToolOutput(out[idx])
			saved += replaceContent(&out[idx], placeholder)
		}
		if reached() {
			return out, saved, nil
		}
	}

	// 回收保护区之前的 reasoning_content。
	for i := range out {
		if out[i].Role != sharedkernel.RoleAssistant || i >= protected || out[i].ReasoningContent == "" {
			continue
		}
		old := out[i].ReasoningContent
		out[i].ReasoningContent = ""
		saved += sharedkernel.EstimateTokenInt(old)
		if reached() {
			return out, saved, nil
		}
	}

	// 裁剪保护区之前的普通 assistant 正文，包括调用组之间的消息。
	for i := range out {
		if out[i].Role != sharedkernel.RoleAssistant || i >= protected {
			continue
		}
		if truncated, ok := truncateMiddleRunes(out[i].Content, recentContentRuneLimit); ok {
			saved += replaceContent(&out[i], truncated)
			if reached() {
				return out, saved, nil
			}
		}
	}

	return out, saved, nil
}

// lastUserIndex 返回最后一条 user 消息的下标；没有 user 消息时返回 0
// （即不回收任何 chunks：片段只挂在 user 消息上，无锚点时保守不动）。
func lastUserIndex(msgs []sharedkernel.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == sharedkernel.RoleUser {
			return i
		}
	}
	return 0
}

func collectToolCallGroups(msgs []sharedkernel.Message) []toolCallGroup {
	var groups []toolCallGroup
	callToGroup := make(map[string]int)
	seen := make(map[string]bool)
	for i, msg := range msgs {
		if msg.Role == sharedkernel.RoleAssistant && len(msg.ToolCalls) > 0 {
			groupIdx := len(groups)
			groups = append(groups, toolCallGroup{assistantIdx: i})
			for _, call := range msg.ToolCalls {
				callToGroup[call.ID] = groupIdx
				seen[call.ID] = false
			}
		}
		if msg.Role == sharedkernel.RoleTool {
			idx, ok := callToGroup[msg.ToolCallID]
			if ok && !seen[msg.ToolCallID] {
				groups[idx].resultIdxs = append(groups[idx].resultIdxs, i)
				seen[msg.ToolCallID] = true
				groups[idx].complete = len(groups[idx].resultIdxs) == len(msgs[groups[idx].assistantIdx].ToolCalls)
			}
		}
	}
	return groups
}

// ProtectedStart 返回第三个最近调用组的起点，包含区间内全部消息。
// 未完成或结果跨界的调用组使边界向前扩展。不足三组时保留全部。
func ProtectedStart(msgs []sharedkernel.Message) int {
	groups := collectToolCallGroups(msgs)
	if len(groups) < KeepRecentToolCallGroups {
		return 0
	}
	start := groups[len(groups)-KeepRecentToolCallGroups].assistantIdx
	for changed := true; changed; {
		changed = false
		for _, g := range groups {
			if g.assistantIdx >= start {
				continue
			}
			crossing := !g.complete
			for _, idx := range g.resultIdxs {
				if idx >= start {
					crossing = true
				}
			}
			if crossing {
				start = g.assistantIdx
				changed = true
			}
		}
	}
	return start
}

// ArtifactCandidates 仅选择保护区之前完整调用组中的大结果。
// application 先持久化原文并附上 Artifact，再调用纯内存 Compress。
func ArtifactCandidates(msgs []sharedkernel.Message) []int {
	start := ProtectedStart(msgs)
	var indices []int
	for _, g := range collectToolCallGroups(msgs) {
		if g.assistantIdx >= start {
			break
		}
		for _, idx := range g.resultIdxs {
			if msgs[idx].Artifact == nil && sharedkernel.EstimateTokenInt(msgs[idx].Content) > oldToolOutputTokenThreshold {
				indices = append(indices, idx)
			}
		}
	}
	return indices
}

func clearedToolOutput(msg sharedkernel.Message) string {
	ref := fmt.Sprintf("[早期工具输出已归档 call_id=%s bytes=%d；read_artifact(artifact_id=%s, offset=0, limit=4000) 按需读取]",
		msg.ToolCallID, msg.Artifact.ByteSize, msg.Artifact.ID)
	if msg.CompactContent != "" {
		return msg.CompactContent + "\n" + ref
	}
	return ref
}

func replaceContent(msg *sharedkernel.Message, content string) int {
	before := sharedkernel.EstimateTokenInt(msg.Content)
	after := sharedkernel.EstimateTokenInt(content)
	if after >= before {
		return 0
	}
	msg.Content = content
	return before - after
}

// truncateMiddleRunes 只在内容超过 limit 时截断，并且始终在 rune
// 边界切分，避免中文、emoji 等多字节字符变成非法 UTF-8。
func truncateMiddleRunes(content string, limit int) (string, bool) {
	runes := []rune(content)
	if limit <= 0 || len(runes) <= limit {
		return content, false
	}
	headLen := limit / 2
	tailLen := limit - headLen
	removed := len(runes) - limit
	return fmt.Sprintf("%s [...中间%d字符已截断...] %s",
		string(runes[:headLen]), removed, string(runes[len(runes)-tailLen:])), true
}
