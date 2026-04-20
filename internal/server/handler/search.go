package handler

import (
	"net/http"

	"github.com/donaldgifford/rfc-api/internal/search"
	"github.com/donaldgifford/rfc-api/internal/server/httperr"
	"github.com/donaldgifford/rfc-api/internal/server/render"
	"github.com/donaldgifford/rfc-api/internal/service"
)

// Search holds the cross-type search handler methods.
type Search struct {
	svc *service.Search
}

// NewSearch constructs a Search handler over the given service.
func NewSearch(svc *service.Search) *Search { return &Search{svc: svc} }

// Query serves GET /api/v1/search. Accepts q, type, limit, cursor;
// all non-q parameters are optional. An empty q is allowed (returns
// an empty page).
func (h *Search) Query(w http.ResponseWriter, r *http.Request) {
	limit, _, err := parseListQuery(r)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	q := r.URL.Query()
	page, err := h.svc.Query(r.Context(), search.Query{
		Q:      q.Get("q"),
		TypeID: q.Get("type"),
		Limit:  limit,
		Cursor: q.Get("cursor"),
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	render.ArrayJSON(w, r, page.Hits, render.PageInfo{
		Total:      page.Total,
		NextCursor: page.NextCursor,
	})
}
