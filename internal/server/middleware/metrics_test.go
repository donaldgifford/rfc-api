package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/obs"
	"github.com/donaldgifford/rfc-api/internal/server/middleware"
	"github.com/donaldgifford/rfc-api/internal/server/routectx"
)

func TestMetrics_RecordsLabels(t *testing.T) {
	m := obs.NewMetrics()
	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := routectx.With(r.Context(), "rfc", "/api/v1/rfc/{id}")
		_ = ctx // triggers Capture write
		routectx.With(r.Context(), "rfc", "/api/v1/rfc/{id}")
		w.WriteHeader(http.StatusAccepted)
	})
	h := middleware.Metrics(m)(innerHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/rfc/0001", http.NoBody))
	if rec.Code != 202 {
		t.Fatalf("status = %d", rec.Code)
	}

	// Scrape the registry and look for the labelled counter line.
	dumpRec := httptest.NewRecorder()
	m.Handler().ServeHTTP(dumpRec, httptest.NewRequestWithContext(t.Context(), "GET", "/", http.NoBody))
	body := dumpRec.Body.String()
	wantSubs := []string{
		`rfc_api_http_requests_total{`,
		`route="/api/v1/rfc/{id}"`,
		`status="202"`,
		`method="GET"`,
	}
	for _, sub := range wantSubs {
		if !strings.Contains(body, sub) {
			t.Errorf("metrics output missing %q\nbody = %s", sub, body)
		}
	}
}

func TestMetrics_NilDisablesMiddleware(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	h := middleware.Metrics(nil)(inner)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), "GET", "/x", http.NoBody))
	if !called {
		t.Error("inner handler not invoked")
	}
}
