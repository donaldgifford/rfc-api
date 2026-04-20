package githubsource_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donaldgifford/rfc-api/internal/worker/githubsource"
)

// fakeGitHub spins an httptest server with a mux matching the
// subset of go-github endpoints the client uses. Each test passes a
// handler set tailored to what it wants to assert.
func fakeGitHub(t *testing.T, handlers map[string]http.HandlerFunc) *githubsource.Client {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, h := range handlers {
		mux.HandleFunc(pattern, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := githubsource.New(&githubsource.Config{
		Token:      "test-pat",
		BaseURL:    srv.URL + "/",
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		MaxRetries: 2,
		MaxBackoff: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("githubsource.New: %v", err)
	}
	return client
}

func TestNew_RejectsNoCreds(t *testing.T) {
	if _, err := githubsource.New(&githubsource.Config{}); err == nil {
		t.Fatal("want error when neither creds nor token are set")
	}
}

func TestNew_RejectsBothCreds(t *testing.T) {
	_, err := githubsource.New(&githubsource.Config{
		AppID:          "1",
		InstallationID: "2",
		PrivateKey:     []byte("stub"),
		Token:          "pat",
	})
	if err == nil {
		t.Fatal("want error when both creds sets are provided")
	}
}

func TestListFiles_FiltersMarkdown(t *testing.T) {
	body := `[
		{"type":"file","name":"0001.md","path":"docs/0001.md","sha":"aaa","size":12},
		{"type":"file","name":"README.txt","path":"docs/README.txt","sha":"bbb","size":5},
		{"type":"dir","name":"sub","path":"docs/sub","sha":"ccc"}
	]`
	client := fakeGitHub(t, map[string]http.HandlerFunc{
		"/api/v3/repos/owner/repo/contents/docs": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		},
	})

	files, err := client.ListFiles(t.Context(), "owner/repo", "docs", "main")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "docs/0001.md" {
		t.Fatalf("want one md file, got %+v", files)
	}
	if files[0].SHA != "aaa" {
		t.Errorf("sha = %q, want aaa", files[0].SHA)
	}
}

func TestGetFile_DecodesBase64(t *testing.T) {
	content := "# Hello\n"
	payload := map[string]any{
		"type":     "file",
		"encoding": "base64",
		"content":  base64.StdEncoding.EncodeToString([]byte(content)),
		"sha":      "deadbeef",
		"path":     "docs/0001.md",
		"name":     "0001.md",
	}
	client := fakeGitHub(t, map[string]http.HandlerFunc{
		"/api/v3/repos/owner/repo/contents/docs/0001.md": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload)
		},
	})

	got, sha, err := client.GetFile(t.Context(), "owner/repo", "docs/0001.md", "main")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if string(got) != content {
		t.Errorf("content = %q, want %q", got, content)
	}
	if sha != "deadbeef" {
		t.Errorf("sha = %q", sha)
	}
}

// TestRateLimit_BackoffThenSuccess asserts a 403 with rate-limit
// headers retries (no crash) and eventually returns the 200 body.
func TestRateLimit_BackoffThenSuccess(t *testing.T) {
	var hits atomic.Int32
	resetAt := time.Now().Add(20 * time.Millisecond).Unix()
	client := fakeGitHub(t, map[string]http.HandlerFunc{
		"/api/v3/repos/owner/repo/contents/docs": func(w http.ResponseWriter, _ *http.Request) {
			if hits.Add(1) == 1 {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetAt))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"type":"file","name":"a.md","path":"docs/a.md","sha":"x"}]`))
		},
	})

	files, err := client.ListFiles(t.Context(), "owner/repo", "docs", "main")
	if err != nil {
		t.Fatalf("ListFiles after backoff: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("want one file after retry, got %d", len(files))
	}
	if got := hits.Load(); got < 2 {
		t.Errorf("want ≥2 requests (retry), got %d", got)
	}
}

func TestSplitRepo_BadShape(t *testing.T) {
	client := fakeGitHub(t, map[string]http.HandlerFunc{})
	if _, err := client.ListFiles(t.Context(), "bad", "docs", "main"); err == nil {
		t.Fatal("want error on malformed repo")
	} else if !strings.Contains(err.Error(), "owner/name") {
		t.Errorf("unexpected error: %v", err)
	}
}
