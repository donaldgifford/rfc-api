package middleware

import "net/http"

// Auth is the Phase 2 authentication stub. Every request passes
// through unchanged. Phase 4 will replace this body with an OIDC JWT
// validator (local JWKS cache, audience + issuer checks, principal
// stashed on the context) per RFC-0001 #Scope.
//
// The signature and chain slot are fixed now so the Phase 4 swap is
// an implementation change, not a plumbing change.
//
// TODO(rfc-api Phase 4): implement OIDC JWT validation. See
// RFC-0001 #Scope and DESIGN-0001 #Middleware chain.
func Auth() Middleware {
	return func(next http.Handler) http.Handler { return next }
}
