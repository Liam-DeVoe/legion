package requests

import (
	"errors"
	"testing"
)

// TestAJudgesApprovalWhoseCommitAnswerWasLostLeavesOneRow is round 5's audit duplicate on
// cybermonklabs/legion#4135. The judge's approval commits the grant and its own request.judged row
// together (D11). When the COMMIT applied and only its answer was lost, ApplyJudgeDecision returns
// an error, and the judge then writes the verdict's row on its own, saying the approval was not
// applied (LogJudged with applied false). Before it writes such a row, LogJudged reads whether the
// approval's own row is there: when it is, it writes nothing more, so the trail holds the one row
// that says what happened, not two that disagree. When the read fails, the row is written with
// applied "unknown", never false. The cut is testCommitCut, run after the transaction committed.
func TestAJudgesApprovalWhoseCommitAnswerWasLostLeavesOneRow(t *testing.T) {
	const reason = "INFRA-12: rotate the deploy key"
	lost := errors.New("unexpected EOF: the COMMIT's answer was lost")
	failureRow := func(err error) map[string]any {
		return map[string]any{"verdict": "approve", "applied": false, "not_applied": err.Error()}
	}

	t.Run("the commit applied and its answer was lost", func(t *testing.T) {
		m, enr, key := newJudgeFixture(t)
		req := pendingJudgeable(t, m, enr, key, reason, 600, "DEEL_API_KEY")
		m.testCommitCut = func() error { return lost }
		_, err := m.ApplyJudgeDecision(t.Context(), *req.RecordID, judgeModel, map[string]any{"verdict": "approve"})
		m.testCommitCut = nil
		if !errors.Is(err, lost) {
			t.Fatalf("ApplyJudgeDecision with its COMMIT's answer lost = %v; want the lost answer's error", err)
		}
		if got := readDecided(t, m, *req.RecordID); got.state != "granted" {
			t.Fatalf("the request after the cut = %+v; want granted: the COMMIT applied", got)
		}
		wrote, err := m.LogJudged(t.Context(), *req.RecordID, judgeModel, failureRow(err))
		if err != nil || wrote != JudgedAlreadyApplied {
			t.Fatalf("LogJudged(applied false) after an approval that applied = %q, %v; want %q and nothing written", wrote, err, JudgedAlreadyApplied)
		}
		if judged := readJudged(t, m, *req.RecordID); len(judged) != 1 || judged[0].applied != "true" {
			t.Errorf("request.judged rows = %+v; want the approval's own row alone, applied true", judged)
		}
	})

	t.Run("no approval applied", func(t *testing.T) {
		m, enr, key := newJudgeFixture(t)
		req := pendingJudgeable(t, m, enr, key, reason, 600, "DEEL_API_KEY")
		wrote, err := m.LogJudged(t.Context(), *req.RecordID, judgeModel, failureRow(lost))
		if err != nil || wrote != JudgedWritten {
			t.Fatalf("LogJudged(applied false) with no approval applied = %q, %v; want %q", wrote, err, JudgedWritten)
		}
		if judged := readJudged(t, m, *req.RecordID); len(judged) != 1 || judged[0].applied != "false" {
			t.Errorf("request.judged rows = %+v; want one, applied false, as before", judged)
		}
	})

	t.Run("whether the approval applied cannot be read", func(t *testing.T) {
		m, enr, key := newJudgeFixture(t)
		req := pendingJudgeable(t, m, enr, key, reason, 600, "DEEL_API_KEY")
		m.testLandedRead = func() error { return errors.New("the read of the approval's row failed") }
		wrote, err := m.LogJudged(t.Context(), *req.RecordID, judgeModel, failureRow(lost))
		if err != nil || wrote != JudgedAppliedUnknown {
			t.Fatalf("LogJudged(applied false) whose read failed = %q, %v; want %q", wrote, err, JudgedAppliedUnknown)
		}
		if judged := readJudged(t, m, *req.RecordID); len(judged) != 1 || judged[0].applied != AppliedUnknown {
			t.Errorf("request.judged rows = %+v; want one, applied %q, never false", judged, AppliedUnknown)
		}
	})
}
