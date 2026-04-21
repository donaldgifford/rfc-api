package meilisearch_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	meili "github.com/meilisearch/meilisearch-go"

	"github.com/donaldgifford/rfc-api/internal/config"
	"github.com/donaldgifford/rfc-api/internal/search/meilisearch"
)

// fakeMeiliSettings runs a minimal Meili stub that:
//   - returns `currentSettings` on GET /indexes/documents/settings
//   - counts every PATCH to the settings endpoint
//   - completes every created task synchronously
//
// Tests use it to assert idempotency (zero PATCHes when settings match)
// and write-when-divergent (one PATCH when they don't).
func fakeMeiliSettings(t *testing.T, currentSettings *meili.Settings) (*meilisearch.Client, *atomic.Int32) {
	t.Helper()

	var patchHits atomic.Int32
	var taskID atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/indexes/documents", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uid":        "documents",
			"primaryKey": "id",
		})
	})
	mux.HandleFunc("/indexes/documents/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(currentSettings)
		case http.MethodPatch:
			patchHits.Add(1)
			tid := taskID.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"taskUid":    tid,
				"indexUid":   "documents",
				"status":     "enqueued",
				"type":       "settingsUpdate",
				"enqueuedAt": "2026-04-20T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/tasks/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		uidStr := strings.TrimPrefix(r.URL.Path, "/tasks/")
		uid, _ := strconv.ParseInt(uidStr, 10, 64)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uid":        uid,
			"indexUid":   "documents",
			"status":     "succeeded",
			"type":       "settingsUpdate",
			"enqueuedAt": "2026-04-20T00:00:00Z",
			"finishedAt": "2026-04-20T00:00:01Z",
			"duration":   "PT1S",
			"startedAt":  "2026-04-20T00:00:00Z",
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := meilisearch.NewWriteClient(config.Meili{URL: srv.URL, MasterKey: "k"})
	if err != nil {
		t.Fatalf("NewWriteClient: %v", err)
	}
	return c, &patchHits
}

func TestApplySettings_NoOpWhenMatches(t *testing.T) {
	// Server already reports exactly the desired shape; ApplySettings
	// should read, compare, and return without writing.
	c, patches := fakeMeiliSettings(t, meilisearch.DesiredSettings())
	if err := meilisearch.ApplySettings(t.Context(), c); err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	if got := patches.Load(); got != 0 {
		t.Errorf("patches = %d, want 0 (idempotent no-op)", got)
	}
}

func TestApplySettings_WritesWhenDivergent(t *testing.T) {
	// Simulate a freshly-created index reporting empty settings.
	c, patches := fakeMeiliSettings(t, &meili.Settings{})
	if err := meilisearch.ApplySettings(t.Context(), c); err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	if got := patches.Load(); got != 1 {
		t.Errorf("patches = %d, want 1 (write on first apply)", got)
	}
}

func TestDesiredSettings_RankingRulesEndWithCreatedAtTiebreaker(t *testing.T) {
	s := meilisearch.DesiredSettings()
	if got := s.RankingRules[len(s.RankingRules)-1]; got != "created_at:desc" {
		t.Errorf("last ranking rule = %q, want created_at:desc tiebreaker", got)
	}
}

func TestDesiredSettings_RequiredAttributes(t *testing.T) {
	s := meilisearch.DesiredSettings()
	mustContain(t, "searchable", s.SearchableAttributes,
		"title", "section_heading", "body_excerpt")
	mustContain(t, "filterable", s.FilterableAttributes,
		"type", "status", "labels", "author_handles", "visibility")
	mustContain(t, "sortable", s.SortableAttributes,
		"created_at", "updated_at")
}

func mustContain(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s attributes missing %q (got %v)", label, w, got)
		}
	}
}
