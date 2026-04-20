package reqctx_test

import (
	"context"
	"testing"

	"github.com/donaldgifford/rfc-api/internal/server/reqctx"
)

func TestWithAndID(t *testing.T) {
	ctx := reqctx.WithID(context.Background(), "abc-123")
	if got := reqctx.ID(ctx); got != "abc-123" {
		t.Errorf("ID = %q, want abc-123", got)
	}
}

func TestID_BareContext(t *testing.T) {
	if got := reqctx.ID(context.Background()); got != "" {
		t.Errorf("ID(bare) = %q, want empty", got)
	}
}
