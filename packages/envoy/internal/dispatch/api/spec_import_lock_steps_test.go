package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/specstore"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest/lockstep"
)

// TestTheRepairIssueTakesTheStoreLockOrder holds the repair issue of a level's holds
// (repairIssueHook) to the store's lock order, written on api/inbox_read.go's
// markInboxReadThreads. The hook runs in the transaction of the create that opens the child issue,
// after the create's own writes (createIssueWith), and its one write is a repair_issue value on each
// hold's node at the sentinel: tuples, which lock no row, so the step a hold can stand on is the
// owner lock the create takes first, its project's. The hook reads the holds again under that lock,
// so the children of two imports at once queue there and the second finds the holds named. Held
// there, the create has read nothing of the store yet: its transaction holds no lock on graph_nodes
// or graph_tuples. The import route runs on a server whose store is one connection of its own, whose
// backend the test watches: the import's own transaction takes no owner lock and commits first, and
// the create then queues behind the project's lock. After it each hold the import kept names the
// child issue the import answers.
func TestTheRepairIssueTakesTheStoreLockOrder(t *testing.T) {
	ctx := context.Background()
	handler, database, deps := newTestServer(t, testServerOptions{settle: time.Hour})
	createReferenceAPIProject(t, handler, "CORE")
	rig := &specRig{handler: handler, database: database}
	level := rig.issue(t, "Level one of the fixture", specRepo, levelFixture, "")
	uploadDocument(t, handler, "/api/v1/issues/"+level.Key, "notes.md", levelNotesFixture)
	rig.approve(t, rig.request(t, level, false))
	rig.addSpecSource(t, specRepo)

	single := storetest.SingleConnection(t, database)
	singleDeps := deps
	singleDeps.Store = single
	served := http.NewServeMux()
	// Registered after the one-connection store, so this server's wait runs before that store
	// closes.
	t.Cleanup(Register(served, t.Context(), singleDeps).Wait)
	var pid uint32
	if err := single.Pool.QueryRow(ctx, `select pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read the import's backend: %v", err)
	}

	owner := lockstep.HoldOwner(t, database, &lockstep.Hold{
		Name: "project CORE's owner lock", Step: lockstep.StepOwner, Taken: true, Kind: "project", ID: "CORE",
		Statement: lockstep.OwnerLockStatement,
		Unlocked:  []string{"graph_nodes", "graph_tuples"},
	}, store.ProjectOwner("CORE"))
	var answer *httptest.ResponseRecorder
	done := make(chan error, 1)
	go func() {
		answer = sessionRequest(t, served, http.MethodPost, "/api/v1/issues/"+level.Key+"/spec-import-level",
			map[string]any{"actor": sessionActor(), "notes": "notes.md"})
		done <- statusOrError(answer, http.StatusOK)
	}()
	lockstep.Follow(t, database, "repairIssueHook", pid, done, []*lockstep.Hold{owner})

	imported := decodeBody[levelImport](t, answer)
	if imported.RepairIssue == nil || len(imported.Held) == 0 {
		t.Fatalf("the import answers %+v, want the holds it kept and their repair issue", imported)
	}
	holds, err := specstore.Holds(ctx, database.Pool, "CORE", specRepo)
	if err != nil {
		t.Fatalf("read the holds of %s: %v", specRepo, err)
	}
	if len(holds) != len(imported.Held) {
		t.Errorf("the approved spec of %s holds %d open holds, want the %d the import kept", specRepo, len(holds), len(imported.Held))
	}
	for _, hold := range holds {
		if hold.RepairIssue != *imported.RepairIssue {
			t.Errorf("the hold of %s names the repair issue %q, want %s, the child the import opened", hold.ID, hold.RepairIssue, *imported.RepairIssue)
		}
	}
}
