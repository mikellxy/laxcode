package filetrace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/telemetry"
)

const MaxPerformanceLogBytes int64 = 32 << 20
const maxPerformanceLines = 100000
const maxPerformanceLineBytes = 1 << 20

var ErrPerformanceLogTooLarge = errors.New("trace log exceeds performance analysis limits")

type DurationStats struct {
	Count   int     `json:"count"`
	TotalMs float64 `json:"total_ms"`
	AvgMs   float64 `json:"avg_ms"`
}

func (s DurationStats) add(count int, total float64) DurationStats {
	s.Count += count
	s.TotalMs += total
	return s
}

func (s DurationStats) rounded() DurationStats {
	if s.Count > 0 {
		s.AvgMs = roundMs(s.TotalMs / float64(s.Count))
	}
	s.TotalMs = roundMs(s.TotalMs)
	return s
}

func roundMs(ms float64) float64 { return math.Round(ms*1000) / 1000 }

type RequestPerformance struct {
	TraceID       string                   `json:"trace_id"`
	RequestID     string                   `json:"request_id"`
	Operation     string                   `json:"operation"`
	StartTime     time.Time                `json:"start_time"`
	EndTime       time.Time                `json:"end_time"`
	Status        string                   `json:"status"`
	TotalMs       float64                  `json:"total_ms"`
	FirstOutputMs *float64                 `json:"first_output_ms"`
	LLMGenerate   DurationStats            `json:"llm_generate"`
	Tools         map[string]DurationStats `json:"tools"`
}

type ChatPerformance struct {
	ChatID      string                   `json:"chat_id"`
	TotalMs     float64                  `json:"total_ms"`
	LLMGenerate DurationStats            `json:"llm_generate"`
	Tools       map[string]DurationStats `json:"tools"`
	Requests    []RequestPerformance     `json:"requests"`
}

type PerformanceSummary struct {
	SessionID            string            `json:"session_id"`
	Chats                []ChatPerformance `json:"chats"`
	Partial              bool              `json:"partial"`
	InvalidLineCount     int               `json:"invalid_line_count"`
	IncompleteTraceCount int               `json:"incomplete_trace_count"`
}

// Decode only the fields needed for metrics; never return raw attributes or errors.
type performanceRecord struct {
	TraceID    string   `json:"trace_id"`
	SpanID     string   `json:"span_id"`
	Name       string   `json:"name"`
	StartTime  string   `json:"start_time"`
	EndTime    string   `json:"end_time"`
	DurationMs *float64 `json:"duration_ms"`
	StatusCode string   `json:"status_code"`
	Attributes struct {
		SessionID     string   `json:"laxcode.session_id"`
		ChatID        string   `json:"laxcode.chat_id"`
		RequestID     string   `json:"laxcode.request_id"`
		Operation     string   `json:"laxcode.operation"`
		ToolName      string   `json:"laxcode.tool_name"`
		FirstOutputMs *float64 `json:"laxcode.first_output_ms"`
	} `json:"attributes"`
}

type performanceTrace struct {
	chatID  string
	request RequestPerformance
	seen    map[string]bool
}

// SummarizePerformance groups completed request roots by chat_id, including nested
// agent calls. A request's root duration is used once; child durations are not added to it.
func SummarizePerformance(ctx context.Context, input io.Reader, sessionID string) (PerformanceSummary, error) {
	result := PerformanceSummary{SessionID: sessionID, Chats: []ChatPerformance{}}
	limited := &io.LimitedReader{R: input, N: MaxPerformanceLogBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), maxPerformanceLineBytes)
	traces := make(map[string]*performanceTrace)
	lines := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		lines++
		if lines > maxPerformanceLines || limited.N == 0 {
			return result, ErrPerformanceLogTooLarge
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record performanceRecord
		if err := json.Unmarshal(line, &record); err != nil {
			result.InvalidLineCount++
			continue
		}
		start, startErr := time.Parse(time.RFC3339Nano, record.StartTime)
		end, endErr := time.Parse(time.RFC3339Nano, record.EndTime)
		duration := float64(end.Sub(start)) / float64(time.Millisecond)
		if record.DurationMs != nil {
			duration = *record.DurationMs
		}
		if record.TraceID == "" || record.SpanID == "" || startErr != nil || endErr != nil || end.Before(start) ||
			duration < 0 || math.IsNaN(duration) || math.IsInf(duration, 0) || duration > float64(math.MaxInt64)/float64(time.Millisecond) {
			result.InvalidLineCount++
			continue
		}
		t := traces[record.TraceID]
		if t == nil {
			t = &performanceTrace{request: RequestPerformance{TraceID: record.TraceID, Tools: map[string]DurationStats{}}, seen: map[string]bool{}}
			traces[record.TraceID] = t
		}
		if t.seen[record.SpanID] {
			continue
		}
		t.seen[record.SpanID] = true
		switch record.Name {
		case telemetry.SpanChat:
			if t.chatID != "" || record.Attributes.ChatID == "" || record.Attributes.SessionID != sessionID {
				result.InvalidLineCount++
				continue
			}
			t.chatID = record.Attributes.ChatID
			t.request.RequestID, t.request.Operation = record.Attributes.RequestID, record.Attributes.Operation
			t.request.StartTime, t.request.EndTime = start.UTC(), end.UTC()
			t.request.TotalMs, t.request.Status = duration, "completed"
			if record.StatusCode == "Error" {
				t.request.Status = "error"
			}
			if first := record.Attributes.FirstOutputMs; first != nil && *first >= 0 && *first <= duration+1 {
				t.request.FirstOutputMs = first
			}
		case telemetry.SpanLLMGenerate:
			t.request.LLMGenerate = t.request.LLMGenerate.add(1, duration)
		case telemetry.SpanToolExec:
			if name := record.Attributes.ToolName; name != "" {
				t.request.Tools[name] = t.request.Tools[name].add(1, duration)
			}
		}
	}
	if limited.N == 0 || errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return result, ErrPerformanceLogTooLarge
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	var completed []*performanceTrace
	for _, t := range traces {
		if t.chatID == "" {
			result.IncompleteTraceCount++
		} else {
			completed = append(completed, t)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		a, b := completed[i].request, completed[j].request
		if a.StartTime.Equal(b.StartTime) {
			return a.TraceID < b.TraceID
		}
		return a.StartTime.Before(b.StartTime)
	})
	chatIndex := make(map[string]int)
	for _, t := range completed {
		index, exists := chatIndex[t.chatID]
		if !exists {
			index = len(result.Chats)
			chatIndex[t.chatID] = index
			result.Chats = append(result.Chats, ChatPerformance{ChatID: t.chatID, Tools: map[string]DurationStats{}})
		}
		chat := &result.Chats[index]
		chat.TotalMs += t.request.TotalMs
		chat.LLMGenerate = chat.LLMGenerate.add(t.request.LLMGenerate.Count, t.request.LLMGenerate.TotalMs)
		for name, stats := range t.request.Tools {
			chat.Tools[name] = chat.Tools[name].add(stats.Count, stats.TotalMs)
			t.request.Tools[name] = stats.rounded()
		}
		t.request.TotalMs = roundMs(t.request.TotalMs)
		t.request.LLMGenerate = t.request.LLMGenerate.rounded()
		chat.Requests = append(chat.Requests, t.request)
	}
	for i := range result.Chats {
		chat := &result.Chats[i]
		chat.TotalMs, chat.LLMGenerate = roundMs(chat.TotalMs), chat.LLMGenerate.rounded()
		for name, stats := range chat.Tools {
			chat.Tools[name] = stats.rounded()
		}
	}
	result.Partial = result.InvalidLineCount > 0 || result.IncompleteTraceCount > 0
	return result, nil
}
