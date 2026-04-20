package service_test

import (
	"errors"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/service"
	"github.com/donaldgifford/rfc-api/internal/store"
	"github.com/donaldgifford/rfc-api/internal/store/memory"
)

func newFixture(t *testing.T) *service.Docs {
	t.Helper()
	mem := memory.New()
	now := time.Now().UTC()
	must(t, mem.Add(&domain.Document{ID: "RFC-0001", Type: "rfc", CreatedAt: now, UpdatedAt: now}))
	must(t, mem.Add(&domain.Document{ID: "ADR-0001", Type: "adr", CreatedAt: now.Add(time.Minute), UpdatedAt: now}))

	reg, err := registry.New([]config.DocumentType{
		{ID: "rfc", Name: "RFCs", Prefix: "RFC"},
		{ID: "adr", Name: "ADRs", Prefix: "ADR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service.NewDocs(mem, reg)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestDocsGet_Happy(t *testing.T) {
	d := newFixture(t)
	doc, err := d.Get(t.Context(), "RFC-0001")
	if err != nil {
		t.Fatal(err)
	}
	if doc.ID != "RFC-0001" {
		t.Errorf("want RFC-0001, got %q", doc.ID)
	}
}

func TestDocsGet_NotFound(t *testing.T) {
	d := newFixture(t)
	_, err := d.Get(t.Context(), "RFC-9999")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDocsListByType_UnknownType(t *testing.T) {
	d := newFixture(t)
	_, err := d.ListByType(t.Context(), "nope", 10, nil)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestDocsListByType_Happy(t *testing.T) {
	d := newFixture(t)
	page, err := d.ListByType(t.Context(), "rfc", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Errorf("Total = %d, want 1", page.Total)
	}
}

func TestDocsListAll(t *testing.T) {
	d := newFixture(t)
	page, err := d.ListAll(t.Context(), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Errorf("Total = %d, want 2", page.Total)
	}
}

func TestDocsList_LimitClamp(t *testing.T) {
	// Ask for 0 / negative / > 200; service should still succeed.
	d := newFixture(t)
	for _, lim := range []int{0, -5, 9999} {
		if _, err := d.ListAll(t.Context(), lim, nil); err != nil {
			t.Errorf("limit=%d: %v", lim, err)
		}
	}
}

func TestDocsListByType_Cursor(t *testing.T) {
	mem := memory.New()
	// 3 RFCs so we can page through.
	base := time.Now().UTC()
	for i, id := range []domain.DocumentID{"RFC-0001", "RFC-0002", "RFC-0003"} {
		must(t, mem.Add(&domain.Document{
			ID: id, Type: "rfc",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
			UpdatedAt: base,
		}))
	}
	reg, err := registry.New([]config.DocumentType{{ID: "rfc", Name: "RFCs", Prefix: "RFC"}})
	if err != nil {
		t.Fatal(err)
	}
	d := service.NewDocs(mem, reg)

	first, err := d.ListByType(t.Context(), "rfc", 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == nil {
		t.Fatal("want next cursor")
	}
	second, err := d.ListByType(t.Context(), "rfc", 2, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(second.Items); got != 1 {
		t.Errorf("second page len = %d, want 1", got)
	}
}

var _ = store.ListQuery{} // compile-time import retention
