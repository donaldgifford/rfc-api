// Package service is the use-case layer between HTTP handlers and the
// persistence / search backends. Services depend on the store and
// search interfaces (not concrete implementations), return framework-
// agnostic domain types, and never import net/http.
//
// Thin on purpose: Phase 2 services are mostly passthroughs that
// translate between handler inputs (strings off the request) and
// store inputs (domain types). As the product grows — authorization
// checks, cross-store joins, cache read-through — this is where the
// logic will live.
package service
