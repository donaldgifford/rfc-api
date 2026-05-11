// Package contract_test extension: pins rfc-api's slug.Slug and
// slug.Slugger to a snapshot generated from upstream
// github-slugger@2.0.0 (see scripts/regen-slug-fixtures.sh).
//
// The unit tests in internal/slug/slug_test.go verify rfc-api's
// port against its OWN spec. This contract test verifies rfc-api's
// port against the SAME bytes upstream emits, so any drift between
// the two trips here before reindex divergence shows up in prod.
// rfc-site runs an equivalent test on its side against the same
// pinned upstream version — independent re-derivation per IMPL-0006
// OQ3.
//
// If this test fails after a deliberate upstream version bump:
// 1) regenerate via `make regen-slug-fixtures`,
// 2) review the diff to confirm only intended changes shifted,
// 3) commit the regenerated fixture + the SLUGGER_VERSION bump in
//    one commit so reviewers can see why.

package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/slug"
)

type slugFixture struct {
	Pure []struct {
		Input    string `json:"input"`
		Expected string `json:"expected"`
	} `json:"pure"`
	CollisionGroups []struct {
		Name     string `json:"name"`
		Sequence []struct {
			Input    string `json:"input"`
			Expected string `json:"expected"`
		} `json:"sequence"`
	} `json:"collision_groups"`
}

func loadSlugFixture(t *testing.T) slugFixture {
	t.Helper()
	path := filepath.Join("testdata", "slug_fixtures.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v (run `make regen-slug-fixtures`?)", path, err)
	}
	var fx slugFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	if len(fx.Pure) == 0 || len(fx.CollisionGroups) == 0 {
		t.Fatalf("fixture %s missing rows: pure=%d collision_groups=%d",
			path, len(fx.Pure), len(fx.CollisionGroups))
	}
	return fx
}

// TestSlug_ContractAgainstUpstream walks every "pure" row in the
// snapshot and asserts slug.Slug emits the same bytes as upstream
// github-slugger. Bare-function semantics — no Slugger state.
func TestSlug_ContractAgainstUpstream(t *testing.T) {
	t.Parallel()
	fx := loadSlugFixture(t)
	for _, row := range fx.Pure {
		t.Run(row.Input, func(t *testing.T) {
			t.Parallel()
			got := slug.Slug(row.Input)
			if got != row.Expected {
				t.Errorf("slug.Slug drift from upstream github-slugger:\n  input=%q\n   want=%q\n    got=%q",
					row.Input, row.Expected, got)
			}
		})
	}
}

// TestSlugger_ContractAgainstUpstream walks each named collision
// group with a fresh Slugger and asserts each step matches what
// upstream github-slugger's stateful slug() emits in the same
// sequence. Covers the per-document suffixing path that the pure
// function cannot exercise.
func TestSlugger_ContractAgainstUpstream(t *testing.T) {
	t.Parallel()
	fx := loadSlugFixture(t)
	for _, group := range fx.CollisionGroups {
		t.Run(group.Name, func(t *testing.T) {
			t.Parallel()
			g := slug.NewSlugger()
			for i, step := range group.Sequence {
				got := g.Slug(step.Input)
				if got != step.Expected {
					t.Errorf("slug.Slugger drift from upstream github-slugger:\n  group=%s step=%d\n  input=%q\n   want=%q\n    got=%q",
						group.Name, i, step.Input, step.Expected, got)
				}
			}
		})
	}
}
