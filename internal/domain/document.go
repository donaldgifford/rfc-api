package domain

import "time"

// Document is the framework-agnostic representation of a single document
// in the corpus. Handlers emit this shape (after render translation);
// services and stores exchange it directly. Per DESIGN-0002, the core
// fields are uniform across types; type-specific frontmatter rides in
// Extensions so adding a new type does not require schema changes.
type Document struct {
	ID         DocumentID     `json:"id"`
	Type       string         `json:"type"`
	Title      string         `json:"title"`
	Status     string         `json:"status"`
	Authors    []Author       `json:"authors,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Body       string         `json:"body,omitempty"`
	Links      []Link         `json:"links,omitempty"`
	Labels     []string       `json:"labels,omitempty"`
	Discussion *Discussion    `json:"discussion,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
	Source     Source         `json:"source"`
}

// DocumentID is the canonical display id, e.g. "RFC-0001". It is the
// stored primary key — globally unique across types — and the shape
// used in every JSON response. URL path segments use the numeric
// portion only; conversion is via docid.URLForm / docid.Canonical.
type DocumentID string

// String returns the canonical display form unchanged.
func (d DocumentID) String() string { return string(d) }

// Author identifies a contributor to a document. Email is optional so
// the frontend can render a mailto when available without forcing
// contributors to publish their address.
type Author struct {
	Name   string `json:"name"`
	Email  string `json:"email,omitempty"`
	Handle string `json:"handle,omitempty"`
}

// Link is one edge in the cross-document reference graph. Direction is
// carried alongside the target so /{type}/{id}/links can return both
// incoming and outgoing references in a single list.
type Link struct {
	Direction LinkDirection `json:"direction"`
	Target    DocumentID    `json:"target"`
	TargetURL string        `json:"href"`
	Label     string        `json:"label,omitempty"`
}

// LinkDirection distinguishes references that point away from the
// subject document from those that point at it.
type LinkDirection string

// Link directions recorded in serialized responses. Callers should
// compare against these rather than the raw string values.
const (
	LinkOutgoing LinkDirection = "outgoing"
	LinkIncoming LinkDirection = "incoming"
)

// Discussion is the summary of PR review conversations associated with
// a document. Per RFC-0001 the API persists this (departure from the
// Oxide model); full comment bodies live behind /discussion.
type Discussion struct {
	URL          string    `json:"url,omitempty"`
	CommentCount int       `json:"comment_count"`
	Participants []Author  `json:"participants,omitempty"`
	LastActivity time.Time `json:"last_activity,omitzero"`
}

// Source identifies where the document was ingested from. Path is
// relative to the repo root; Commit pins the exact revision the
// currently-served body came from so the API is reproducible.
// CommitTime is the upstream commit timestamp, used by parsers as a
// fallback for CreatedAt/UpdatedAt when the frontmatter omits them
// (parsers must never I/O, so the worker resolves this and threads
// it through Source at ingest time).
type Source struct {
	Repo       string    `json:"repo"`
	Path       string    `json:"path"`
	Commit     string    `json:"commit,omitempty"`
	CommitTime time.Time `json:"commit_time,omitzero"`
}
