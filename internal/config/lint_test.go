package config_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoOsGetenvOutsideConfig enforces DESIGN-0001 #Configuration: no
// os.Getenv calls anywhere in the tree except internal/config/.
//
// The rule keeps the config surface discoverable: one package knows the
// full set of env-var names, and changes to naming are visible in one
// grep scope. Violations are cheap to miss in review; a test makes them
// cheap to catch.
func TestNoOsGetenvOutsideConfig(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	searchDirs := []string{
		filepath.Join(repoRoot, "cmd"),
		filepath.Join(repoRoot, "internal"),
	}

	var violations []string
	for _, root := range searchDirs {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// internal/config is the allowed home.
			if strings.Contains(path, filepath.Join("internal", "config")) {
				return nil
			}
			// Skip test files too; test-only os.Getenv reads are harmless.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "os.Getenv") {
				rel, _ := filepath.Rel(repoRoot, path)
				violations = append(violations, rel)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("walk %q: %v", root, err)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("os.Getenv found outside internal/config/:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod upward)")
		}
		dir = parent
	}
}
