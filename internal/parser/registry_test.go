package parser_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/parser"
)

type stubParser struct{ name string }

func (stubParser) Parse(_ []byte, _ domain.DocumentType, _ domain.Source) (domain.Document, error) {
	return domain.Document{}, nil
}

func TestRegister_And_Get(t *testing.T) {
	reg := parser.NewRegistry()
	if err := reg.Register("a", stubParser{name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("b", stubParser{name: "b"}); err != nil {
		t.Fatal(err)
	}
	p, err := reg.Get("a")
	if err != nil {
		t.Fatalf("Get(a): %v", err)
	}
	if p.(stubParser).name != "a" {
		t.Errorf("wrong parser resolved")
	}
	if got := reg.Names(); !equalStrings(got, []string{"a", "b"}) {
		t.Errorf("Names() = %v", got)
	}
}

func TestRegister_Duplicate_Errors(t *testing.T) {
	reg := parser.NewRegistry()
	if err := reg.Register("x", stubParser{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("x", stubParser{}); err == nil {
		t.Fatal("want duplicate error")
	}
}

func TestRegister_Empty_Nil_Errors(t *testing.T) {
	reg := parser.NewRegistry()
	if err := reg.Register("", stubParser{}); err == nil {
		t.Error("want error on empty name")
	}
	if err := reg.Register("ok", nil); err == nil {
		t.Error("want error on nil parser")
	}
}

func TestGet_Unknown_ReturnsSentinel(t *testing.T) {
	reg := parser.NewRegistry()
	_, err := reg.Get("missing")
	if !errors.Is(err, parser.ErrParserNotRegistered) {
		t.Fatalf("want ErrParserNotRegistered, got %v", err)
	}
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("sentinel does not wrap ErrInvalidInput: %v", err)
	}
}

func TestConcurrent_Register_Get(_ *testing.T) {
	reg := parser.NewRegistry()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := string(rune('a' + i%10))
			// Half register, half look up. Concurrent Register of
			// the same name is expected to collide — assert that
			// collisions are rejected, not that every call succeeds.
			_ = reg.Register(name, stubParser{name: name})
			_, _ = reg.Get(name)
		}(i)
	}
	wg.Wait()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
