package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// `envoy-dispatch backfill-block-ids` over a database that holds no document prints one
// `documents=0` line and exits 0, rather than nothing, so an operator can tell a run that owed
// nothing from one that printed nothing.
func TestBackfillBlockIDsSaysWhenNoDocumentWasOwed(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	var stdout, stderr bytes.Buffer
	code := runSubcommand(ctx, []string{"backfill-block-ids"},
		envGetter(map[string]string{"DATABASE_URL": database.Pool.Config().ConnString()}), &stdout, &stderr)
	if printed := stdout.String() + stderr.String(); code != 0 || !strings.Contains(printed, "backfill-block-ids: documents=0\n") {
		t.Errorf("backfill-block-ids over a database with no document exited %d and printed %q; want exit 0 and a documents=0 line", code, printed)
	}
}
