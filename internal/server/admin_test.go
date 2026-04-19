package server_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/server"
)

// freePort returns a currently-unused TCP port. Inherent TOCTOU, but
// good enough for tests that bind immediately after.
func freePort(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// quietLogger discards output so tests don't flood stdout.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitForURL polls url until it 200s or deadline expires.
func waitForURL(t *testing.T, url string, deadline time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("url %q not ready within %v", url, deadline)
}

func TestAdminServer_Endpoints(t *testing.T) {
	addr := freePort(t)
	srv := server.NewAdmin(
		config.Admin{
			Listen:       addr,
			ReadTimeout:  5 * time.Second,
			PprofEnabled: false,
		},
		[]server.ReadinessProbe{server.AlwaysReady{}},
		noop.NewTracerProvider(),
		quietLogger(),
	)

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	base := "http://" + addr
	waitForURL(t, base+"/healthz", 2*time.Second)

	type urlCase struct {
		path       string
		wantStatus int
		wantInBody string
		wantCType  string
	}
	cases := []urlCase{
		{
			path:       "/healthz",
			wantStatus: http.StatusOK,
			wantInBody: `"status":"ok"`,
			wantCType:  "application/json",
		},
		{
			path:       "/readyz",
			wantStatus: http.StatusOK,
			wantInBody: `"status":"ready"`,
			wantCType:  "application/json",
		},
		{
			path:       "/metrics",
			wantStatus: http.StatusOK,
			wantInBody: "go_gc_duration_seconds",
		},
		{
			path:       "/debug/pprof/",
			wantStatus: http.StatusNotFound, // PprofEnabled=false -> unregistered
		},
	}

	client := &http.Client{Timeout: 2 * time.Second}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+tc.path, http.NoBody)
			if err != nil {
				t.Fatalf("build req: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("%s status = %d, want %d", tc.path, resp.StatusCode, tc.wantStatus)
			}
			body, _ := io.ReadAll(resp.Body)
			if tc.wantInBody != "" && !strings.Contains(string(body), tc.wantInBody) {
				t.Errorf("%s body missing %q; got %q", tc.path, tc.wantInBody, string(body))
			}
			if tc.wantCType != "" && !strings.HasPrefix(resp.Header.Get("Content-Type"), tc.wantCType) {
				t.Errorf("%s Content-Type = %q, want prefix %q",
					tc.path, resp.Header.Get("Content-Type"), tc.wantCType)
			}
		})
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start() error = %v, want nil on ctx-cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not return after ctx cancel")
	}
}

func TestAdminServer_PprofEnabled(t *testing.T) {
	addr := freePort(t)
	srv := server.NewAdmin(
		config.Admin{Listen: addr, ReadTimeout: 5 * time.Second, PprofEnabled: true},
		[]server.ReadinessProbe{server.AlwaysReady{}},
		noop.NewTracerProvider(),
		quietLogger(),
	)

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	base := "http://" + addr
	waitForURL(t, base+"/healthz", 2*time.Second)

	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/debug/pprof/", http.NoBody)
	if err != nil {
		t.Fatalf("build req: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("/debug/pprof/ status = %d, want 200 when PprofEnabled=true", resp.StatusCode)
	}

	cancel()
	<-errCh
}
