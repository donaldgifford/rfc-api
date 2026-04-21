package meilisearch

import (
	"context"
	"fmt"
	"strings"

	"github.com/donaldgifford/rfc-api/internal/domain"
)

// indexBatchSize bounds how many sub-documents a single AddDocuments
// call ships. 1024 keeps individual payloads in the low-MB range
// while still letting a reindex drive the Meili task queue near its
// practical ceiling.
const indexBatchSize = 1024

// visibilityInternal is the stored-field value set on every indexed
// sub-doc until RFC-0001 Phase 4 hooks visibility to the authenticated
// caller's scopes (ADR-0003 # Neutral).
const visibilityInternal = "internal"

// TypeResolver is the narrow slice of the document-type registry the
// Indexer needs. Domain registry implements it; the tests pass a
// trivial map-backed shim.
type TypeResolver interface {
	Get(id string) (domain.DocumentType, bool)
}

// Indexer writes per-section sub-documents to the configured Meili
// instance. Upsert fully replaces the doc's sub-docs (delete-by-
// filter + add); Delete removes every sub-doc for a given parent.
type Indexer struct {
	client *Client
	types  TypeResolver
}

// NewIndexer returns an Indexer backed by a write-scoped Client and
// a TypeResolver. Nil args surface at construction, not on first
// write.
func NewIndexer(c *Client, types TypeResolver) (*Indexer, error) {
	if c == nil {
		return nil, fmt.Errorf("meilisearch indexer: nil client")
	}
	if types == nil {
		return nil, fmt.Errorf("meilisearch indexer: nil types")
	}
	return &Indexer{client: c, types: types}, nil
}

// indexDocument is the flat shape Meili stores. The `id` is globally
// unique across parents + sections; `parent_id` is the DocumentID the
// sub-doc rolls up to; everything else is the per-section content +
// filterable facets.
//
// Extensions are flattened as `ext_<prefix>_<key>` (lowercased) so
// per-type filtered search is cheap without polluting the declared
// filterableAttributes list with every type's vocabulary. Underscore
// delimiters (not dots) keep the field names valid Meili attributes
// without quoting.
type indexDocument struct {
	ID             string         `json:"id"`
	ParentID       string         `json:"parent_id"`
	Type           string         `json:"type"`
	Title          string         `json:"title"`
	SectionHeading string         `json:"section_heading"`
	SectionSlug    string         `json:"section_slug,omitempty"`
	BodyExcerpt    string         `json:"body_excerpt"`
	Status         string         `json:"status"`
	Labels         []string       `json:"labels"`
	AuthorHandles  []string       `json:"author_handles"`
	Visibility     string         `json:"visibility"`
	CreatedAt      int64          `json:"created_at"`
	UpdatedAt      int64          `json:"updated_at"`
	Extensions     map[string]any `json:"-"` // flattened onto the top level via MarshalJSON
}

// Upsert replaces every sub-document for `doc` atomically from the
// caller's perspective: the old sub-docs are cleared (delete-by-
// filter on parent_id), then the freshly-split set is added. Either
// step's failure bubbles up so the reindex job retries.
func (ix *Indexer) Upsert(ctx context.Context, doc *domain.Document) error {
	if doc == nil {
		return fmt.Errorf("meilisearch indexer: nil document")
	}
	dt, ok := ix.types.Get(doc.Type)
	if !ok {
		return fmt.Errorf("meilisearch indexer: unknown type %q", doc.Type)
	}

	records := buildRecords(doc, dt)
	if len(records) == 0 {
		return nil
	}

	// Clear prior sub-docs for this parent before writing the new set.
	// A doc that lost an H2 between ingests would otherwise leave an
	// orphan section live on the index.
	if err := ix.clearParent(ctx, string(doc.ID)); err != nil {
		return err
	}

	idx := ix.client.svc.Index(IndexName)
	for start := 0; start < len(records); start += indexBatchSize {
		end := min(start+indexBatchSize, len(records))
		batch := make([]map[string]any, 0, end-start)
		for i := start; i < end; i++ {
			batch = append(batch, records[i].toMap())
		}
		task, err := idx.AddDocumentsWithContext(ctx, batch, nil)
		if err != nil {
			return fmt.Errorf("meilisearch: add documents: %w", err)
		}
		if err := ix.client.awaitTask(ctx, task.TaskUID, "add documents"); err != nil {
			return err
		}
	}
	return nil
}

// Delete drops every sub-document rolling up to id (the head doc +
// every section). No-op if Meili reports the filter matched nothing.
func (ix *Indexer) Delete(ctx context.Context, id domain.DocumentID) error {
	return ix.clearParent(ctx, string(id))
}

func (ix *Indexer) clearParent(ctx context.Context, parentID string) error {
	idx := ix.client.svc.Index(IndexName)
	filter := fmt.Sprintf(`parent_id = %q`, parentID)
	task, err := idx.DeleteDocumentsByFilterWithContext(ctx, filter, nil)
	if err != nil {
		return fmt.Errorf("meilisearch: delete by filter: %w", err)
	}
	return ix.client.awaitTask(ctx, task.TaskUID, "delete by filter")
}

// buildRecords is the pure side of Upsert: doc + type → index
// records. Separated so the split logic is unit-testable without a
// Meili instance.
func buildRecords(doc *domain.Document, dt domain.DocumentType) []indexDocument {
	sections := splitSections(doc.Body)
	records := make([]indexDocument, 0, len(sections))
	handles := authorHandles(doc.Authors)
	prefixLower := strings.ToLower(dt.Prefix)

	for _, s := range sections {
		id := string(doc.ID)
		if s.Slug != "" {
			// Meili document ids reject `#` (and most punctuation) —
			// only alphanumeric + `-` + `_` are allowed. Use `__`
			// between parent id and section slug so a naive
			// lexicographic sort still groups by parent.
			id = string(doc.ID) + "__" + s.Slug
		}
		records = append(records, indexDocument{
			ID:             id,
			ParentID:       string(doc.ID),
			Type:           doc.Type,
			Title:          doc.Title,
			SectionHeading: s.Heading,
			SectionSlug:    s.Slug,
			BodyExcerpt:    s.Body,
			Status:         doc.Status,
			Labels:         normalizeStrings(doc.Labels),
			AuthorHandles:  handles,
			Visibility:     visibilityInternal,
			CreatedAt:      doc.CreatedAt.Unix(),
			UpdatedAt:      doc.UpdatedAt.Unix(),
			Extensions:     flattenExtensions(doc.Extensions, prefixLower),
		})
	}
	return records
}

// toMap flattens indexDocument onto a Meili-friendly map so the
// extension fields (ext_<prefix>_<k>) land at the top level rather
// than nested under an object.
func (d *indexDocument) toMap() map[string]any {
	m := map[string]any{
		"id":              d.ID,
		"parent_id":       d.ParentID,
		"type":            d.Type,
		"title":           d.Title,
		"section_heading": d.SectionHeading,
		"body_excerpt":    d.BodyExcerpt,
		"status":          d.Status,
		"labels":          d.Labels,
		"author_handles":  d.AuthorHandles,
		"visibility":      d.Visibility,
		"created_at":      d.CreatedAt,
		"updated_at":      d.UpdatedAt,
	}
	if d.SectionSlug != "" {
		m["section_slug"] = d.SectionSlug
	}
	for k, v := range d.Extensions {
		m[k] = v
	}
	return m
}

// flattenExtensions expands `doc.Extensions["key"] = val` into
// `ext_<prefix>_<key>: val`. Nested objects pass through as-is;
// Meili's JSON-friendly store accepts them. Keys lowercased so
// multi-case variants collapse.
func flattenExtensions(ext map[string]any, prefixLower string) map[string]any {
	if len(ext) == 0 {
		return nil
	}
	out := make(map[string]any, len(ext))
	for k, v := range ext {
		out["ext_"+prefixLower+"_"+strings.ToLower(k)] = v
	}
	return out
}

// authorHandles collects non-empty author handles. When no handle is
// set, falls back to the author's name so per-author search still
// resolves on docs that only carry free-form author strings.
func authorHandles(authors []domain.Author) []string {
	out := make([]string, 0, len(authors))
	for _, a := range authors {
		switch {
		case a.Handle != "":
			out = append(out, a.Handle)
		case a.Name != "":
			out = append(out, a.Name)
		}
	}
	return out
}

// normalizeStrings returns a non-nil slice for consistent JSON
// serialization. Meili treats `null` and `[]` identically in
// filters but the stable shape keeps downstream hashing easier.
func normalizeStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
