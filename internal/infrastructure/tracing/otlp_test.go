package tracing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestNewOTLPExportsTraceWithServiceName(t *testing.T) {
	requests := make(chan *collectortracepb.ExportTraceServiceRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("path = %q, want /v1/traces", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := new(collectortracepb.ExportTraceServiceRequest)
		if err := proto.Unmarshal(body, req); err != nil {
			t.Errorf("Unmarshal: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- req
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "")
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "none")
	t.Setenv("OTEL_SERVICE_NAME", "laxcode-test")

	handle, err := NewOTLP(context.Background())
	if err != nil {
		t.Fatalf("NewOTLP: %v", err)
	}
	ctx, parent := handle.Tracer.Start(context.Background(), "ReAct")
	_, child := handle.Tracer.Start(ctx, "llm-generate")
	child.End()
	parent.End()
	if err := handle.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case req := <-requests:
		if len(req.ResourceSpans) != 1 {
			t.Fatalf("ResourceSpans = %d, want 1", len(req.ResourceSpans))
		}
		resourceSpans := req.ResourceSpans[0]
		var serviceName string
		for _, attr := range resourceSpans.Resource.Attributes {
			if attr.Key == "service.name" {
				serviceName = attr.Value.GetStringValue()
			}
		}
		if serviceName != "laxcode-test" {
			t.Errorf("service.name = %q, want laxcode-test", serviceName)
		}
		spans := resourceSpans.ScopeSpans[0].Spans
		if len(spans) != 2 {
			t.Fatalf("spans = %d, want 2", len(spans))
		}
		byName := make(map[string][]byte, len(spans))
		var childParent []byte
		for _, span := range spans {
			byName[span.Name] = span.SpanId
			if span.Name == "llm-generate" {
				childParent = span.ParentSpanId
			}
		}
		if string(childParent) != string(byName["ReAct"]) {
			t.Errorf("llm-generate parent does not match ReAct")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OTLP export")
	}
}
