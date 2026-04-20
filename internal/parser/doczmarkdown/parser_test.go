package doczmarkdown_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/parser/doczmarkdown"
)

func rfcType() domain.DocumentType {
	return domain.DocumentType{
		ID:        "rfc",
		Name:      "RFCs",
		Prefix:    "RFC",
		Lifecycle: []string{"Draft", "Proposed", "Accepted", "Rejected"},
	}
}

func src() domain.Source {
	return domain.Source{Repo: "o/r", Path: "docs/rfc/0001.md", Commit: "deadbeef"}
}

func TestParse_HappyPath(t *testing.T) {
	raw := []byte(`---
id: RFC-0001
title: "First"
status: Draft
author: Ada, Bob
created: 2026-04-20T00:00:00Z
updated: 2026-04-20T01:00:00Z
labels:
  - policy
custom_field: hello
---

# Body

This references [RFC-0002](RFC-0002) and a bare ADR-0003 mention.
`)
	doc, err := doczmarkdown.Parser{}.Parse(raw, rfcType(), src())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.ID != "RFC-0001" {
		t.Errorf("ID = %q", doc.ID)
	}
	if doc.Title != "First" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.Status != "Draft" {
		t.Errorf("Status = %q", doc.Status)
	}
	if len(doc.Authors) != 2 || doc.Authors[0].Name != "Ada" || doc.Authors[1].Name != "Bob" {
		t.Errorf("authors = %+v", doc.Authors)
	}
	if !doc.CreatedAt.Equal(time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt = %v", doc.CreatedAt)
	}
	if len(doc.Labels) != 1 || doc.Labels[0] != "policy" {
		t.Errorf("labels = %v", doc.Labels)
	}
	if doc.Extensions["custom_field"] != "hello" {
		t.Errorf("extensions = %+v", doc.Extensions)
	}
	if doc.Source != src() {
		t.Errorf("source = %+v", doc.Source)
	}

	// Links: one RFC-0002, one ADR-0003 (deduped).
	if len(doc.Links) != 2 {
		t.Fatalf("want 2 links, got %d: %+v", len(doc.Links), doc.Links)
	}
	var sawRFC, sawADR bool
	for _, l := range doc.Links {
		switch l.Target {
		case "RFC-0002":
			sawRFC = true
			if l.TargetURL != "/api/v1/rfc/0002" {
				t.Errorf("RFC-0002 URL = %q", l.TargetURL)
			}
		case "ADR-0003":
			sawADR = true
		}
	}
	if !sawRFC || !sawADR {
		t.Errorf("missing expected links: %+v", doc.Links)
	}
}

func TestParse_MissingFence_Errors(t *testing.T) {
	_, err := doczmarkdown.Parser{}.Parse([]byte("no frontmatter\n"), rfcType(), src())
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestParse_MissingTitle_Errors(t *testing.T) {
	raw := []byte(`---
id: RFC-0001
status: Draft
---
body
`)
	_, err := doczmarkdown.Parser{}.Parse(raw, rfcType(), src())
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParse_LifecycleViolation_Errors(t *testing.T) {
	raw := []byte(`---
id: RFC-0001
title: x
status: Bogus
---
body
`)
	_, err := doczmarkdown.Parser{}.Parse(raw, rfcType(), src())
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if !strings.Contains(err.Error(), "lifecycle") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParse_PrefixMismatch_Errors(t *testing.T) {
	raw := []byte(`---
id: ADR-0001
title: x
status: Draft
---
body
`)
	_, err := doczmarkdown.Parser{}.Parse(raw, rfcType(), src())
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestParse_StructuredAuthors(t *testing.T) {
	raw := []byte(`---
id: RFC-0001
title: x
status: Draft
authors:
  - name: Ada
    email: ada@example.com
    handle: "@ada"
  - name: Bob
---
body
`)
	doc, err := doczmarkdown.Parser{}.Parse(raw, rfcType(), src())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Authors) != 2 {
		t.Fatalf("authors len = %d", len(doc.Authors))
	}
	if doc.Authors[0].Email != "ada@example.com" || doc.Authors[0].Handle != "@ada" {
		t.Errorf("first author = %+v", doc.Authors[0])
	}
}

func TestParse_RealRepoDoc_RFC0001(t *testing.T) {
	// Spot-check a real repo doc — the parser must handle what we
	// already have on disk cleanly.
	raw, err := readFile(t, "../../../docs/rfc/0001-rfc-api-backend-api-for-the-markdown-portal.md")
	if err != nil {
		t.Fatal(err)
	}
	typ := rfcType()
	typ.Lifecycle = nil // real docs can use any status value
	doc, err := doczmarkdown.Parser{}.Parse(raw, typ, src())
	if err != nil {
		t.Fatalf("real RFC doc should parse: %v", err)
	}
	if doc.Title == "" {
		t.Errorf("real doc parsed with empty title")
	}
}
