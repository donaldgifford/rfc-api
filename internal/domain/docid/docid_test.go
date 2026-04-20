package docid_test

import (
	"testing"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/domain/docid"
)

func TestCanonical(t *testing.T) {
	got := docid.Canonical("rfc", "0001")
	if got != domain.DocumentID("RFC-0001") {
		t.Errorf("Canonical(rfc, 0001) = %q, want RFC-0001", got)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		in        domain.DocumentID
		wantType  string
		wantURLID string
		wantOK    bool
	}{
		{"RFC-0001", "rfc", "0001", true},
		{"ADR-0042", "adr", "0042", true},
		{"SEC-0001", "sec", "0001", true},
		{"RFC0001", "", "", false},
		{"-0001", "", "", false},
		{"RFC-", "", "", false},
		{"", "", "", false},
		{"RFC-00-01", "", "", false},
	}
	for _, tc := range tests {
		gotType, gotURLID, gotOK := docid.Parse(tc.in)
		if gotType != tc.wantType || gotURLID != tc.wantURLID || gotOK != tc.wantOK {
			t.Errorf("Parse(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, gotType, gotURLID, gotOK,
				tc.wantType, tc.wantURLID, tc.wantOK)
		}
	}
}

func TestURLForm(t *testing.T) {
	if got := docid.URLForm("RFC-0042"); got != "0042" {
		t.Errorf("URLForm(RFC-0042) = %q, want 0042", got)
	}
	if got := docid.URLForm("malformed"); got != "" {
		t.Errorf("URLForm(malformed) = %q, want empty", got)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, id := range []domain.DocumentID{"RFC-0001", "ADR-0042", "SEC-9999"} {
		typeID, urlID, ok := docid.Parse(id)
		if !ok {
			t.Fatalf("Parse(%q) returned !ok", id)
		}
		if got := docid.Canonical(typeID, urlID); got != id {
			t.Errorf("round-trip: Canonical(Parse(%q)) = %q, want %q", id, got, id)
		}
	}
}
