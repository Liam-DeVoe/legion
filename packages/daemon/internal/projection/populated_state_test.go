package projection

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/capabilities"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// populatedStatePath is the projected part of the state golden: what Project serves over the
// seed below, with the two readings internal/daemon's source.State puts on it from the same
// snapshot (`strandedClaims` and each root's `architectStalls`). internal/api's populatedState
// reads it and adds what the deployment answers beside the record — the daemon's boots, the
// settings, the capabilities, the repositories, the consumers and the pause — and its
// TestStateGolden writes that whole body to packages/contracts/fixtures/daemon-api/state.json.
// So every issue view, the admission block, the waiting wakes and the held outbox rows of that
// fixture are the projection's own output, and a value no seed can make the projection serve
// cannot be written into it.
var populatedStatePath = filepath.Join("testdata", "populated-state.json")

// populatedReadAt is the instant the populated state is read at (Inputs.Now): the quiet times and
// the held rows' waits are measured from it.
var populatedReadAt = time.Date(2026, 9, 22, 9, 18, 0, 0, time.UTC)

// The draws of the experiments the fixture's project declares (config.Project.ExperimentsDeclared):
// LEGION-208's tree was drawn into the memory group when it was opened, and each issue of that
// tree was drawn for `plan-length` and `planning` when the issue was.
var (
	populatedMemoryDrawnAt = time.Date(2026, 9, 20, 11, 4, 5, 0, time.UTC)
	populatedPlanDrawnAt   = time.Date(2026, 9, 21, 18, 42, 11, 0, time.UTC)
)

// TestThePopulatedStateIsTheProjectionOfItsSeed writes the projected part of the state golden
// (`-update`) and fails where the file differs from what Project serves over the seed. The seed
// is the record a daemon would hold: issue records, phases rows, slots, open tree lifecycles, the
// Dispatch mirror, gate rows, pull requests, a production check, plan tasks, experiment answers,
// held outbox rows, the claims of the moment with their capability reports, the wakes the pacing
// holds and the claims nothing will wake. Each case the state's readers test is a row here; the
// comment on each row says what it is there for.
func TestThePopulatedStateIsTheProjectionOfItsSeed(t *testing.T) {
	state := populatedProjection(t)
	goldenAt(t, populatedStatePath, state)
}

// populatedProjection is the populated state's record projected, with the two readings the
// daemon's source puts on the projection from the same snapshot (internal/daemon's source.State):
// the stranded claims it judged, as `strandedClaims`, and the architect-stalled arms of each root,
// on that root's view.
func populatedProjection(t *testing.T) api.State {
	t.Helper()
	seed := populatedSeed(t)
	state, err := Project(context.Background(), seed.inputs)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	state.StrandedClaims = api.StrandedClaimsOf(seed.inputs.Stranded)
	for key, arms := range seed.architectStalls {
		view, shown := state.Issues[key]
		if !shown {
			t.Fatalf("the seed names architect-stalled arms for %s, which the state does not show", key)
		}
		view.ArchitectStalls = arms
		state.Issues[key] = view
	}
	return state
}

// populatedRecord is the seed: the inputs Project reads, its store among them, and the
// architect-stalled arms internal/daemon's source.State reads beside them.
type populatedRecord struct {
	inputs          Inputs
	architectStalls map[string][]record.ArchitectStallArm
}

// populatedSeed is the record the state golden projects. Its issues:
//
//   - LEGION-207 merged and closed; its tree lingers, its lifecycle closed, and its production
//     check is open at its second look.
//   - LEGION-208, the tree root at work in implementing, carries every optional a claim view has
//     between its architect, planner and implementer, a plan task's counts, a pull request
//     with changes requested, an approved design gate, the memory draw and the handshake of its
//     architect's entry; LEGION-209 is a child of its tree the workflow holds at admitted behind
//     two Dispatch blockers.
//   - LEGION-210, held from implementing on an escalation, with a failed implementer nothing
//     will wake and no spec registered.
//   - LEGION-211, a root another tree handed over whose tree has not been opened, waiting in the
//     line's starting part, its implementer suspended with two failed sends. It carries a run,
//     so no Dispatch link holds it (blockers.Exempt).
//   - LEGION-212, a root a person answered, holding its slot and waiting in the line's returned
//     part, its spec sent back with changes.
//   - LEGION-214, a root whose Legion label is off, its spec a version past its approval.
//   - LEGION-215, a labelled root whose recorded Dispatch chain reaches LEGION-208's live tree.
//   - LEGION-218 and LEGION-219, reviewing roots whose rounds wait on GitHub's merge state: the
//     first for GitHub's first answer, the second stuck on `blocked` and told of it.
func populatedSeed(t *testing.T) populatedRecord {
	t.Helper()
	at := func(day, hour, minute, second int) time.Time {
		return time.Date(2026, 9, day, hour, minute, second, 0, time.UTC)
	}
	token := func(issue string, role claim.Role) claim.Token {
		t.Helper()
		token, err := claim.NewToken("legion", issue, role)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	const pod = "7f0c2f9a-6a4b-4f2e-9a1c-2f0d5a3b7e11"
	podLocator := func(issue string, role claim.Role, generation uint64) *runtime.Locator {
		return sandboxLocator(token(issue, role), "legion-legion-legion-208", pod, generation)
	}
	updateReasons := "the pod template changed: image ghcr.io/cybermonklabs/legion-worker moved"
	answeredAt := at(22, 9, 21, 0)

	issue := func(key, tree string, ph phase.Phase, status, repo string, generation uint64) record.Issue {
		branch := "main"
		if repo == "zeta/tools" {
			branch = "release/v7.1"
		}
		return record.Issue{
			Key: key, Tree: tree, Project: "LEGION", Title: key, Phase: ph, Status: status, Generation: generation,
			Repo: repo, DefaultBranch: branch, GitHubRepo: repo, HandedOver: true,
		}
	}

	// LEGION-207: merged, its tree lingering and its lifecycle closed. A todo on it before the
	// deadline takes the tree back without a re-admission.
	merged := issue("LEGION-207", "LEGION-207", phase.Done, "done", "acme/widgets", 2)
	lingerUntil := at(22, 10, 10, 12)
	merged.LingerUntil = &lingerUntil
	merged.MovedAt = at(22, 8, 40, 12)

	// LEGION-208: the root at work. Its implementer's step is the newest movement of its tree.
	root := issue("LEGION-208", "LEGION-208", phase.Implementing, "in_progress", "acme/widgets", 3)
	root.MovedAt = at(22, 8, 58, 14)

	// LEGION-209: a child of LEGION-208's tree the workflow holds at admitted; Dispatch says it
	// waits on LEGION-210 and on LEGION-213, a key the daemon has no record of.
	child := issue("LEGION-209", "LEGION-208", phase.Admitted, "in_progress", "acme/widgets", 1)
	child.Parent = new("LEGION-208")
	child.MovedAt = at(22, 7, 30, 0)

	// LEGION-210: held from implementing when its architect escalated the failed implementer.
	escalated := issue("LEGION-210", "LEGION-210", phase.Held, "in_progress", "zeta/tools", 1)
	escalated.Hold = &record.Hold{From: phase.Implementing, Reason: record.HoldEscalated}
	escalated.MovedAt = at(22, 6, 18, 0)

	// LEGION-211: a root another tree handed over with the phase it ran there. Its own tree has
	// not been opened, so it holds no slot and waits in the starting part of the line.
	handed := issue("LEGION-211", "LEGION-211", phase.Implementing, "todo", "acme/widgets", 2)
	handed.Rank = "00011"
	handed.HandedOverFrom = "LEGION-200"
	handed.MovedAt = at(21, 9, 18, 0)

	// LEGION-215: a labelled root whose Dispatch chain, recorded with it, names LEGION-209, a
	// child of LEGION-208's live tree: it stands as that tree's unreleased work, held for its
	// ancestry outside the line.
	chained := issue("LEGION-215", "LEGION-215", phase.Admitted, "todo", "acme/widgets", 1)
	chained.Rank = "00015"
	chained.Ancestry = []string{"LEGION-209", "LEGION-208"}
	chained.MovedAt = at(22, 7, 45, 0)

	// LEGION-216: a root that has never run, queued in the line's starting part behind
	// LEGION-211, which Dispatch says waits on LEGION-210: the promotion passes over it.
	queued := issue("LEGION-216", "LEGION-216", phase.Admitted, "todo", "acme/widgets", 1)
	queued.Rank = "00016"
	queued.MovedAt = at(22, 8, 30, 0)

	// LEGION-212: its architect asked a person, and the person answered. It keeps the slot it
	// took when the daemon started it, and waits in the line for its architect's resume.
	answered := issue("LEGION-212", "LEGION-212", phase.AwaitingPerson, "in_progress", "acme/widgets", 1)
	answered.Awaiting = &record.Awaiting{
		Ask: "9a8fd348-55d5-4d72-a793-3cd037f75f13", Since: record.EventMark{Seq: 14, At: at(22, 8, 5, 0)},
		AnsweredAt: &answeredAt, AnsweredBy: record.AnsweredByAsk,
	}
	answered.MovedAt = at(22, 9, 3, 0)

	// LEGION-214: a root of its own tree whose record says the Legion label is off.
	labelOff := issue("LEGION-214", "LEGION-214", phase.Admitted, "todo", "zeta/tools", 1)
	labelOff.HandedOver = false

	// LEGION-218 and LEGION-219: reviewing roots. Each reviewer's completion is the newest
	// movement of its tree; LEGION-219's first read after the approval took its merge state
	// from unread to `blocked`, which moved it once more.
	waiting := issue("LEGION-218", "LEGION-218", phase.Reviewing, "needs_review", "acme/widgets", 1)
	waiting.MovedAt = at(22, 9, 7, 21)
	stuck := issue("LEGION-219", "LEGION-219", phase.Reviewing, "needs_review", "zeta/tools", 3)
	stuck.MovedAt = at(20, 16, 42, 58)

	architect208 := token("LEGION-208", claim.RoleArchitect)
	planner208 := token("LEGION-208", claim.RolePlanner)
	implementer208 := token("LEGION-208", claim.RoleImplementer)
	store := projectionStore{
		issues: []record.Issue{merged, root, child, escalated, handed, answered, labelOff, chained, queued, waiting, stuck},
		phases: map[string][]record.PhaseRow{
			"LEGION-207": {
				{Issue: "LEGION-207", Role: claim.RoleImplementer, Claim: token("LEGION-207", claim.RoleImplementer),
					HandoffCommit: "7d1e4f2a9c3b5e8d0a6f2c4b8e1d3a5f7c9b2e40", Pass: 1, Rounds: 1},
				// Its reviewer approved the head that merged; the claim is gone, so it is no worker.
				{Issue: "LEGION-207", Role: claim.RoleReviewer, Pass: 1,
					Decision: &record.ReviewDecision{State: "approved", Head: "7d1e4f2a9c3b5e8d0a6f2c4b8e1d3a5f7c9b2e40"}},
			},
			"LEGION-208": {
				{Issue: "LEGION-208", Role: claim.RoleArchitect, Claim: architect208, Entry: 2},
				// The planner's completion named its Dispatch handoff record.
				{Issue: "LEGION-208", Role: claim.RolePlanner, Claim: planner208,
					HandoffRecord: "6f1d2c3b-4a5e-4f60-8a7b-9c0d1e2f3a4b", HandoffVersion: 1},
				// The implementer holds the work its row names: the entry its claim was given.
				{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: implementer208, Entry: 4},
				// The reviewer's round asked for changes; its claim is gone, so it is no worker.
				{Issue: "LEGION-208", Role: claim.RoleReviewer, Decision: &record.ReviewDecision{State: "changes_requested"}},
			},
			"LEGION-210": {{Issue: "LEGION-210", Role: claim.RoleImplementer, Claim: token("LEGION-210", claim.RoleImplementer), Pass: 1, Rounds: 1}},
			// The transition that sent LEGION-211's work back raised its implementer's row to
			// entry 2; its claim was given entry 1.
			"LEGION-211": {{Issue: "LEGION-211", Role: claim.RoleImplementer, Claim: token("LEGION-211", claim.RoleImplementer), Rounds: 1, Entry: 2}},
			"LEGION-218": {
				{Issue: "LEGION-218", Role: claim.RoleImplementer, Claim: token("LEGION-218", claim.RoleImplementer),
					HandoffCommit: "6c2f8a0d4b7e13952a8c0f6d3b5e9172c4a8d063", Pass: 1},
				{Issue: "LEGION-218", Role: claim.RoleReviewer, Claim: token("LEGION-218", claim.RoleReviewer), Pass: 1, Entry: 1,
					Decision: &record.ReviewDecision{State: "approved", Head: "6c2f8a0d4b7e13952a8c0f6d3b5e9172c4a8d063"}},
			},
			"LEGION-219": {
				{Issue: "LEGION-219", Role: claim.RoleImplementer, Claim: token("LEGION-219", claim.RoleImplementer),
					HandoffCommit: "0f5a7c2e9b48d1630a7f2c8e4b6d0915a3c7e284", Pass: 2, Rounds: 1,
					BackwardMoves: map[string]int{"reviewing-implementing": 1}},
				// The architect and the reviewer were told of the stuck round
				// (workflow's tellStuckMergeState), which the decision's two marks record.
				{Issue: "LEGION-219", Role: claim.RoleReviewer, Claim: token("LEGION-219", claim.RoleReviewer), Pass: 2, Rounds: 1, Entry: 2,
					Decision: &record.ReviewDecision{State: "approved", Head: "0f5a7c2e9b48d1630a7f2c8e4b6d0915a3c7e284",
						ToldStuckHead: "0f5a7c2e9b48d1630a7f2c8e4b6d0915a3c7e284", ToldStuckState: record.MergeStateBlocked}},
			},
		},
		// The issues the daemon has started hold the cap's slots: LEGION-211's tree has not been
		// opened, LEGION-209 waits at admitted, and LEGION-207 and LEGION-214 hold none.
		slots: []record.Slot{
			{Issue: "LEGION-208", Index: 0, AdmittedAt: at(21, 18, 42, 11)},
			{Issue: "LEGION-210", Index: 1, AdmittedAt: at(21, 19, 5, 26)},
			{Issue: "LEGION-212", Index: 2, AdmittedAt: at(22, 8, 1, 30)},
			{Issue: "LEGION-218", Index: 3, AdmittedAt: at(22, 8, 14, 52)},
			{Issue: "LEGION-219", Index: 4, AdmittedAt: at(19, 10, 27, 44)},
		},
		lifecycles: []string{"LEGION-208", "LEGION-210", "LEGION-212", "LEGION-218", "LEGION-219"},
		pullRequests: map[string]*record.PullRequest{
			"LEGION-207": {Issue: "LEGION-207", Repo: "acme/widgets", Number: 1239, HeadSHA: "7d1e4f2a9c3b5e8d0a6f2c4b8e1d3a5f7c9b2e40",
				State: record.PullRequestMerged},
			// GitHub's own merge state for the head as the daemon's last read found it: a review the
			// repository requires holds this one.
			"LEGION-208": {Issue: "LEGION-208", Repo: "acme/widgets", Number: 1247, HeadSHA: "5b3d9e2c8f1a47d06b2e5c9f3a8d1e4b7c0f6a29",
				State: record.PullRequestOpen, MergeState: record.MergeStateBlocked},
			// No read of the pull request has answered since the push, so the record holds no
			// merge state and no moment of a read, and no CI settlement has brought a run stamped
			// with the head.
			"LEGION-218": {Issue: "LEGION-218", Repo: "acme/widgets", Number: 1251, HeadSHA: "6c2f8a0d4b7e13952a8c0f6d3b5e9172c4a8d063",
				State: record.PullRequestOpen, HeadUpdatedAt: at(22, 8, 58, 40)},
			// The head has stood two days, every read since the first after the approval answered
			// `blocked`, the last of them a minute before this state, and two of the fence's runs
			// are stamped with the head.
			"LEGION-219": {Issue: "LEGION-219", Repo: "zeta/tools", Number: 642, HeadSHA: "0f5a7c2e9b48d1630a7f2c8e4b6d0915a3c7e284",
				State: record.PullRequestOpen, MergeState: record.MergeStateBlocked, HeadUpdatedAt: at(20, 14, 5, 33), StateReadAt: at(22, 9, 16, 12),
				CheckRuns: []record.AttemptRun{
					{Name: "test", ID: 38230245501, Head: "0f5a7c2e9b48d1630a7f2c8e4b6d0915a3c7e284"},
					{Name: "lint", ID: 38230245502, Head: "0f5a7c2e9b48d1630a7f2c8e4b6d0915a3c7e284"},
				}},
		},
		// LEGION-207's check opened before the release that records the project's
		// `production_check` role, so it keeps the implementer; look 2 is open, woken by the
		// Dispatch issue.deployed that release still read.
		checks: []record.ProductionCheck{{
			Issue: "LEGION-207", Project: "LEGION", Tree: "LEGION-207", Repo: "acme/widgets", Number: 1239,
			MergeSHA: "3e9c0a7b5d2f4e8a1c6b9d0f2a4e7c5b8d1f3a60", OpenedAt: at(22, 8, 40, 12), BoundAt: time.Date(2026, 10, 6, 8, 40, 12, 0, time.UTC),
			Role: claim.RoleImplementer, Looks: 2, LookOpen: true,
			LastWakeKind: "issue.deployed", LastWakeDetail: "run 38230245571 completed 2026-09-22T09:10:44Z", LastWakeAt: new(at(22, 9, 10, 51)),
		}},
		// LEGION-208's lifecycle lane is running row 801; the supervise row queued after it came
		// due two and a half minutes ago and waits behind it (record.Store.HeldRows).
		held: []record.HeldRow{{OutboxRow: record.OutboxRow{ID: 802, Kind: record.OutboxKindSupervise, Issue: "LEGION-208", NextAt: at(22, 9, 15, 30)}, WaitsOn: 801}},
		mirror: []record.DispatchIssueRow{
			{Key: "LEGION-207", Project: "LEGION", Status: "done", Blockers: []string{}, Seq: 3},
			{Key: "LEGION-208", Project: "LEGION", Status: "in_progress", Blockers: []string{}, Seq: 4},
			{Key: "LEGION-209", Project: "LEGION", Status: "in_progress", Blockers: []string{"LEGION-210", "LEGION-213"}, Seq: 5, Parent: "LEGION-208"},
			{Key: "LEGION-210", Project: "LEGION", Status: "in_progress", Blockers: []string{}, Seq: 6},
			{Key: "LEGION-211", Project: "LEGION", Status: "todo", Blockers: []string{"LEGION-210"}, Seq: 7, Rank: "00011"},
			{Key: "LEGION-212", Project: "LEGION", Status: "in_progress", Blockers: []string{}, Seq: 8},
			{Key: "LEGION-214", Project: "LEGION", Status: "todo", Blockers: []string{}, Seq: 9},
			{Key: "LEGION-215", Project: "LEGION", Status: "todo", Blockers: []string{}, Seq: 12, Rank: "00015"},
			{Key: "LEGION-216", Project: "LEGION", Status: "todo", Blockers: []string{"LEGION-210"}, Seq: 13, Rank: "00016"},
			{Key: "LEGION-218", Project: "LEGION", Status: "needs_review", Blockers: []string{}, Seq: 10},
			{Key: "LEGION-219", Project: "LEGION", Status: "needs_review", Blockers: []string{}, Seq: 11},
		},
		// The deployment gates `root-issues`, the one step spec: LEGION-207's, LEGION-208's,
		// LEGION-211's, LEGION-218's and LEGION-219's specs stand approved at the version Dispatch
		// holds (LEGION-208's must: a shut gate of its tree would hold its child at design_gate,
		// the word the held ladder reads before dispatch_blocker), LEGION-212's reviewer asked for
		// changes at the version Dispatch holds and the architect was told, LEGION-214's document
		// stands a version above the one its person approved, and nobody registered LEGION-210's.
		gates: map[string]map[record.DesignStep]record.DesignGate{
			"LEGION-207": {record.DesignStepSpec: {Issue: "LEGION-207", Step: record.DesignStepSpec, ArtifactID: "0c7e5a31-9d84-4b26-a0f3-5e81c2d47b90", LatestVersion: 4, ApprovedVersion: new(4)}},
			"LEGION-208": {record.DesignStepSpec: {Issue: "LEGION-208", Step: record.DesignStepSpec, ArtifactID: "d2f1c6b4-8e07-4a53-9c1d-6b8f2e5a7093", LatestVersion: 7, ApprovedVersion: new(7)}},
			"LEGION-214": {record.DesignStepSpec: {Issue: "LEGION-214", Step: record.DesignStepSpec, ArtifactID: "4e8b2a61-7d93-4c05-b1f6-8a3e5c9d2074", LatestVersion: 2, ApprovedVersion: new(1)}},
			"LEGION-211": {record.DesignStepSpec: {Issue: "LEGION-211", Step: record.DesignStepSpec, ArtifactID: "e5a2c1d7-3b46-4f82-9e07-2d4a6c8b1f53", LatestVersion: 3, ApprovedVersion: new(3)}},
			"LEGION-212": {record.DesignStepSpec: {Issue: "LEGION-212", Step: record.DesignStepSpec, ArtifactID: "b71f0a94-6c3e-4d28-85af-19c7d0e3b462", LatestVersion: 2,
				ToldChangesVersion: new(2), ToldChangesReason: "name the rollback path for the schema change"}},
			"LEGION-218": {record.DesignStepSpec: {Issue: "LEGION-218", Step: record.DesignStepSpec, ArtifactID: "f3b8d205-7c41-4e69-9a02-5d7e1c6b4f38", LatestVersion: 5, ApprovedVersion: new(5)}},
			"LEGION-219": {record.DesignStepSpec: {Issue: "LEGION-219", Step: record.DesignStepSpec, ArtifactID: "a9142c7e-0b63-4d85-8f17-3e6a0d9b2c54", LatestVersion: 2, ApprovedVersion: new(2)}},
		},
		// LEGION-208's tree was drawn into the memory group, a tree experiment every issue of the
		// tree shows; `plan-length` and `planning` are drawn for each issue on its own, so a root
		// and its child stand in different arms; LEGION-210 carries a row a hand-over settled at
		// no arm at all, which is how a reader tells a unit settled at no arm from a unit never
		// drawn.
		assignments: map[string][]record.Assignment{
			"LEGION-208": {
				{Experiment: config.MemoryExperimentName, Unit: string(config.ExperimentTree), Variant: config.MemoryVariantMemory, Mode: string(config.ExperimentRandom), DrawnAt: &populatedMemoryDrawnAt},
				{Experiment: "plan-length", Unit: string(config.ExperimentIssue), Variant: "short", Mode: string(config.ExperimentRandom), DrawnAt: &populatedPlanDrawnAt},
			},
			"LEGION-209": {
				{Experiment: "plan-length", Unit: string(config.ExperimentIssue), Variant: "long", Mode: string(config.ExperimentRandom), DrawnAt: &populatedPlanDrawnAt},
				{Experiment: "planning", Unit: string(config.ExperimentIssue), Variant: "b", Mode: string(config.ExperimentRandom), DrawnAt: &populatedPlanDrawnAt, Plan: string(config.PlanGroupDefault)},
			},
			"LEGION-210": {{Experiment: "plan-length", Unit: string(config.ExperimentTree)}},
		},
		// LEGION-208's implementer counts its returns on its plan's task (record.ReturnCounts):
		// three passes, two of them ended by a review that requested changes, each pair of the
		// moves back that made them.
		planTasks: []record.PlanTask{{Issue: "LEGION-208", Counts: record.ReturnCounts{
			Pass: 3, Rounds: 2, BackwardMoves: map[string]int{"reviewing-implementing": 1, "testing-implementing": 1},
		}}},
	}

	architectStep := supervise.StepRecord{
		At: at(22, 9, 16, 58), TurnStartedAt: at(22, 9, 16, 40), Tool: "read", CallID: "toolu_arch_3",
		ArgsBytes: 74, ArgsHash: "3f7c1a9d62b40e58", StartedAt: at(22, 9, 16, 55), EndedAt: at(22, 9, 16, 58),
	}
	implementerStep := supervise.StepRecord{
		At: at(22, 9, 17, 12), TurnStartedAt: at(22, 9, 16, 30), Tool: "bash", CallID: "toolu_impl_11",
		ArgsBytes: 128, ArgsHash: "9b2e45c0d1f8a673", StartedAt: at(22, 9, 17, 12),
	}
	claims := []supervise.Claim{
		// The pacing holds this claim's relaunch onto this daemon's plugin.
		{Token: token("LEGION-207", claim.RoleImplementer), Project: "legion", Tree: "LEGION-207", Issue: "LEGION-207", Role: claim.RoleImplementer,
			Generation: 2, State: supervise.StateWorking, Session: "ses_implementer_207"},
		// The architect is ready in its tmux pane: the turn it began and ended, the ready that
		// dates the death's cause, and a start failure of a launch since the death. Its
		// handshake is that of the entry it was last queued, which a turn confirmed.
		{Token: architect208, Project: "legion", Tree: "LEGION-208", Issue: "LEGION-208", Role: claim.RoleArchitect, Generation: 3,
			State: supervise.StateReady, Session: "ses_architect_208",
			Locator:    &runtime.Locator{Runtime: runtime.RuntimeTmux, Claim: architect208, Incarnation: "31881:9442101", Tmux: &runtime.TmuxLocator{Window: "@4", Pane: "%12"}},
			GivenEntry: 2, Handshake: supervise.EntryHandshake{Entry: 2, Generation: 3, QueuedAt: at(22, 8, 58, 14), ConfirmedAt: at(22, 8, 58, 51)},
			Step: architectStep, LastTurnStart: at(22, 9, 16, 40), LastTurnEnd: at(22, 9, 16, 58), LastReady: at(22, 9, 15, 44),
			LastDeathCause: "the pane's process was gone at the daemon's read", LastStartFailure: "the pod was not ready within the worker boot timeout"},
		// The pod's update round, which every claim of the pod shares: the planner answered it
		// with the head it pushed and waits for its launch onto the replacement.
		{Token: planner208, Project: "legion", Tree: "LEGION-208", Issue: "LEGION-208", Role: claim.RolePlanner, Generation: 3,
			State: supervise.StateSuspended, Session: "ses_planner_208", Locator: podLocator("LEGION-208", claim.RolePlanner, 2),
			UpdateRound: "pod:" + pod, UpdateReasons: updateReasons, UpdateTaskedAt: new(at(22, 9, 2, 0)), UpdateSignalledAt: new(at(22, 9, 6, 30)),
			UpdateHead: "4c1b8e2a6f9d3047b5e1c8a2f6d940b3e7c5a128"},
		// The implementer was asked the same round and has not answered; two of its turns ended
		// in a provider error in a row; it holds the task of its entry and is in a call with a
		// start and no end, the shape a stalled turn leaves behind.
		{Token: implementer208, Project: "legion", Tree: "LEGION-208", Issue: "LEGION-208", Role: claim.RoleImplementer, Generation: 3,
			State: supervise.StateWorking, Session: "ses_implementer_208", Locator: podLocator("LEGION-208", claim.RoleImplementer, 5),
			ServingGeneration: 3, GivenEntry: 4, UpdateRound: "pod:" + pod, UpdateReasons: updateReasons, UpdateTaskedAt: new(at(22, 9, 2, 0)),
			ProviderErrorRun: 2, Pending: &supervise.Delivery{Phase: phase.Implementing, QueuedAt: at(22, 9, 16, 58), Generation: 3, Entry: 4},
			Step: implementerStep},
		// Its implementer failed and nothing has launched it since: the cause, the sentence, the
		// moment and the counts it failed with, the death whose refused relaunches spent the
		// budget, and the sentence a launch try was refused with for the shared leak check.
		{Token: token("LEGION-210", claim.RoleImplementer), Project: "legion", Tree: "LEGION-210", Issue: "LEGION-210", Role: claim.RoleImplementer,
			Generation: 1, State: supervise.StateFailed, Session: "ses_implementer_210",
			FailureCause: supervise.CauseDied, Failure: "launch failures ran out", FailedAt: at(22, 8, 47, 3),
			Budgets: supervise.Budgets{LaunchFailures: 4, Deaths: 1}, PendingCause: supervise.CauseDied,
			LeakCheckCause: "the shared public leak check could not be installed: GitHub answered the read of example-org/deployment at leak-check-v3 with HTTP 404: Not Found"},
		// A release suspended it and the wake never brought its pod back; it keeps the task it held
		// through the suspension, and the two sends that never reached a turn are on it.
		{Token: token("LEGION-211", claim.RoleImplementer), Project: "legion", Tree: "LEGION-211", Issue: "LEGION-211", Role: claim.RoleImplementer,
			Generation: 2, State: supervise.StateSuspended, Session: "ses_implementer_211", GivenEntry: 1,
			Pending: &supervise.Delivery{Phase: phase.Implementing, QueuedAt: at(22, 8, 54, 20), Generation: 2, Entry: 1,
				SendFailures: 2, LastSendError: "post the prompt: dial tcp 10.42.0.17:13371: connect: connection refused"}},
		// A tester of its tree retired while the issue stood in its phase.
		{Token: token("LEGION-212", claim.RoleTester), Project: "legion", Tree: "LEGION-212", Issue: "LEGION-212", Role: claim.RoleTester,
			Generation: 1, State: supervise.StateRetired, Session: "ses_tester_212"},
		{Token: token("LEGION-218", claim.RoleImplementer), Project: "legion", Tree: "LEGION-218", Issue: "LEGION-218", Role: claim.RoleImplementer,
			Generation: 1, State: supervise.StateIdle, Session: "ses_implementer_218"},
		{Token: token("LEGION-218", claim.RoleReviewer), Project: "legion", Tree: "LEGION-218", Issue: "LEGION-218", Role: claim.RoleReviewer,
			Generation: 1, State: supervise.StateIdle, Session: "ses_reviewer_218", GivenEntry: 1},
		{Token: token("LEGION-219", claim.RoleImplementer), Project: "legion", Tree: "LEGION-219", Issue: "LEGION-219", Role: claim.RoleImplementer,
			Generation: 3, State: supervise.StateIdle, Session: "ses_implementer_219"},
		{Token: token("LEGION-219", claim.RoleReviewer), Project: "legion", Tree: "LEGION-219", Issue: "LEGION-219", Role: claim.RoleReviewer,
			Generation: 3, State: supervise.StateIdle, Session: "ses_reviewer_219", GivenEntry: 2},
	}
	// The session's capability report, from the process the locator names: two rows it proved
	// and one it did not, with the fact its check found.
	implementerLocator := podLocator("LEGION-208", claim.RoleImplementer, 5)
	reports := map[claim.Token]capabilities.Report{
		implementer208: {
			Claim: implementer208, Generation: 3, Locator: *implementerLocator,
			MeasuredAt: at(22, 9, 17, 3), ReportedAt: at(22, 9, 17, 4), ElapsedMs: 900,
			Rows: []capabilities.Row{
				{Name: capabilities.Subagents, OK: true, Detail: "6 task agents discovered"},
				{Name: capabilities.DispatchEnvoyTools, OK: true, Detail: "dispatch read LEGION-208 answered; envoy_whoami is active"},
				{Name: capabilities.GitHub, Detail: "gh api user: HTTP 401: Bad credentials"},
			},
		},
	}
	// A wave going out at its operator's pace, with one wake of each lane still held:
	// LEGION-207's implementer waits for its relaunch onto this daemon's plugin, LEGION-208's
	// architect for the catch-up a child's re-admission wrote it in place of a launch, which waits
	// on its outbox row, and LEGION-208's planner for its launch onto the pod's replacement. The
	// two lanes are one list in claim order.
	wakes := api.WakesWaitingOf([]supervise.WaitingWake{
		{Claim: token("LEGION-207", claim.RoleImplementer), Issue: "LEGION-207", Role: claim.RoleImplementer, Kind: supervise.WakePluginRefresh, At: at(22, 9, 17, 56)},
		{Claim: planner208, Issue: "LEGION-208", Role: claim.RolePlanner, Kind: supervise.WakeLaunch, At: at(22, 9, 18, 11)},
	}, []api.WakeWaiting{
		{Claim: architect208, Issue: "LEGION-208", Role: claim.RoleArchitect, Kind: supervise.WakeCatchUp, DueAt: at(22, 9, 18, 3)},
	})
	// The claims nothing will wake, one of each state the rule names a claim in.
	stranded := []supervise.Claim{claims[4], claims[5], claims[6]}
	return populatedRecord{
		inputs: Inputs{
			Store: store, Project: "LEGION", Steps: config.DesignGateRootIssues.Steps(),
			Experiments: []config.Experiment{{Name: config.MemoryExperimentName}, {Name: "plan-length"}, {Name: "planning"}},
			Claims:      claims, Stranded: stranded, Reports: reports, Wakes: wakes, Now: populatedReadAt,
		},
		// No architect claim of LEGION-210 stands in the record, and the work of its architect's
		// entry has waited past the bound with no start of one queued: the second arm holds.
		architectStalls: map[string][]record.ArchitectStallArm{"LEGION-210": {record.ArchitectClaimUnreachable}},
	}
}
