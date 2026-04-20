package domain_test

import (
	"testing"

	"github.com/donaldgifford/rfc-api/internal/domain"
)

func TestDocumentID_String(t *testing.T) {
	if got := domain.DocumentID("RFC-0001").String(); got != "RFC-0001" {
		t.Errorf("String = %q", got)
	}
}
