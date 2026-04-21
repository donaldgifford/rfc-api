// Package parser hosts the compiled-in parser registry and the
// concrete implementations (docz-markdown, test-parser). Per
// IMPL-0004 RD1, v1 is compile-time — adding a parser is a code
// change. Out-of-process / plugin loading is explicitly out of scope.
//
// Lookup is by name (string). Parsers register themselves at package
// init time; the worker looks them up when dispatching an ingest job.
package parser

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/donaldgifford/rfc-api/internal/domain"
)

// ErrParserNotRegistered is returned when Get is called with an
// unknown name. Wraps domain.ErrInvalidInput so httperr.classify
// routes it to a 400.
var ErrParserNotRegistered = fmt.Errorf("%w: parser not registered", domain.ErrInvalidInput)

// Registry is the name → Parser lookup. Zero-value is usable.
//
// Registry is safe for concurrent Get and Register — the worker may
// race registration against lookup at startup, and tests register
// parsers in parallel test functions.
type Registry struct {
	mu    sync.RWMutex
	byKey map[string]domain.Parser
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byKey: make(map[string]domain.Parser)}
}

// Register binds name to parser. Duplicate-name registration is an
// error so a typo in `SourceRepo.Parser` is caught at startup rather
// than silently overwriting a production binding.
func (r *Registry) Register(name string, parser domain.Parser) error {
	if name == "" {
		return errors.New("parser.Register: name is required")
	}
	if parser == nil {
		return errors.New("parser.Register: parser is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byKey == nil {
		r.byKey = make(map[string]domain.Parser)
	}
	if _, dup := r.byKey[name]; dup {
		return fmt.Errorf("parser.Register: %q already registered", name)
	}
	r.byKey[name] = parser
	return nil
}

// Get returns the parser registered under name, or ErrParserNotRegistered
// (wrapped with the requested name) when absent.
func (r *Registry) Get(name string) (domain.Parser, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byKey[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrParserNotRegistered, name)
	}
	return p, nil
}

// Names returns the registered parser names, sorted for test
// stability and /readyz-style enumeration.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byKey))
	for name := range r.byKey {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Default is the process-wide registry. Parser packages register
// themselves here from an init() hook so importing the package is
// sufficient to make the parser available. Tests that want isolation
// build their own Registry via NewRegistry.
var Default = NewRegistry()

// MustRegister is the init-time convenience that panics on error. A
// duplicate registration is always a developer bug — no graceful
// fallback makes sense here.
func MustRegister(name string, parser domain.Parser) {
	if err := Default.Register(name, parser); err != nil {
		panic(fmt.Sprintf("parser.MustRegister(%q): %v", name, err))
	}
}
