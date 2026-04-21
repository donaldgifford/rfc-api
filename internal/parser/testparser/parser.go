// Package testparser is a minimal YAML-only parser used by the
// DESIGN-0002 "fake type" harness and other tests that want to seed
// documents without the Markdown + frontmatter round-trip. The
// format is a single YAML document whose top-level keys mirror
// domain.Document fields.
//
// Production code never uses this parser — it skips the frontmatter-
// fence contract the docz-markdown parser enforces and does no link
// extraction. Its job is to make cross-cutting tests (parse →
// persist → serve) readable without inlining Markdown fixtures.
package testparser

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/docid"
	"github.com/donaldgifford/rfc-api/internal/parser"
)

// ParserName is the registry key for this parser.
const ParserName = "test-parser"

// Parser implements domain.Parser over a YAML document. The zero
// value is ready; the parser holds no state so it is safe for
// concurrent use per the domain.Parser contract.
type Parser struct{}

// New returns a Parser. Kept for symmetry with doczmarkdown.
func New() Parser { return Parser{} }

// rawDoc mirrors the user-facing YAML shape. Unknown keys are
// ignored — tests should use Extensions for anything ad-hoc.
type rawDoc struct {
	ID         string          `yaml:"id"`
	Title      string          `yaml:"title"`
	Status     string          `yaml:"status"`
	Authors    []domain.Author `yaml:"authors"`
	Labels     []string        `yaml:"labels"`
	Body       string          `yaml:"body"`
	Extensions map[string]any  `yaml:"extensions"`
	Created    time.Time       `yaml:"created"`
	Updated    time.Time       `yaml:"updated"`
}

// idPattern matches PREFIX-DIGITS with no surrounding context.
var idPattern = regexp.MustCompile(`^([A-Za-z]+)-(\d+)$`)

// Parse implements domain.Parser. The input is a YAML document;
// top-level keys map 1-to-1 onto domain.Document fields. Lifecycle
// validation mirrors doczmarkdown so the fake-type harness exercises
// the same rejection path production parsers take.
func (Parser) Parse(raw []byte, t domain.DocumentType, src domain.Source) (domain.Document, error) {
	var d rawDoc
	if err := yaml.Unmarshal(raw, &d); err != nil {
		return domain.Document{}, fmt.Errorf("%w: %s", domain.ErrInvalidInput, err.Error())
	}
	if d.ID == "" {
		return domain.Document{}, fmt.Errorf("%w: missing id", domain.ErrInvalidInput)
	}
	if d.Title == "" {
		return domain.Document{}, fmt.Errorf("%w: missing title", domain.ErrInvalidInput)
	}
	if d.Status == "" {
		return domain.Document{}, fmt.Errorf("%w: missing status", domain.ErrInvalidInput)
	}

	m := idPattern.FindStringSubmatch(d.ID)
	if m == nil {
		return domain.Document{}, fmt.Errorf("%w: id %q is not PREFIX-NNNN", domain.ErrInvalidInput, d.ID)
	}
	if !strings.EqualFold(m[1], t.Prefix) {
		return domain.Document{}, fmt.Errorf("%w: id prefix %q != type prefix %q",
			domain.ErrInvalidInput, m[1], t.Prefix)
	}

	if len(t.Lifecycle) > 0 && !slices.Contains(t.Lifecycle, d.Status) {
		return domain.Document{}, fmt.Errorf("%w: status %q not in %s lifecycle",
			domain.ErrInvalidInput, d.Status, t.ID)
	}

	// Same fallback order as doczmarkdown: frontmatter → commit time
	// from the Source → time.Now(). Keeps the test-parser behavior
	// aligned with the production parser for the fake-type harness.
	created := d.Created
	if created.IsZero() {
		created = src.CommitTime
	}
	if created.IsZero() {
		created = time.Now().UTC()
	}
	updated := d.Updated
	if updated.IsZero() {
		updated = src.CommitTime
	}
	if updated.IsZero() {
		updated = created
	}

	return domain.Document{
		ID:         docid.Canonical(t.ID, m[2]),
		Type:       t.ID,
		Title:      d.Title,
		Status:     d.Status,
		Authors:    d.Authors,
		CreatedAt:  created,
		UpdatedAt:  updated,
		Body:       d.Body,
		Labels:     d.Labels,
		Extensions: d.Extensions,
		Source:     src,
	}, nil
}

// init registers this parser in the process-wide registry so a blank
// import is enough to make it resolvable by name.
func init() {
	parser.MustRegister(ParserName, Parser{})
}
