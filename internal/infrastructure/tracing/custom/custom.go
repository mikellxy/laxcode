package custom

// 本包供使用方实现自己的 trace.TracerProvider（其 Span.End 即上报触发点）。
// 内置组合根根据 OTEL_EXPORTER_OTLP_ENDPOINT 选择 OTLP/HTTP 或 filetrace；
// 需要自定义 provider 的使用方可直接用 tracing.New 构造 Handle。
//
// 例如：
//
//	h := tracing.New(myProvider{})
