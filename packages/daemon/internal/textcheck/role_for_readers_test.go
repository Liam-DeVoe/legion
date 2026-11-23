package textcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The list of every reader of phase.RoleFor stands here, and not beside the table of what RoleFor
// answers (internal/workflow's role_for_test.go, TestRoleForAnswersEveryPhaseOfPhaseAll), because
// the check reads every Go file of packages/daemon, test files included, for the test names the
// list points at. A test of internal/workflow that read them would put every test file of the
// module on the filter of the daemon's Go job, this package's and the live tests' among them, so
// an edit to one of those would start that job's whole suite. This package's job already reads
// that set for TestEveryCreatedDatabaseIsAFileCopy, and the check needs nothing a product package
// compiles: it parses source.

// roleForSite is one function of the daemon that reads phase.RoleFor, or one of its forwarders
// (readsRoleFor): the file it is in, relative to the module root, and its name, a method's
// prefixed with its receiver type.
type roleForSite struct{ file, fn string }

// roleForReader is what the reader list pins for one site: how many times the function reads
// RoleFor, and the behaviour tests that hold what the site does with the answer today. The first
// holds its answer for a phase the architect works; where a task of the deployment's roles added
// the read or moved it onto a forwarder, the failing test of that task follows, which holds its
// answer at step, where the forwarder names the role of the claim that works the running step:
// the role the plan gives it, or the architect where its key is owner.
type roleForReader struct {
	calls int
	tests []string
}

// roleForReaders is every non-test reader of phase.RoleFor in the daemon, read directly or through
// one of its forwarders (internal/workflow's RoleFor, record.Issue.RoleAt and Walk.RoleAt), and the
// tests that hold each one's behaviour. A reader is a call, or the function passed as a value to a
// caller that calls it. Five of them have a guard of their own for admitted, the phase RoleFor
// answers the architect for: Walk.endingRole reads the role whose step a move ends, which
// noticeFor leaves out of the notice of the move out of admitted; Engine.leftItsPhase records
// no worker's completion while the issue stands at admitted; the completion's entry fence
// (intake.EntryFence, which the completion route and the workflow's handoff both ask) answers a
// completion at admitted as one of a phase its role does not work; the stalled-tree
// notice's reason tells an issue at admitted as standing in a phase no role works, the words it
// wrote before RoleFor named the architect there; and record.Issue.OwnersPlace, the rule of the
// owner's own place, answers false at admitted, where the owner does no step. Every other site
// reaches its answer through a guard that starts, resumes or reports no architect as a phase
// worker (OpenRoleOf, the worker of
// an open phase, answers none for the architect's phases, so the paths that read it are no readers
// here), or names the role an admitted root's entry numbers are of, or names the architect in the
// notice of a task its owner stopped in the architect's phase, where the stop ends no process
// (Engine.taskStopped, at admitted as at accepting), or answers the issue's own phase for the
// architect's completion there, as its reader took the issue's phase while RoleFor named no role
// (phase.Of, and the completion route's answerCompletion, which reads the phase LeftBy answers
// where the role does not work the issue's, and reads the role that works the retro only while
// the issue stands at the retro), or reads only the places of a plan's steps, which
// admitted never is (reportsBefore, Walk.RoleAt, completionOf, which reads the role of a place
// only while the task runs there and so never at admitted, whose task is at no act,
// Engine.startOwnersStep, which reads the place of a step or a retro the owner does, and
// Engine.restartTask, which reads its task's first step, a place no plan runs as admitted), or
// reads the retro's own phase (retroHeldNotice and Engine.startHeldRetro, which run only for a
// parent held before its retro for the issues beneath it), or answers RoleFor's role for every
// phase but step and its place's, which admitted never is, to the readers this list holds
// (record.Issue.RoleAt), or reads the phase a held issue's retry restores only past the refusal of
// a plan's own hold, which is the one hold an issue at admitted takes (Engine.retryOrEscalate, for
// the retry of a named claim), or settles nothing at admitted whatever it reads, since it settles
// a round only in reviewing (Engine.settleRound, which reads whether the owner works the review
// step), or never reads admitted at all because a check before it refuses
// that phase, which is what each named test holds it to. The owner walk reads no role: where it
// starts is its caller's to say (record.NoticeStartsAtParent, for a notice).
//
// A reader this list does not hold fails the test, so a new one is decided rather than taking
// whatever RoleFor answers at admitted.
var roleForReaders = map[roleForSite]roleForReader{
	{file: "cmd/legion/main.go", fn: "entriesBehind"}: {calls: 1, tests: []string{"TestStatePlainNamesTheRolesBehindTheirEntryAndTheRootsTheStateNamesAnArmFor"}},
	{file: "internal/admit/admit.go", fn: "Admission.startMidPhaseMembers"}: {calls: 1, tests: []string{
		"TestReadmissionStartsTheTreesMidPhaseChildren", "TestTheAdmissionReadsTheRunningDeploymentRole"}},
	{file: "internal/admit/stalled.go", fn: "stalledTreeReason"}: {calls: 1, tests: []string{
		"TestAWaitByDesignIsNeverToldAsAStall", "TestTheAdmissionReadsTheRunningDeploymentRole"}},
	{file: "internal/api/handoff.go", fn: "server.answerCompletion"}: {calls: 2, tests: []string{
		"TestArchitectUnchangedByItsCompletionAtAdmitted", "TestADeploymentRoleReportsWithAVerdictAndACommit",
		"TestTheRetroOfARoleOfTheDeploymentIsReported"}},
	{file: "internal/api/operator.go", fn: "server.restartWorkedPhase"}: {calls: 1, tests: []string{
		"TestAnOperatorRetryOfAStoppedArchitectRelaunchesItThroughItsMachine", "TestTheOperatorRetriesADeploymentRolesClaim"}},
	{file: "internal/intake/facts.go", fn: "EntryFence"}: {calls: 1, tests: []string{
		"TestArchitectUnchangedByItsCompletionAtAdmitted", "TestADeploymentRoleReportsWithAVerdictAndACommit",
		"TestACompletionOfALeftPhaseOfAnEntryRaisedSinceIsRefused"}},
	{file: "internal/phase/phase.go", fn: "Of"}: {calls: 1, tests: []string{"TestArchitectUnchangedByItsCompletionAtAdmitted"}},
	{file: "internal/projection/project.go", fn: "Project"}: {calls: 1, tests: []string{
		"TestProjectShowsARootsArchitectHandshake", "TestTheServedStateNamesTheRunningDeploymentRole"}},
	{file: "internal/record/record.go", fn: "Issue.RoleAt"}: {calls: 1, tests: []string{"TestAnIssueReadsTheRoleOfItsRunningStep"}},
	{file: "internal/record/record.go", fn: "Issue.OwnersPlace"}: {calls: 1, tests: []string{
		"TestThePlaceOfAnIssueIsTheOwnersWhereTheArchitectWorksIt", "TestASignOffEndsOnlyTheOwnersOwnAcceptance"}},
	{file: "internal/workflow/architect_start.go", fn: "writeCarriedPhase"}: {calls: 1, tests: []string{
		"TestTheArchitectTaskOfEachShape", "TestTheWorkflowsOtherReadersNameTheRunningDeploymentRole"}},
	{file: "internal/workflow/effects.go", fn: "HeldNotice"}: {calls: 1, tests: []string{
		"TestACarriedRootResumesFromTheApprovedHeadAndTheHold", "TestAHeldStepOfTheDeploymentNamesItsRole"}},
	{file: "internal/workflow/effects.go", fn: "RoleFor"}: {calls: 1, tests: []string{"TestRoleForAnswersEveryPhaseOfPhaseAll"}},
	// The role of the retro's row where the owner does the retro, read at a place of the owner's.
	{file: "internal/workflow/effects.go", fn: "Engine.startOwnersStep"}: {calls: 1, tests: []string{
		"TestAnOwnerStepIsToldToTheOwnerFromEachStartPath", "TestATreeAdmittedAgainTellsTheOwnerItsStep"}},
	{file: "internal/workflow/engine.go", fn: "Engine.claimFailed"}: {calls: 1, tests: []string{
		"TestAnArchitectsClaimFailedHoldsNoIssueAndWritesNoHeldNotice", "TestAHeldStepOfTheDeploymentNamesItsRole"}},
	{file: "internal/workflow/engine.go", fn: "Engine.leftItsPhase"}: {calls: 1, tests: []string{
		"TestArchitectUnchangedByAWorkersCompletionAtAdmitted", "TestALateCompletionBeforeADeploymentStepIsTakenAndMovesNothing"}},
	{file: "internal/workflow/engine.go", fn: "Engine.restartPhase"}: {calls: 1, tests: []string{
		"TestTheOperatorsRestartOfAPhaseStartsItsWorkerWithThePhasesTask", "TestARetryOfADeploymentStepRestartsItsRole"}},
	{file: "internal/workflow/engine.go", fn: "Engine.retryPhase"}: {calls: 1, tests: []string{
		"TestTheOperatorsRestartOfAPhaseStartsItsWorkerWithThePhasesTask", "TestARetryOfADeploymentStepRestartsItsRole"}},
	// The role of the held phase a retry naming a claim restarts, read past the refusal of a
	// plan's own hold.
	{file: "internal/workflow/engine.go", fn: "Engine.retryOrEscalate"}: {calls: 1, tests: []string{
		"TestARestartNamingAClaimRestartsNoStepAnotherClaimWorks"}},
	// The role the move starts, read by the phase on an issue with no plan rows and by the place on
	// a task with them.
	{file: "internal/workflow/engine.go", fn: "Engine.transition"}: {calls: 2, tests: []string{
		"TestAnApprovalMovesTheIssueIntoAcceptingStartingNobody", "TestAnIssueAtAdmittedRunsNoStep",
		"TestADeploymentRoleOfAnyTypeStartsAndReports"}},
	// The requester's role, twice, and the roles of the two earlier places that run as step.
	{file: "internal/workflow/engine.go", fn: "backwardRefusal"}: {calls: 4, tests: []string{
		"TestArchitectUnchangedByABackwardMoveFromAdmitted", "TestABackwardMoveToStepNamesWhichStep", "TestADeploymentRoleMovesItsTaskBack"}},
	// The role that works the place a running task stands at, which a current completion names.
	{file: "internal/workflow/engine.go", fn: "completionOf"}: {calls: 1, tests: []string{
		"TestArchitectUnchangedByAWorkersCompletionAtAdmitted", "TestTheRetroRunsAsTheRoleOfItsAgent",
		"TestALateReportIsTakenAfterAnEventAndRefusedAfterAStop"}},
	{file: "internal/workflow/gate.go", fn: "Engine.tellGateMoved"}: {calls: 1, tests: []string{
		"TestEveryPhaseHolderAndTheRootArchitectAreToldTheGateClosedAndOpened", "TestTheWorkflowsOtherReadersNameTheRunningDeploymentRole"}},
	// The merge's role and the role a step's role runs as; the retro's is its agent's
	// (plan.RetroAgent), which reads no RoleFor.
	{file: "internal/workflow/line.go", fn: "Walk.RoleAt"}: {calls: 2, tests: []string{"TestADeploymentRoleOfAnyTypeStartsAndReports", "TestTheRetroRunsAsTheRoleOfItsAgent"}},
	// The role whose step a move ends (Engine.transition, Engine.arrive, Engine.holdBeforeRetro and
	// endOfTransition read it), read by the place on a task with plan rows and by the phase on an
	// issue with none.
	{file: "internal/workflow/line.go", fn: "Walk.endingRole"}: {calls: 2, tests: []string{
		"TestThePhaseFinishedNoticeOutOfAdmittedCarriesNoRole", "TestAHeldStepOfTheDeploymentNamesItsRole"}},
	{file: "internal/workflow/merge_forward.go", fn: "Engine.mergeForward"}: {calls: 1, tests: []string{
		"TestAConflictKeepsPhaseAndAsksTheImplementerToMergeForward", "TestAMergeForwardAtAStepOfADeploymentRoleTellsThatRole"}},
	{file: "internal/workflow/merge_forward.go", fn: "Engine.mergeForwardLanded"}: {calls: 1, tests: []string{
		"TestAConflictKeepsPhaseAndAsksTheReviewerToReadTheMerge", "TestAMergeForwardAtAStepOfADeploymentRoleTellsThatRole"}},
	{file: "internal/workflow/plan_stop.go", fn: "Engine.restartTask"}: {calls: 1, tests: []string{
		"TestATodoOnAStoppedRootStartsItsTaskAtStepOne", "TestARetryOfADeploymentStepRestartsItsRole"}},
	{file: "internal/workflow/plan_stop.go", fn: "Engine.taskStopped"}: {calls: 1, tests: []string{
		"TestStopTaskHoldsTheIssueAndStopsTheStep", "TestAStopReachesTheDeploymentRoleThatRuns"}},
	{file: "internal/workflow/plan_stop.go", fn: "StoppedStepRole"}: {calls: 1, tests: []string{
		"TestStopTaskHoldsTheIssueAndStopsTheStep", "TestAStopReachesTheDeploymentRoleThatRuns"}},
	{file: "internal/workflow/retro_hold.go", fn: "Engine.startHeldRetro"}: {calls: 1, tests: []string{
		"TestAParentWithNoAcceptanceIsHeldBeforeItsRetro", "TestAHeldRetroAfterADeploymentStepGetsItsReport"}},
	{file: "internal/workflow/retro_hold.go", fn: "retroHeldNotice"}: {calls: 1, tests: []string{"TestAParentWithNoAcceptanceIsHeldBeforeItsRetro"}},
	// Whether the owner works the review step, where no round is settled; outside reviewing no
	// round is settled either (reviewRound), so its architect at admitted changes nothing.
	{file: "internal/workflow/review.go", fn: "Engine.settleRound"}: {calls: 1, tests: []string{
		"TestAnOwnersReviewStepEndsByItsReport"}},
	{file: "internal/workflow/review.go", fn: "Engine.tellChecks"}: {calls: 1, tests: []string{
		"TestASettlementMovesNothingAndTellsTheThreeRoles", "TestTheWorkflowsOtherReadersNameTheRunningDeploymentRole"}},
	{file: "internal/workflow/standing.go", fn: "reportsBefore"}: {calls: 1, tests: []string{"TestATaskNamesItsWorkingCopyItsStepAndTheReportsBeforeIt"}},
	{file: "internal/workflow/steps.go", fn: "Engine.startAgent"}: {calls: 2, tests: []string{
		"TestAnIssueAtAdmittedRunsNoStep", "TestTheStartOfADeploymentStepNamesItsAgent", "TestAnAcceptanceStartNamesTheAgentThatDoesIt"}},
	{file: "internal/workflow/steps.go", fn: "Engine.upgradedStart"}: {calls: 2, tests: []string{
		"TestAnIssueAtAdmittedRunsNoStep", "TestATaskMovesToItsRowsPlaceAndCountsItsReturn"}},
	{file: "internal/workflow/stranded.go", fn: "Stranded"}: {calls: 1, tests: []string{
		"TestStrandedNamesTheClaimsNothingWillWake", "TestTheResumePathsGiveTheRunningDeploymentRoleItsWork"}},
	// The worker of an open phase: a ready worker's task (readyWorkerTask), the boot's resume
	// (suspendedPhaseOwners), a release's backstop and the silence repair read it.
	{file: "internal/workflow/wake.go", fn: "OpenRoleOf"}: {calls: 1, tests: []string{
		"TestAnArchitectGetsNoBackstop", "TestArchitectUnchangedByAReadyWorkerOfAnIssueAtAdmitted",
		"TestTheBootsResumeReadsRoleForAndStartsNoArchitectAtAdmittedOrAccepting", "TestTheResumePathsGiveTheRunningDeploymentRoleItsWork"}},
}

// R2: the reader list is complete and nothing reads RoleFor that this test does not hold. The
// daemon's packages are parsed from the module root, which is this package's own directory with
// the two last elements removed; test files are left out of the reader list and read for the
// test names the list points at.
func TestEveryReaderOfRoleForLeavesTheArchitectUnchanged(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[roleForSite]int{}
	declared := map[string]bool{}
	fset := token.NewFileSet()
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
					declared[fn.Name.Name] = true
				}
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			site := roleForSite{file: filepath.ToSlash(relative), fn: "(package level)"}
			node := ast.Node(decl)
			if fn, ok := decl.(*ast.FuncDecl); ok {
				if fn.Body == nil {
					continue
				}
				site.fn, node = functionName(fn), fn.Body
			}
			ast.Inspect(node, func(node ast.Node) bool {
				if readsRoleFor(node) {
					found[site]++
				}
				return true
			})
		}
		return nil
	}); err != nil {
		t.Fatalf("read the daemon's packages: %v", err)
	}

	for _, site := range sortedSites(roleForReaders) {
		pinned := roleForReaders[site]
		calls, reads := found[site]
		switch {
		case !reads:
			t.Errorf("%s's %s reads RoleFor in this test's table and not in the source: remove the entry, or name the function that reads it now", site.file, site.fn)
		case calls != pinned.calls:
			t.Errorf("%s's %s calls RoleFor %d times, want the %d this test's table pins: decide what each new call answers for a phase the architect works", site.file, site.fn, calls, pinned.calls)
		}
		if len(pinned.tests) == 0 {
			t.Errorf("%s's %s names no behaviour test: name the test that holds what it answers for admitted", site.file, site.fn)
		}
		for _, test := range pinned.tests {
			if !declared[test] {
				t.Errorf("%s's %s names the behaviour test %s, which no test file of the daemon declares", site.file, site.fn, test)
			}
		}
	}
	for _, site := range sortedSites(found) {
		if _, pinned := roleForReaders[site]; !pinned {
			t.Errorf("%s's %s reads RoleFor and is in no entry of this test's table: decide what it must answer for admitted, which the architect works, and write the site and the test that holds it here",
				site.file, site.fn)
		}
	}
}

// functionName is fn's name, a method's prefixed with its receiver type.
func functionName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	receiver := fn.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	if named, ok := receiver.(*ast.Ident); ok {
		return named.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// readsRoleFor is whether node reads phase.RoleFor or one of its forwarders: internal/workflow's
// RoleFor, a selector naming either through its package, in a call or passed as a value, or a call
// of the bare name inside the package that declares it; and record.Issue.RoleAt and
// internal/workflow's Walk.RoleAt, each of which can answer phase.RoleFor, and so the architect
// for admitted and accepting: the issue's at every phase but step that the role of its place
// (record.Issue.PlaceRole) does not answer, and the walk's at a step of one of Legion's roles but
// the fresh agent of acceptance's, and at the merge; a selector of RoleAt on any value. A bare
// RoleFor that is no call is a field key and not a read, and appauth.AppRoleFor is another
// function and is not one of these.
func readsRoleFor(node ast.Node) bool {
	switch read := node.(type) {
	case *ast.CallExpr:
		name, ok := read.Fun.(*ast.Ident)
		return ok && name.Name == "RoleFor"
	case *ast.SelectorExpr:
		if read.Sel.Name == "RoleAt" {
			return true
		}
		pkg, ok := read.X.(*ast.Ident)
		return ok && (pkg.Name == "phase" || pkg.Name == "workflow") && read.Sel.Name == "RoleFor"
	}
	return false
}

// sortedSites is every key of sites, by file and then by function, so a failure lists the sites
// in one order whatever order the map walk gives.
func sortedSites[V any](sites map[roleForSite]V) []roleForSite {
	keys := make([]roleForSite, 0, len(sites))
	for site := range sites {
		keys = append(keys, site)
	}
	slices.SortFunc(keys, func(a, b roleForSite) int {
		if a.file != b.file {
			return strings.Compare(a.file, b.file)
		}
		return strings.Compare(a.fn, b.fn)
	})
	return keys
}
