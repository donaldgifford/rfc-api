package service

import (
	"context"
	"fmt"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/search"
)

// Search is the cross-type search service. It knows the default page
// size and accepted type filters; it does not know about ranking, the
// backend index, or the specific search engine.
type Search struct {
	client   search.Client
	registry domain.DocumentTypeRegistry
}

// NewSearch constructs a Search service.
func NewSearch(c search.Client, r domain.DocumentTypeRegistry) *Search {
	return &Search{client: c, registry: r}
}

// Query forwards to the underlying search client, normalizing limit
// and rejecting unknown type filters as invalid input.
func (s *Search) Query(ctx context.Context, q search.Query) (search.Page, error) {
	if q.TypeID != "" {
		if _, ok := s.registry.Get(q.TypeID); !ok {
			return search.Page{}, fmt.Errorf("%w: unknown type %q", domain.ErrInvalidInput, q.TypeID)
		}
	}
	q.Limit = normalizeLimit(q.Limit)
	return s.client.Query(ctx, q)
}
