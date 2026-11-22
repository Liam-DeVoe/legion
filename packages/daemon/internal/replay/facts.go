package replay

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// replayFact is what makes a fact of this package an intake.Fact. intake.Fact's one method is
// unexported, so a type of another package is a fact only by embedding one of intake's; the
// embedded intake.EntrySweep carries nothing and names no issue, and no handler of the daemon's
// chain takes a replay fact for one, since each of them reads a fact by its own type, which is
// this package's: the mirror and the engine do nothing for it, and admission runs its pass, as for
// every fact it does not know.
type replayFact struct{ intake.EntrySweep }

// RecordedMove is one recorded phase move a replay enters at the end of its work segment, by
// workflow.(*Engine).ReplayTransition (MoveTransition): Issue at Generation moved From To by
// Trigger. The replay's handler, which runs before the daemon's chain, makes the move, and the
// pass of the same fact gives out whatever slot the move frees. It is named apart from Move, the
// trace's reading of a `workflow: phase changed` line, which is what it enters again.
type RecordedMove struct {
	replayFact
	Issue      string
	Generation uint64
	From, To   phase.Phase
	Trigger    workflow.TriggerKind
}

// ReadyMark is the merger's ready mark of the release at 9542c6fb inside merging (TriggerReady,
// MoveMark, assumption 20), which the replay's handler writes into the replay's own table of marks
// (replayTables) for a rule that gives the slot back at it. Under a rule that reads no mark it
// changes nothing.
type ReadyMark struct {
	replayFact
	Issue      string
	Generation uint64
}

// Settled is the replay's own fact once its worker stub has settled the claim of a root's
// architect whose root has handed work to a person (record.Issue.DeclaredSeq): the daemon applies
// a fact when such a claim settles (supervision's ClaimReady, TaskConfirmed), so the admission pass
// that enters the root's wait on the person (workflow's ReconcileAwaiting) runs at the moment the
// claim settled, and the replay's stub, which applies no fact of supervision's, applies this one in
// their place (review Q2, item 15). It carries the root, and no handler of the chain reads it but
// admission, whose pass runs.
type Settled struct {
	replayFact
	Issue string
}

// replayHandler is the handler a replay puts before the daemon's chain (admit.Handlers) for the
// replay's own facts: a RecordedMove through the engine's ReplayTransition, refused with its reason
// where the issue does not stand where the record says, and a ReadyMark into the replay's table of
// marks. Every other fact it leaves to the chain.
type replayHandler struct {
	engine *workflow.Engine
	clock  func() time.Time
}

// refusedMove is the code of a replay's refused move, whose message is ReplayTransition's reason.
const refusedMove = "REPLAY_MOVE_REFUSED"

// Apply makes a replay fact's change in the fact's own transaction.
func (h replayHandler) Apply(ctx context.Context, tx pgx.Tx, fact intake.Fact) (intake.Result, error) {
	switch fact := fact.(type) {
	case RecordedMove:
		reason, err := h.engine.ReplayTransition(ctx, tx, fact.Issue, fact.Generation, fact.From, fact.Trigger, fact.To)
		if err != nil || reason == "" {
			return intake.Result{}, err
		}
		return intake.Result{Refusal: &intake.Refusal{Status: 409, Code: refusedMove, Message: reason}}, nil
	case ReadyMark:
		_, err := tx.Exec(ctx, `insert into replay_ready_marks (issue, generation, marked_at) values ($1, $2, $3)
			on conflict (issue, generation) do nothing`, fact.Issue, int64(fact.Generation), h.clock())
		return intake.Result{}, err
	}
	return intake.Result{}, nil
}

// The assumptions a fact a replay leaves out rests on (FactSources), and a row its worker stub
// finishes without acting (OutboxHandling): the tool's own, after the plan's, each named where a
// run left out such a fact or finished such a row.
const (
	assumeNoticesChangeNothing      = 26
	assumePublicationsDecideNothing = 27
	assumeWorkspaceInSegment        = 28
	assumeNoGitHub                  = 29
	assumeNoProductionCheck         = 30
	assumeSupervisionSettles        = 31
	assumeCompletionPassed          = 32
	assumeBootRepairsNothing        = 33
)

// FactSources is how a replay comes by each kind of fact the daemon applies, by the name of its
// type in package intake: every type intake.Fact is, the ones Dispatch events decode to and the
// ones the daemon's other producers make (its API, ticks, boots, outbox executors, supervision and
// GitHub intake). A fact type with no entry here has no source in the replay, and the inventory
// test (TestEveryFactANonDispatchProducerAppliesHasASource) names it.
var FactSources = map[string]FactSource{
	// The facts Dispatch events decode to: each recorded event of the project's issues through
	// intake.DecodeDispatchEvent, at its recorded time or offset, carrying the replay's copy of
	// Dispatch (the shadow), with the preparation the daemon's consumer makes stood in for.
	"DispatchIssue":         {Way: FactInjected, From: "each recorded issue event of the project, through intake.DecodeDispatchEvent and the preparation's stand-in, and the echo of each status write the stub makes"},
	"DispatchArtifact":      {Way: FactInjected, From: "each recorded artifact.approved, artifact.changes_requested and artifact.version event, and the gate seed the stub executes"},
	"DispatchAskOpened":     {Way: FactInjected, From: "each recorded ask.opened event"},
	"DispatchAskRestored":   {Way: FactInjected, From: "each recorded ask event of a restored decision block"},
	"DispatchAskClosed":     {Way: FactInjected, From: "each recorded ask.answered and ask.resolved event"},
	"DispatchAskReply":      {Way: FactInjected, From: "each recorded comment on an ask and ask.handed_back event"},
	"DispatchApprovalMoved": {Way: FactInjected, From: "each recorded ask.edited event of an approval request a version moved past"},
	"DispatchClaim":         {Way: FactInjected, From: "each recorded issue.claimed and issue.released event"},

	// The API's facts, rebuilt from the record's lines at the end of their segment or their
	// recorded offset in it.
	"SignOff":           {Way: FactInjected, From: "a recorded move by sign_off, at the end of its accepting segment, and again at each leave of a member beneath while it is refused"},
	"BackwardMove":      {Way: FactInjected, From: "a recorded move by backward, at the end of its segment"},
	"CloseRoot":         {Way: FactInjected, From: "a recorded leave of a root at done with no person's close and no move to done, at its offset in the architect's segment"},
	"RetryOrEscalate":   {Way: FactInjected, From: "each recorded `workflow: a held phase is released` of an architect's retry, and each `workflow: a phase is held` of an escalation, at its offset from the hold"},
	"RestartPhase":      {Way: FactInjected, From: "each recorded `workflow: a held phase is released` of the operator's retry, at its offset from the hold"},
	"ChildAdoption":     {Way: FactInjected, From: "each recorded `workflow: a queued root joined a live tree`, the release of a queued root into a tree, at its offset in the releasing architect's segment"},
	"GateRegistered":    {Way: FactInjected, From: "each recorded `workflow: design gate registered`, with what register_gate reads of the document rebuilt from the shadow, at its offset in the architect's segment"},
	"ClaimFailed":       {Way: FactInjected, From: "each recorded `supervise: claim failed`, at its offset in the segment of the failed claim's issue"},
	"ProjectResumed":    {Way: FactInjected, From: "each recorded `pause: the project is resumed`, after the replay's own resume of its pause"},
	"PullRequestMerged": {Way: FactInjected, From: "a recorded move by pull_request_merged, at the end of its merging segment, on the replay's own pull request record of the issue"},

	// The daemon's own producers, made by the replay as the daemon makes them.
	"ControllerTick":           {Way: FactProduced, From: "the replay's ticks, a minute after each boot and every tick interval, observing from the shadow the keys blockers.WaitedOn selects"},
	"DaemonBooted":             {Way: FactProduced, From: "the replay's boot at each recorded process start, after its reconcile of the shadow's listing"},
	"DispatchConsumerPosition": {Way: FactProduced, From: "the release of a boot's hold on its listing at the recorded `admission: the Dispatch consumer has caught up; releasing held roots`"},
	"CatchUpApplied":           {Way: FactProduced, From: "the end of a boot's catch-up of a root, where the record shows it applied"},
	"LingerExpired":            {Way: FactProduced, From: "the stub's execution of a linger_close row at its next_at"},
	"Handover":                 {Way: FactProduced, From: "the stub's execution of a hand-over status write whose issue the shadow shows handed over already"},
	"GateAboveDispatch":        {Way: FactProduced, From: "the stub's execution of a gate_seed row that reads the document below the version it was queued at in the shadow"},
	"PullRequestMergeability":  {Way: FactProduced, From: "a conflict a stretch draws (spec R2): the replay's own pull request record read as conflicting at its head, the conflict confirmed"},
	"PullRequestSynchronized":  {Way: FactProduced, From: "the end of a drawn conflict's merge-forward segment: the head the merge forward left, on the replay's own pull request record"},

	// Left out, each under the assumption it rests on.
	"HandoffComplete":         {Way: FactLeftOut, Assumption: assumeCompletionPassed},
	"ClaimReady":              {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"TaskConfirmed":           {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"SessionLost":             {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"SessionStoreFault":       {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"WorkerSilent":            {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"WorkerBackgroundStalled": {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"EntrySweep":              {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"MemoryGuardKill":         {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"VolumeNearFull":          {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"ListenerWake":            {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"PodBackstopDue":          {Way: FactLeftOut, Assumption: assumeSupervisionSettles},
	"CutoverMergerRepair":     {Way: FactLeftOut, Assumption: assumeBootRepairsNothing},
	"GateRequestFound":        {Way: FactLeftOut, Assumption: assumeBootRepairsNothing},
	"CheckReport":             {Way: FactLeftOut, Assumption: assumeNoProductionCheck},
	"CheckLookEnded":          {Way: FactLeftOut, Assumption: assumeNoProductionCheck},
	"CheckWakeDue":            {Way: FactLeftOut, Assumption: assumeNoProductionCheck},
	"CheckBound":              {Way: FactLeftOut, Assumption: assumeNoProductionCheck},
	"PullRequestOpened":       {Way: FactLeftOut, Assumption: assumeNoGitHub},
	"PullRequestReview":       {Way: FactLeftOut, Assumption: assumeNoGitHub},
	"PullRequestChecks":       {Way: FactLeftOut, Assumption: assumeNoGitHub},
	"PullRequestClosed":       {Way: FactLeftOut, Assumption: assumeNoGitHub},
	"PullRequestDequeued":     {Way: FactLeftOut, Assumption: assumeNoGitHub},
	"PullRequestMissing":      {Way: FactLeftOut, Assumption: assumeNoGitHub},
	"Push":                    {Way: FactLeftOut, Assumption: assumeNoGitHub},
	"BasePush":                {Way: FactLeftOut, Assumption: assumeNoGitHub},
	"MergeQueueReadDone":      {Way: FactLeftOut, Assumption: assumeNoGitHub},
	"ConflictConfirmDone":     {Way: FactLeftOut, Assumption: assumeNoGitHub},
}
