package handler

import (
	"log/slog"
	"net/http"

	"github.com/donaldgifford/rfc-api/internal/server/reqctx"
)

// Webhook holds the GitHub webhook handler. HMAC verification runs
// ahead of this handler as per-route middleware (see
// internal/server/middleware/githubhmac.go); by the time control
// reaches here the signature has been validated and r.Body has been
// reset to a fresh reader.
type Webhook struct {
	logger *slog.Logger
}

// NewWebhook constructs a Webhook handler.
func NewWebhook(logger *slog.Logger) *Webhook {
	if logger == nil {
		logger = slog.Default()
	}
	return &Webhook{logger: logger}
}

// GitHub serves POST /api/v1/webhooks/github.
//
// Phase 2 scope: log the event type and delivery id, return 202. The
// real enqueue lands with the sync worker (separate design doc).
func (h *Webhook) GitHub(w http.ResponseWriter, r *http.Request) {
	event := r.Header.Get("X-GitHub-Event")
	delivery := r.Header.Get("X-GitHub-Delivery")

	h.logger.InfoContext(r.Context(), "github webhook accepted",
		"github.event", event,
		"github.delivery", delivery,
		"request_id", reqctx.ID(r.Context()),
	)
	w.WriteHeader(http.StatusAccepted)
}
