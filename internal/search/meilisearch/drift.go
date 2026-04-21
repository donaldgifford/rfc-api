package meilisearch

import (
	"context"
	"fmt"
	"sort"

	meili "github.com/meilisearch/meilisearch-go"
)

// DistinctParentsByType returns one count per document type: the
// number of documents currently represented in the search index.
// Works around Meili's per-section record shape by running one
// type-filtered search per candidate type with `distinct: parent_id`
// set — the reply's totalHits reflects distinct parent ids.
//
// The caller threads in the set of types to probe (from the
// DocumentType registry). The drift loop compares the reply against
// `postgres.Docs.CountByType`; a non-zero delta increments a
// Prometheus gauge.
func (c *Client) DistinctParentsByType(ctx context.Context, types []string) (map[string]int, error) {
	out := make(map[string]int, len(types))
	idx := c.svc.Index(IndexName)
	for _, t := range types {
		req := &meili.SearchRequest{
			Query:    "",
			Limit:    0,
			Distinct: "parent_id",
			Filter:   fmt.Sprintf(`visibility = %q AND type = %q`, visibilityInternal, t),
		}
		resp, err := idx.SearchWithContext(ctx, "", req)
		if err != nil {
			return nil, fmt.Errorf("meilisearch drift %q: %w", t, err)
		}
		// TotalHits is the authoritative count when distinct is set;
		// fall back to EstimatedTotalHits on older Meili servers.
		n := int(resp.TotalHits)
		if n == 0 {
			n = int(resp.EstimatedTotalHits)
		}
		out[t] = n
	}
	return out, nil
}

// DriftReport is the comparison between Postgres and Meili per type.
// A non-zero Delta means reindex catch-up is warranted.
type DriftReport struct {
	Type     string
	Postgres int
	Meili    int
	Delta    int // Postgres - Meili; negative means Meili has extra
}

// CompareDrift subtracts Meili counts from Postgres counts per type
// and returns a stable-ordered report. Unknown types on either side
// appear with a zero count on the missing side.
func CompareDrift(pg, idx map[string]int) []DriftReport {
	typesSet := make(map[string]struct{})
	for t := range pg {
		typesSet[t] = struct{}{}
	}
	for t := range idx {
		typesSet[t] = struct{}{}
	}
	reports := make([]DriftReport, 0, len(typesSet))
	for t := range typesSet {
		p := pg[t]
		m := idx[t]
		reports = append(reports, DriftReport{
			Type:     t,
			Postgres: p,
			Meili:    m,
			Delta:    p - m,
		})
	}
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Type < reports[j].Type
	})
	return reports
}
