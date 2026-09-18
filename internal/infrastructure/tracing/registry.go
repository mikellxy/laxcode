package tracing

// HandleDB 保留给需要自行管理 provider 注册表的嵌入方。内置组合根不读取它，
// 而是根据 OTEL_EXPORTER_OTLP_ENDPOINT 显式选择 OTLP/HTTP 或 filetrace。
var HandleDB = map[string]*Handle{}

// Register 把命名 Handle 注入 HandleDB，通常由 custom 实现的 init 调用。
func Register(name string, h *Handle) {
	HandleDB[name] = h
}
