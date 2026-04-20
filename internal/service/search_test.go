package service_test

import (
	"errors"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
	"github.com/donaldgifford/rfc-api/internal/search"
	"github.com/donaldgifford/rfc-api/internal/service"
)

func newSearch(t *testing.T) *service.Search {
	t.Helper()
	reg, err := registry.New([]config.DocumentType{{ID: "rfc", Name: "RFCs", Prefix: "RFC"}})
	if err != nil {
		t.Fatal(err)
	}
	return service.NewSearch(search.NoopClient{}, reg)
}

func TestSearchQuery_Noop(t *testing.T) {
	s := newSearch(t)
	page, err := s.Query(t.Context(), search.Query{Q: "whatever"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 0 {
		t.Errorf("noop should return 0 hits, got %d", len(page.Hits))
	}
}

func TestSearchQuery_UnknownTypeFilter(t *testing.T) {
	s := newSearch(t)
	_, err := s.Query(t.Context(), search.Query{Q: "x", TypeID: "nope"})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}
