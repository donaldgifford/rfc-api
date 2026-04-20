package domain

// DocumentType is the value-object description of a content type in
// the registry. Populated from config at startup (see
// internal/domain/registry). The shape is intentionally minimal —
// per DESIGN-0002 we grow it conservatively; a bloated DocumentType
// is a sign type-awareness is leaking somewhere it shouldn't.
type DocumentType struct {
	// ID is the stable lowercase route segment: "rfc", "adr".
	ID string

	// Name is the human display name, e.g. "Request for Comments".
	Name string

	// Prefix is the display-id prefix. Unique across the registry;
	// enforced at load time. Examples: "RFC", "ADR", "SEC".
	Prefix string

	// Lifecycle is the ordered vocabulary of valid status values for
	// this type. The API surfaces status as a free string at read
	// time; the worker enforces membership at ingest time.
	Lifecycle []string
}

// DocumentTypeRegistry is the lookup surface the rest of the system
// uses. The on-disk config loader hands back a concrete implementation
// of this interface at startup. Implementations must be safe for
// concurrent reads — the registry is read-only after construction.
type DocumentTypeRegistry interface {
	// Get returns the DocumentType for the given route-segment id
	// ("rfc"). The second return is false for unknown ids.
	Get(id string) (DocumentType, bool)

	// ByPrefix returns the DocumentType whose Prefix matches the
	// given id prefix ("RFC"). Prefix matching is case-insensitive
	// so callers can pass the raw segment from a display id.
	ByPrefix(prefix string) (DocumentType, bool)

	// List returns every registered type. Order is registration
	// order (i.e. config order) so callers can render a stable
	// /api/v1/types response.
	List() []DocumentType
}
