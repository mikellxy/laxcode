package run_qa

import (
	"reflect"
	"testing"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestEventConsumerSuppressesReasoningAndStreamsAnswer(t *testing.T) {
	var output []string
	consume := newEventConsumer(func(s string) { output = append(output, s) })
	for _, chunk := range []sharedkernel.StreamChunk{
		{Kind: sharedkernel.ChunkReasoningStart},
		{Kind: sharedkernel.ChunkReasoningDelta, Delta: "hidden reasoning"},
		{Kind: sharedkernel.ChunkReasoningEnd},
		{Kind: sharedkernel.ChunkTextStart},
		{Kind: sharedkernel.ChunkTextDelta, Delta: "visible answer"},
		{Kind: sharedkernel.ChunkTextEnd},
	} {
		consume(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeChunk, ChunkEvent: &chunk})
	}
	want := []string{
		colorGreen + "[LaxCode QA] answer: ",
		colorGreen + "visible answer" + colorReset,
		colorReset + "\n",
	}
	if !reflect.DeepEqual(output, want) {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestEventConsumerDoesNotExposeToolEvents(t *testing.T) {
	var output []string
	consume := newEventConsumer(func(s string) { output = append(output, s) })
	consume(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeToolCall, Content: "read_file"})
	if len(output) != 0 {
		t.Fatalf("QA mode should not expose tool events: %q", output)
	}
}
