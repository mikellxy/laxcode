package tracing

import (
	"context"
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const defaultServiceName = "laxcode"

// NewOTLP constructs a batched OTLP/HTTP trace exporter. Endpoint, headers,
// TLS and compression are configured through the standard OTEL_EXPORTER_OTLP_*
// environment variables.
func NewOTLP(ctx context.Context) (*Handle, error) {
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}

	resourceOptions := []resource.Option{
		resource.WithAttributes(attribute.String("service.name", defaultServiceName)),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	}
	if serviceName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); serviceName != "" {
		resourceOptions = append(resourceOptions,
			resource.WithAttributes(attribute.String("service.name", serviceName)))
	}
	res, err := resource.New(ctx, resourceOptions...)
	if err != nil {
		_ = exporter.Shutdown(ctx)
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
	)
	return New(provider), nil
}
