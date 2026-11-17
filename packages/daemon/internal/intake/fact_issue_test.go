package intake

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Each fact type of this package is decided one way or the other: a fact that names one issue,
// which IssueOf answers, or a fact that names none or several, which IssueOf answers false for.
// The types are read from the package's files as they stand when the test runs (every type with
// an isFact method), so a fact type added later fails here by name until it is put in one of the
// two lists.
func TestIssueOfAnswersEachFactThatNamesOneIssue(t *testing.T) {
	const issue = "LEGION-1"
	namesOne := map[string]Fact{
		"DispatchIssue":           DispatchIssue{Key: issue},
		"DispatchArtifact":        DispatchArtifact{Key: issue},
		"GateAboveDispatch":       GateAboveDispatch{Read: DispatchArtifact{Key: issue}},
		"DispatchAskOpened":       DispatchAskOpened{Key: issue},
		"DispatchAskRestored":     DispatchAskRestored{Key: issue},
		"DispatchAskClosed":       DispatchAskClosed{Key: issue},
		"DispatchAskReply":        DispatchAskReply{Key: issue},
		"DispatchApprovalMoved":   DispatchApprovalMoved{Key: issue},
		"DispatchClaim":           DispatchClaim{Key: issue},
		"Handover":                Handover{Issue: issue},
		"MergeQueueReadDone":      MergeQueueReadDone{Issue: issue},
		"PullRequestMissing":      PullRequestMissing{Issue: issue},
		"HandoffComplete":         HandoffComplete{Issue: issue},
		"BackwardMove":            BackwardMove{Issue: issue},
		"RetryOrEscalate":         RetryOrEscalate{Issue: issue},
		"RestartPhase":            RestartPhase{Issue: issue},
		"SignOff":                 SignOff{Issue: issue},
		"GateRegistered":          GateRegistered{Issue: issue},
		"CloseRoot":               CloseRoot{Issue: issue},
		"ClaimFailed":             ClaimFailed{Issue: issue},
		"SessionLost":             SessionLost{Issue: issue},
		"SessionStoreFault":       SessionStoreFault{Issue: issue},
		"MemoryGuardKill":         MemoryGuardKill{Issue: issue},
		"ClaimReady":              ClaimReady{Issue: issue},
		"TaskConfirmed":           TaskConfirmed{Issue: issue},
		"GateRequestFound":        GateRequestFound{Issue: issue},
		"CatchUpApplied":          CatchUpApplied{Issue: issue},
		"LingerExpired":           LingerExpired{Issue: issue},
		"CheckReport":             CheckReport{Issue: issue},
		"CheckWakeDue":            CheckWakeDue{Issue: issue},
		"CheckLookEnded":          CheckLookEnded{Issue: issue},
		"CheckBound":              CheckBound{Issue: issue},
		"PodBackstopDue":          PodBackstopDue{Issue: issue},
		"WorkerSilent":            WorkerSilent{Issue: issue},
		"WorkerBackgroundStalled": WorkerBackgroundStalled{Issue: issue},
	}
	namesNone := map[string]Fact{
		"ChildAdoption":            ChildAdoption{Tree: issue, Children: []AdoptedChild{{Issue: issue}}},
		"RepoMappingChanged":       RepoMappingChanged{},
		"DispatchConsumerPosition": DispatchConsumerPosition{},
		"ControllerTick":           ControllerTick{},
		"EntrySweep":               EntrySweep{},
		"PullRequestOpened":        PullRequestOpened{Branch: "legion/" + issue},
		"PullRequestSynchronized":  PullRequestSynchronized{Branch: "legion/" + issue},
		"PullRequestReview":        PullRequestReview{},
		"PullRequestChecks":        PullRequestChecks{},
		"PullRequestMergeability":  PullRequestMergeability{},
		"PullRequestMerged":        PullRequestMerged{},
		"PullRequestClosed":        PullRequestClosed{},
		"PullRequestDequeued":      PullRequestDequeued{},
		"Push":                     Push{},
		"BasePush":                 BasePush{},
		"DaemonBooted":             DaemonBooted{},
		"CutoverMergerRepair":      CutoverMergerRepair{},
		"ListenerWake":             ListenerWake{},
	}

	types := factTypes(t)
	for _, name := range types {
		_, one := namesOne[name]
		_, none := namesNone[name]
		switch {
		case one && none:
			t.Errorf("fact type %s is in both lists: it names one issue, or it names none", name)
		case !one && !none:
			t.Errorf("fact type %s is in neither list: decide whether it names one issue (IssueOf answers it) or none, and put it in that list", name)
		}
	}
	for listed, list := range map[string]map[string]Fact{"names one issue": namesOne, "names none": namesNone} {
		for name, fact := range list {
			if !slices.Contains(types, name) {
				t.Errorf("the list %q holds %s, which is no fact type of this package", listed, name)
			}
			if got := reflect.TypeOf(fact).Name(); got != name {
				t.Errorf("the list %q holds a %s under the name %s", listed, got, name)
			}
		}
	}
	for name, fact := range namesOne {
		if got, ok := IssueOf(fact); got != issue || !ok {
			t.Errorf("IssueOf(%s naming %s) = %q, %v; want %q, true", name, issue, got, ok, issue)
		}
	}
	for name, fact := range namesNone {
		if got, ok := IssueOf(fact); got != "" || ok {
			t.Errorf("IssueOf(%s) = %q, %v; want \"\", false: it names no one issue", name, got, ok)
		}
	}
}

// factTypes is the name of every type of this package with an isFact method, read from the
// package's files other than its tests. A read that finds none fails the test: it read nothing.
func factTypes(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			method, ok := decl.(*ast.FuncDecl)
			if !ok || method.Recv == nil || method.Name.Name != "isFact" {
				continue
			}
			receiver := method.Recv.List[0].Type
			if star, ok := receiver.(*ast.StarExpr); ok {
				receiver = star.X
			}
			if ident, ok := receiver.(*ast.Ident); ok {
				types = append(types, ident.Name)
			}
		}
	}
	if len(types) == 0 {
		t.Fatal("no type with an isFact method was read from this package's files: the test read nothing")
	}
	return types
}
