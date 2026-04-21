package worker_test

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/obs"
	"github.com/donaldgifford/rfc-api/internal/worker"
)

func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.New([]config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC"},
		{ID: "adr", Name: "ADRs", Prefix: "ADR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseDeps(t *testing.T) *worker.Deps {
	t.Helper()
	return &worker.Deps{
		Config: config.Worker{
			AdminListen:           "127.0.0.1:0",
			ScannerInterval:       time.Minute,
			ProcessorPollInterval: time.Second,
			MaxConcurrent:         1,
		},
		Registry:       testRegistry(t),
		TracerProvider: noop.NewTracerProvider(),
		Metrics:        obs.NewMetrics(),
		Logger:         silentLogger(),
	}
}

func TestNew_NilDeps(t *testing.T) {
	if _, err := worker.New(nil); err == nil {
		t.Fatal("want non-nil error on nil deps")
	}
}

func TestNew_RejectsUnknownType(t *testing.T) {
	deps := baseDeps(t)
	deps.Config.SourceRepos = []config.SourceRepo{{
		TypeID: "unknown",
		Repo:   "owner/repo",
		Path:   "docs/",
	}}
	_, err := worker.New(deps)
	if err == nil {
		t.Fatal("want error on unknown type_id")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNew_RejectsMissingRepoFields(t *testing.T) {
	cases := []struct {
		name string
		src  config.SourceRepo
		want string
	}{
		{"missing type_id", config.SourceRepo{Repo: "o/r", Path: "p"}, "type_id is required"},
		{"missing repo", config.SourceRepo{TypeID: "rfc", Path: "p"}, "repo is required"},
		{"missing path", config.SourceRepo{TypeID: "rfc", Repo: "o/r"}, "path is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := baseDeps(t)
			deps.Config.SourceRepos = []config.SourceRepo{tc.src}
			_, err := worker.New(deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestNew_RequiresPool(t *testing.T) {
	deps := baseDeps(t)
	// Empty source list passes validation; then pool check trips.
	_, err := worker.New(deps)
	if err == nil || !strings.Contains(err.Error(), "nil pool") {
		t.Fatalf("want 'nil pool' error, got %v", err)
	}
}
