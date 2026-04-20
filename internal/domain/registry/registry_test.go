package registry_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain/registry"
)

func rfc() config.DocumentType {
	return config.DocumentType{ID: "rfc", Name: "Request for Comments", Prefix: "RFC"}
}

func adr() config.DocumentType {
	return config.DocumentType{ID: "adr", Name: "Architecture Decision Record", Prefix: "ADR"}
}

func TestNew_OK(t *testing.T) {
	r, err := registry.New([]config.DocumentType{rfc(), adr()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.List(); len(got) != 2 || got[0].ID != "rfc" || got[1].ID != "adr" {
		t.Fatalf("List order wrong: %+v", got)
	}
	if _, ok := r.Get("rfc"); !ok {
		t.Errorf("Get(rfc) not found")
	}
	if _, ok := r.Get("does-not-exist"); ok {
		t.Errorf("Get(missing) should not be found")
	}
	if got, ok := r.ByPrefix("rfc"); !ok || got.ID != "rfc" {
		t.Errorf("ByPrefix(rfc) case-insensitive lookup failed: %+v ok=%v", got, ok)
	}
}

func TestNew_Empty(t *testing.T) {
	_, err := registry.New(nil)
	if !errors.Is(err, registry.ErrNoTypes) {
		t.Fatalf("want ErrNoTypes, got %v", err)
	}
}

func TestNew_DuplicateID(t *testing.T) {
	_, err := registry.New([]config.DocumentType{rfc(), rfc()})
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("want duplicate id error, got %v", err)
	}
}

func TestNew_DuplicatePrefix(t *testing.T) {
	// Different id, same prefix (case-insensitive).
	conflicting := config.DocumentType{ID: "rfc2", Name: "Other RFCs", Prefix: "rfc"}
	_, err := registry.New([]config.DocumentType{rfc(), conflicting})
	if err == nil || !strings.Contains(err.Error(), "duplicate prefix") {
		t.Fatalf("want duplicate prefix error, got %v", err)
	}
}

func TestNew_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		in   config.DocumentType
		want string
	}{
		{"missing id", config.DocumentType{Name: "x", Prefix: "X"}, "id is required"},
		{"upper id", config.DocumentType{ID: "RFC", Name: "x", Prefix: "X"}, "must be lowercase"},
		{"missing prefix", config.DocumentType{ID: "rfc", Name: "x"}, "prefix is required"},
		{"missing name", config.DocumentType{ID: "rfc", Prefix: "RFC"}, "name is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registry.New([]config.DocumentType{tc.in})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestList_DefensiveCopy(t *testing.T) {
	r, err := registry.New([]config.DocumentType{rfc()})
	if err != nil {
		t.Fatal(err)
	}
	got := r.List()
	got[0].ID = "mutated"
	if again := r.List(); again[0].ID != "rfc" {
		t.Errorf("caller mutation leaked back into registry: %+v", again)
	}
}
