package run_sse

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
)

type chatTrace struct {
	ctx         context.Context
	span        telemetry.Span
	tracer      telemetry.Tracer
	handle      *tracing.Handle // 仅本地 filetrace 归本请求关闭；OTLP 归服务关闭。
	requestID   string
	startedAt   time.Time
	firstOutput bool
	err         error
}

func (s *server) startChatTrace(ctx context.Context, sessionID, operation, chatID string, startedAt time.Time) *chatTrace {
	t := &chatTrace{tracer: s.tracer, requestID: uuid.NewString(), startedAt: startedAt}
	// OTLP 已在服务启动时装配。本地文件需先知道 session_id，span 起点仍
	// 使用 handler 入口时间；非法路径不得通过请求参数创建追踪文件。
	if t.tracer == nil && sessionID != "" && sessionID != "." && sessionID != ".." &&
		filepath.Base(sessionID) == sessionID && !strings.ContainsAny(sessionID, `/\`) {
		if provider, err := filetrace.New(layout.TracingLog(s.homeDir, sessionID)); err == nil {
			t.handle = tracing.New(provider)
			t.tracer = t.handle.Tracer
		}
	}
	t.tracer = telemetry.OrNoop(t.tracer)
	ctx = telemetry.ContextWithChatID(ctx, chatID)
	t.ctx, t.span = telemetry.StartAt(ctx, t.tracer, telemetry.SpanChat, startedAt,
		telemetry.AttrSessionID.String(sessionID), telemetry.AttrOperation.String(operation),
		telemetry.AttrRequestID.String(t.requestID))
	if chatID != "" {
		t.span.SetAttributes(telemetry.AttrChatID.String(chatID))
	}
	return t
}

func (t *chatTrace) close() {
	if t.err == nil {
		t.err = t.ctx.Err()
	}
	telemetry.CloseSpan(t.span, telemetry.WithErr(t.err))
	if t.handle != nil {
		_ = t.handle.Shutdown(context.WithoutCancel(t.ctx))
	}
}

func (t *chatTrace) consumer(base func(*reactservice.ReactEvent)) func(*reactservice.ReactEvent) {
	return func(event *reactservice.ReactEvent) {
		if !t.firstOutput && event.Type == reactservice.ReActEventTypeChunk &&
			event.ChunkEvent != nil && event.ChunkEvent.IsOutput() {
			t.firstOutput = true
			t.span.AddEvent(telemetry.EventFirstOutput)
			t.span.SetAttributes(telemetry.AttrFirstOutputMs.Int64(time.Since(t.startedAt).Milliseconds()))
		}
		base(event)
	}
}

func (s *server) assembleAgent(t *chatTrace, in agentasm.Input) (*agentasm.Assembled, error) {
	ctx, span := telemetry.Start(t.ctx, t.tracer, telemetry.SpanAgentAssemble)
	in.Tracer = t.tracer
	assembled, err := s.assemble(ctx, in)
	telemetry.CloseSpan(span, telemetry.WithErr(err))
	return assembled, err
}

func latestChatID(messages []sharedkernel.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sharedkernel.RoleUser {
			return messages[i].ChatID
		}
	}
	return ""
}
