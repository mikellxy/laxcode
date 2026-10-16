package filetrace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/telemetry"
)

func performanceTestRecord(trace, span, name string, at time.Time, ms float64, attrs map[string]any) logRecord {
	return logRecord{TraceID: trace, SpanID: span, Name: name, StartTime: at.Format(time.RFC3339Nano),
		EndTime: at.Add(time.Duration(ms * float64(time.Millisecond))).UTC().Format(time.RFC3339Nano), DurationMs: ms, Attributes: attrs}
}

func performanceTestRoot(trace, chat, operation string, at time.Time, ms float64) logRecord {
	return performanceTestRecord(trace, "root", telemetry.SpanChat, at, ms, map[string]any{
		"laxcode.session_id": "session", "laxcode.chat_id": chat, "laxcode.request_id": trace + "-request", "laxcode.operation": operation,
	})
}

func performanceTestLog(t *testing.T, records ...logRecord) string {
	t.Helper()
	var log strings.Builder
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		log.Write(data)
		log.WriteByte('\n')
	}
	return log.String()
}

func TestPerformanceSummaryGroupsRequestsAndWeightsCalls(t *testing.T) {
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	send := performanceTestRoot("send", "chat-1", "send", start, 1000)
	send.Attributes["laxcode.first_output_ms"] = 12.5
	send.Attributes["private_key"] = "DO_NOT_EXPOSE"
	send.StatusMsg = "DO_NOT_EXPOSE"
	resume := performanceTestRoot("resume", "chat-1", "resume", start.Add(time.Hour), 500)
	resume.StatusCode = "Error"
	second := performanceTestRoot("second", "chat-2", "send", start.Add(2*time.Hour), 100)
	second.Attributes["laxcode.first_output_ms"] = 0
	child := performanceTestRecord("resume", "child-llm", telemetry.SpanLLMGenerate, start, 80, map[string]any{"laxcode.session_id": "sub:child"})
	llm := performanceTestRecord("send", "llm-1", telemetry.SpanLLMGenerate, start, 10, nil)
	failedLLM := performanceTestRecord("send", "llm-2", telemetry.SpanLLMGenerate, start, 30, nil)
	failedLLM.StatusCode = "Error"
	log := performanceTestLog(t,
		resume, child, second, llm, send, failedLLM, llm, // Deliberately unordered, with a duplicate.
		performanceTestRecord("send", "read-1", telemetry.SpanToolExec, start, 0.1, map[string]any{"laxcode.tool_name": "read_file"}),
		performanceTestRecord("send", "read-2", telemetry.SpanToolExec, start, 0.3, map[string]any{"laxcode.tool_name": "read_file"}),
		performanceTestRecord("resume", "read-3", telemetry.SpanToolExec, start, 0.5, map[string]any{"laxcode.tool_name": "read_file"}),
		performanceTestRecord("resume", "child-tool", telemetry.SpanToolExec, start, 100, map[string]any{"laxcode.tool_name": "run_sub_agent"}),
	)
	result, err := SummarizePerformance(context.Background(), strings.NewReader(log), "session")
	if err != nil || len(result.Chats) != 2 || result.Partial {
		t.Fatalf("summary=%+v err=%v", result, err)
	}
	chat := result.Chats[0]
	if chat.ChatID != "chat-1" || chat.TotalMs != 1500 || len(chat.Requests) != 2 {
		t.Fatalf("chat grouping or idle-time exclusion failed: %+v", chat)
	}
	if chat.LLMGenerate != (DurationStats{Count: 3, TotalMs: 120, AvgMs: 40}) {
		t.Fatalf("weighted LLM stats: %+v", chat.LLMGenerate)
	}
	if chat.Tools["read_file"] != (DurationStats{Count: 3, TotalMs: 0.9, AvgMs: 0.3}) {
		t.Fatalf("sub-millisecond tools: %+v", chat.Tools)
	}
	if chat.Requests[0].Operation != "send" || *chat.Requests[0].FirstOutputMs != 12.5 || chat.Requests[0].LLMGenerate.AvgMs != 20 {
		t.Fatalf("send stats: %+v", chat.Requests[0])
	}
	if chat.Requests[1].Operation != "resume" || chat.Requests[1].FirstOutputMs != nil || chat.Requests[1].Status != "error" {
		t.Fatalf("resume stats: %+v", chat.Requests[1])
	}
	if first := result.Chats[1].Requests[0].FirstOutputMs; first == nil || *first != 0 {
		t.Fatal("zero first output was confused with missing output")
	}
	data, err := json.Marshal(result)
	if err != nil || strings.Contains(string(data), "DO_NOT_EXPOSE") {
		t.Fatalf("raw metadata escaped into response: %s %v", data, err)
	}
}

func TestPerformanceSummaryPartialAndSessionFiltering(t *testing.T) {
	start := time.Now()
	root := performanceTestRoot("good", "chat", "send", start, 100)
	wrong := performanceTestRoot("foreign", "FOREIGN_CHAT", "send", start, 200)
	wrong.Attributes["laxcode.session_id"] = "other-session"
	negative := performanceTestRecord("good", "negative", telemetry.SpanLLMGenerate, start, -10, nil)
	log := performanceTestLog(t, root, wrong, negative,
		performanceTestRecord("unfinished", "llm", telemetry.SpanLLMGenerate, start, 5, nil)) + "invalid json\n{\"trace_id\":"
	result, err := SummarizePerformance(context.Background(), strings.NewReader(log), "session")
	if err != nil || !result.Partial || result.InvalidLineCount != 4 || result.IncompleteTraceCount != 2 || len(result.Chats) != 1 {
		t.Fatalf("partial summary=%+v err=%v", result, err)
	}
	if result.Chats[0].LLMGenerate.Count != 0 {
		t.Fatal("invalid LLM duration was counted")
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "FOREIGN_CHAT") {
		t.Fatal("another session's chat was included")
	}
}

func TestPerformanceSummaryLegacyDurationAndEmptyLog(t *testing.T) {
	log := `{"trace_id":"trace","span_id":"root","name":"chat","start_time":"2026-10-16T12:00:00+08:00","end_time":"2026-10-16T04:00:00.000123Z","attributes":{"laxcode.session_id":"session","laxcode.chat_id":"chat"}}`
	result, err := SummarizePerformance(context.Background(), strings.NewReader(log), "session")
	if err != nil || result.Partial || len(result.Chats) != 1 || result.Chats[0].TotalMs != 0.123 {
		t.Fatalf("legacy duration: %+v %v", result, err)
	}
	empty, err := SummarizePerformance(context.Background(), strings.NewReader(""), "session")
	if err != nil || empty.Chats == nil || len(empty.Chats) != 0 || empty.Partial {
		t.Fatalf("empty summary: %+v %v", empty, err)
	}
}

type performanceErrorReader struct{}

func (performanceErrorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestPerformanceSummaryLimitsAndCancellation(t *testing.T) {
	for _, log := range []string{strings.Repeat("\n", maxPerformanceLines+1), strings.Repeat("x", maxPerformanceLineBytes+1)} {
		if _, err := SummarizePerformance(context.Background(), strings.NewReader(log), "session"); !errors.Is(err, ErrPerformanceLogTooLarge) {
			t.Fatalf("expected size limit: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SummarizePerformance(ctx, strings.NewReader("\n"), "session"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	if _, err := SummarizePerformance(context.Background(), performanceErrorReader{}, "session"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read failure: %v", err)
	}
}
