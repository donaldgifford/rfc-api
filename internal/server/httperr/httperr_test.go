package httperr_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/server/httperr"
	"github.com/donaldgifford/rfc-api/internal/server/reqctx"
)

func TestWrite_SentinelMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
	}{
		{"not found", domain.ErrNotFound, http.StatusNotFound, "/problems/not-found"},
		{"invalid input", domain.ErrInvalidInput, http.StatusBadRequest, "/problems/invalid-input"},
		{"conflict", domain.ErrConflict, http.StatusConflict, "/problems/conflict"},
		{"upstream", domain.ErrUpstream, http.StatusBadGateway, "/problems/upstream"},
		{"unknown defaults 500", errors.New("something else"), http.StatusInternalServerError, "/problems/internal"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/docs/RFC-9999", http.NoBody)

			httperr.Write(rr, r, tc.err)

			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			if got := rr.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", got)
			}

			var p httperr.Problem
			if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
				t.Fatalf("unmarshal problem: %v (body=%q)", err, rr.Body.String())
			}
			if p.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", p.Type, tc.wantType)
			}
			if p.Status != tc.wantStatus {
				t.Errorf("Status = %d, want %d", p.Status, tc.wantStatus)
			}
			if p.Instance != "/api/v1/docs/RFC-9999" {
				t.Errorf("Instance = %q, want %q", p.Instance, "/api/v1/docs/RFC-9999")
			}
		})
	}
}

// Wrapped errors (%w chains) classify the same as the sentinel.
func TestWrite_WrapsClassifyByRoot(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("service lookup: %w", domain.ErrNotFound)

	rr := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/docs/RFC-0001", http.NoBody)
	httperr.Write(rr, r, wrapped)

	if rr.Code != http.StatusNotFound {
		t.Errorf("wrapped %%w error status = %d, want 404", rr.Code)
	}
}

// 500 responses must NOT surface the raw error message -- could leak
// paths, SQL, or stack fragments. Lower-tier responses may.
func TestWrite_DetailSafetyFor500(t *testing.T) {
	t.Parallel()

	secretErr := errors.New("pg: SELECT * FROM users WHERE token='hunter2'")

	rr := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/docs/RFC-0001", http.NoBody)
	httperr.Write(rr, r, secretErr)

	body := rr.Body.String()
	if strings.Contains(body, "hunter2") {
		t.Errorf("500 response body leaks raw error: %s", body)
	}
	if strings.Contains(body, "SELECT") {
		t.Errorf("500 response body leaks SQL fragment: %s", body)
	}

	var p httperr.Problem
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Detail != "an internal error occurred" {
		t.Errorf("500 Detail = %q, want the fixed safe string", p.Detail)
	}
}

// Classified (<500) responses surface the error message in Detail so
// clients can act on them ("no document with id RFC-9999").
func TestWrite_ClassifiedDetailSurfacesMessage(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("no document with id RFC-9999: %w", domain.ErrNotFound)

	rr := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/docs/RFC-9999", http.NoBody)
	httperr.Write(rr, r, err)

	var p httperr.Problem
	if uerr := json.Unmarshal(rr.Body.Bytes(), &p); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if !strings.Contains(p.Detail, "RFC-9999") {
		t.Errorf("Detail = %q, want to contain the id", p.Detail)
	}
}

// request_id from context is echoed in the problem body.
func TestWrite_IncludesRequestIDFromContext(t *testing.T) {
	t.Parallel()

	const reqID = "01HX-TEST-REQ-ID"

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/docs", http.NoBody)
	r = r.WithContext(reqctx.WithID(r.Context(), reqID))

	rr := httptest.NewRecorder()
	httperr.Write(rr, r, domain.ErrNotFound)

	var p httperr.Problem
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.RequestID != reqID {
		t.Errorf("RequestID = %q, want %q", p.RequestID, reqID)
	}
}

// No request id on context -> empty string (omitempty in JSON).
func TestWrite_OmitsRequestIDWhenAbsent(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/docs", http.NoBody)
	rr := httptest.NewRecorder()
	httperr.Write(rr, r, domain.ErrNotFound)

	// Raw body check because Problem.RequestID with omitempty and ""
	// should not appear as a key.
	if strings.Contains(rr.Body.String(), `"request_id"`) {
		t.Errorf("expected request_id omitted; body = %s", rr.Body.String())
	}
}
