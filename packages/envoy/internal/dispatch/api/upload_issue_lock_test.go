package api

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// statementOf is a queryer that records the one statement it is asked to run and runs none.
type statementOf struct{ sql string }

var errNotRun = errors.New("not run")

type unrunRow struct{}

func (unrunRow) Scan(...any) error { return errNotRun }

func (s *statementOf) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	s.sql = sql
	return unrunRow{}
}

func (s *statementOf) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	s.sql = sql
	return nil, errNotRun
}

func (s *statementOf) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }

// TestTheUploadLockProbeKnowsTheUploadsOwnStatement: the upload lock probe's check of the row's
// holder (holdsUploadIssueLock) recognizes the statement an upload takes the issue row with,
// issueStatus under ownerRowLock as it is sent, project clause and all, and no other owner
// statement: not a settlement's lock of the row, nor issueStatus read with no lock. Without it,
// the probe's check that the upload holds no row while its bytes are in the store could never
// fail, since the statement it looked for had stopped existing once the clause went in.
func TestTheUploadLockProbeKnowsTheUploadsOwnStatement(t *testing.T) {
	sent := &statementOf{}
	if _, _, err := issueStatus(context.Background(), sent, everyReader, "TEST-1", ownerRowLock); !errors.Is(err, errNotRun) {
		t.Fatalf("issueStatus ran its statement or failed otherwise: %v", err)
	}
	if !holdsUploadIssueLock(sent.sql) {
		t.Fatalf("the probe does not know the upload's own lock of the issue row:\n%s", sent.sql)
	}
	unlocked := &statementOf{}
	if _, _, err := issueStatus(context.Background(), unlocked, everyReader, "TEST-1", ""); !errors.Is(err, errNotRun) {
		t.Fatalf("issueStatus ran its statement or failed otherwise: %v", err)
	}
	for _, other := range []string{settlementOwnerStatement, lockIssueStatement, unlocked.sql} {
		if holdsUploadIssueLock(other) {
			t.Errorf("the probe takes %q for the upload's lock of the issue row", other)
		}
	}
}
