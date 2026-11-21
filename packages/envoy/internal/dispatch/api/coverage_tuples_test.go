package api

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// coverageOf is the requirement of coverage whose block id is block.
func coverageOf(t *testing.T, coverage model.IssueCoverage, block string) model.RequirementCoverage {
	t.Helper()
	for _, requirement := range coverage.Requirements {
		if requirement.BlockID == block {
			return requirement
		}
	}
	t.Fatalf("the coverage of %s lists no requirement %s: %+v", coverage.Issue, block, coverage.Requirements)
	return model.RequirementCoverage{}
}

// TestTheCoverageReadOverTheTuplesHoldsTheFourStatesApart: the coverage read over the tuples reads
// a requirement no check serves as a gap; one whose check holds no result, one whose check's newest
// result is a fail and one whose check passed against words it no longer holds as unproven, each
// with where its check stands; one whose check passed against its words as proved, and unproven
// once that result is retracted; an overtaken requirement as closed, with no gap and no check
// under it; and a proposed one as raising no gap.
func TestTheCoverageReadOverTheTuplesHoldsTheFourStatesApart(t *testing.T) {
	handler, database, deps := newTestServer(t, testServerOptions{})
	ctx := context.Background()
	alice := model.Actor{Kind: "user", ID: "alice"}
	issue := createInteractionIssue(t, handler, "COVT", "Coverage states",
		":::requirement{#rg}\nNo check proves this.\n:::\n\n:::requirement{#ru1}\nA check with no result.\n:::\n\n"+
			":::requirement{#ru2}\nA check that failed.\n:::\n\n:::requirement{#ru3}\nA check reworded after its pass.\n:::\n\n"+
			":::requirement{#rp}\nA check that passed.\n:::\n\n:::requirement{#ro}\nOvertaken.\n:::\n\n:::requirement{#rq}\nProposed.\n:::\n")
	spec := "dispatch://" + issue.Key + "/spec#b-"
	checks := func(reworded string) string {
		return checkServing("cu1", spec+"ru1") + "\n" + checkServing("cu2", spec+"ru2") + "\n" +
			":::check{#cu3 serves=\"" + spec + "ru3\" surface=\"cli\" run=\"go test ./...\"}\n" + reworded + "\n:::\n\n" +
			checkServing("cp", spec+"rp") + "\n" + checkServing("co", spec+"ro")
	}
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", checks("The check that proves it."))
	document := readDecisionDocument(t, database, issue.Key, "spec")
	for block, state := range map[string]string{"ro": graphstore.StateOvertaken, "rq": graphstore.StateProposed} {
		succeeded(t, "set "+block+" "+state, dispatchRequest(t, handler, http.MethodPost,
			"/api/v1/issues/"+issue.Key+"/nodes/"+url.PathEscape(document.address(block))+"/state", map[string]any{"state": state}, "alice"))
	}
	record := func(check, verdict string, fields map[string]any) model.CheckResult {
		t.Helper()
		recorded := recordResult(t, handler, issue.Key, resultBody(check, verdict, headCommit, fields))
		if recorded.Code != http.StatusCreated {
			t.Fatalf("record a %s of %s: status=%d body=%s", verdict, check, recorded.Code, recorded.Body.String())
		}
		return decodeBody[recordedResult](t, recorded).Result
	}
	record("cu2", "fail", map[string]any{"failed": 1})
	record("cu3", "pass", nil)
	passed := record("cp", "pass", nil)
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", checks("The check that proves it, reworded."))

	read := func(what string) model.IssueCoverage {
		t.Helper()
		coverage, err := directServer(deps).issueCoverageOverTuples(ctx, database.Pool, alice, issue.Key, nil)
		if err != nil {
			t.Fatalf("%s: read the coverage of %s over the tuples: %v", what, issue.Key, err)
		}
		t.Logf("%s: %+v", what, coverage)
		return coverage
	}
	coverage := read("before the retraction")
	for _, want := range []struct {
		block, coverage, proves string
		gaps                    []string
	}{
		{"rg", "gap", "", []string{noCheckReason}},
		{"ru1", "unproven", provesNone, []string{}},
		{"ru2", "unproven", provesFail, []string{}},
		{"ru3", "unproven", provesReworded, []string{}},
		{"rp", "proved", provesPass, []string{}},
		{"ro", "closed", "", []string{}},
		{"rq", graphstore.StateProposed, "", []string{}},
	} {
		requirement := coverageOf(t, coverage, want.block)
		if requirement.Coverage != want.coverage || !slices.Equal(requirement.Gaps, want.gaps) {
			t.Errorf("%s reads %s with the gaps %q, want %s with %q", want.block, requirement.Coverage, requirement.Gaps, want.coverage, want.gaps)
		}
		switch {
		case want.proves == "" && len(requirement.Proof) > 0:
			t.Errorf("%s lists the checks %+v, want none", want.block, requirement.Proof)
		case want.proves != "" && (len(requirement.Proof) != 1 || requirement.Proof[0].Proves != want.proves || requirement.Proof[0].Issue != issue.Key):
			t.Errorf("%s lists the checks %+v, want its one check of %s at %s", want.block, requirement.Proof, issue.Key, want.proves)
		}
	}
	if response := retractAsPerson(t, handler, passed.ID, "The run named the wrong commit."); response.Code != http.StatusCreated {
		t.Fatalf("retract the pass of cp: status=%d body=%s", response.Code, response.Body.String())
	}
	if requirement := coverageOf(t, read("after the retraction"), "rp"); requirement.Coverage != "unproven" ||
		len(requirement.Proof) != 1 || requirement.Proof[0].Proves != provesNone {
		t.Errorf("after its one pass was retracted rp reads %s with %+v, want unproven, its check holding no result that stands", requirement.Coverage, requirement.Proof)
	}
}

// TestTheCoverageAndResultsTwinsAnswerAsTheReadsTheyReplace: the census of the twins reads no
// difference between the coverage read over the tuples and the coverage read over the spec's
// markdown, nor between the checks read over the tuples and the checks of the Verification
// document, on an issue the release before would have written - not converted, its documents
// each a room of its own - and on an issue born converted, each with requirements, checks
// serving one and two of them, a plan task and a recorded result. A serves tuple written into the
// store alone, which no document's markdown holds, is read by the twin and not by the read it
// replaces, and the census names that difference, so a census reading none is a census that reads.
func TestTheCoverageAndResultsTwinsAnswerAsTheReadsTheyReplace(t *testing.T) {
	handler, database, deps := newTestServer(t, testServerOptions{})
	ctx := context.Background()
	alice := model.Actor{Kind: "user", ID: "alice"}
	createReferenceAPIProject(t, handler, "TWIN")
	server := directServer(deps)
	seed := func(unconverted bool) string {
		t.Helper()
		issue := createDigestIssue(t, handler, database, "TWIN", "Twins", "A paragraph of prose.\n\n"+
			":::requirement{#r1}\nThe list comes first.\n:::\n\n:::requirement{#r2}\nAn ask keeps its urgency.\n:::\n", unconverted)
		spec := "dispatch://" + issue.Key + "/spec#b-"
		owner := "/api/v1/issues/" + issue.Key
		uploadDocument(t, handler, owner, "verification.md", checkServing("c1", spec+"r1")+"\n"+
			":::check{#c2 serves=\""+spec+"r1 "+spec+"r2\" surface=\"cli\" run=\"go test ./...\"}\nThe second check.\n:::\n")
		uploadDocument(t, handler, owner, "plan.md", planTaskServing("t1", spec+"r1"))
		if recorded := recordResult(t, handler, issue.Key, resultBody("c1", "pass", headCommit, nil)); recorded.Code != http.StatusCreated {
			t.Fatalf("record a pass of c1 on %s: status=%d body=%s", issue.Key, recorded.Code, recorded.Body.String())
		}
		return issue.Key
	}
	for _, unconverted := range []bool{true, false} {
		key := seed(unconverted)
		differences, err := server.twinCensus(ctx, database.Pool, alice, key)
		if err != nil {
			t.Fatalf("take the census of the twins of %s: %v", key, err)
		}
		t.Logf("the census of the twins of %s (unconverted %t) reads %d differences: %q", key, unconverted, len(differences), differences)
		if len(differences) > 0 {
			t.Errorf("the census of the twins of %s (unconverted %t) reads %q, want no difference", key, unconverted, differences)
		}
		if coverage, err := server.issueCoverageOverTuples(ctx, database.Pool, alice, key, nil); err != nil ||
			coverageOf(t, coverage, "r1").Checks != 2 || coverageOf(t, coverage, "r2").Checks != 1 {
			t.Errorf("the coverage of %s over the tuples is %+v, %v: want c2 listed once under each requirement it serves", key, coverage, err)
		}
	}

	// The planted difference: a serves tuple of c1 to r2 that only the store holds.
	key := seed(false)
	checks := readDecisionDocument(t, database, key, "verification-md")
	requirements := readDecisionDocument(t, database, key, "spec")
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := graphstore.Write(ctx, tx, nil, []graphstore.Tuple{{
		FromKind: refs.BlockNodeKind, FromID: checks.address("c1"), Kind: servesTupleKind,
		ToKind: refs.BlockNodeKind, ToID: requirements.address("r2"),
		At: time.Now().UTC(), Who: alice, SourceEvent: graphstore.NoEvent, Asserted: true,
	}}); err != nil {
		t.Fatalf("write the planted serves tuple: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the planted serves tuple: %v", err)
	}
	differences, err := server.twinCensus(ctx, database.Pool, alice, key)
	if err != nil {
		t.Fatalf("take the census of the twins of %s: %v", key, err)
	}
	t.Logf("with the planted serves tuple the census of %s reads %q", key, differences)
	if len(differences) != 1 || !strings.Contains(differences[0], "r2") || !strings.Contains(differences[0], "checks=2") {
		t.Errorf("with a serves tuple only the store holds the census of %s reads %q, want the one difference naming r2 with two checks", key, differences)
	}
}
