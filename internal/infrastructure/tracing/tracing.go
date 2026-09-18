// Package tracing 是 DDD 架构下 OpenTelemetry 的装配入口：负责
// TracerProvider 的选择、Handle 的构造与进程退出前的 Shutdown。
//
// span 名/属性键等观测语义常量与无副作用的埋点辅助（noop 归一、session_id
// ctx 传播、span 收尾）已下沉到 internal/domain/telemetry，供 domain/
// application 层引用；本包不承载任何埋点语义，只保留装配职责。
//
// 内置 OTLP/HTTP 上报由 NewOTLP 装配；未配置远端时，组合根使用 filetrace
// 本地落盘。两种实现都通过 Handle 暴露同一 Tracer 与 Shutdown 生命周期。
//
// 注意：OTel API 的 TracerProvider / Tracer / Span 均为密封接口（内嵌未导出
// 方法），自定义实现须按官方 "API Implementations" 约定内嵌
// go.opentelemetry.io/otel/trace/embedded 包中的对应接口来满足。
package tracing

import (
	"context"

	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Handle 是 tracing 的装配句柄：持有供应用服务与工具注册表注入的 Tracer，
// 以及进程退出前须调用的 Shutdown 钩子。装配方创建并持有它，退出路径
// defer 一次 Shutdown。
type Handle struct {
	Tracer   trace.Tracer
	provider trace.TracerProvider
}

// New 以 TracerProvider 构造句柄；nil provider 缺省为 OTel 官方 noop
// 实现——此时全部埋点零开销、不产生任何观测输出。
func New(tp trace.TracerProvider) *Handle {
	if tp == nil {
		tp = noop.NewTracerProvider()
	}
	return &Handle{Tracer: tp.Tracer(telemetry.InstrumentationName), provider: tp}
}

// Shutdown 在进程退出前调用：实现侧（如官方 SDK 的 TracerProvider）
// 具备 Shutdown(context.Context) error 方法时转发，以强制 flush 尚未
// 导出的 span（批量导出间隔可能长于进程存活时间）；否则为空操作。
func (h *Handle) Shutdown(ctx context.Context) error {
	if s, ok := h.provider.(interface{ Shutdown(context.Context) error }); ok {
		return s.Shutdown(ctx)
	}
	return nil
}
