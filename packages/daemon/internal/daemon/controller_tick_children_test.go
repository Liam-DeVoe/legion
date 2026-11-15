package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
)

// The issue in implementing these tests start from, and the child its architect files under it
// in triage and never releases, which sends this daemon no event.
const (
	startedParent = "CAPTURE-60"
	filedChild    = "CAPTURE-61"
)

// The controller tick's re-read reads the children of every issue whose phases have started
// (dispatch://INFRA-3320 R13), in both branches of the read (readObserved), so a child filed and
// not released reaches the daemon within one wake interval though it sends no event, and so does
// its move to icebox. The admission pass of the same tick reads the mirror the re-read wrote:
// the first tick moves the parent into awaiting_children, and the tick after the child's move to
// icebox takes it back to the start it held.
//
// Below the single-read bound the turn reads its waited-on keys one at a time and lists the
// project once for the children, taking the summary of each issue whose parent has started and
// that no single read answered: the filed child alone, whose row the mirror never held. Once
// the parent waits, its open child is a key the turn waits on and is read singly. Above the bound
// the one listing the turn makes already takes every summary, the child's among them.
func TestTheTickRereadReadsTheChildrenOfAnIssueWhosePhasesStarted(t *testing.T) {
	parent := startedParent
	filed := dispatch.IssueSummary{Key: filedChild, Status: "triage", Parent: &parent, LastSeq: 1}

	t.Run("below the single-read bound", func(t *testing.T) {
		pool := isolatedOutboxPool(t)
		// The listing shows the parent at a sequence past its mirror row, which the turn must not
		// take: it waits on nothing, and the children read takes a child's summary alone.
		listedParent := dispatch.IssueSummary{Key: startedParent, Status: "testing", LastSeq: 5}
		client := &tickDispatch{summaries: []dispatch.IssueSummary{listedParent, filed}}
		w := tickRuntime(t, pool, client)
		seedStartedParent(t, pool, startedParent)

		w.applyControllerTick(context.Background(), time.Now())

		if asked, listings := client.asked(), client.listings(); len(asked) != 0 || listings != 1 {
			t.Fatalf("the first turn read %v singly and listed the project %d times, want no single read and the one listing of the children", asked, listings)
		}
		mirrored := tickMirrored(t, pool)
		if row := mirrored[filedChild]; row.Status != "triage" || row.Parent != startedParent || row.Seq != 1 {
			t.Fatalf("the mirror holds %s as %+v, want the listing's reading: triage under %s", filedChild, row, startedParent)
		}
		if row := mirrored[startedParent]; row.Status != "in_progress" || row.Seq != 1 {
			t.Fatalf("the mirror holds %s as %+v, want its own row as seeded (in_progress, 1): the children read takes a child's summary alone", startedParent, row)
		}
		if got := tickIssue(t, pool, startedParent); got.Phase != phase.AwaitingChildren {
			t.Fatalf("%s after the turn = %s, want awaiting_children: the turn's own pass read its filed child", startedParent, got.Phase)
		}

		// The child moves to icebox. It still sends no event; the next turn reads it, singly now,
		// since the waiting parent's open child is a key the turn waits on, and the listing takes
		// nothing a single read answered.
		iceboxed := filed
		iceboxed.Status, iceboxed.LastSeq = "icebox", 2
		client.summaries = []dispatch.IssueSummary{listedParent, iceboxed}
		client.serve(dispatch.Issue{Key: filedChild, Project: "CAPTURE", Status: "icebox", Parent: &parent, LastSeq: 2})

		w.applyControllerTick(context.Background(), time.Now().Add(time.Second))

		if asked, listings := client.asked(), client.listings(); !slices.Equal(asked, []string{filedChild}) || listings != 2 {
			t.Fatalf("the turns read %v singly and listed the project %d times, want %s read singly on the second and a listing on each", asked, listings, filedChild)
		}
		if row := tickMirrored(t, pool)[filedChild]; row.Status != "icebox" || row.Seq != 2 {
			t.Fatalf("the mirror holds %s as %+v, want icebox at 2", filedChild, row)
		}
		if got := tickIssue(t, pool, startedParent); got.Phase != phase.Implementing || got.ChildrenWait != nil {
			t.Fatalf("%s after its only child moved to icebox = phase %s, held start %+v; want implementing, the start it held", startedParent, got.Phase, got.ChildrenWait)
		}
	})

	t.Run("above the single-read bound", func(t *testing.T) {
		pool := isolatedOutboxPool(t)
		client := &tickDispatch{summaries: []dispatch.IssueSummary{filed}}
		for i := 1; i <= blockerReadFanout+1; i++ {
			client.summaries = append(client.summaries, dispatch.IssueSummary{Key: ownBlocker(i), Status: "in_progress", LastSeq: 1})
		}
		w := tickRuntime(t, pool, client)
		seedStartedParent(t, pool, startedParent)
		seedBlockedRoots(t, pool, blockerReadFanout+1)

		w.applyControllerTick(context.Background(), time.Now())

		if asked, listings := client.asked(), client.listings(); len(asked) != 0 || listings != 1 {
			t.Fatalf("the turn read %v singly and listed the project %d times, want the one listing and no single read", asked, listings)
		}
		if row := tickMirrored(t, pool)[filedChild]; row.Status != "triage" || row.Parent != startedParent {
			t.Fatalf("the mirror holds %s as %+v, want the listing's reading: triage under %s", filedChild, row, startedParent)
		}
		if got := tickIssue(t, pool, startedParent); got.Phase != phase.AwaitingChildren {
			t.Fatalf("%s after the turn = %s, want awaiting_children", startedParent, got.Phase)
		}
	})
}

// seedStartedParent records key as a root of this project at work in implementing: its record,
// its mirror row, its tree lifecycle open under the workflow and the one admission slot, as
// promotion leaves a root whose first phase it started. No child of it is in the mirror.
func seedStartedParent(t *testing.T, pool *store.Pool, key string) {
	t.Helper()
	seedTickIssue(t, pool, record.Issue{
		Key: key, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "CAPTURE",
		Title: key, Tree: key, Phase: phase.Implementing, Generation: 1, Status: "in_progress", Rank: "A",
		HandedOver: true, LastDispatchSeq: 1,
	})
	seedTickMirror(t, pool, key, "in_progress", 1)
	ctx := context.Background()
	token, err := claim.ProjectToken("CAPTURE")
	if err != nil {
		t.Fatalf("project token: %v", err)
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		records := record.NewStore()
		if _, err := records.OpenTreeLifecycle(ctx, tx, token, key, treelifecycle.AuthorityWorkflow); err != nil {
			return err
		}
		return records.PutSlot(ctx, tx, record.Slot{Issue: key, Index: 0, AdmittedAt: time.Now().UTC()})
	}); err != nil {
		t.Fatalf("open the tree of %s and give it its slot: %v", key, err)
	}
}

// tickIssue is the record of key as the store holds it.
func tickIssue(t *testing.T, pool *store.Pool, key string) record.Issue {
	t.Helper()
	var got *record.Issue
	if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		var err error
		got, err = record.NewStore().Issue(context.Background(), tx, key)
		return err
	}); err != nil || got == nil {
		t.Fatalf("read the record of %s = %v, %v", key, got, err)
	}
	return *got
}
