package daemon

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/store"
)

// podEndFaults is the release's store with the end of a pause's hold on the pod refused once
// (outbox.podDeleted), the first act of a teardown after the runtime answered; fail runs at that
// refusal. Every other call reaches the real store.
type podEndFaults struct {
	outboxTrees
	fail    func()
	refused bool
}

func (s *podEndFaults) EndPausePodHold(ctx context.Context, project, issue string, end store.PausePodHoldEnd, at time.Time) (bool, error) {
	if !s.refused {
		s.refused = true
		if s.fail != nil {
			s.fail()
		}
		return false, errors.New("the pause's hold on the pod cannot be read")
	}
	return s.outboxTrees.EndPausePodHold(ctx, project, issue, end, at)
}

// A retried teardown of the release of a pod no claim owns records what the release did, as
// store.ReleaseTeardown's rule has it, whatever the retry itself finds: an attempt that took the
// pod and failed before its record, counted or with its daemon dead in it, leaves the retry no
// Sandbox to take, and the release is recorded once; an attempt that found no Sandbox and failed
// after the runtime's answer leaves nothing noted, and its retry records none.
func TestARetriedTeardownOfAPodNoClaimOwnsRecordsWhatTheReleaseDid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		// foundNothing is a runtime that holds no Sandbox of the issue from the first attempt on
		// (releaseRuntime.sandboxGone); otherwise the first attempt takes the pod, and the
		// Sandbox is gone from its refusal on.
		foundNothing bool
		// died leaves the failed attempt as a daemon that died in it leaves its row: not counted.
		died bool
		// note is what the mark says of the teardown after the failed attempt.
		note store.ReleaseTeardown
		// records is how many releases the retry leaves recorded, and line the one line it logs.
		records int
		line    string
	}{
		{name: "an attempt that took the pod and failed before its record",
			note: store.TeardownReached, records: 1, line: idleRigUnownedTeardown},
		{name: "an attempt that took the pod and whose daemon died before its record", died: true,
			note: store.TeardownReached, records: 1, line: idleRigUnownedTeardown},
		{name: "an attempt that found no Sandbox and failed after the runtime's answer", foundNothing: true,
			note: store.TeardownNotReached, records: 0, line: idleRigUnownedFoundNothing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newIdleRig(t)
			var logs syncBuffer
			rig.runner.log = jsonLogger(&logs)
			gone := func() {
				rig.rt.mu.Lock()
				defer rig.rt.mu.Unlock()
				rig.rt.sandboxGone = true
			}
			faults := &podEndFaults{outboxTrees: rig.st, fail: gone}
			rig.runner.trees = faults
			rig.stoppedImplementer(t, false)
			if queued := rig.queuedBy(t, func() { rig.w.releaseUnownedPods(ctx) }); !slices.Contains(queued, "issue_release") {
				t.Fatalf("queued rows = %v, want the release's teardown among them", queued)
			}
			if tc.foundNothing {
				gone()
			}

			if err := rig.runner.RunOnce(ctx); err != nil {
				t.Fatalf("run the teardown once: %v", err)
			}
			if !faults.refused {
				t.Fatal("the teardown never reached the end of the pod's hold, so no attempt failed after the runtime's answer")
			}
			var attempts int
			var note string
			if err := rig.pool.QueryRow(ctx, `select o.attempts, r.teardown from outbox o join issue_releases r on r.issue = o.issue
				where o.issue = $1 and o.kind = 'issue_release'`, rig.issue.Key).Scan(&attempts, &note); err != nil {
				t.Fatalf("read the failed teardown and its mark: %v", err)
			}
			if attempts != 1 || store.ReleaseTeardown(note) != tc.note {
				t.Fatalf("after the failed attempt the teardown has %d attempts and its mark says %q, want 1 and %q", attempts, note, tc.note)
			}
			// The row due again, as its backoff leaves it; a daemon that died in the attempt
			// counted none, since only a run's retry counts one (record.QueuedSupervise.Attempts).
			if tc.died {
				attempts = 0
			}
			if _, err := rig.pool.Exec(ctx, `update outbox set attempts = $2, next_at = now() - interval '1 minute'
				where issue = $1 and kind = 'issue_release'`, rig.issue.Key, attempts); err != nil {
				t.Fatalf("make the teardown due again: %v", err)
			}
			rig.drain(t)

			if rig.marked(t) {
				t.Fatal("the retried teardown left its mark behind, so it never finished")
			}
			if releases := rig.records(t).Releases; len(releases) != tc.records {
				t.Fatalf("releases recorded = %+v, want %d", releases, tc.records)
			}
			for _, line := range []string{idleRigUnownedTeardown, idleRigUnownedFoundNothing} {
				want := 0
				if line == tc.line {
					want = 1
				}
				if got := loggedMessages(t, logs.String(), line); len(got) != want {
					t.Fatalf("the teardown logged %d lines %q, want %d", len(got), line, want)
				}
			}
		})
	}
}

// clearFaults is the release's store with the clear of a release's mark refused once
// (ClearIssueRelease), the last act of a teardown, after its record. Every other call reaches the
// real store.
type clearFaults struct {
	outboxTrees
	refused bool
}

func (s *clearFaults) ClearIssueRelease(ctx context.Context, project, issue string) error {
	if !s.refused {
		s.refused = true
		return errors.New("the release's mark cannot be cleared")
	}
	return s.outboxTrees.ClearIssueRelease(ctx, project, issue)
}

// A teardown that recorded its release and then failed to clear the mark — a refused write, or a
// daemon that died between the two, which leaves the store as the refusal does — runs again over
// the standing mark, and the release is still recorded once: the record names the mark, and the
// second write takes the first one's (store.RecordPodRelease). Both release rules record their
// release this way, and each records it again on the retry: the idle release whatever the runtime
// answers, the release of a pod no claim owns because the mark says its teardown reached the
// runtime.
func TestATeardownRetriedAfterItsRecordRecordsTheReleaseOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, release := range []struct {
		name  string
		queue func(t *testing.T, rig *idleRig)
	}{
		{"the idle release", func(_ *testing.T, rig *idleRig) { rig.w.releaseIdlePod(ctx, rig.issue.Key) }},
		{"the release of a pod no claim owns", func(t *testing.T, rig *idleRig) {
			rig.stoppedImplementer(t, false)
			rig.w.releaseUnownedPods(ctx)
		}},
	} {
		t.Run(release.name, func(t *testing.T) {
			rig := newIdleRig(t)
			faults := &clearFaults{outboxTrees: rig.st}
			rig.runner.trees = faults
			if queued := rig.queuedBy(t, func() { release.queue(t, rig) }); !slices.Contains(queued, "issue_release") {
				t.Fatalf("queued rows = %v, want the release's teardown among them", queued)
			}
			rig.drain(t)
			if !faults.refused {
				t.Fatal("the teardown never reached the clear of its mark")
			}
			if !rig.marked(t) {
				t.Fatal("the mark is gone though its clear was refused")
			}
			if releases := rig.records(t).Releases; len(releases) != 1 {
				t.Fatalf("releases recorded before the retry = %+v, want the one the first attempt wrote", releases)
			}

			if _, err := rig.pool.Exec(ctx, `update outbox set next_at = now() - interval '1 minute'
				where issue = $1 and kind = 'issue_release'`, rig.issue.Key); err != nil {
				t.Fatalf("make the teardown due again: %v", err)
			}
			rig.drain(t)

			if rig.marked(t) {
				t.Fatal("the retried teardown left its mark behind, so it never finished")
			}
			if releases := rig.records(t).Releases; len(releases) != 1 {
				t.Fatalf("releases recorded after the retry = %+v, want the one release", releases)
			}
		})
	}
}
