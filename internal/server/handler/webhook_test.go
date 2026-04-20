package handler_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/handler"
)

func TestWebhookGitHub_Accepts(t *testing.T) {
	h := handler.NewWebhook(nil)
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/webhooks/github",
		strings.NewReader(`{"ok":true}`))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "abc-123")

	rec := httptest.NewRecorder()
	h.GitHub(rec, req)
	if rec.Code != 202 {
		t.Errorf("status = %d, want 202", rec.Code)
	}
}
