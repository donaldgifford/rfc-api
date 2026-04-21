package testparser_test

import (
	"errors"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/parser"
	"github.com/donaldgifford/rfc-api/internal/parser/testparser"
)

func tstType() domain.DocumentType {
	return domain.DocumentType{
		ID:        "tst",
		Name:      "Tests",
		Prefix:    "TST",
		Lifecycle: []string{"Draft", "Proposed", "Accepted"},
	}
}

func TestParse_HappyPath(t *testing.T) {
	raw := []byte(`id: TST-0001
title: Sample
status: Draft
authors:
  - name: Alice
    handle: alice
labels: [foo, bar]
body: "hello"
extensions:
  ticket: TST-9
`)
	doc, err := (testparser.Parser{}).Parse(raw, tstType(), domain.Source{Repo: "o/r", Path: "p.yaml"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.ID != "TST-0001" {
		t.Errorf("ID = %q, want TST-0001", doc.ID)
	}
	if doc.Type != "tst" {
		t.Errorf("Type = %q, want tst", doc.Type)
	}
	if doc.Title != "Sample" || doc.Status != "Draft" {
		t.Errorf("title/status = %q/%q", doc.Title, doc.Status)
	}
	if len(doc.Authors) != 1 || doc.Authors[0].Name != "Alice" {
		t.Errorf("authors = %+v", doc.Authors)
	}
	if len(doc.Labels) != 2 {
		t.Errorf("labels = %+v", doc.Labels)
	}
	if doc.Extensions["ticket"] != "TST-9" {
		t.Errorf("extensions = %+v", doc.Extensions)
	}
	if doc.Source.Repo != "o/r" {
		t.Errorf("source = %+v", doc.Source)
	}
}

func TestParse_RejectsBadLifecycle(t *testing.T) {
	raw := []byte(`id: TST-0001
title: X
status: NotAStatus
`)
	_, err := (testparser.Parser{}).Parse(raw, tstType(), domain.Source{})
	if err == nil {
		t.Fatal("want error for out-of-lifecycle status")
	}
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParse_RejectsPrefixMismatch(t *testing.T) {
	raw := []byte(`id: RFC-0001
title: X
status: Draft
`)
	_, err := (testparser.Parser{}).Parse(raw, tstType(), domain.Source{})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParse_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"no id", "title: X\nstatus: Draft\n"},
		{"no title", "id: TST-1\nstatus: Draft\n"},
		{"no status", "id: TST-1\ntitle: X\n"},
		{"bad id shape", "id: BAD\ntitle: X\nstatus: Draft\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (testparser.Parser{}).Parse([]byte(tc.raw), tstType(), domain.Source{})
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestParse_RejectsMalformedYAML(t *testing.T) {
	raw := []byte("id: TST-0001\ntitle: [unclosed\n")
	_, err := (testparser.Parser{}).Parse(raw, tstType(), domain.Source{})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParse_TypeWithoutLifecycle_AllowsAnyStatus(t *testing.T) {
	raw := []byte(`id: XYZ-0001
title: X
status: WhateverIWant
`)
	doc, err := (testparser.Parser{}).Parse(raw, domain.DocumentType{
		ID: "xyz", Name: "XYZ", Prefix: "XYZ", // no Lifecycle
	}, domain.Source{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Status != "WhateverIWant" {
		t.Errorf("status = %q", doc.Status)
	}
}

func TestInit_Registered(t *testing.T) {
	// testparser/init registers into parser.Default; verify lookup.
	got, err := parser.Default.Get(testparser.ParserName)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil {
		t.Error("got nil parser for test-parser")
	}
}
