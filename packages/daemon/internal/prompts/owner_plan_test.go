package prompts

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// The owner of an issue picks which of Legion's step roles its one task runs. Its prompt therefore
// lists each role with the type of its step, the owner's own acceptance step, the two operations
// that give and stop a plan, a stop's answers by their codes and that a stop before an issue's
// first step settles the plan that step would walk, the notices that a stopped task, a parent
// held before its retro and a plan of an earlier owner send it, the two stops a task-stopped
// notice tells of (a failed step's, and another architect's stop_task, whose notice names that
// architect) and the stop it is not sent for (its own), when a plan must be given and how a plan
// replaces the default steps once the work started, and the answers a later version gets from the
// workflow (workflow.planGiven): the running task's block, its text included, is refused and the
// unchanged block taken, each refusal names its cause and changes nothing so the same version can
// be given again, and an issue that started its work before Legion took plans takes none. A new
// version of `plan.md` names the token of the text it replaces, since Dispatch refuses a replacing
// upload that names none (envoy's PRECONDITION_REQUIRED), and a parked or done child takes a new
// plan only after its rerun, since a version for its done or left task is refused TASK_DONE
// (workflow.planGiven): the rerun, then stop_task, then register_plan. Each is held by its terms:
// a term alone where the term is the rule (a code, an operation, a role), and otherwise the terms
// of the clause that states the rule, in their order. Both architect compositions share that
// part, so both are read. This shows the text is loaded, never that an owner follows it.
func TestTheOwnersPromptListsTheRolesAndTheNewNotices(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, tc := range []struct {
		name   string
		isRoot bool
	}{{"root architect", true}, {"sub-architect", false}} {
		t.Run(tc.name, func(t *testing.T) {
			composed := composedText(t, composer, claim.RoleArchitect, tc.isRoot)
			text := foldWhitespace(composed)
			for _, term := range []string{
				"planner (planning)",
				"implementer (implementation)",
				"tester (testing)",
				"reviewer (review)",
				"acceptance:owner",
				"register_plan",
				"stop_task",
				"task-stopped",
				"retro-held",
				"plan-not-taken",
				// The hold a stop leaves and what ends or refuses it, by its contract terms: the
				// person's `todo`, the tool that refuses a release, its two refusal codes, and the
				// case of a task that never started, which waits for its gate instead.
				"`todo`",
				"retry_or_escalate",
				"`TASK_STOPPED`",
				"`HELD_FOR_CHILDREN`",
				"never started",
				// stop_task's answers by their codes, and the stop before an issue's first step,
				// which settles the plan that step would walk first.
				"`TASK_NOT_FOUND`",
				"`TASK_DONE`",
				"`TREE_LINGERING`",
				"`ISSUE_OUTSIDE_TREE`",
				"`TASK_STALE_GENERATION`",
				"`TASK_AWAITING_PERSON`",
				"settles the plan",
				// sign_off's answers when the record moved under it, by their codes.
				"`ISSUE_NOT_OWNED`",
				"`SIGNOFF_STALE_GENERATION`",
				"`SIGNOFF_STALE_ENTRY`",
				// A later version's answers by their codes.
				"`PLAN_VERSION_OLD`",
				"`PLAN_DOCUMENT_CHANGED`",
				"`PLAN_STALE_GENERATION`",
			} {
				if !strings.Contains(text, term) {
					t.Errorf("the composed %s prompt omits %q", tc.name, term)
				}
			}
			for _, terms := range [][]string{
				// A root's plan comes before its gate, and a child's before its release.
				{"register_plan", "register_gate"},
				{"register_plan", "release_children"},
				// An issue already at work runs the task default: stop it, then give the plan.
				{"stop_task", "default", "register_plan"},
				// A later version changes only a task that waits or is stopped, refuses a change
				// of the running task, and takes its block unchanged.
				{"later version", "waits", "stopped"},
				{"running task", "`PLAN_REFUSED`", "task <id> is running"},
				{"running task's block", "goes on"},
				// A refusal changes nothing, so the same version is given again, and an issue
				// that started before plans takes none.
				{"changes nothing", "given again"},
				{"started its work before Legion took plans", "`PLAN_REFUSED`"},
				// Another architect's stop names it in the notice; one's own stop sends none.
				{"`stop_task`", "`stoppedBy`"},
				{"no notice", "200", "`stop_task`"},
				// A new version of plan.md names the token of the text it replaces.
				{"`plan.md` exists", "--precondition-json", "`dispatch doc-read --issue <issue> --artifact plan.md`", "`400 PRECONDITION_REQUIRED`"},
				// A parked or done child takes a new plan after its rerun: stop, then give it.
				{"parked or done child", "only after `rerun_child`", "`TASK_DONE`"},
				{"after the rerun", "`stop_task`", "`register_plan`", "first step"},
			} {
				if !clauseHolds(composed, terms...) {
					t.Errorf("no clause of the composed %s prompt holds %q, in that order", tc.name, terms)
				}
			}
		})
	}
}

// An owner's plan can leave out the tester or the reviewer, so the merger verifies the steps its
// task's `Steps:` line names rather than a tester's and a reviewer's cycles. A task with no review
// step on a base whose rules require an approving review leaves a merge only a person or the owner
// can unblock, and Legion posts no approval of its own, so the merger waits and tells the task's
// owner which review is missing. This shows the text is loaded, never that a merger follows it.
func TestTheMergerWaitsForAMissingReview(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	text := foldWhitespace(composedText(t, composer, claim.RoleMerger, false))
	for _, phrase := range []string{
		"Verify that each step your task's Steps line names completed (today's line, on a task with no `Task:` line), and that the retro completed.",
		"If your task's Steps line names no reviewer step and the base's rules require an approving review that no step of this task gave, do not merge, do not approve the pull request yourself, and do not return the task. Wait, and tell the owner of the task which review is missing: the rule that requires it, and that the task's steps have no review step. Only the owner or a person can give that review or change the plan.",
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the composed merger prompt omits %q", phrase)
		}
	}
	if retired := "Verify tester and reviewer cycles completed"; strings.Contains(text, retired) {
		t.Errorf("the composed merger prompt still says %q", retired)
	}
}

// On a task whose `Steps:` line names no reviewer, the implementer writes the two lines of the PR
// body's brief the reviewer writes on any other task. What it writes there is stated once, in *The
// brief for the human* of `skill://legion-worker/references/pr-body.md`, so the implementer's
// prompt names the case and points there, as the reviewer's prompt does. This shows the text is
// loaded, never that an implementer follows it.
func TestTheImplementerPointsToTheBriefOnATaskWithNoReviewer(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	text := foldWhitespace(composedText(t, composer, claim.RoleImplementer, false))
	pointer := "On a task whose `Steps:` line names no reviewer, also fill `Look at first` and `Not proven / risk` in the live PR body at each round (*The brief for the human* in that file)."
	if !strings.Contains(text, pointer) {
		t.Errorf("the composed implementer prompt omits %q", pointer)
	}
}

// On a task whose `Steps:` line names no reviewer, nobody adjudicates a bot's review thread, so the
// implementer's prompt gives it the duty, held here by its contract terms rather than its wording:
// the paragraph of the composed implementer prompt that names that case and bot threads names the
// retro among the completions it answers them before, `request_backward_move` to `implementing`
// for a thread whose fix the retro cannot push, and the completion's `summary`, in which it names
// each thread it declined to its architect. This shows the text is loaded, never that an
// implementer follows it.
func TestTheImplementerAnswersTheBotThreadsOnATaskWithNoReviewer(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var held []string
	for _, paragraph := range strings.Split(composedText(t, composer, claim.RoleImplementer, false), "\n\n") {
		if paragraph = foldWhitespace(paragraph); strings.Contains(paragraph, "names no reviewer") && strings.Contains(paragraph, "bot thread") {
			held = append(held, paragraph)
		}
	}
	if len(held) == 0 {
		t.Fatal("the composed implementer prompt holds no paragraph on bot threads on a task whose `Steps:` line names no reviewer")
	}
	for _, term := range []string{"retro", "`request_backward_move`", "`implementing`", "`summary`", "declined", "architect"} {
		if !slices.ContainsFunc(held, func(paragraph string) bool { return strings.Contains(paragraph, term) }) {
			t.Errorf("the composed implementer prompt's duty on bot threads on a task with no reviewer names no %s: %q", term, held)
		}
	}
}

// A catch-up that names an issue waiting in `accepting` leaves the root architect its acceptance.
// The daemon part states what to do, in two sentences: sign off or send back, and the head a
// parent's sign-off needs; the role part points to it rather than saying it again, so the composed
// root prompt holds the parent's head rule once. This shows the text is loaded, never that an
// architect follows it.
func TestTheRootsCatchUpStatesItsAcceptanceOnce(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	text := foldWhitespace(composedText(t, composer, claim.RoleArchitect, true))
	stated := "No worker runs there and the notice that moved it there may never have reached you, so read each one's evidence, then `sign_off` it or send it back with `request_backward_move`. Sign off a parent only on a head that the step before your acceptance passed and that carries the merges of every child its criteria need; otherwise run its last round (`skill://legion-architect/references/acceptance.md`, \"The last round\")."
	if !strings.Contains(text, stated) {
		t.Errorf("the composed root architect prompt omits %q", stated)
	}
	if n := strings.Count(text, "carries the merges of every child its criteria need"); n != 1 {
		t.Errorf("the composed root architect prompt says the head a parent's sign-off needs %d times, want 1", n)
	}
}

// A planner step of an owner's plan adds no task block. The owner's plan and a planner's plan are
// one document, `plan.md`, and Legion carries out each task block of a version the owner gives, so
// a block the planner wrote there would become work of its own. The planner tells the two cases
// apart by the document: where `plan.md` holds a `plan_task` block with the id its `Task:` line
// names, an owner wrote that plan, and the planner changes no task block, adds none, and writes its
// plan below them as plain sections; where it holds none (the task `default`, or an issue with no
// `Task:` line), the planner writes each task as a `plan_task` block. The old rule, which wrote a
// block in every case, is gone. Each rule is held by the terms of the clause that states it, in
// their order. This shows the text is loaded, never that a planner follows it.
func TestThePlannerAddsNoTaskBlockToAnOwnersPlan(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	composed := composedText(t, composer, claim.RolePlanner, false)
	text := foldWhitespace(composed)
	for _, terms := range [][]string{
		// Read plan.md first, with the command that lists its blocks.
		{"`plan.md`", "first", "`dispatch doc-read --issue <issue> --artifact plan.md --blocks`"},
		// A block whose id the Task: line names is the owner's: change none, add none.
		{"`plan_task`", "`Task:`", "owner", "no `plan_task` block", "add none", "plain sections"},
		// With no such block, the planner writes its tasks as plan_task blocks.
		{"no such block", "`default`", "`Task:`", "`plan_task` block", "`plan.md`"},
	} {
		if !clauseHolds(composed, terms...) {
			t.Errorf("no clause of the composed planner prompt holds %q, in that order", terms)
		}
	}
	if retired := "Write each task of the plan as a `plan_task` block of `plan.md`"; strings.Contains(text, retired) {
		t.Errorf("the composed planner prompt still says %q, which adds a block to an owner's plan", retired)
	}
}

// The first report of a run an event ended is taken and moves nothing, with or without plan rows
// (workflow's leftItsPhase), so each phase worker is told to end its turn on it. A report of the
// step at which a task with plan rows was stopped is refused as late (intake's
// LateReportOfStoppedTask): the answer names no result and no cause, sends the agent to
// `dispatch steps`, and keeps the handoff record the report named as used, so each phase worker is
// told what TASK_STOPPED means and that a later run of its step writes a handoff of its own. Each
// duty is held by the terms of the clause that states it, in their order, so a rewording that
// keeps them passes. This shows the text is loaded, never that a worker follows it.
func TestAPhaseWorkerIsToldWhatALateReportsAnswerMeans(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	duties := [][]string{
		// The first report after an event ended the run is taken and moves nothing: end the turn.
		{"first report", "is no refusal", "taken", "moves nothing", "ended step's report", "end your turn"},
		// The report of the step a task was stopped at is refused, and the answer gives the stop.
		{"stopped", "`TASK_STOPPED`", "reason"},
		// It moved nothing, `dispatch steps` shows the run's end, and its handoff record is used,
		// so a later run writes a handoff of its own.
		{"moved nothing", "`dispatch steps --issue", "handoff record", "used", "later run", "handoff of its own"},
	}
	for _, role := range []claim.Role{claim.RolePlanner, claim.RoleImplementer, claim.RoleTester, claim.RoleReviewer} {
		composed := composedText(t, composer, role, false)
		for _, terms := range duties {
			if !clauseHolds(composed, terms...) {
				t.Errorf("no clause of the composed %s prompt holds %q, in that order", role, terms)
			}
		}
	}
}

// The two architect role parts name a step only where the issue's task holds it. An owner's plan
// can leave out the planner, the tester, the reviewer and the owner's own acceptance, so each
// sentence that named one of them as if every issue ran it now says where it runs and, where it
// does not, who holds its duty: the order of the starts is the order of the steps of the issue's
// plan; `accepting` and the sign-off are the owner's only where the task holds `acceptance:owner`;
// the move back names an earlier step's phase; a failed step goes back to the implementer or to
// the earlier step its completion names. A parent with no `acceptance:owner` step gets no reading
// of its finished whole from its owner, and each part says so in one sentence where it speaks of a
// parent and its children. Each part gives `register_plan` and `stop_task` with the arguments of
// the daemon's routes. The words the parts carried before are gone from both, and from the
// architect's three daemon parts in go/, which Compose joins to them (architect-root.md or
// architect.md, then architect-common.md): a retired phrase there reaches its reader as surely as
// one in a role part. Each of the three is read by its name, so a part that is missing fails the
// read rather than passing with nothing read. This shows the text is loaded, never that an owner
// follows it.
func TestTheArchitectRolePartsNameOnlyTheStepsAnIssuesTaskRuns(t *testing.T) {
	// A parent with no acceptance step gets no reading of its whole: Legion holds it before its
	// retro (`retro-held`), so a parent whose work the owner wants to read is given the step. Held
	// by the terms of the one clause that says so, in their order.
	parentHeld := []string{"parent", "no step `acceptance:owner`", "`retro-held`", "the step `acceptance:owner`"}
	shared := []string{
		"`register_plan` (`issue`, `artifactId`, `version`)",
		"`stop_task` (`issue`, `task`)",
		"in the order of the steps of",
		"holds your step `acceptance:owner`",
		"A task with no `acceptance:owner` step has no `accepting`",
		"`to` the phase of an earlier step of the task, and `reason`",
		"is a gap that a tester step and a reviewer step name and neither refuses",
		"goes back to the implementer, or to the earlier step its completion names",
	}
	retired := []string{
		"fixed workflow table",
		"by the daemon's table",
		"the tester and the reviewer name and neither refuses",
		"`implementing` or `testing`",
		"at the approved head",
		"reviewed head",
		"the planner's `plan` handoff",
		"so its planner may already be reading it",
		"child's planner may already be working",
		"once the issue enters `accepting`",
		"since the reviewer approves only",
		"The reviewer's approval moves this child to `accepting`",
		"is mandatory after your acceptance",
		"Your last act is `sign_off` of this child in `accepting`",
		"a parent only on an approved head",
		"`implementing`, or `testing` where the task has a tester step",
	}
	for _, tc := range []struct {
		part string
		want []string
	}{
		{"architect-root.md", append(slices.Clone(shared), "each issue's `plan.md`", "before `register_gate`")},
		{"architect.md", append(slices.Clone(shared), "the agent of its first step may already be reading it", "The agent of your child's first step may already be working", "given it its plan with `register_plan`")},
	} {
		t.Run(tc.part, func(t *testing.T) {
			part := rolePart(t, tc.part)
			text := foldWhitespace(part)
			for _, phrase := range tc.want {
				if !strings.Contains(text, phrase) {
					t.Errorf("roles/%s omits %q", tc.part, phrase)
				}
			}
			if !clauseHolds(part, parentHeld...) {
				t.Errorf("no clause of roles/%s holds %q, in that order", tc.part, parentHeld)
			}
			for _, phrase := range retired {
				if strings.Contains(text, phrase) {
					t.Errorf("roles/%s still says %q", tc.part, phrase)
				}
			}
		})
	}
	for _, part := range []string{"architect-root.md", "architect.md", "architect-common.md"} {
		body, err := goParts.ReadFile("go/" + part)
		if err != nil {
			t.Fatalf("read the daemon part go/%s: %v", part, err)
		}
		text := foldWhitespace(string(body))
		for _, phrase := range retired {
			if strings.Contains(text, phrase) {
				t.Errorf("go/%s still says %q", part, phrase)
			}
		}
	}
}

// holdsAndRetires fails t for each phrase of want that the folded text of one part omits and for
// each phrase of gone that it still holds, naming the part.
func holdsAndRetires(t *testing.T, part, text string, want, gone []string) {
	t.Helper()
	text = foldWhitespace(text)
	for _, phrase := range want {
		if !strings.Contains(text, phrase) {
			t.Errorf("%s omits %q", part, phrase)
		}
	}
	for _, phrase := range gone {
		if strings.Contains(text, phrase) {
			t.Errorf("%s still says %q", part, phrase)
		}
	}
}

// An owner names the steps of its task in the order they run, and the one rule of that order is
// that the implementer's step stands before each step that reads its pull request
// (plan.Supported). The owner's acceptance reads a tester's handoff and a reviewer's approval only
// where that step stands before the acceptance, since a step after it has written nothing yet.
// Each part is read alone, never in the composition, so one part's words cannot stand for
// another's. This shows the text is loaded, never that an owner follows it.
func TestTheAgentsAreToldNoOrder(t *testing.T) {
	evidence := "the tester's handoff where a tester step comes before yours, and the reviewer's approval of the head by SHA where a reviewer step comes before yours"
	retiredEvidence := "the tester's handoff where the task has a tester step"
	for _, tc := range []struct {
		part       string
		read       func(*testing.T) string
		want, gone []string
	}{
		{
			"go/architect-common.md", func(t *testing.T) string { return goPart(t, "architect-common.md") },
			[]string{"Name the steps of a task in the order they run.", evidence},
			[]string{"in that order", retiredEvidence},
		},
		{
			"roles/architect.md", func(t *testing.T) string { return rolePart(t, "architect.md") },
			[]string{evidence}, []string{retiredEvidence},
		},
		{
			"roles/architect-root.md", func(t *testing.T) string { return rolePart(t, "architect-root.md") },
			[]string{evidence}, []string{retiredEvidence},
		},
	} {
		t.Run(tc.part, func(t *testing.T) {
			holdsAndRetires(t, tc.part, tc.read(t), tc.want, tc.gone)
		})
	}
}

// A move back goes to the phase of any earlier step of the issue's task, in the task's own order
// (workflow's backwardRows), so the daemon's parts name no fixed target for it. A head that
// conflicts with its base keeps the issue in its phase, which can be any phase after
// implementing, a planner's among them. A step reads a planner's plan only where a planner step
// stands before it in its task: a step before the planner has no plan to build from. Each part
// is read alone and by its name, so a missing part fails the read. This shows the text is
// loaded, never that an agent follows it.
func TestAMoveBackGoesToAnyEarlierStepInTheGoParts(t *testing.T) {
	toArchitect := []string{"`request_backward_move` with `issue`, `to` (the phase of an earlier step of the task) and `reason`"}
	retiredTo := []string{"`implementing`, or `testing` where the task has a tester step"}
	for _, tc := range []struct {
		part       string
		want, gone []string
	}{
		{
			"architect-common.md",
			[]string{
				"send it back with `request_backward_move` (`issue`, `to` the phase of an earlier step of the task, and `reason`)",
				"the issue stays in the phase the notice names, a phase after `implementing`",
			},
			[]string{
				"`implementing` or, where the task has a tester step, `testing`",
				"the phase the notice names (`testing`, `reviewing`, `accepting`, `retro` or `merging`)",
			},
		},
		{"architect.md", toArchitect, retiredTo},
		{"architect-root.md", toArchitect, retiredTo},
		{
			"worker-common.md",
			[]string{
				"each step after a planner step of its task, and each step of a task with no `Task:` line",
				"A step with no planner step before it in its task has no planner's plan",
			},
			[]string{"`Steps:` line names the planner", "`Steps:` line names no planner"},
		},
	} {
		t.Run(tc.part, func(t *testing.T) {
			holdsAndRetires(t, "go/"+tc.part, goPart(t, tc.part), tc.want, tc.gone)
		})
	}
}

// The architect's role parts say what its daemon parts say of a move back: it goes to the phase of
// any earlier step of the task. Each role part is read alone, and the composition of each
// architect is read whole and folded, since the old clause in a role part is cut by a line break
// and Compose joins the daemon parts to it. This shows the text is loaded, never that an architect
// follows it.
func TestAMoveBackGoesToAnyEarlierStepInTheRoleParts(t *testing.T) {
	moveBack := "send it back with `request_backward_move` (`issue`, `to` the phase of an earlier step of the task, and `reason`)"
	retired := "`implementing`, or `testing` where the task has a tester step"
	for _, part := range []string{"architect.md", "architect-root.md"} {
		t.Run(part, func(t *testing.T) {
			holdsAndRetires(t, "roles/"+part, rolePart(t, part), []string{moveBack}, []string{retired})
		})
	}
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, isRoot := range []bool{true, false} {
		t.Run(fmt.Sprintf("composed, root %t", isRoot), func(t *testing.T) {
			holdsAndRetires(t, fmt.Sprintf("the composed architect prompt (root %t)", isRoot),
				composedText(t, composer, claim.RoleArchitect, isRoot), nil, []string{retired})
		})
	}
}

// A step reads the reports of the steps before it in its task, and nothing of a step that comes
// after it: a planner's plan exists for a step only where a planner step stands before it, and a
// tester's handoff is read by the steps after the tester. A step after the reviewer can send the
// work back, so an approval does not end the change. Each part is read alone, and the composition
// of each step that builds from a plan is read whole, since Compose joins go/worker-common.md into
// it. This shows the text is loaded, never that a worker follows it.
func TestEachStepIsToldTheStepsAroundItInAnyOrder(t *testing.T) {
	planRead := []string{
		"where a planner step comes before yours, or your task has no `Task:` line, it is the issue's `plan.md`",
		"where no planner step comes before yours, it is your task's `Task text:`",
		"A task with no planner step before yours has no plan handoff for you",
	}
	planRetired := []string{"`Steps:` line names a planner,", "where the line names no planner", "`Steps:` line names no planner has no plan handoff"}
	for _, tc := range []struct {
		part       string
		want, gone []string
	}{
		{
			"tester.md",
			append(slices.Clone(planRead),
				"each step after yours reads what you wrote, and so does the retro",
				"Read the implementation handoff at the version that the `Step <j> (implementer) reported:` line of your task names"),
			append(slices.Clone(planRetired),
				"the reviewer where your task has a reviewer step",
				"Read the implementation handoff at the version your task's `Handoff:` line names"),
		},
		{"core/tester.md", []string{"so a defect comes back before the steps after yours run"}, []string{"so a defect comes back before the review"}},
		{
			"reviewer.md",
			append(slices.Clone(planRead), "After your approval, the head changes only when a later step of your task sends the work back"),
			append(slices.Clone(planRetired), "After approval, no implementation or further review change may happen"),
		},
		{
			"implementer.md",
			append(slices.Clone(planRead),
				"so the steps after yours, the tester's among them, run on that head",
				"the plan handoff's `requiredSkills` for your role, where a planner step comes before yours"),
			append(slices.Clone(planRetired), "so the tester runs that head"),
		},
		{
			"core/planner.md",
			[]string{"Plan the assigned issue completely for each step that follows yours", "the plan goes to the steps after yours with the choice unmade", "a gap the steps after yours inherit"},
			[]string{"completely for implementation, for each step", "the plan goes to the implementer", "a gap the implementation inherits"},
		},
	} {
		t.Run(tc.part, func(t *testing.T) {
			holdsAndRetires(t, "roles/"+tc.part, rolePart(t, tc.part), tc.want, tc.gone)
		})
	}
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, role := range []claim.Role{claim.RoleImplementer, claim.RoleTester, claim.RoleReviewer} {
		t.Run("composed "+string(role), func(t *testing.T) {
			holdsAndRetires(t, "the composed "+string(role)+" prompt", composedText(t, composer, role, false), nil,
				[]string{"`Steps:` line names a planner", "`Steps:` line names the planner", "`Steps:` line names no planner"})
		})
	}
}

// The owner's sign-off moves the task to its next step, and to the retro only where the acceptance
// is the task's last step (workflow's signOff); the retro starts once the task's last step passed,
// whichever step that is. Each daemon part is read alone, by its name. This shows the text is
// loaded, never that an agent follows it.
func TestSignOffMovesTheTaskToItsNextStepInTheGoParts(t *testing.T) {
	moves := "it moves the task to its next step, or to `retro` where your acceptance is its last step"
	for _, tc := range []struct {
		part       string
		want, gone []string
	}{
		{"architect-common.md", []string{"and moves the task to its next step, or to `retro` where your acceptance is its last step"}, []string{"and moves the issue to `retro`"}},
		{"architect.md", []string{moves}, []string{"it moves the issue to `retro`"}},
		{"architect-root.md", []string{moves}, []string{"it moves the issue to `retro`"}},
		{"implementer.md", []string{"once the task's last step passed, whichever step that is"}, []string{"the architect's acceptance where the task has the step `acceptance:owner`"}},
	} {
		t.Run(tc.part, func(t *testing.T) {
			holdsAndRetires(t, "go/"+tc.part, goPart(t, tc.part), tc.want, tc.gone)
		})
	}
}

// The architect's role parts say what its daemon parts say of a sign-off: it moves the task to its
// next step, and to the retro only where the acceptance is the task's last step, so the retro
// starts once the task's last step passes and the steps after the acceptance run without the
// owner. Each role part is read alone, and each architect's composition is read whole and folded,
// since the daemon parts join it. This shows the text is loaded, never that an architect follows
// it.
func TestSignOffMovesTheTaskToItsNextStepInTheRoleParts(t *testing.T) {
	moves := "which moves the task to its next step, or to retro where your acceptance is its last step"
	for _, tc := range []struct {
		part       string
		want, gone []string
	}{
		{
			"architect.md",
			[]string{
				moves,
				"the daemon starts the implementer on it once the task's last step passes",
				"the steps after your acceptance in its task, its retro and its merge then run without you",
			},
			[]string{"which moves the child to retro", "once you sign off"},
		},
		{"architect-root.md", []string{moves}, []string{"(which moves it to retro)"}},
	} {
		t.Run(tc.part, func(t *testing.T) {
			holdsAndRetires(t, "roles/"+tc.part, rolePart(t, tc.part), tc.want, tc.gone)
		})
	}
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, isRoot := range []bool{true, false} {
		t.Run(fmt.Sprintf("composed, root %t", isRoot), func(t *testing.T) {
			holdsAndRetires(t, fmt.Sprintf("the composed architect prompt (root %t)", isRoot),
				composedText(t, composer, claim.RoleArchitect, isRoot), nil,
				[]string{"and moves the issue to `retro`", "which moves the child to retro", "(which moves it to retro)"})
		})
	}
}

// `accepting`, `retro` and `merging` hold no slot and take a merge forward wherever the task places
// the acceptance, so no part says the review comes before them (record.PastReview reads the three
// phases, never the review's place). A parent is held before its retro wherever its last step is not
// the owner's acceptance, and a planner's plan goes to the steps after it, whichever they are. Each
// part is read alone, by its name. This shows the text is loaded, never that an agent follows it.
func TestNoPartPutsTheAcceptancePastTheReview(t *testing.T) {
	for _, tc := range []struct {
		part       string
		read       func(*testing.T) string
		want, gone []string
	}{
		{
			"go/architect-common.md", func(t *testing.T) string { return goPart(t, "architect-common.md") },
			[]string{
				"a stop in `accepting`, `retro` or `merging` holds none, and a stop in any other phase keeps the slot it holds",
				"a parent whose task passed its last step, which is not your `acceptance:owner` step",
				"the issue stands in `accepting`, `retro` or `merging` (`merge-next`)",
				"moves the issue on to its task's next step with no further notice",
			},
			[]string{"past its review", "Past review", "a stop before `accepting` keeps it", "no `acceptance:owner` step to wait in"},
		},
		{
			"go/worker-common.md", func(t *testing.T) string { return goPart(t, "worker-common.md") },
			[]string{"In `accepting`, `retro` or `merging`, where the task has a reviewer step, the reviewer is asked as well"},
			[]string{"Past review"},
		},
		{
			"roles/core/reviewer.md", func(t *testing.T) string { return rolePart(t, "core/reviewer.md") },
			[]string{"while the issue stands in `accepting`, `retro` or `merging`"},
			[]string{"past its review"},
		},
		{
			"roles/planner.md", func(t *testing.T) string { return rolePart(t, "planner.md") },
			[]string{"hands the choice to the steps after yours, unseen"},
			[]string{"hands the choice to whoever implements it"},
		},
	} {
		t.Run(tc.part, func(t *testing.T) {
			holdsAndRetires(t, tc.part, tc.read(t), tc.want, tc.gone)
		})
	}
}
