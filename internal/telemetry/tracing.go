package telemetry

import (
	"context"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Tracing 管理进程级 Trace Provider 的安装和关闭。
type Tracing struct {
	provider trace.TracerProvider
	shutdown func(context.Context) error
	enabled  bool
}

// NewTracing 按标准 OTLP 环境变量创建 Trace Provider，未配置端点时保持无操作。
func NewTracing(ctx context.Context, version string) (*Tracing, error) {
	if !traceEndpointConfigured() {
		return &Tracing{
			provider: trace.NewNoopTracerProvider(),
			shutdown: func(context.Context) error { return nil },
		}, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	serviceResource, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", "limen"),
		attribute.String("service.version", strings.TrimSpace(version)),
	))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(serviceResource),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	return &Tracing{provider: provider, shutdown: provider.Shutdown, enabled: true}, nil
}

// Install 安装 Trace Provider，并只启用不携带业务正文的 W3C Trace Context 传播。
func (tracing *Tracing) Install() {
	if tracing == nil || tracing.provider == nil {
		return
	}
	otel.SetTracerProvider(tracing.provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

// Enabled 表示当前进程是否启用了 OTLP Trace 导出。
func (tracing *Tracing) Enabled() bool {
	return tracing != nil && tracing.enabled
}

// Shutdown 在给定期限内刷新并关闭 Trace Provider。
func (tracing *Tracing) Shutdown(ctx context.Context) error {
	if tracing == nil || tracing.shutdown == nil {
		return nil
	}
	return tracing.shutdown(ctx)
}

// traceEndpointConfigured 判断运维人员是否显式配置了 OTLP 端点。
func traceEndpointConfigured() bool {
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" ||
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != ""
}
