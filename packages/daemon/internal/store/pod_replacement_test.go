package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// The Legion reviewer's Important 1 on #5285 at 7da847c0: two acts on one target (what the act
// takes and the uid of what it takes) each stand recorded under an operation of their own, and
// the runtime reads an act made from its target alone. One target is taken once, so the finish of
// one act on it finishes every record of that target, in the one transaction that queues its
// notice, and a later finish of another record of it finds nothing and queues nothing. A finish
// with no notice, of an act made over a read that found nothing, takes the other records of its
// target too: the read before the act that took the target is the last word of what it held. A
// record of another target stands.
func TestTheFinishOfAnActFinishesEveryRecordOfItsTarget(t *testing.T) {
	st := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, st)
	for _, r := range []PodReplacement{
		{Operation: "round-1", Issue: rootIssue, PodUID: "pod-a", Act: runtime.PodAct("pod-a"), Record: []byte(`{}`)},
		{Operation: "launch-1", Issue: rootIssue, PodUID: "pod-a", Act: runtime.PodAct("pod-a"), Record: []byte(`{}`)},
		{Operation: "launch-2", Issue: rootIssue, PodUID: "pod-a", Act: runtime.VolumeAct("uid-sandbox"), Record: []byte(`{}`)},
		{Operation: "launch-3", Issue: rootIssue, Act: runtime.VolumeAct("uid-sandbox"), Record: []byte(`{}`)},
	} {
		if err := st.RecordPodReplacement(ctx, "legion", r); err != nil {
			t.Fatalf("record %s: %v", r.Operation, err)
		}
	}
	row, err := record.NewOutboxRow(rootIssue, record.Notice{Kind: record.WorkspaceTakenNotice, Role: claim.RoleArchitect,
		Reason: "The pod is gone."}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	notice := &IdleReleaseRow{Kind: string(row.Kind), Payload: row.Payload}

	finished, told, err := st.FinishPodReplacement(ctx, "legion", rootIssue, "launch-1", "", notice)
	if err != nil || !finished || !told {
		t.Fatalf("the finish of launch-1 = %t, %t, %v; want its record finished and its notice queued", finished, told, err)
	}
	if standing := operationsStanding(t, st); !slices.Equal(standing, []string{"launch-2", "launch-3"}) {
		t.Errorf("records standing once launch-1's act on pod-a is finished = %v, want the two records of the Sandbox's volume: round-1's record of pod-a goes with launch-1's", standing)
	}
	finished, told, err = st.FinishPodReplacement(ctx, "legion", rootIssue, "round-1", "", notice)
	if err != nil || finished || told {
		t.Errorf("the later finish of round-1 = %t, %t, %v; want no record found and nothing queued", finished, told, err)
	}
	var queued int
	if err := st.Pool().QueryRow(ctx, `select count(*) from outbox where issue = $1 and kind = 'notice' and payload->>'kind' = 'workspace-taken'`,
		rootIssue).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("workspace-taken notices queued = %d, want one for the one act on pod-a", queued)
	}

	finished, told, err = st.FinishPodReplacement(ctx, "legion", rootIssue, "launch-2", "", nil)
	if err != nil || !finished || told {
		t.Fatalf("the finish of launch-2 with no notice = %t, %t, %v; want its record finished and nothing queued", finished, told, err)
	}
	if standing := operationsStanding(t, st); len(standing) != 0 {
		t.Errorf("records standing once launch-2's delete of the Sandbox is finished = %v, want none: launch-3's record of the same Sandbox goes with it", standing)
	}
}

// A launch's delete of the issue's Sandbox and its kept volume over a live pod of that Sandbox
// takes the pod with the volume (runtime.TakenPod.WithPod): its finish takes the records of acts on
// that pod too, and leaves a record of another pod (the Legion reviewer's Important 1 on #5285 at
// acda4d1e). The same finish with no pod named leaves the pod's records, as a launch's delete over
// a Sandbox that ran no pod of its own does.
func TestTheFinishOfAVolumeActWithItsPodFinishesThatPodsRecords(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withPod  string
		standing []string
	}{
		{name: "with_its_pod", withPod: "pod-a", standing: []string{"round-b"}},
		{name: "control:_no_pod_named", standing: []string{"round-a", "round-b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := migratedStore(t)
			ctx := context.Background()
			openRunningTree(t, st)
			for _, r := range []PodReplacement{
				{Operation: "round-a", Issue: rootIssue, PodUID: "pod-a", Act: runtime.PodAct("pod-a"), Record: []byte(`{}`)},
				{Operation: "round-b", Issue: rootIssue, PodUID: "pod-b", Act: runtime.PodAct("pod-b"), Record: []byte(`{}`)},
				{Operation: "launch-volume", Issue: rootIssue, PodUID: "pod-a", Act: runtime.VolumeAct("uid-sandbox"), Record: []byte(`{}`)},
			} {
				if err := st.RecordPodReplacement(ctx, "legion", r); err != nil {
					t.Fatalf("record %s: %v", r.Operation, err)
				}
			}
			if finished, _, err := st.FinishPodReplacement(ctx, "legion", rootIssue, "launch-volume", tc.withPod, nil); err != nil || !finished {
				t.Fatalf("the finish of launch-volume = %t, %v; want its record finished", finished, err)
			}
			if standing := operationsStanding(t, st); !slices.Equal(standing, tc.standing) {
				t.Errorf("records standing once the launch's delete of the Sandbox is finished = %v, want %v", standing, tc.standing)
			}
		})
	}
}

// operationsStanding is the operation of every record of a pod replacement that stands, oldest
// first.
func operationsStanding(t *testing.T, st *Store) []string {
	t.Helper()
	standing, err := st.PodReplacements(context.Background(), "legion")
	if err != nil {
		t.Fatal(err)
	}
	operations := make([]string, 0, len(standing))
	for _, r := range standing {
		operations = append(operations, r.Operation)
	}
	return operations
}
