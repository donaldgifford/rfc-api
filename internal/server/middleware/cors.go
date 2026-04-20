package middleware

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// CORSConfig bundles the small CORS surface the API needs. Per
// DESIGN-0001 #Middleware chain CORS is default-deny; origins must
// be explicitly allow-listed. v1 goals stay narrow: GET + OPTIONS
// only, no credentialed requests, no exposed response headers. That
// keeps the browser contract small while rfc-site + MCP are the only
// consumers.
type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	MaxAge         int
}

// DefaultCORS builds a CORSConfig from a caller-supplied origin list.
// The method/header allow-lists are fixed; configuration lives at the
// origin level so operators only need to answer one question.
func DefaultCORS(origins []string) CORSConfig {
	return CORSConfig{
		AllowedOrigins: origins,
		AllowedMethods: []string{http.MethodGet, http.MethodOptions, http.MethodPost},
		AllowedHeaders: []string{"Content-Type", "Authorization", "X-Request-ID"},
		MaxAge:         600,
	}
}

// CORS is the project-owned CORS middleware. Rationale over github.com/rs/cors:
// our surface is narrow, we never want wildcard origins, and spelling
// the rule out here keeps one fewer direct dependency. If the surface
// ever grows (credentialed requests, preflight header negotiation
// beyond a fixed list), revisit.
//
// cfg is taken by pointer to avoid an 80-byte value copy on every
// middleware construction.
func CORS(cfg *CORSConfig) Middleware {
	if cfg == nil {
		return passThrough
	}
	return func(next http.Handler) http.Handler {
		methods := strings.Join(cfg.AllowedMethods, ", ")
		headers := strings.Join(cfg.AllowedHeaders, ", ")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || !slices.Contains(cfg.AllowedOrigins, origin) {
				// No Origin header or origin not allow-listed: pass
				// through without CORS response headers. Non-browser
				// clients (curl, Go) are unaffected; browsers get a
				// default-deny.
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")

			// Any OPTIONS from an allow-listed origin short-circuits
			// with 204 and the preflight headers. Strict preflights
			// (with Access-Control-Request-Method) and lenient OPTIONS
			// probes from some browsers both get a consistent response
			// — otherwise the latter falls through to the mux, which
			// has no OPTIONS route and would return a plain 405 that
			// bypasses the RFC 7807 envelope.
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", methods)
				w.Header().Set("Access-Control-Allow-Headers", headers)
				if cfg.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAge))
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
