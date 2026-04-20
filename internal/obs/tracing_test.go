package obs_test

import (
	"testing"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/obs"
)

// Empty endpoint returns a no-op provider. Shutdown is a no-op too;
// tests must not hang.
func TestNewTracerProvider_NoEndpointIsNoop(t *testing.T) {
	t.Parallel()

	tp, err := obs.NewTracerProvider(
		t.Context(),
		config.OTel{OTLPEndpoint: "", TraceSampleRatio: 0.1},
		"test-version", "test-commit",
	)
	if err != nil {
		t.Fatalf("NewTracerProvider() error = %v, want nil", err)
	}
	if tp == nil {
		t.Fatal("NewTracerProvider() = nil, want provider")
	}
	if tp.Provider() == nil {
		t.Fatal("Provider() = nil, want a TracerProvider")
	}
	if err := tp.Shutdown(t.Context()); err != nil {
		t.Errorf("Shutdown() error = %v, want nil for no-op provider", err)
	}
}

func TestNewTracerProvider_SDKWhenEndpointSet(t *testing.T) {
	t.Parallel()

	// otlptracegrpc.New defers the dial; an unreachable endpoint
	// still constructs successfully. This exercises the SDK branch
	// (exporter + resource + sampler + batcher) for coverage.
	tp, err := obs.NewTracerProvider(
		t.Context(),
		config.OTel{OTLPEndpoint: "http://127.0.0.1:14317", TraceSampleRatio: 0.05},
		"test-version", "test-commit",
	)
	if err != nil {
		t.Fatalf("NewTracerProvider(sdk) error = %v", err)
	}
	if err := tp.Shutdown(t.Context()); err != nil {
		// Unreachable collector can surface an error on shutdown;
		// the path is still exercised.
		t.Logf("Shutdown(sdk) error = %v (non-fatal)", err)
	}
}
