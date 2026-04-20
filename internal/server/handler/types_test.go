package handler_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/server/handler"
)

func TestTypesList(t *testing.T) {
	reg, err := registry.New([]config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC", Lifecycle: []string{"Draft", "Accepted"}},
		{ID: "adr", Name: "ADRs", Prefix: "ADR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := handler.NewTypes(reg)

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/types", nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0]["id"] != "rfc" || out[0]["display_prefix"] != "RFC" {
		t.Errorf("body = %+v", out)
	}
}
