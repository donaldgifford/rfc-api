package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/server/httperr"
)

// KeyFunc extracts the rate-limit bucket key for a request. v1 default
// is IPKey (first X-Forwarded-For hop, falling back to RemoteAddr);
// Phase 4 swaps in a function that prefers the authenticated
// principal.
type KeyFunc func(*http.Request) string

// RateLimitConfig names the knobs. RPS ≤ 0 or Burst ≤ 0 disables the
// middleware outright — constructors pass the server config straight
// through, so "rate limit disabled" is a single zero-value answer.
type RateLimitConfig struct {
	RPS   float64
	Burst int
	TTL   time.Duration
	Sweep time.Duration
	Key   KeyFunc
}

// RateLimit applies a per-key token-bucket rate limit via
// golang.org/x/time/rate. Per-key limiters are TTL-evicted in the
// background so the map does not grow unbounded. The sweeper goroutine
// is tied to a background context; the returned Middleware has no
// lifecycle handle — the limiter lives for the process lifetime,
// which matches the server's actual scope.
//
// 429 responses use the RFC 7807 envelope with a Retry-After header
// (seconds, rounded up from the limiter's wait estimate).
func RateLimit(cfg RateLimitConfig) Middleware {
	if cfg.RPS <= 0 || cfg.Burst <= 0 {
		return passThrough
	}
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	if cfg.Sweep <= 0 {
		cfg.Sweep = 5 * time.Minute
	}
	if cfg.Key == nil {
		cfg.Key = IPKey
	}

	l := newLimiterStore(rate.Limit(cfg.RPS), cfg.Burst, cfg.TTL)
	go l.runSweeper(cfg.Sweep)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := cfg.Key(r)
			limiter := l.forKey(key)
			reservation := limiter.Reserve()
			if !reservation.OK() {
				// Bucket exhausted beyond burst; reject.
				httperr.Write(w, r, fmt.Errorf("%w: rate limit exceeded", domain.ErrInvalidInput))
				return
			}
			if delay := reservation.Delay(); delay > 0 {
				reservation.Cancel()
				retry := int(delay.Seconds())
				if retry < 1 {
					retry = 1
				}
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retry))
				httperr.Write(w, r, errors.New("rate limit exceeded"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IPKey derives a rate-limit key from the request's client IP. Trusts
// the first X-Forwarded-For hop for deployments behind an ingress;
// falls back to net.SplitHostPort on RemoteAddr otherwise. v1 does
// not attempt forwarded-header spoofing detection — the ingress is
// expected to rewrite the header before the request reaches the
// server.
func IPKey(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// passThrough is used when rate-limit config is zero-value.
var passThrough Middleware = func(next http.Handler) http.Handler { return next }

type limiterEntry struct {
	limiter *rate.Limiter
	last    time.Time
}

type limiterStore struct {
	mu      sync.Mutex
	entries map[string]*limiterEntry
	rps     rate.Limit
	burst   int
	ttl     time.Duration
}

func newLimiterStore(rps rate.Limit, burst int, ttl time.Duration) *limiterStore {
	return &limiterStore{
		entries: make(map[string]*limiterEntry),
		rps:     rps,
		burst:   burst,
		ttl:     ttl,
	}
}

func (s *limiterStore) forKey(key string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		e = &limiterEntry{limiter: rate.NewLimiter(s.rps, s.burst)}
		s.entries[key] = e
	}
	e.last = time.Now()
	return e.limiter
}

func (s *limiterStore) runSweeper(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for now := range t.C {
		s.evictBefore(now.Add(-s.ttl))
	}
}

func (s *limiterStore) evictBefore(cutoff time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.entries {
		if e.last.Before(cutoff) {
			delete(s.entries, k)
		}
	}
}
