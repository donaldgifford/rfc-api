package domain

// Parser turns a raw document payload into a domain.Document. One
// Parser implementation handles one source format (docz-markdown,
// framework-markdown, etc.); the worker picks the Parser by name
// using the registry in internal/parser.
//
// Implementations must be safe for concurrent use — the worker's
// ingest goroutines invoke Parse from multiple leased-job handlers.
//
// Contract:
//   - raw is the complete document body as fetched from source (see
//     IMPL-0003 #Ingest pipeline).
//   - t supplies the target DocumentType so parsers can enforce
//     Lifecycle on Status and know the Prefix for canonical-id
//     derivation.
//   - src is the repo + path + commit pointer the worker resolved;
//     parsers copy it verbatim into Document.Source.
//
// Errors wrap the sentinels in this package so httperr.classify
// maps them consistently across every parser.
type Parser interface {
	Parse(raw []byte, t DocumentType, src Source) (Document, error)
}
