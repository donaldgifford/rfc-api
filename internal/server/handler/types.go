package handler

import (
	"net/http"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/server/render"
)

// Types holds the document-type registry handler.
type Types struct {
	registry domain.DocumentTypeRegistry
}

// NewTypes constructs a Types handler over the given registry.
func NewTypes(r domain.DocumentTypeRegistry) *Types { return &Types{registry: r} }

// typeDTO is the wire shape for /api/v1/types responses, per
// DESIGN-0001 #API / Interface Changes.
type typeDTO struct {
	ID            string   `json:"id"`
	DisplayPrefix string   `json:"display_prefix"`
	Title         string   `json:"title"`
	Statuses      []string `json:"statuses"`
}

// List serves GET /api/v1/types. Pure registry read — no DB, no
// cache. The response always fits in a single payload because the
// registered type count is small by design.
func (h *Types) List(w http.ResponseWriter, _ *http.Request) {
	items := h.registry.List()
	out := make([]typeDTO, len(items))
	for i := range items {
		statuses := items[i].Lifecycle
		if statuses == nil {
			statuses = []string{}
		}
		out[i] = typeDTO{
			ID:            items[i].ID,
			DisplayPrefix: items[i].Prefix,
			Title:         items[i].Name,
			Statuses:      statuses,
		}
	}
	render.JSON(w, http.StatusOK, out)
}
