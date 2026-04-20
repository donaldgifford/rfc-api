package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/server"
)

func TestMainServer_CatchAllReturnsRFC7807_404(t *testing.T) {
	addr := freePort(t)
	srv := server.New(&server.Deps{
		Config: config.Server{
			Listen:       addr,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		},
		TracerProvider: noop.NewTracerProvider(),
		Logger:         quietLogger(),
	})

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	base := "http://" + addr
	waitForMainServer(t, base)

	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/not-a-real-path", http.NoBody)
	if err != nil {
		t.Fatalf("build req: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}

	body, _ := io.ReadAll(resp.Body)
	var p struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v (body=%q)", err, string(body))
	}
	if p.Type != "/problems/not-found" {
		t.Errorf("Type = %q, want /problems/not-found", p.Type)
	}
	if p.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want 404", p.Status)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start() error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not return after ctx cancel")
	}
}

func TestMainServer_EchoesXRequestID(t *testing.T) {
	addr := freePort(t)
	srv := server.New(&server.Deps{
		Config: config.Server{
			Listen:       addr,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		},
		TracerProvider: noop.NewTracerProvider(),
		Logger:         quietLogger(),
	})

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	base := "http://" + addr
	waitForMainServer(t, base)

	client := &http.Client{Timeout: 2 * time.Second}

	t.Run("client id echoed", func(t *testing.T) {
		const id = "test-client-supplied-id"
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/any", http.NoBody)
		if err != nil {
			t.Fatalf("build req: %v", err)
		}
		req.Header.Set("X-Request-ID", id)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if got := resp.Header.Get("X-Request-ID"); got != id {
			t.Errorf("X-Request-ID = %q, want %q", got, id)
		}
	})

	t.Run("server generates id when absent", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/any", http.NoBody)
		if err != nil {
			t.Fatalf("build req: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if got := resp.Header.Get("X-Request-ID"); got == "" {
			t.Errorf("X-Request-ID = empty, want generated id")
		}
	})

	cancel()
	<-errCh
}

// waitForMainServer polls /any-path until the server responds (with
// any status -- we just need to know it's accepting connections).
func waitForMainServer(t *testing.T, base string) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for range 40 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/any", http.NoBody)
		if err != nil {
			t.Fatalf("build req: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("main server %q not ready", base)
}
