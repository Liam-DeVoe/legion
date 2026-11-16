package requests

import (
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/airlock"
)

// TestEveryReaderReadsTheProjectsOfBothCredentials is round 6's m-d and m-f on
// cybermonklabs/legion#4135: the pending list, the record's page and the grant list read the
// projects of both credentials a request has had — the enrollment that asked and the one it hangs
// off now — as the decision does (TestADecisionChecksTheProjectsOfBothEnrollments), so a request
// the decision would refuse a person is one that person is not shown either.
func TestEveryReaderReadsTheProjectsOfBothCredentials(t *testing.T) {
	t.Run("a pending request now on a credential naming PRIV", func(t *testing.T) {
		m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
		req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "rotate the shared key", "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil {
			t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending for anyone to decide", req, err)
		}
		if listed := pendingRecords(t, m, outsider); !slices.Contains(listed, *req.RecordID) {
			t.Fatalf("PendingForApprover(%s) before the move = %v; want the request, which names no project", outsider, listed)
		}
		second, _ := newEnrollment(t, m.Store, "session", "box-r-"+t.Name(), new(fixtureOperator), nil)
		nameProjects(t, m, second, privProject)
		if _, err := m.Store.Pool.Exec(t.Context(), `update requests set enrollment_id=$2 where id=$1`, req.ID, second); err != nil {
			t.Fatalf("move the request: %v", err)
		}

		if listed := pendingRecords(t, m, outsider); slices.Contains(listed, *req.RecordID) {
			t.Errorf("PendingForApprover(%s) = %v; want the request hidden: the credential it hangs off names PRIV", outsider, listed)
		}
		if listed := pendingRecords(t, m, insider); !slices.Contains(listed, *req.RecordID) {
			t.Errorf("PendingForApprover(%s) = %v; want the request: PRIV lists them", insider, listed)
		}
		detail, err := m.ReadRecord(t.Context(), *req.RecordID)
		if err != nil || !slices.Contains(detail.SessionProjects, privProject) {
			t.Fatalf("ReadRecord = %+v, %v; want its session projects to name PRIV", detail.SessionProjects, err)
		}
		if may, err := m.MaySee(t.Context(), outsider, detail); err != nil || may {
			t.Errorf("MaySee(%s, the moved request) = %v, %v; want false: the page hides it, so it offers no Approve the decision refuses", outsider, may, err)
		}
		if may, err := m.MaySee(t.Context(), insider, detail); err != nil || !may {
			t.Errorf("MaySee(%s, the moved request) = %v, %v; want true", insider, may, err)
		}
	})

	t.Run("a grant whose asking credential names PRIV", func(t *testing.T) {
		m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
		req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "rotate the shared key", "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil {
			t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending for anyone to decide", req, err)
		}
		dec, err := m.ApplyDecision(t.Context(), *req.RecordID, true, outsider)
		if err != nil || dec.State != "granted" {
			t.Fatalf("%s's approval = %+v, %v; want it granted: nothing names a private project yet", outsider, dec, err)
		}
		second, _ := newEnrollment(t, m.Store, "session", "box-g-"+t.Name(), new(fixtureOperator), nil)
		for _, move := range []string{`update requests set enrollment_id=$2 where id=$1`, `update grants set enrollment_id=$2 where request_id=$1`} {
			if _, err := m.Store.Pool.Exec(t.Context(), move, req.ID, second); err != nil {
				t.Fatalf("move the request and its grant: %v", err)
			}
		}
		if listed := approverGrants(t, m, outsider); !slices.Contains(listed, dec.GrantID) {
			t.Fatalf("GrantsForApprover(%s) after the move = %v; want the grant they approved", outsider, listed)
		}
		// The asking credential's list is written here directly, standing in for any way the
		// projects of the two credentials come to differ.
		nameProjects(t, m, enr, privProject)

		if listed := approverGrants(t, m, outsider); slices.Contains(listed, dec.GrantID) {
			t.Errorf("GrantsForApprover(%s) = %v; want the grant hidden: the credential that asked names PRIV", outsider, listed)
		}
	})
}
