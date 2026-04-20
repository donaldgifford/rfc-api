// Package obs wires rfc-api's observability primitives (tracing now,
// metrics polish in Phase 3).
//
// Tracing: OpenTelemetry TracerProvider backed by an OTLP/gRPC
// exporter when OTEL_EXPORTER_OTLP_ENDPOINT is set; a no-op provider
// otherwise, so dev can run without a collector.
package obs

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/donaldgifford/rfc-api/internal/config"
)

// ServiceName is the OTel service.name attribute applied to every
// span / metric emitted by this process.
const ServiceName = "rfc-api"

// TracerProvider wraps the active OTel TracerProvider along with a
// Shutdown hook that flushes and closes the underlying exporter.
// Callers should defer Shutdown so exported spans make it to the
// collector before the process exits.
type TracerProvider struct {
	tp       trace.TracerProvider
	shutdown func(context.Context) error
}

// NewTracerProvider constructs a TracerProvider from the supplied
// OTel config. When cfg.OTLPEndpoint is empty, returns a no-op
// provider (Shutdown is a no-op too); otherwise builds an SDK
// provider that ships via OTLP/gRPC.
//
// The returned provider is also installed globally (otel.SetTracerProvider)
// so third-party packages that use otel.Tracer() pick it up.
//
// version / commit are used to populate resource attributes so
// backend filters can correlate spans to a build.
func NewTracerProvider(ctx context.Context, cfg config.OTel, version, commit string) (*TracerProvider, error) {
	if cfg.OTLPEndpoint == "" {
		tp := noop.NewTracerProvider()
		otel.SetTracerProvider(tp)
		return &TracerProvider{
			tp:       tp,
			shutdown: func(_ context.Context) error { return nil },
		}, nil
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(stripScheme(cfg.OTLPEndpoint)),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("build OTLP gRPC exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(ServiceName),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		return nil, fmt.Errorf("merge OTel resource: %w", err)
	}

	sampler := sdktrace.ParentBased(
		sdktrace.TraceIDRatioBased(cfg.TraceSampleRatio),
	)

	sdkTP := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	otel.SetTracerProvider(sdkTP)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Unused outside of closure shape; silences staticcheck if commit
	// never flows into a resource attr.
	_ = commit

	return &TracerProvider{
		tp: sdkTP,
		shutdown: func(shutdownCtx context.Context) error {
			if err := sdkTP.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("sdk tracer shutdown: %w", err)
			}
			if err := exp.Shutdown(shutdownCtx); err != nil {
				// otlptrace.Exporter.Shutdown is idempotent; a
				// second error is rarely new info.
				return fmt.Errorf("exporter shutdown: %w", err)
			}
			return nil
		},
	}, nil
}

// Provider returns the underlying TracerProvider for handing to
// otelhttp.NewHandler or any code that needs tracer access.
func (p *TracerProvider) Provider() trace.TracerProvider {
	return p.tp
}

// Shutdown flushes queued spans and closes the exporter. Safe to call
// multiple times; no-op after the first successful shutdown. Respect
// the supplied ctx deadline so shutdown doesn't hang a Kubernetes
// termination.
func (p *TracerProvider) Shutdown(ctx context.Context) error {
	return p.shutdown(ctx)
}

// Exporter is re-exported to help callers avoid an otlptrace import
// for type assertions in tests.
type Exporter = otlptrace.Exporter

// stripScheme removes http:// or https:// prefix from an endpoint
// so otlptracegrpc.WithEndpoint receives bare host:port.
// OTEL_EXPORTER_OTLP_ENDPOINT is commonly written with a scheme
// (http://localhost:4317) per the OTel SDK spec; the gRPC exporter
// expects host:port and chooses transport via WithInsecure.
func stripScheme(endpoint string) string {
	for _, prefix := range []string{"http://", "https://"} {
		if len(endpoint) > len(prefix) && endpoint[:len(prefix)] == prefix {
			return endpoint[len(prefix):]
		}
	}
	return endpoint
}
