// Package handler owns the HTTP-facing surface. One file per resource
// per DESIGN-0001: docs, search, types, webhook. Handlers parse input
// (path values + query string), call the service layer, and render
// output or delegate to httperr on error. No SQL, no outbound HTTP,
// no parsing logic inline.
//
// Handlers read route metadata from routectx.From — never from
// r.Pattern — so the type id and matched pattern have a single
// source of truth (DESIGN-0001 #Handler pattern).
package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/docid"
	"github.com/donaldgifford/rfc-api/internal/server/cursor"
	"github.com/donaldgifford/rfc-api/internal/server/httperr"
	"github.com/donaldgifford/rfc-api/internal/server/render"
	"github.com/donaldgifford/rfc-api/internal/server/routectx"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store"
	"github.com/donaldgifford/rfc-api/internal/store/list"
)

// Docs holds the Docs-service handler methods.
//
// The registry lives here (not behind the service) because every
// filter / sort gate in IMPL-0007 is a query-string concern: the
// handler needs to translate `filter=type:rfc` into a 400 response
// when "rfc" is not registered, *before* a call ever reaches the
// service. Pushing this validation through the service would
// silently turn parse errors into "no matches" results.
type Docs struct {
	svc *service.Docs
	reg domain.DocumentTypeRegistry
}

// NewDocs constructs a Docs handler over the given service and
// type registry. The registry is the source of truth for filter
// value validation (DESIGN-0003 #Type-filter-validation); the
// service uses its own registry copy for its own checks (e.g.
// ListByType).
func NewDocs(svc *service.Docs, reg domain.DocumentTypeRegistry) *Docs {
	return &Docs{svc: svc, reg: reg}
}

// Get serves GET /api/v1/{type}/{id}. Reconstructs the canonical
// display id from the route-segment type and the URL numeric id via
// docid.Canonical.
func (h *Docs) Get(w http.ResponseWriter, r *http.Request) {
	route, _ := routectx.From(r.Context())
	id := docid.Canonical(route.TypeID, r.PathValue("id"))

	doc, err := h.svc.Get(r.Context(), id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	render.JSON(w, http.StatusOK, doc)
}

// ListByType serves GET /api/v1/{type}. Paginated; headers carry
// total + next-link.
func (h *Docs) ListByType(w http.ResponseWriter, r *http.Request) {
	route, _ := routectx.From(r.Context())
	limit, cur, err := parseListQuery(r)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	page, err := h.svc.ListByType(r.Context(), route.TypeID, limit, cur)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	writePage(w, r, page)
}

// ListAll serves GET /api/v1/docs — the cross-type aggregation.
// Phase 3 wires the ?filter= + ?sort= contract from DESIGN-0003: the
// query parsers validate every input against the document-type
// registry + Sort enum and emit problem+json on any malformation; the
// response's X-Total-Count-Unfiltered header is set only when at
// least one filter is active.
func (h *Docs) ListAll(w http.ResponseWriter, r *http.Request) {
	q, err := parseListAllQuery(r, h.reg)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	page, err := h.svc.ListAll(r.Context(), q.Limit, q.Cursor, q.TypeIDs, q.Sort)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	var unfilteredTotal *int
	if q.HasFilter {
		n, err := h.svc.CountAll(r.Context())
		if err != nil {
			httperr.Write(w, r, err)
			return
		}
		unfilteredTotal = &n
	}
	writePageWithUnfiltered(w, r, page, unfilteredTotal)
}

// Links serves GET /api/v1/{type}/{id}/links.
func (h *Docs) Links(w http.ResponseWriter, r *http.Request) {
	route, _ := routectx.From(r.Context())
	id := docid.Canonical(route.TypeID, r.PathValue("id"))

	links, err := h.svc.Links(r.Context(), id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if links == nil {
		links = []domain.Link{}
	}
	render.JSON(w, http.StatusOK, links)
}

// Discussion serves GET /api/v1/{type}/{id}/discussion.
func (h *Docs) Discussion(w http.ResponseWriter, r *http.Request) {
	route, _ := routectx.From(r.Context())
	id := docid.Canonical(route.TypeID, r.PathValue("id"))

	d, err := h.svc.Discussion(r.Context(), id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	render.JSON(w, http.StatusOK, d)
}

// Authors serves GET /api/v1/{type}/{id}/authors.
func (h *Docs) Authors(w http.ResponseWriter, r *http.Request) {
	route, _ := routectx.From(r.Context())
	id := docid.Canonical(route.TypeID, r.PathValue("id"))

	authors, err := h.svc.Authors(r.Context(), id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if authors == nil {
		authors = []domain.Author{}
	}
	render.JSON(w, http.StatusOK, authors)
}

// Revisions serves GET /api/v1/{type}/{id}/revisions.
func (h *Docs) Revisions(w http.ResponseWriter, r *http.Request) {
	route, _ := routectx.From(r.Context())
	id := docid.Canonical(route.TypeID, r.PathValue("id"))

	revs, err := h.svc.Revisions(r.Context(), id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	render.JSON(w, http.StatusOK, revs)
}

// parseListQuery reads and validates the two pagination query params
// that every list endpoint accepts. Returns domain.ErrInvalidInput
// for out-of-range limits or malformed cursors.
func parseListQuery(r *http.Request) (int, *list.Cursor, error) {
	q := r.URL.Query()

	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > service.MaxListLimit {
			return 0, nil, fmt.Errorf("%w: limit must be 1..%d", domain.ErrInvalidInput, service.MaxListLimit)
		}
		limit = n
	}

	cur, err := cursor.Decode(q.Get("cursor"))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	return limit, cur, nil
}

// writePage serializes a store.Page as a bare JSON array with Link +
// X-Total-Count headers per the Resolved Decision envelope rule.
func writePage(w http.ResponseWriter, r *http.Request, page store.Page) {
	writePageWithUnfiltered(w, r, page, nil)
}

// writePageWithUnfiltered is writePage plus the
// X-Total-Count-Unfiltered seam introduced by IMPL-0007. When
// unfilteredTotal is non-nil, render.ArrayJSON sets that header so
// clients can distinguish "filter matched zero rows" from "no
// documents exist at all" without a follow-up request. nil keeps
// today's behavior — the header is omitted entirely.
func writePageWithUnfiltered(
	w http.ResponseWriter,
	r *http.Request,
	page store.Page,
	unfilteredTotal *int,
) {
	info := render.PageInfo{
		Total:           page.Total,
		NextCursor:      cursor.Encode(page.NextCursor),
		TotalUnfiltered: unfilteredTotal,
	}
	render.ArrayJSON(w, r, page.Items, info)
}
