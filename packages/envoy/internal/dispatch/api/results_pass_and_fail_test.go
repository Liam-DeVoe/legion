// results_pass_and_fail_test.go holds GET /api/v1/issues/{key}/results to R4 for a
// `before_merge` check: a pass and a fail that count at one commit make the state at that commit
// not known, in either order and from any role. A later pass there does not end that state, and a
// retraction of the wrong result does. The test reads the route's answer as JSON into the types of
// this file, so it builds at a commit before the rule and fails there on its own assertion.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// passAndFailHead is the head of the one open pull request of the tests' issue.
const passAndFailHead = "f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6"

// passAndFailCheck is one check of the results read and its pass_stands, with only the members
// this test reads.
type passAndFailCheck struct {
	BlockID    string `json:"block_id"`
	PassStands struct {
		Answer   string `json:"answer"`
		Reason   string `json:"reason"`
		Subjects []struct {
			Basis struct {
				Commit string `json:"commit"`
			} `json:"basis"`
			Answer  string   `json:"answer"`
			Reason  string   `json:"reason"`
			Results []string `json:"results"`
		} `json:"subjects"`
	} `json:"pass_stands"`
}

// passAndFailRig is a Dispatch whose issue has pull request #70 open at passAndFailHead, and the
// check k1, a `before_merge` check that no CI job proves.
type passAndFailRig struct {
	*ciJobRig
}

func newPassAndFailRig(t *testing.T) passAndFailRig {
	t.Helper()
	rig := newCIJobRig(t)
	writeHead(t, rig.database, ciJobRepository, 70, passAndFailHead, "open", rig.issue)
	return passAndFailRig{rig}
}

// record records role's verdict of k1 at the head, and answers the stored result's id.
func (r passAndFailRig) record(t *testing.T, role, verdict string) string {
	t.Helper()
	fields := map[string]any{"role": role}
	if verdict == "fail" {
		fields["failed"] = 1
	}
	response := recordResult(t, r.handler, r.issue, resultBody("k1", verdict, passAndFailHead, fields))
	if response.Code != http.StatusCreated {
		t.Fatalf("record %s's %s of k1: status=%d body=%s", role, verdict, response.Code, response.Body.String())
	}
	var recorded struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &recorded); err != nil || recorded.Result.ID == "" {
		t.Fatalf("decode the recorded %s: %v (body %s)", verdict, err, response.Body.String())
	}
	return recorded.Result.ID
}

// retract retracts result as its recorder, failing the test unless the route takes it.
func (r passAndFailRig) retract(t *testing.T, result string) {
	t.Helper()
	response := dispatchRequest(t, r.handler, http.MethodPost, "/api/v1/results/"+result+"/retract",
		map[string]any{"reason": "Recorded against the wrong build."}, "alice")
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("retract %s: status=%d body=%s", result, response.Code, response.Body.String())
	}
}

// read is k1 as the results read answers it, at commit at where at is not empty.
func (r passAndFailRig) read(t *testing.T, at string) passAndFailCheck {
	t.Helper()
	target := "/api/v1/issues/" + r.issue + "/results"
	if at != "" {
		target += "?at=" + at
	}
	response := dispatchRequest(t, r.handler, http.MethodGet, target, nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read the results of %s: status=%d body=%s", r.issue, response.Code, response.Body.String())
	}
	var read struct {
		Checks []passAndFailCheck `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil {
		t.Fatalf("decode the results of %s: %v", r.issue, err)
	}
	for _, check := range read.Checks {
		if check.BlockID == "k1" {
			return check
		}
	}
	t.Fatalf("the read of %s holds no check k1: %s", r.issue, response.Body.String())
	return passAndFailCheck{}
}

// head fails the test unless the head's subject reads answer for reason, with results where
// results is not nil, and unless the check's answer is answer too.
func (r passAndFailRig) head(t *testing.T, what, answer, reason string, results []string) {
	t.Helper()
	check := r.read(t, "")
	if len(check.PassStands.Subjects) != 1 || check.PassStands.Subjects[0].Basis.Commit != passAndFailHead {
		t.Fatalf("%s: k1 has the subjects %+v, want the head of #70 alone", what, check.PassStands.Subjects)
	}
	subject := check.PassStands.Subjects[0]
	if subject.Answer != answer || subject.Reason != reason || check.PassStands.Answer != answer {
		t.Fatalf("%s: the head reads %q (reason %q) and the check %q, want %q (reason %q) for both",
			what, subject.Answer, subject.Reason, check.PassStands.Answer, answer, reason)
	}
	if results != nil && !slices.Equal(subject.Results, results) {
		t.Fatalf("%s: the head's results are %v, want %v", what, subject.Results, results)
	}
}

// TestAPassAndAFailAtOneHeadReadNotKnown: R4. A pass and a fail of k1 at one head make that head
// not known, in either order and from any role; a later pass does not end it, and a read at the
// commit gives the same. A retraction of the fail leaves the pass, and of the pass the fail.
func TestAPassAndAFailAtOneHeadReadNotKnown(t *testing.T) {
	// The case production printed as a pass from tester and implementer: the tester's pass at one
	// commit, then its fail at the same commit under the same words.
	t.Run("a pass then a fail", func(t *testing.T) {
		rig := newPassAndFailRig(t)
		pass := rig.record(t, "tester", "pass")
		fail := rig.record(t, "tester", "fail")
		rig.head(t, "the tester's pass, then its fail", "unknown", "pass_and_fail", []string{pass, fail})
	})
	t.Run("a fail then a pass", func(t *testing.T) {
		rig := newPassAndFailRig(t)
		rig.record(t, "tester", "fail")
		rig.record(t, "tester", "pass")
		rig.head(t, "the tester's fail, then its pass", "unknown", "pass_and_fail", nil)
	})
	t.Run("a pass of the implementer and a fail of the tester", func(t *testing.T) {
		rig := newPassAndFailRig(t)
		rig.record(t, "implementer", "pass")
		rig.record(t, "tester", "fail")
		rig.head(t, "the implementer's pass and the tester's fail", "unknown", "pass_and_fail", nil)
	})
	t.Run("a later pass", func(t *testing.T) {
		rig := newPassAndFailRig(t)
		rig.record(t, "tester", "pass")
		fail := rig.record(t, "tester", "fail")
		later := rig.record(t, "tester", "pass")
		rig.head(t, "a pass after the pass and the fail", "unknown", "pass_and_fail", []string{later, fail})
	})
	t.Run("a read at the commit", func(t *testing.T) {
		rig := newPassAndFailRig(t)
		rig.record(t, "tester", "pass")
		rig.record(t, "tester", "fail")
		if at := rig.read(t, passAndFailHead); at.PassStands.Answer != "unknown" || at.PassStands.Reason != "pass_and_fail" {
			t.Fatalf("a read at the head answers %+v, want unknown for pass_and_fail", at.PassStands)
		}
	})
	t.Run("the fail retracted", func(t *testing.T) {
		rig := newPassAndFailRig(t)
		pass := rig.record(t, "tester", "pass")
		rig.retract(t, rig.record(t, "tester", "fail"))
		rig.head(t, "the pass, with the fail retracted", "yes", "", []string{pass})
	})
	t.Run("the pass retracted", func(t *testing.T) {
		rig := newPassAndFailRig(t)
		pass := rig.record(t, "tester", "pass")
		fail := rig.record(t, "tester", "fail")
		rig.retract(t, pass)
		rig.head(t, "the fail, with the pass retracted", "no", "fail", []string{fail})
	})
}

// TestAHeadNamesTheNewestResultsThatDecideIt: R10 and R9. A head that a pass and a fail make not
// known names the newest pass and the newest fail there, and a head that reads no for a fail names
// the newest fail there. Each case holds two results of each kind the head picks from, so a head
// that named an older one would name another result than the one each case wants.
func TestAHeadNamesTheNewestResultsThatDecideIt(t *testing.T) {
	t.Run("the newest pass and the newest fail", func(t *testing.T) {
		rig := newPassAndFailRig(t)
		rig.record(t, "tester", "pass")
		rig.record(t, "tester", "fail")
		pass := rig.record(t, "implementer", "pass")
		fail := rig.record(t, "implementer", "fail")
		rig.head(t, "two passes and two fails", "unknown", "pass_and_fail", []string{pass, fail})
	})
	t.Run("the newest of two fails", func(t *testing.T) {
		rig := newPassAndFailRig(t)
		rig.record(t, "tester", "fail")
		fail := rig.record(t, "implementer", "fail")
		rig.head(t, "two fails", "no", "fail", []string{fail})
	})
}

// The heads of TestTheCheckReasonIsTheSameWhateverTheOrderOfItsHeads: reasonOrderBoth holds a
// pass and a fail of k1, and reasonOrderOther the one result each case gives it.
const (
	reasonOrderBoth  = "f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7"
	reasonOrderOther = "f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8f8"
)

// learnedInOrder sets when Dispatch learned the head of each of numbers, pull requests of repo,
// so delivery.HeadsAttributedTo lists them in the order given: the first was learned last. The
// times are fixed ones, a minute apart, and no clock's.
func learnedInOrder(t *testing.T, database *store.Store, repo string, numbers ...int) {
	t.Helper()
	newest := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for index, number := range numbers {
		at := newest.Add(-time.Duration(index) * time.Minute)
		if _, err := database.Pool.Exec(context.Background(),
			`update pull_request_heads set event_at = $3, checked_at = $3 where repo = $1 and number = $2`,
			repo, number, at); err != nil {
			t.Fatalf("set when the head of %s#%d was learned: %v", repo, number, err)
		}
	}
}

// TestTheCheckReasonIsTheSameWhateverTheOrderOfItsHeads: k1's pass_stands names the reason
// pass_and_fail where its answer is unknown because a head holds a pass and a fail, and no reason
// where its answer is no, whichever of its two heads Dispatch learned last and so lists first. #70
// is open at a head holding a pass and a fail, and #71 at a head holding the one result of each
// case. Each case reads k1 with the heads in both orders, and first checks that the read lists
// them in the order it set.
func TestTheCheckReasonIsTheSameWhateverTheOrderOfItsHeads(t *testing.T) {
	for _, each := range []struct {
		name, verdict, answer, reason string
	}{
		{"beside a head that holds a fail", "fail", "no", ""},
		{"beside a head that holds a pass", "pass", "unknown", "pass_and_fail"},
	} {
		t.Run(each.name, func(t *testing.T) {
			for _, order := range [][2]int{{70, 71}, {71, 70}} {
				rig := passAndFailRig{newCIJobRig(t)}
				writeHead(t, rig.database, ciJobRepository, 70, reasonOrderBoth, "open", rig.issue)
				writeHead(t, rig.database, ciJobRepository, 71, reasonOrderOther, "open", rig.issue)
				learnedInOrder(t, rig.database, ciJobRepository, order[0], order[1])
				eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", reasonOrderBoth)
				eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "fail", reasonOrderBoth)
				eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", each.verdict, reasonOrderOther)
				stands := rig.read(t, "").PassStands
				first := map[int]string{70: reasonOrderBoth, 71: reasonOrderOther}[order[0]]
				if len(stands.Subjects) != 2 || stands.Subjects[0].Basis.Commit != first {
					t.Fatalf("with #%d learned last, k1's subjects are %+v, want the two open heads, #%d's first", order[0], stands.Subjects, order[0])
				}
				if stands.Answer != each.answer || stands.Reason != each.reason {
					t.Fatalf("with #%d learned last, k1 reads %q (reason %q), want %q (reason %q)",
						order[0], stands.Answer, stands.Reason, each.answer, each.reason)
				}
			}
		})
	}
}

// TestTheReasonOfAnUnknownCheckNamesAPassAndAFailAtAnyOfItsHeads: the gate's rule alone
// (passStandsOf), for the case a route read cannot set up at one head alone: a check CI proves
// whose read failed at one head (read_failed) and whose other head holds a pass and a fail. The
// check reads unknown for pass_and_fail with either head first.
func TestTheReasonOfAnUnknownCheckNamesAPassAndAFailAtAnyOfItsHeads(t *testing.T) {
	const unread, both = "e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1", "e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2"
	moved := false
	at := func(id, verdict string, minute int) model.CheckResult {
		return model.CheckResult{ID: id, Verdict: verdict, CommitSHA: both, Role: "ci",
			CommitStands: &model.ResultCommitStands{Answer: model.CommitAtBasis}, CheckWordsMoved: &moved,
			CreatedAt: time.Date(2026, 11, 19, 0, minute, 0, 0, time.UTC)}
	}
	check := model.CheckBlock{BlockID: "c1", When: docs.CheckWhenBeforeMerge,
		Results: []model.CheckResult{at("fail", "fail", 2), at("pass", "pass", 1)}}
	const repo = "acme/widgets"
	seventy, seventyOne := 70, 71
	unreadHead := headSubject{basis: model.CheckBasis{Kind: model.CheckBasisPullRequest, Commit: unread, PullRequest: &seventy}, repo: repo}
	bothHead := headSubject{basis: model.CheckBasis{Kind: model.CheckBasisPullRequest, Commit: both, PullRequest: &seventyOne}, repo: repo}
	ci := map[ciHeadKey]*model.CheckCI{{repo: repo, commit: unread}: {Run: "go-checks", State: model.CIStateUnread}}
	for _, order := range [][]headSubject{{unreadHead, bothHead}, {bothHead, unreadHead}} {
		stands := passStandsOf(check, comparison{bases: []model.CheckBasis{order[0].basis, order[1].basis}, subjects: order, ci: ci})
		if len(stands.Subjects) != 2 || stands.Subjects[0].Basis.Commit != order[0].basis.Commit || stands.Subjects[0].Reason == stands.Subjects[1].Reason {
			t.Fatalf("with the head at %s first, the subjects are %+v, want that head first of two, one read_failed and one pass_and_fail", order[0].basis.Commit, stands.Subjects)
		}
		if stands.Answer != model.PassStandsUnknown || stands.Reason != model.PassStandsReasonPassAndFail {
			t.Fatalf("with the head at %s first, c1 reads %q (reason %q), want unknown for pass_and_fail: the head at %s holds a pass and a fail",
				order[0].basis.Commit, stands.Answer, stands.Reason, both)
		}
	}
}
