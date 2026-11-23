package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// mergeForwardHead is the head the merge forward of these tests is asked of, and mergeForwardLanded
// the head its merge leaves.
const (
	mergeForwardHead   = "c0de"
	mergeForwardLanded = "d00d"
)

// askedOf records what a merge forward asked in at leaves on LEGION-1: the implementer's phases row
// running step 1 of the plan under implementer, the claim the ask reached (record.WorkerClaim), and
// the open pull request #42 at the head that conflicted, holding the ask, not landed.
func (r *ownerHoldRig) askedOf(t *testing.T, implementer claim.Token, at phase.Phase) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := r.records.PutPhase(ctx, tx, record.PhaseRow{Issue: "LEGION-1", Role: claim.RoleImplementer, Claim: implementer, Entry: 1,
			StepTask: "a", StepPlace: 1}); err != nil {
			return err
		}
		return r.records.PutPullRequest(ctx, tx, record.PullRequest{Issue: "LEGION-1", Repo: "acme/widgets", Number: 42, Branch: "legion/LEGION-1",
			State: record.PullRequestOpen, HeadSHA: mergeForwardHead, Failing: []string{},
			MergeForward: &record.MergeForward{Head: mergeForwardHead, Base: "main", Phase: at, Why: record.ConflictCheckNotGreen, AskedAt: r.now}})
	}); err != nil {
		t.Fatalf("record the merge forward asked of LEGION-1 in %s: %v", at, err)
	}
}

// lands records the merge forward's landing on LEGION-1's pull request, as the synchronize that
// moved its head does (workflow's mergeForwardLanded): the new head, and whether the landing asked
// the reviewer to read the merge.
func (r *ownerHoldRig) lands(t *testing.T, reviewAsked bool) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		pr, err := r.records.PullRequest(ctx, tx, "LEGION-1")
		if err != nil || pr == nil {
			return errors.Join(err, errors.New("LEGION-1 records no pull request"))
		}
		landed := *pr.MergeForward
		landed.Landed, landed.ReviewAsked = mergeForwardLanded, reviewAsked
		pr.MergeForward, pr.HeadSHA = &landed, mergeForwardLanded
		return r.records.PutPullRequest(ctx, tx, *pr)
	}); err != nil {
		t.Fatalf("land LEGION-1's merge forward: %v", err)
	}
}

// sent runs one merge-forward message of LEGION-1 through the executor as the runner does, and
// deletes the row where the executor finished it. It answers the executor's error.
func (r *ownerHoldRig) sent(t *testing.T, message record.MergeForwardMessage) error {
	t.Helper()
	row := leasedOutboxRow(t, r.pool, r.records, mustOutboxRow(t, "LEGION-1", message, r.now))
	err := r.runner.execute(context.Background(), row)
	if err == nil {
		if _, err := r.pool.Exec(context.Background(), "delete from outbox where id = $1", row.ID); err != nil {
			t.Fatalf("finish row %d: %v", row.ID, err)
		}
	}
	return err
}

// mergeForwardAsk is the ask of a merge forward in at to the implementer's claim token.
func mergeForwardAsk(token claim.Token, at phase.Phase) record.MergeForwardMessage {
	return record.MergeForwardMessage{Role: claim.RoleImplementer, Claim: token, Step: record.MergeForwardAsk, Head: mergeForwardHead, Base: "main",
		Phase: at, PullRequest: 42, Why: record.ConflictCheckNotGreen}
}

// mergeForwardReview is the review the landing in at asks of the reviewer's claim token.
func mergeForwardReview(token claim.Token, at phase.Phase) record.MergeForwardMessage {
	return record.MergeForwardMessage{Role: claim.RoleReviewer, Claim: token, Step: record.MergeForwardReview, Head: mergeForwardHead,
		Landed: mergeForwardLanded, Base: "main", Phase: at, PullRequest: 42}
}

// waits fails the test unless err is a wait of the row on its backoff.
func waits(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, wait.ErrWaiting) {
		t.Fatalf("%s = %v, want a wait", what, err)
	}
}

// A merge forward is an act as the implementer, and the owner's slot holds the App of the role the
// act is done as. On `implementer:owner reviewer`, standing at the reviewer's step, another agent's,
// a base that moved asks the owner to merge it forward. The ask waits while the owner's slot does
// not hold the implement App: the executor has the runtime write the slot, which it judges through
// the daemon's ActsAs, and asks whether the slot's gh files hold the implement App; once they do,
// the ask is delivered, once. A write-back that runs while the ask is open writes nothing, so it
// does not undo the App the owner was told it acts as. Once the merge forward landed the owner acts
// as nothing again, and the write-back the landing queues writes the slot.
//
// The control: on `implementer reviewer` the ask goes to the implementer's own claim, and the
// owner's slot is never written or asked.
func TestAMergeForwardAskedOfTheOwnerWaitsForTheImplementApp(t *testing.T) {
	t.Parallel()
	reviewer := record.TaskPlace{Kind: record.PlaceStep, Position: 2}

	t.Run("implementer:owner reviewer", func(t *testing.T) {
		t.Parallel()
		r := newOwnerHoldRig(t, phase.Reviewing, reviewer, "implementer:owner", "reviewer")
		r.started(t, "step", 1, true)
		r.askedOf(t, r.owner, phase.Reviewing)
		r.rt.ScriptSlotHolds(runtime.ErrSlotCredentialPending)
		ask := leasedOutboxRow(t, r.pool, r.records, mustOutboxRow(t, "LEGION-1", mergeForwardAsk(r.owner, phase.Reviewing), r.now))
		waits(t, r.runner.execute(context.Background(), ask), "the ask while the owner's slot does not hold the implement App")
		if got := r.published(); len(got) != 0 {
			t.Fatalf("the ask published %v while the owner's slot does not hold the implement App, want nothing", got)
		}
		want := []string{"WriteSlotCredential", "SlotHoldsCredential implementer"}
		if got := slotCalls(r.rt, r.owner); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("the ask asked the runtime %q of the owner's slot, want %q", got, want)
		}

		r.rt.ScriptSlotHolds(nil)
		ask.LeaseToken = leaseOutboxRow(t, r.pool, ask.ID)
		if err := r.runner.execute(context.Background(), ask); err != nil {
			t.Fatalf("the ask once the owner's slot holds the implement App = %v", err)
		}
		if _, err := r.pool.Exec(context.Background(), "delete from outbox where id = $1", ask.ID); err != nil {
			t.Fatalf("finish the ask: %v", err)
		}
		if got := r.published(); len(got) != 1 || got[0] != notify.RoleTopicPrefix+string(r.owner) {
			t.Fatalf("the ask published %v once the owner's slot holds the implement App, want one to the owner's topic", got)
		}
		want = append(want, "WriteSlotCredential", "SlotHoldsCredential implementer")
		if got := slotCalls(r.rt, r.owner); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("the ask asked the runtime %q of the owner's slot, want %q", got, want)
		}

		r.writtenBack(t)
		if got := slotCalls(r.rt, r.owner); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("a write-back while the ask is open asked the runtime %q of the owner's slot, want nothing more than %q", got, want)
		}
		r.lands(t, false)
		r.writtenBack(t)
		want = append(want, "WriteSlotCredential")
		if got := slotCalls(r.rt, r.owner); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("the landing's write-back asked the runtime %q of the owner's slot, want %q", got, want)
		}
	})

	t.Run("control: implementer reviewer", func(t *testing.T) {
		t.Parallel()
		r := newOwnerHoldRig(t, phase.Reviewing, reviewer, "implementer", "reviewer")
		implementer := mustClaimToken(t, "LEGION-1", claim.RoleImplementer)
		if _, _, err := r.runner.supervisor.Create(context.Background(), supervise.Claim{Token: implementer, Project: "legion", Tree: "LEGION-1",
			Issue: "LEGION-1", Role: claim.RoleImplementer, State: supervise.StateWorking}, ""); err != nil {
			t.Fatalf("create the implementer's claim: %v", err)
		}
		r.askedOf(t, implementer, phase.Reviewing)
		r.rt.ScriptSlotHolds(runtime.ErrSlotCredentialPending)
		if err := r.sent(t, mergeForwardAsk(implementer, phase.Reviewing)); err != nil {
			t.Fatalf("the ask to the implementer = %v, want it delivered", err)
		}
		if got := r.published(); len(got) != 1 || got[0] != notify.RoleTopicPrefix+string(implementer) {
			t.Fatalf("the ask published %v, want one to the implementer's topic", got)
		}
		if got := slotCalls(r.rt, r.owner); len(got) != 0 {
			t.Fatalf("the ask to the implementer asked the runtime %q of the owner's slot, want nothing", got)
		}
	})
}

// The review a landed merge forward asks of the reviewer is an act as the reviewer, and GitHub
// refuses an approval from the pull request's author, the implement App. On `implementer:owner
// reviewer:owner` the review goes to the owner's claim. In the owner's retro, which the owner does
// as its implementer step, the slot holds the implement App, so the review waits and asks the
// runtime nothing. Once the retro ended and the issue stands in merging, the review waits for the
// slot's gh files to hold the reviewer's App, which is the owner's own, so it writes nothing; the
// retro's write-back writes the slot, and the review is delivered once. The same holds of the review
// a merge forward asked of the owner in merging asks: the ask holds the implement App, and the
// review reaches the owner only after the landing's write-back.
func TestTheReviewOfAMergeForwardReachesTheOwnerOnceItsSlotHoldsTheReviewApp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	held := func(t *testing.T, r *ownerHoldRig, review record.OutboxRow) {
		t.Helper()
		before := len(r.published())
		r.rt.ScriptSlotHolds(runtime.ErrSlotCredentialPending)
		review.LeaseToken = leaseOutboxRow(t, r.pool, review.ID)
		waits(t, r.runner.execute(ctx, review), "the review while the write-back has not landed")
		if got := r.published(); len(got) != before {
			t.Fatalf("the review published %v before the write-back landed, want nothing more than %d", got, before)
		}
		if got := slotCalls(r.rt, r.owner); len(got) == 0 || got[len(got)-1] != "SlotHoldsCredential reviewer" {
			t.Fatalf("the review asked the runtime %q of the owner's slot, want an ask for the reviewer's App last", got)
		}
		r.writtenBack(t)
		r.rt.ScriptSlotHolds(nil)
		review.LeaseToken = leaseOutboxRow(t, r.pool, review.ID)
		if err := r.runner.execute(ctx, review); err != nil {
			t.Fatalf("the review once the write-back landed = %v", err)
		}
		if got := r.published(); len(got) != before+1 || got[before] != notify.RoleTopicPrefix+string(r.owner) {
			t.Fatalf("the review published %v once the write-back landed, want one more, to the owner's topic", got)
		}
	}

	t.Run("landed in the owner's retro", func(t *testing.T) {
		t.Parallel()
		r := newOwnerHoldRig(t, phase.Retro, record.TaskPlace{Kind: record.PlaceRetro}, "implementer:owner", "reviewer:owner")
		r.started(t, "step", 1, true)
		r.started(t, "step", 2, true)
		r.started(t, "retro", 0, false)
		r.askedOf(t, r.owner, phase.Retro)
		r.lands(t, true)
		review := leasedOutboxRow(t, r.pool, r.records, mustOutboxRow(t, "LEGION-1", mergeForwardReview(r.owner, phase.Retro), r.now))
		r.rt.ScriptSlotHolds(nil)
		waits(t, r.runner.execute(ctx, review), "the review during the owner's retro")
		if got := r.published(); len(got) != 0 {
			t.Fatalf("the review published %v during the owner's retro, want nothing", got)
		}
		if got := slotCalls(r.rt, r.owner); len(got) != 0 {
			t.Fatalf("the review during the owner's retro asked the runtime %q of the owner's slot, want nothing", got)
		}
		if err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
			_, _, err := r.records.EndStepStart(ctx, tx, "LEGION-1", record.StepPlace{Task: "a", Kind: "retro", Place: 0})
			return err
		}); err != nil {
			t.Fatalf("end the retro: %v", err)
		}
		r.stands(t, phase.Merging, record.TaskPlace{Kind: record.PlaceMerge})
		held(t, r, review)
	})

	t.Run("asked of the owner in merging", func(t *testing.T) {
		t.Parallel()
		r := newOwnerHoldRig(t, phase.Merging, record.TaskPlace{Kind: record.PlaceMerge}, "implementer:owner", "reviewer:owner")
		r.started(t, "step", 1, true)
		r.started(t, "step", 2, true)
		r.started(t, "retro", 0, true)
		r.askedOf(t, r.owner, phase.Merging)
		r.rt.ScriptSlotHolds(nil)
		if err := r.sent(t, mergeForwardAsk(r.owner, phase.Merging)); err != nil {
			t.Fatalf("the ask in merging = %v", err)
		}
		if got := slotCalls(r.rt, r.owner); strings.Join(got, ",") != "WriteSlotCredential,SlotHoldsCredential implementer" {
			t.Fatalf("the ask in merging asked the runtime %q of the owner's slot, want a write and an ask for the implementer's App", got)
		}
		r.lands(t, true)
		review := leasedOutboxRow(t, r.pool, r.records, mustOutboxRow(t, "LEGION-1", mergeForwardReview(r.owner, phase.Merging), r.now))
		held(t, r, review)
	})
}

// While the owner's slot acts as the implementer for a merge forward asked of it, nothing asks the
// owner for an act as another role. The owner's review step of `implementer:owner reviewer:owner`
// and its acceptance of `implementer:owner acceptance:owner`, told while the ask is open, wait as a
// notice held for its architect, with no stale line, since their runs go on. Once the merge forward
// landed and the write-back wrote the slot, each is told once. The landed message the landing sends
// the owner's review step, which tells it to go on at the new head, waits the same way.
func TestTheOwnersStepsWaitWhileItsSlotActsForAMergeForward(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		at    phase.Phase
		words []string
		role  claim.Role
	}{
		{name: "the owner's review step", at: phase.Reviewing, words: []string{"implementer:owner", "reviewer:owner"}, role: claim.RoleReviewer},
		{name: "the owner's acceptance", at: phase.Accepting, words: []string{"implementer:owner", "acceptance:owner"}, role: claim.RoleAcceptance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			r := newOwnerHoldRig(t, tc.at, record.TaskPlace{Kind: record.PlaceStep, Position: 2}, tc.words...)
			r.started(t, "step", 1, true)
			r.started(t, "step", 2, false)
			r.askedOf(t, r.owner, tc.at)
			r.rt.ScriptSlotHolds(nil)
			notice := record.Notice{Kind: record.OwnerStepNotice, Role: tc.role, Phase: tc.at, Reason: "Step 2 of 2.",
				OwnerStep: &record.NoticeStep{Task: "a", Kind: "step", Place: 2, Starts: 1}}
			row := leasedOutboxRow(t, r.pool, r.records, mustOutboxRow(t, "LEGION-1", notice, r.now))
			if err := r.runner.execute(ctx, row); !errors.Is(err, errNoticeWaits) {
				t.Fatalf("the owner-step of step 2 while the merge forward is open = %v, want a notice held for its architect", err)
			}
			if got := r.published(); len(got) != 0 {
				t.Fatalf("the owner-step of step 2 published %v while the merge forward is open, want nothing", got)
			}
			if lines := staleLines(t, r.logged.String()); len(lines) != 0 {
				t.Fatalf("the owner-step of step 2 logged %d stale lines while the merge forward is open, want none: its run goes on", len(lines))
			}

			r.lands(t, false)
			r.writtenBack(t)
			row.LeaseToken = leaseOutboxRow(t, r.pool, row.ID)
			if err := r.runner.execute(ctx, row); err != nil {
				t.Fatalf("the owner-step of step 2 once the merge forward landed = %v", err)
			}
			if got := r.published(); len(got) != 1 {
				t.Fatalf("the owner-step of step 2 published %v once the merge forward landed, want one", got)
			}
			if tc.role != claim.RoleReviewer {
				return
			}
			r.rt.ScriptSlotHolds(runtime.ErrSlotCredentialPending)
			landed := record.MergeForwardMessage{Role: claim.RoleReviewer, Claim: r.owner, Step: record.MergeForwardLanded, Head: mergeForwardHead,
				Landed: mergeForwardLanded, Base: "main", Phase: tc.at, PullRequest: 42}
			waits(t, r.sent(t, landed), "the landed to the owner's review step while its slot's files hold another App")
			r.rt.ScriptSlotHolds(nil)
			if err := r.sent(t, landed); err != nil {
				t.Fatalf("the landed to the owner's review step once its slot holds the reviewer's App = %v", err)
			}
			if got := r.published(); len(got) != 2 {
				t.Fatalf("the notices published %v after the landed, want the owner-step and the landed", got)
			}
		})
	}
}
