package render_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/render"
)

func TestJSON_SetsContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	render.JSON(rec, 201, map[string]string{"hello": "world"})
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if rec.Code != 201 {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["hello"] != "world" {
		t.Errorf("body = %+v", out)
	}
}

func TestArrayJSON_HeadersAndBody(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/rfc?limit=2", nil)
	rec := httptest.NewRecorder()
	items := []map[string]string{{"id": "RFC-0001"}, {"id": "RFC-0002"}}
	render.ArrayJSON(rec, req, items, render.PageInfo{
		Total:      5,
		NextCursor: "abc",
	})
	if got := rec.Header().Get("X-Total-Count"); got != "5" {
		t.Errorf("X-Total-Count = %q", got)
	}
	link := rec.Header().Get("Link")
	if link == "" {
		t.Fatal("Link header missing")
	}
	// Must carry the updated cursor query param and the rel=next.
	wantSubs := []string{`rel="next"`, "cursor=abc", "/api/v1/rfc"}
	for _, sub := range wantSubs {
		if !contains(link, sub) {
			t.Errorf("Link %q missing %q", link, sub)
		}
	}

	var out []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Errorf("body len = %d", len(out))
	}
}

func TestArrayJSON_NoCursorNoLink(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/rfc", nil)
	rec := httptest.NewRecorder()
	render.ArrayJSON(rec, req, []any{}, render.PageInfo{Total: 0})
	if rec.Header().Get("Link") != "" {
		t.Errorf("Link should be absent for empty pagination")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
