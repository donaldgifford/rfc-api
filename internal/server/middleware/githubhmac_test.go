package middleware_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/middleware"
)

func signBody(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestVerifyGitHubHMAC_Accepts(t *testing.T) {
	body := `{"ok":true}`
	secret := "shh"
	handled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(buf) != body {
			t.Errorf("inner saw body = %q", string(buf))
		}
		handled = true
		w.WriteHeader(202)
	})
	h := middleware.VerifyGitHubHMAC(secret)(inner)

	req := httptest.NewRequestWithContext(t.Context(), "POST", "/webhook", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signBody(secret, body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !handled {
		t.Fatal("inner not invoked")
	}
	if rec.Code != 202 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestVerifyGitHubHMAC_RejectsBadSignature(t *testing.T) {
	h := middleware.VerifyGitHubHMAC("shh")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner should not be invoked")
	}))
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/webhook", strings.NewReader("payload"))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestVerifyGitHubHMAC_RejectsMissingHeader(t *testing.T) {
	h := middleware.VerifyGitHubHMAC("shh")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner should not be invoked")
	}))
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/webhook", strings.NewReader("payload"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestVerifyGitHubHMAC_EmptySecretRejectsAll(t *testing.T) {
	h := middleware.VerifyGitHubHMAC("")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner should not be invoked")
	}))
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/webhook", strings.NewReader("payload"))
	req.Header.Set("X-Hub-Signature-256", "sha256=00")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
