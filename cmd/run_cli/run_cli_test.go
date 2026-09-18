package run_cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/cliprinter"
)

func TestEventConsumerStreamsTextAndReasoning(t *testing.T) {
	var output []cliprinter.StreamEvent
	rcf := newEventConsumer(func(s cliprinter.StreamEvent) { output = append(output, s) })
	chunks := []sharedkernel.StreamChunk{
		{Kind: sharedkernel.ChunkReasoningStart},
		{Kind: sharedkernel.ChunkReasoningDelta, Delta: "先思考"},
		{Kind: sharedkernel.ChunkReasoningDelta, Delta: "再回答"},
		{Kind: sharedkernel.ChunkReasoningEnd},
		{Kind: sharedkernel.ChunkTextStart},
		{Kind: sharedkernel.ChunkTextDelta, Delta: "hello"},
		{Kind: sharedkernel.ChunkTextDelta, Delta: " world"},
		{Kind: sharedkernel.ChunkTextEnd},
	}
	want := []cliprinter.StreamEvent{
		{Kind: cliprinter.ThinkingStart}, {Kind: cliprinter.ThinkingDelta, Text: "先思考"}, {Kind: cliprinter.ThinkingDelta, Text: "再回答"}, {Kind: cliprinter.ThinkingEnd},
		{Text: ColorGreen + "[LaxCode] LLM generates: "}, {Text: ColorGreen + "hello" + ColorReset}, {Text: ColorGreen + " world" + ColorReset}, {Text: ColorReset + "\n"},
	}
	for i, chunk := range chunks {
		rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeChunk, ChunkEvent: &chunk})
		if !reflect.DeepEqual(output, want[:i+1]) {
			t.Fatalf("第 %d 个 chunk 应立即输出且不重复前缀：got %#v, want %#v", i, output, want[:i+1])
		}
	}
}

func TestEventConsumerShowsToolOnlyAtExecution(t *testing.T) {
	var output []cliprinter.StreamEvent
	rcf := newEventConsumer(func(s cliprinter.StreamEvent) { output = append(output, s) })
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeChunk})
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeChunk, ChunkEvent: &sharedkernel.StreamChunk{
		Kind: sharedkernel.ChunkToolCall, ToolCall: &sharedkernel.ToolCall{Name: "bash"},
	}})
	if len(output) != 0 {
		t.Fatalf("参数就绪时不应提前显示工具执行提示：%#v", output)
	}
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeToolCall, Content: "bash: ls"})
	want := []cliprinter.StreamEvent{{Text: ColorYellow + "[LaxCode] tool execute... bash: ls" + ColorReset + "\n"}}
	if !reflect.DeepEqual(output, want) {
		t.Fatalf("工具执行提示不符：got %#v, want %#v", output, want)
	}
}

func TestEventConsumerShowsRecovery(t *testing.T) {
	var output []cliprinter.StreamEvent
	rcf := newEventConsumer(func(s cliprinter.StreamEvent) { output = append(output, s) })
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeRecovery, Content: "正在恢复"})
	want := []cliprinter.StreamEvent{{Text: ColorYellow + "[LaxCode] 正在恢复" + ColorReset + "\n"}}
	if !reflect.DeepEqual(output, want) {
		t.Fatalf("恢复提示不符：got %#v, want %#v", output, want)
	}
}

func TestEventConsumerMapsHumanInTheLoop(t *testing.T) {
	var output []cliprinter.StreamEvent
	confirm := make(chan string, 1)
	rcf := newEventConsumer(func(s cliprinter.StreamEvent) { output = append(output, s) })
	rcf(&reactservice.ReactEvent{
		Type:             reactservice.ReActEventTypeHumanInTheLoop,
		Content:          "allow risky action?",
		HumanConfirmChan: confirm,
	})

	want := cliprinter.StreamEvent{
		Kind:             cliprinter.HumanInTheLoop,
		Text:             ColorYellow + "[LaxCode] approval required: allow risky action?" + ColorReset + "\n",
		HumanConfirmChan: confirm,
	}
	if len(output) != 1 || output[0] != want {
		t.Fatalf("人工确认事件映射不符：got %#v, want %#v", output, want)
	}
}

func TestFormatRuntimeErrorAddsPersistRetryHint(t *testing.T) {
	got := formatRuntimeError(errors.Join(reactservice.ErrPersistRequestContext, errors.New("disk full")))
	if !strings.Contains(got, "下次对话开始前先恢复上一轮") {
		t.Fatalf("持久化错误应提示下次输入自动恢复：%q", got)
	}
	plain := formatRuntimeError(errors.New("provider error"))
	if strings.Contains(plain, "下次对话开始前先恢复上一轮") {
		t.Fatalf("普通错误不应显示持久化恢复提示：%q", plain)
	}
}
