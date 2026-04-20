package doczmarkdown_test

import (
	"os"
	"testing"
)

func readFile(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(path)
}
