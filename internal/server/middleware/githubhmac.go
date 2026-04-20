package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/server/httperr"
)

// maxWebhookBody caps the HMAC-verified body at 25 MiB. GitHub's
// published webhook payload cap is 25 MiB, which bounds the worst-
// case memory buffer at the middleware. Anything larger is rejected
// before a hash is computed.
const maxWebhookBody = 25 * 1024 * 1024

// signaturePrefix is the GitHub v2 webhook signature scheme — the
// only one we accept.
const signaturePrefix = "sha256="

// VerifyGitHubHMAC validates the X-Hub-Signature-256 header against
// the raw request body using the supplied secret. On success it
// replaces r.Body with a fresh bytes.Reader so the downstream handler
// sees the same bytes. On failure it writes 401 problem+json and
// stops.
//
// The empty-secret guard refuses to register the middleware rather
// than silently accepting every webhook — an empty secret is almost
// always a config bug. Callers should have validated cfg.Webhook.Secret
// at startup before wiring this middleware in.
func VerifyGitHubHMAC(secret string) Middleware {
	if secret == "" {
		// Rather than panic at startup, return a middleware that
		// rejects every request. Callers that need a lenient path
		// should not register this middleware in the first place.
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httperr.Write(w, r, errUnauthenticatedHMAC)
			})
		}
	}

	secretBytes := []byte(secret)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sig := r.Header.Get("X-Hub-Signature-256")
			if !strings.HasPrefix(sig, signaturePrefix) {
				httperr.Write(w, r, errUnauthenticatedHMAC)
				return
			}
			expected, err := hex.DecodeString(sig[len(signaturePrefix):])
			if err != nil {
				httperr.Write(w, r, errUnauthenticatedHMAC)
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
			if err != nil {
				httperr.Write(w, r, fmt.Errorf("%w: read body: %w", domain.ErrInvalidInput, err))
				return
			}
			if err := r.Body.Close(); err != nil {
				// Best-effort; the inbound body is read, the close
				// failure is not actionable for the client.
				_ = err
			}
			if len(body) > maxWebhookBody {
				httperr.Write(w, r, fmt.Errorf("%w: body exceeds %d bytes", domain.ErrInvalidInput, maxWebhookBody))
				return
			}

			mac := hmac.New(sha256.New, secretBytes)
			mac.Write(body)
			if !hmac.Equal(mac.Sum(nil), expected) {
				httperr.Write(w, r, errUnauthenticatedHMAC)
				return
			}

			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			next.ServeHTTP(w, r)
		})
	}
}

// errUnauthenticatedHMAC is returned for any HMAC verification
// failure. Uniform error means attackers cannot distinguish "bad
// signature" from "missing header" from "wrong digest" by response
// shape. Classified as domain.ErrUnauthenticated to surface as 401.
var errUnauthenticatedHMAC = fmt.Errorf("%w: webhook signature invalid", domain.ErrUnauthenticated)
