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
