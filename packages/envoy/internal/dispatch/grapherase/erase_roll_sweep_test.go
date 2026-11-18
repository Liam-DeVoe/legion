package grapherase

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/processes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest/lockstep"
)

// rollBuildCommit is the commit of the release before this one, serving beside this build's
// unstamped test service during a roll.
const rollBuildCommit = "release-before-this-one"

// During a roll, this build's block-index tick writes the node of every row the release before it
// wrote with none. That release's removal of a document states nothing, so a document it removes
// after this build wrote its node leaves the node standing behind no row: the project census's
// orphaned class. The next tick of the roll erases that node, through the erase's own path
// (EraseOrphaned, which cmd/dispatch gives the document service), and the census then reads the
// store clean, the store census included, with the erasure recorded as every erase records one.
//
// Its control is the document service without its eraser (docs.Deps' EraseOrphanedNode nil, as
// before the sweep): the tick then erases nothing, and the census still names the node orphaned.
func TestARollTickErasesTheNodeOfADocumentThePreviousReleaseRemoved(t *testing.T) {
	for _, withEraser := range []bool{true, false} {
		name := "with the eraser"
		if !withEraser {
			name = "control: without the eraser"
		}
		t.Run(name, func(t *testing.T) {
			database := storetest.Open(t)
			ctx := context.Background()
			seeded := seedObjectNodes(t, database)
			service := rollService(t, database, withEraser)
			registerRollBuild(t, database)
			if tick := reconcile(t, service); !tick.AnotherBuild {
				t.Fatalf("the first tick found no other build: %+v", tick)
			}
			requireOrphaned(t, database, 0)

			// The release before this one removes the document whose node this build wrote.
			if _, err := database.Pool.Exec(ctx, `delete from artifacts where ref_key = $1`, seeded.documentRef); err != nil {
				t.Fatalf("remove the document as the release before does: %v", err)
			}
			requireOrphaned(t, database, 1)

			tick := reconcile(t, service)
			if !withEraser {
				if tick.OrphansErased != 0 {
					t.Errorf("the tick without an eraser erased %d node(s), want none", tick.OrphansErased)
				}
				requireOrphaned(t, database, 1)
				return
			}
			if tick.OrphansErased != 1 {
				t.Errorf("the roll's next tick erased %d orphaned node(s), want the removed document's", tick.OrphansErased)
			}
			if held := readTypeNode(t, database, graphstore.KindArtifact, seeded.documentRef); held.row || held.out != 0 || held.in != 0 || held.erased != 1 {
				t.Errorf("after the tick the store holds %+v of the removed document's node, want no row, no tuple and one erasure", held)
			}
			requireOrphaned(t, database, 0)
			requireCensusZero(t, database, "after the roll's tick erased the orphaned node")
			for _, standing := range []struct{ kind, id string }{
				{graphstore.KindIssue, seeded.issue}, {graphstore.KindAsk, seeded.ask}, {graphstore.KindMessage, seeded.message},
			} {
				if held := readTypeNode(t, database, standing.kind, standing.id); !held.row || held.erased != 0 {
					t.Errorf("after the tick the store holds %+v of %s %s, whose row stands, want it as seeded", held, standing.kind, standing.id)
				}
			}
		})
	}
}

// A create of this build at the address of an orphaned node writes the node and then the row in
// one transaction, holding the node's row until it commits. The roll tick's sweep lists the
// orphaned node from what is committed, and its confirmed erase then queues behind the create on
// the node: once the create commits, the erase reads the row standing and refuses, and the sweep
// leaves the node, erasing nothing. The node, its tuples and the created row all stand, and the
// census reads nothing.
//
// Its control is the erase's check of a standing row taken out (reads.go's ObjectRowStands
// refusal): the sweep's erase then removes the node the created row stands behind, and the census
// names that row missing its node.
func TestARollTickSweepLosesNothingOfACreateItRacesWith(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedObjectNodes(t, database)
	service := rollService(t, database, true)
	registerRollBuild(t, database)
	if _, err := database.Pool.Exec(ctx, `delete from artifacts where ref_key = $1`, seeded.documentRef); err != nil {
		t.Fatalf("remove the document as the release before does: %v", err)
	}
	requireOrphaned(t, database, 1)
	before := readTypeNode(t, database, graphstore.KindArtifact, seeded.documentRef)

	create := &lockstep.Hold{Name: "a create of " + seeded.documentRef + " that wrote its node and its row"}
	lockstep.Begin(t, database, create)
	if _, err := graphstore.WriteUncaused(ctx, create.Tx, []graphstore.Node{graphstore.ArtifactNode(
		seeded.documentRef, "ERASE", "", model.Actor{Kind: "user", ID: "alice"}, time.Now().UTC())}, nil); err != nil {
		t.Fatalf("write the created document's node: %v", err)
	}
	if _, err := create.Tx.Exec(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, created_by)
		values ('ERASE-1', 'ERASE', 'notes-md', 'Notes made again', 'doc', '{"kind":"user","id":"alice"}')
	`); err != nil {
		t.Fatalf("write the created document's row: %v", err)
	}

	type answer struct {
		tick docs.BlockIndexTick
		err  error
	}
	ticked := make(chan answer, 1)
	go func() {
		tick, err := service.ReconcileBlockIndex(ctx, docs.BlockIndexCursors{}, nil)
		ticked <- answer{tick, err}
	}()
	waiting := ""
	for deadline := time.Now().Add(lockstep.Wait); waiting == ""; {
		if err := database.Pool.QueryRow(ctx, `
			select coalesce((select query from pg_stat_activity
			                 where application_name = $1 and wait_event_type = 'Lock' limit 1), '')
		`, eraseApplicationName).Scan(&waiting); err != nil {
			t.Fatalf("read where the sweep's erase waits: %v", err)
		}
		select {
		case early := <-ticked:
			t.Fatalf("the tick answered (%+v, %v) before its erase waited on the create, so this test proves nothing", early.tick, early.err)
		default:
		}
		if waiting == "" && time.Now().After(deadline) {
			t.Fatalf("the sweep's erase waited on no lock within %s", lockstep.Wait)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("the sweep's erase waits at %q", waiting)
	if err := create.Tx.Commit(ctx); err != nil {
		t.Fatalf("commit the create: %v", err)
	}
	create.Release()
	var got answer
	select {
	case got = <-ticked:
	case <-time.After(lockstep.Wait):
		t.Fatalf("the tick did not answer within %s of the create's commit", lockstep.Wait)
	}
	if got.err != nil || got.tick.OrphansErased != 0 {
		t.Errorf("the tick answered %v and erased %d node(s), want no error and nothing erased", got.err, got.tick.OrphansErased)
	}
	if after := readTypeNode(t, database, graphstore.KindArtifact, seeded.documentRef); after != before {
		t.Errorf("after the create and the sweep the store holds %+v of the document's node, want %+v as the create left it", after, before)
	}
	if name := rowText(t, database, "artifacts", "name", "ref_key", seeded.documentRef); name != "Notes made again" {
		t.Errorf("the created document's row reads %q, want the create's name", name)
	}
	incomplete, err := graphcensus.ProjectComplete(ctx, database.Pool)
	if err != nil || len(incomplete) != 0 {
		t.Errorf("after the create and the sweep the project census names %q (%v), want nothing", incomplete, err)
	}
}

// rollService is a document service of this build, unstamped, whose block-index tick erases
// through EraseOrphaned when withEraser holds, as cmd/dispatch builds it, and erases nothing
// otherwise.
func rollService(t *testing.T, database *store.Store, withEraser bool) *docs.Service {
	t.Helper()
	deps := docs.Deps{Store: database, Events: events.NewBroker(), ServerURL: "https://dispatch.example", Settle: time.Hour}
	if withEraser {
		deps.EraseOrphanedNode = EraseOrphaned(database.Pool.Config().ConnString(), 0)
	}
	service := docs.New(deps)
	t.Cleanup(func() { service.Shutdown(context.Background()) })
	return service
}

// registerRollBuild records a live server process of the release before this one, as a process
// registers once it has bound its port, so this build's tick finds another build serving.
func registerRollBuild(t *testing.T, database *store.Store) {
	t.Helper()
	registration, err := processes.Register(context.Background(), database.Pool, processes.Instance{
		Commit:          rollBuildCommit,
		SigningKeys:     []string{"rolling-update-signing-key"},
		SchemaVersion:   pmdoc.SchemaVersion(),
		SchemaCreatable: pmdoc.SchemaCreatable(),
	})
	if err != nil {
		t.Fatalf("register a process of the release before: %v", err)
	}
	t.Cleanup(func() {
		if err := registration.Deregister(context.Background()); err != nil {
			t.Errorf("deregister the process of the release before: %v", err)
		}
	})
}

// reconcile runs one block-index tick of service and fails the test where it fails.
func reconcile(t *testing.T, service *docs.Service) docs.BlockIndexTick {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tick, err := service.ReconcileBlockIndex(ctx, docs.BlockIndexCursors{}, nil)
	if err != nil {
		t.Fatalf("run a block-index tick: %v", err)
	}
	return tick
}
