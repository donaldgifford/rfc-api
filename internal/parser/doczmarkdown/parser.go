// Package doczmarkdown is the parser for docz-managed Markdown:
// YAML frontmatter between leading `---` fences, Markdown body
// below. Handles RFC + ADR frontmatter shape that every doc in
// this repo already uses.
//
// Registration happens from init() into parser.Default so importing
// the package under cmd/rfc-api/ or test glue is enough to make
// name `docz-markdown` resolvable.
package doczmarkdown

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/docid"
	"github.com/donaldgifford/rfc-api/internal/parser"
)

// ParserName is the registry key for this parser.
const ParserName = "docz-markdown"

// Parser implements domain.Parser over docz frontmatter + Markdown.
// The zero value is ready; no goroutine-state lives here so it is
// safe for concurrent use per the domain.Parser contract.
type Parser struct{}

// New returns a Parser. Kept for symmetry with other ctor conventions
// even though Parser{} works.
func New() Parser { return Parser{} }

// frontmatter models the YAML block. Unknown keys overflow into
// Extensions via the yaml.Node pass in frontmatterAndBody below.
type frontmatter struct {
	ID      string    `yaml:"id"`
	Title   string    `yaml:"title"`
	Status  string    `yaml:"status"`
	Author  string    `yaml:"author"`
	Authors []author  `yaml:"authors"`
	Created time.Time `yaml:"created"`
	Updated time.Time `yaml:"updated"`
	Labels  []string  `yaml:"labels"`
}

type author struct {
	Name   string `yaml:"name"`
	Email  string `yaml:"email"`
	Handle string `yaml:"handle"`
}

// frontmatterKeys is used to partition recognized vs unknown
// frontmatter fields when building Extensions. Kept separate from
// the struct-tag parse so a tag rename in one place doesn't silently
// leak into Extensions.
var frontmatterKeys = map[string]bool{
	"id": true, "title": true, "status": true, "author": true,
	"authors": true, "created": true, "updated": true, "labels": true,
}

// Parse implements domain.Parser.
func (Parser) Parse(raw []byte, t domain.DocumentType, src domain.Source) (domain.Document, error) {
	fm, ext, body, err := frontmatterAndBody(raw)
	if err != nil {
		return domain.Document{}, err
	}

	if fm.ID == "" {
		return domain.Document{}, fmt.Errorf("%w: missing frontmatter id", domain.ErrInvalidInput)
	}
	if fm.Title == "" {
		return domain.Document{}, fmt.Errorf("%w: missing frontmatter title", domain.ErrInvalidInput)
	}
	if fm.Status == "" {
		return domain.Document{}, fmt.Errorf("%w: missing frontmatter status", domain.ErrInvalidInput)
	}

	urlID, err := urlIDFromFrontmatter(fm.ID, t.Prefix)
	if err != nil {
		return domain.Document{}, err
	}

	if len(t.Lifecycle) > 0 && !slices.Contains(t.Lifecycle, fm.Status) {
		return domain.Document{}, fmt.Errorf("%w: status %q not in %s lifecycle",
			domain.ErrInvalidInput, fm.Status, t.ID)
	}

	created := fm.Created
	if created.IsZero() {
		created = time.Now().UTC()
	}
	updated := fm.Updated
	if updated.IsZero() {
		updated = created
	}

	links := extractLinks(body)

	return domain.Document{
		ID:         docid.Canonical(t.ID, urlID),
		Type:       t.ID,
		Title:      fm.Title,
		Status:     fm.Status,
		Authors:    parseAuthors(&fm),
		CreatedAt:  created,
		UpdatedAt:  updated,
		Body:       body,
		Links:      links,
		Labels:     fm.Labels,
		Extensions: ext,
		Source:     src,
	}, nil
}

// frontmatterAndBody splits raw into (frontmatter struct, unknown-
// keys map, body string). Returns ErrMalformedFrontmatter (wrapping
// ErrInvalidInput) when the YAML is missing or unparseable.
func frontmatterAndBody(raw []byte) (frontmatter, map[string]any, string, error) {
	open := []byte("---\n")
	if !bytes.HasPrefix(raw, open) {
		return frontmatter{}, nil, "", fmt.Errorf("%w: frontmatter fence missing", domain.ErrInvalidInput)
	}
	rest := raw[len(open):]
	yamlBlock, after, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return frontmatter{}, nil, "", fmt.Errorf("%w: frontmatter fence unterminated", domain.ErrInvalidInput)
	}
	body := string(after)

	var fm frontmatter
	if err := yaml.Unmarshal(yamlBlock, &fm); err != nil {
		return frontmatter{}, nil, "", fmt.Errorf("%w: %s", domain.ErrInvalidInput, err.Error())
	}

	// Pass 2: gather anything the typed struct ignored into
	// Extensions so type-specific frontmatter survives parsing
	// unchanged (DESIGN-0002 Extensions shape).
	var all map[string]any
	if err := yaml.Unmarshal(yamlBlock, &all); err != nil {
		return frontmatter{}, nil, "", fmt.Errorf("%w: %s", domain.ErrInvalidInput, err.Error())
	}
	ext := make(map[string]any, len(all))
	for k, v := range all {
		if frontmatterKeys[k] {
			continue
		}
		ext[k] = v
	}
	if len(ext) == 0 {
		ext = nil
	}
	return fm, ext, body, nil
}

// parseAuthors prefers the structured `authors:` list. Falls back
// to comma-splitting the `author:` scalar when only the legacy form
// is present.
func parseAuthors(fm *frontmatter) []domain.Author {
	if len(fm.Authors) > 0 {
		out := make([]domain.Author, 0, len(fm.Authors))
		for _, a := range fm.Authors {
			out = append(out, domain.Author{Name: a.Name, Email: a.Email, Handle: a.Handle})
		}
		return out
	}
	if fm.Author == "" {
		return nil
	}
	parts := strings.Split(fm.Author, ",")
	out := make([]domain.Author, 0, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			continue
		}
		out = append(out, domain.Author{Name: name})
	}
	return out
}

// frontmatterIDPattern matches "PREFIX-DIGITS" with no surrounding
// context. Matching here is tighter than the body-link regex: the
// frontmatter id is authoritative, not a soft reference.
var frontmatterIDPattern = regexp.MustCompile(`^([A-Za-z]+)-(\d+)$`)

func urlIDFromFrontmatter(raw, wantPrefix string) (string, error) {
	m := frontmatterIDPattern.FindStringSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("%w: id %q is not PREFIX-NNNN", domain.ErrInvalidInput, raw)
	}
	gotPrefix, urlID := m[1], m[2]
	if !strings.EqualFold(gotPrefix, wantPrefix) {
		return "", fmt.Errorf("%w: id prefix %q != type prefix %q",
			domain.ErrInvalidInput, gotPrefix, wantPrefix)
	}
	return urlID, nil
}

// linkPattern matches an inline Markdown link ([text](target)) where
// the target is a bare PREFIX-NNNN token. The outer regex captures
// the optional text; extractLinks also walks the AST so reference-
// style links ([text][ref]) resolve the same way.
var linkPattern = regexp.MustCompile(`\[([^\]]+)\]\(([A-Za-z]+-\d+)\)`)

// bareRefPattern matches a standalone PREFIX-NNNN in prose. Used as
// a fallback when no Markdown link surrounds the reference.
var bareRefPattern = regexp.MustCompile(`\b([A-Za-z]+-\d+)\b`)

// extractLinks walks the body for outgoing references. Returns a
// dedup'd slice so a document that mentions RFC-0001 five times
// produces exactly one Link.
func extractLinks(body string) []domain.Link {
	seen := make(map[string]bool)
	out := make([]domain.Link, 0)

	// AST walk first so reference-style link definitions resolve.
	md := goldmark.New()
	doc := md.Parser().Parse(text.NewReader([]byte(body)))
	if doc != nil {
		//nolint:errcheck,gosec // walker callback never errors; ast.Walk has no other failure mode.
		ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			link, ok := n.(*ast.Link)
			if !ok {
				return ast.WalkContinue, nil
			}
			dest := string(link.Destination)
			m := bareRefPattern.FindStringSubmatch(dest)
			if m == nil {
				return ast.WalkContinue, nil
			}
			label := nodeText(link, []byte(body))
			addLink(&out, seen, m[1], label)
			return ast.WalkContinue, nil
		})
	}

	// Inline [text](PREFIX-NNNN) fallback.
	for _, m := range linkPattern.FindAllStringSubmatch(body, -1) {
		addLink(&out, seen, m[2], m[1])
	}

	// Bare PREFIX-NNNN in prose — last pass so an earlier, richer
	// match wins the label slot.
	for _, m := range bareRefPattern.FindAllStringSubmatch(body, -1) {
		addLink(&out, seen, m[1], "")
	}
	return out
}

func addLink(out *[]domain.Link, seen map[string]bool, target, label string) {
	target = strings.ToUpper(target)
	if seen[target] {
		return
	}
	seen[target] = true
	typeID, urlID, ok := docid.Parse(domain.DocumentID(target))
	if !ok {
		return
	}
	*out = append(*out, domain.Link{
		Direction: domain.LinkOutgoing,
		Target:    domain.DocumentID(target),
		TargetURL: fmt.Sprintf("/api/v1/%s/%s", typeID, urlID),
		Label:     label,
	})
}

// nodeText returns the concatenated text of a Markdown node. Used
// to populate the Link label for AST-sourced references.
func nodeText(n ast.Node, source []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			b.Write(t.Segment.Value(source))
		}
	}
	return b.String()
}

// init registers this parser in the process-wide registry so a
// blank import from cmd/rfc-api/work.go is enough to make it
// resolvable by name.
func init() {
	parser.MustRegister(ParserName, Parser{})
}
