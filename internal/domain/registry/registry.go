// Package registry turns the raw config.DocumentTypes slice into a
// validated, read-only domain.DocumentTypeRegistry.
//
// Prefix and id uniqueness is enforced at New(). DESIGN-0002 Resolved
// Decisions call this a firm constraint: two types sharing a prefix
// or id refuses to start the service. Returning a validation error
// from New() (propagated out of cmd/rfc-api/serve.go) keeps that
// promise.
package registry

import (
	"errors"
	"fmt"
	"strings"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/domain"
)

// ErrNoTypes is returned by New() when the supplied config declares
// zero document types. A running server with an empty registry would
// mount no /api/v1/{type} routes and silently pass healthchecks —
// better to fail loudly at startup.
var ErrNoTypes = errors.New("registry: no document types configured")

// Registry is the in-memory implementation of
// domain.DocumentTypeRegistry. Safe for concurrent reads; never
// mutated after construction.
type Registry struct {
	byID     map[string]domain.DocumentType
	byPrefix map[string]domain.DocumentType // keys are lowercase
	ordered  []domain.DocumentType
}

// New validates entries and returns a read-only registry. Validation:
//
//   - at least one entry
//   - every id non-empty, lowercase, unique
//   - every prefix non-empty, uppercased, unique (case-insensitively)
//   - every name non-empty
//
// Lifecycle is optional — a type without a declared lifecycle accepts
// any free string at the API layer and relies on the worker to
// enforce conventions at ingest time.
func New(entries []config.DocumentType) (*Registry, error) {
	if len(entries) == 0 {
		return nil, ErrNoTypes
	}

	r := &Registry{
		byID:     make(map[string]domain.DocumentType, len(entries)),
		byPrefix: make(map[string]domain.DocumentType, len(entries)),
		ordered:  make([]domain.DocumentType, 0, len(entries)),
	}

	for i, e := range entries {
		t, err := validate(e)
		if err != nil {
			return nil, fmt.Errorf("document_types[%d]: %w", i, err)
		}
		if _, dup := r.byID[t.ID]; dup {
			return nil, fmt.Errorf("document_types[%d]: duplicate id %q", i, t.ID)
		}
		prefixKey := strings.ToLower(t.Prefix)
		if _, dup := r.byPrefix[prefixKey]; dup {
			return nil, fmt.Errorf("document_types[%d]: duplicate prefix %q", i, t.Prefix)
		}
		r.byID[t.ID] = t
		r.byPrefix[prefixKey] = t
		r.ordered = append(r.ordered, t)
	}

	return r, nil
}

// Get returns the DocumentType for the given route-segment id.
func (r *Registry) Get(id string) (domain.DocumentType, bool) {
	t, ok := r.byID[id]
	return t, ok
}

// ByPrefix returns the DocumentType whose prefix matches (case-
// insensitively). Convenience for code that has a display id and
// wants the owning type.
func (r *Registry) ByPrefix(prefix string) (domain.DocumentType, bool) {
	t, ok := r.byPrefix[strings.ToLower(prefix)]
	return t, ok
}

// List returns registered types in config order.
func (r *Registry) List() []domain.DocumentType {
	out := make([]domain.DocumentType, len(r.ordered))
	copy(out, r.ordered)
	return out
}

func validate(e config.DocumentType) (domain.DocumentType, error) {
	id := strings.TrimSpace(e.ID)
	if id == "" {
		return domain.DocumentType{}, errors.New("id is required")
	}
	if id != strings.ToLower(id) {
		return domain.DocumentType{}, fmt.Errorf("id %q must be lowercase", e.ID)
	}

	prefix := strings.TrimSpace(e.Prefix)
	if prefix == "" {
		return domain.DocumentType{}, errors.New("prefix is required")
	}

	name := strings.TrimSpace(e.Name)
	if name == "" {
		return domain.DocumentType{}, errors.New("name is required")
	}

	return domain.DocumentType{
		ID:        id,
		Name:      name,
		Prefix:    strings.ToUpper(prefix),
		Lifecycle: append([]string(nil), e.Lifecycle...),
	}, nil
}
