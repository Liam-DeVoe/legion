package requests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/record"
)

// wantListed fails t unless exactly the logins in want, of every login in all, list recordID.
func wantListed(t *testing.T, m *Machine, recordID string, all, want []string) {
	t.Helper()
	for _, login := range all {
		if _, got := listedRow(t, m, login, PendingStates{}, recordID); got != slices.Contains(want, login) {
			t.Errorf("%s's list holds record %s: %v; want %v", login, recordID, got, slices.Contains(want, login))
		}
	}
}

// TestOtherPeoplesRowsCostTheListOneBound: a reader with no request of their own reads a list of
// many people's requests, each naming its own operator as responsible and touching the public
// TEAM, while Dispatch takes 300 ms to answer each question (slowAccess). Before the fallback
// none of these rows was on the reader's list, and none asked Dispatch anything. Each now asks
// whether its own person may see it, but all of them share listFallbackBound: once it is spent,
// the rest are left off without a question, so the list costs at most the bound's worth of
// questions, not two per row. It counts the questions Dispatch was asked, not time.
func TestOtherPeoplesRowsCostTheListOneBound(t *testing.T) {
	const rows = 20
	const takes = 300 * time.Millisecond
	m, _, _, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
	for i := range rows {
		operator := fmt.Sprintf("op%02d@example.com", i)
		enr, key := newEnrollment(t, m.Store, "session", fmt.Sprintf("box-slow-%02d", i), new(operator), nil)
		if req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "TEAM-7: rotate the deploy key", "SHARED_KEY"), "", nil); err != nil || req.State != "pending" {
			t.Fatalf("Create = %+v, %v; want it pending", req, err)
		}
	}
	answerFrom(t, m, dispatchRows(publicTeam))
	var asked atomic.Int32
	m.Projects = slowAccess{inner: m.Projects, takes: takes, asked: &asked}
	listed, err := m.PendingForApprover(t.Context(), insider, PendingStates{})
	if err != nil || len(listed) != 0 {
		t.Fatalf("PendingForApprover(%s) = %d rows, %v; want none: every row is another person's, who may see it", insider, len(listed), err)
	}
	t.Logf("the list asked Dispatch %d questions for %d other people's rows", asked.Load(), rows)
	// Without the clock the list asks two questions per row (40 here, 12 s). With it, 300 ms
	// questions start at 0, 300, ..., 1800 ms inside 2 s: 7, the last cut short. A slow box only
	// lowers the count, since every question takes at least its 300 ms. The test does not read
	// listFallbackBound, so a clock taken away, or one as long as Dispatch's 5 s wait, fails it.
	const most = 7
	if asked.Load() > most {
		t.Fatalf("the list asked Dispatch %d questions for %d other people's rows; want at most %d", asked.Load(), rows, most)
	}
}

// failForPerson answers every access question about one person with an error, as a Dispatch that
// cannot answer for that person does, and passes every other question on.
type failForPerson struct {
	inner  projectaccess.Access
	person string
}

func (f failForPerson) Access(ctx context.Context, project, person string) (projectaccess.Answer, error) {
	if record.CanonicalLogin(person) == f.person {
		return projectaccess.Answer{}, errors.New("dispatch did not answer for this person")
	}
	return f.inner.Access(ctx, project, person)
}

func (f failForPerson) MemberKind(ctx context.Context, person string) (string, error) {
	return f.inner.MemberKind(ctx, person)
}

func (f failForPerson) States(ctx context.Context, projects []string) (map[string]projectaccess.Answer, error) {
	return f.inner.States(ctx, projects)
}

// strandedRow is the fixture operator's request naming TEAM-7, made while TEAM is public; TEAM is
// then private with insider alone on its list, so the operator, its responsible person, may no
// longer see it, and the fallback lists it for insider (listedInStead).
func strandedRow(t *testing.T) (*Machine, string) {
	t.Helper()
	m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
	req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "TEAM-7: rotate the deploy key", "SHARED_KEY"), "", nil)
	if err != nil || req.RecordID == nil || req.State != "pending" {
		t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending", req, err)
	}
	answerFrom(t, m, dispatchRows(privateTeam(insider)))
	return m, *req.RecordID
}

// capturedLog is every line the package's slog writes at WARN or above while the test runs.
func capturedLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// TestTheFallbackFailsClosed: listedInStead lists a row whose responsible person may not see it
// only for a reader whose own read answered that they may, and whose read of that person
// answered that they may not. A read that fails leaves the row off, whichever read it is:
//   - control: the stranded row is on insider's list and off outsider's;
//   - outsider's own read fails: the row stays off outsider's list, though the responsible
//     person's read would answer;
//   - the responsible person's read fails: the row stays off insider's list;
//   - an outage: the stranded row is off insider's list, and the row it left off is logged,
//     naming the record and its responsible person, so a request an outage leaves on nobody's
//     list says so.
func TestTheFallbackFailsClosed(t *testing.T) {
	t.Run("control", func(t *testing.T) {
		m, stranded := strandedRow(t)
		wantListed(t, m, stranded, []string{insider, outsider}, []string{insider})
	})
	t.Run("the reader's own read fails", func(t *testing.T) {
		m, stranded := strandedRow(t)
		m.Projects = failForPerson{inner: m.Projects, person: outsider}
		wantListed(t, m, stranded, []string{outsider}, nil)
	})
	t.Run("the responsible person's read fails", func(t *testing.T) {
		m, stranded := strandedRow(t)
		m.Projects = failForPerson{inner: m.Projects, person: fixtureOperator}
		wantListed(t, m, stranded, []string{insider}, nil)
	})
	t.Run("an outage", func(t *testing.T) {
		m, stranded := strandedRow(t)
		dispatch := answerFrom(t, m, dispatchRows(privateTeam(insider)))
		dispatch.SetDown(true)
		logged := capturedLog(t)
		wantListed(t, m, stranded, []string{insider}, nil)
		line := logged.String()
		if !strings.Contains(line, rowUnansweredMessage) || !strings.Contains(line, stranded) || !strings.Contains(line, "responsible="+fixtureOperator) {
			t.Fatalf("the list logged %q; want one line saying it left off %s, naming its responsible person %s", line, stranded, fixtureOperator)
		}
	})
}

// TestTheReadersOwnRowsAreNotCutByTheClock: twenty other people's rows, under a Dispatch that
// takes 300 ms a question, spend the fallback's clock before insider's own older row is reached.
// That row is read on the list's own context, not the clock, and is listed.
func TestTheReadersOwnRowsAreNotCutByTheClock(t *testing.T) {
	m, _, _, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
	mine, mineKey := newEnrollment(t, m.Store, "session", "box-own-row", new(insider), nil)
	own, err := m.Create(t.Context(), mine, signRequest(t, m, mineKey, "TEAM-7: rotate the deploy key", "SHARED_KEY"), "", nil)
	if err != nil || own.RecordID == nil {
		t.Fatalf("Create(own) = %+v, %v; want it pending", own, err)
	}
	var others []string
	for i := range 20 {
		enr, key := newEnrollment(t, m.Store, "session", fmt.Sprintf("box-other-%02d", i), new(fmt.Sprintf("op%02d@example.com", i)), nil)
		req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "TEAM-7: rotate the deploy key", "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil || req.State != "pending" {
			t.Fatalf("Create = %+v, %v; want it pending", req, err)
		}
		others = append(others, *req.RecordID)
	}
	answerFrom(t, m, dispatchRows(publicTeam))
	m.Projects = slowAccess{inner: m.Projects, takes: 300 * time.Millisecond}
	listed, err := m.PendingForApprover(t.Context(), insider, PendingStates{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range listed {
		ids = append(ids, p.RecordID)
	}
	if !slices.Equal(ids, []string{*own.RecordID}) {
		t.Fatalf("insider's list = %v; want exactly its own row %s, read after the clock was spent (others %v)", ids, *own.RecordID, others)
	}
}

// TestTheReadersMaySeeIsReadFirst: a row the reader may not see costs no question about its
// responsible person. The operator's TEAM-7 request; TEAM then private with the operator alone on
// its list. outsider's list asks about outsider and never about the operator. Control: the
// operator's own list holds the row and asks about the operator.
func TestTheReadersMaySeeIsReadFirst(t *testing.T) {
	m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
	req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "TEAM-7: rotate the deploy key", "SHARED_KEY"), "", nil)
	if err != nil || req.RecordID == nil || req.State != "pending" {
		t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending", req, err)
	}
	answerFrom(t, m, dispatchRows(privateTeam(fixtureOperator)))
	asked := &askLog{inner: m.Projects}
	m.Projects = asked
	wantListed(t, m, *req.RecordID, []string{outsider}, nil)
	about := asked.askedAbout()
	if slices.Contains(about, fixtureOperator) || !slices.Contains(about, outsider) {
		t.Fatalf("outsider's list asked Dispatch about %v; want outsider, and never %s, the responsible person, for a row outsider may not see", about, fixtureOperator)
	}
	wantListed(t, m, *req.RecordID, []string{fixtureOperator}, []string{fixtureOperator})
	if !slices.Contains(asked.askedAbout()[len(about):], fixtureOperator) {
		t.Fatalf("control: the operator's list asked nothing about the operator: %v", asked.askedAbout()[len(about):])
	}
}

// TestARequestWhoseResponsiblePersonMayNotSeeItWaitsForThoseWhoMay: a request for a shared secret
// is listed for the person responsible for the agent that made it (responsiblePerson), and a
// private project's request for nobody outside the project (MaySee). Where the responsible person
// is outside the project, the request is listed for everyone who may see it instead
// (listedInStead), so it waits where somebody sees it:
//   - an outsider's own session asks with a reason naming a PRIV issue: the two people on PRIV's
//     list, the insider and the fixture's operator, list it, and the outsider does not;
//   - the operator's request names a TEAM issue while TEAM is public, and is the operator's alone;
//     TEAM then turns private with the insider alone on its list, and the insider lists it from
//     then on, the operator no more;
//   - control: the operator's request names a PRIV issue, and the operator is on PRIV's list, so it
//     stays the operator's alone.
func TestARequestWhoseResponsiblePersonMayNotSeeItWaitsForThoseWhoMay(t *testing.T) {
	people := []string{outsider, insider, fixtureOperator}
	t.Run("an outsider's request naming a private project's issue", func(t *testing.T) {
		m, _, _, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
		out, outKey := newEnrollment(t, m.Store, "session", "box-r-outsider", new(outsider), nil)
		req, err := m.Create(t.Context(), out, signRequest(t, m, outKey, "PRIV-12: rotate the deploy key", "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil || req.State != "pending" {
			t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending", req, err)
		}
		wantListed(t, m, *req.RecordID, people, []string{insider, fixtureOperator})
	})
	t.Run("a project that turns private under its responsible person", func(t *testing.T) {
		m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
		req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "TEAM-7: rotate the deploy key", "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil || req.State != "pending" {
			t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending", req, err)
		}
		wantListed(t, m, *req.RecordID, people, []string{fixtureOperator})
		answerFrom(t, m, dispatchRows(privateTeam(insider)))
		wantListed(t, m, *req.RecordID, people, []string{insider})
	})
	t.Run("a responsible person on the private project (control)", func(t *testing.T) {
		m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
		req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "PRIV-12: rotate the deploy key", "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil || req.State != "pending" {
			t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending", req, err)
		}
		wantListed(t, m, *req.RecordID, people, []string{fixtureOperator})
	})
}

// TestTheJudgesRefusalReadsNothingOfDispatch: the model judge's refusal of a request it holds is
// final whatever project the request belongs to, and asks Dispatch nothing, since a refusal grants
// nothing (applyDecisionAttempt). A request whose reason names a private project's issue is
// refused (the run's policy and withheld checks refuse before its project check), and so is one
// whose reason names a public project's issue while Dispatch does not answer. Control: the judge's
// grant of the private project's request is refused, record.ErrNotApprover, since a grant asks
// Dispatch again.
func TestTheJudgesRefusalReadsNothingOfDispatch(t *testing.T) {
	rules := airlock.Rules{Secrets: []airlock.SecretRule{{Name: "SHARED_KEY", Hosts: []string{"shared.example"}, Judge: new(uint32(900))}}}
	const why = "the model judge refused this request: SHARED_KEY is withheld from this session. This request will not be granted. Tell whoever gave you this work."
	held := func(t *testing.T, reason string) (*Machine, Request) {
		t.Helper()
		m, enr, key, _ := newProjectFixture(t, rules, publicTeam)
		m.Chain = NewChainVerifier(m.Store, testAudience, time.Minute, m.Airlock)
		m.Judge = func(PendingRequest) {}
		req := pendingJudgeable(t, m, enr, key, reason, 600, "SHARED_KEY")
		if _, ok := judgeHold(t, m, req.ID); !ok {
			t.Fatalf("request %s is not held for the judge", req.ID)
		}
		return m, req
	}
	for _, c := range []struct {
		name, reason string
		down         bool
	}{
		{"a private project's issue", "PRIV-12: rotate the deploy key", false},
		{"a public project's issue while Dispatch is down", "TEAM-7: rotate the deploy key", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, req := held(t, c.reason)
			dispatch := answerFrom(t, m, dispatchRows(publicTeam))
			dispatch.SetDown(c.down)
			dec, err := m.DenyJudged(t.Context(), *req.RecordID, judgeModel, map[string]any{"verdict": "deny"}, why)
			if err != nil || dec.State != "denied" {
				t.Fatalf("DenyJudged = %+v, %v; want the request denied", dec, err)
			}
			if calls := dispatch.Calls(); calls != 0 {
				t.Errorf("the judge's refusal asked Dispatch %d times; want none", calls)
			}
		})
	}
	t.Run("the judge's grant of a private project's request (control)", func(t *testing.T) {
		m, req := held(t, "PRIV-12: rotate the deploy key")
		answerFrom(t, m, dispatchRows(publicTeam))
		dec, err := m.ApplyJudgeDecision(t.Context(), *req.RecordID, judgeModel, map[string]any{"verdict": "grant"})
		if !errors.Is(err, record.ErrNotApprover) {
			t.Fatalf("ApplyJudgeDecision = %+v, %v; want record.ErrNotApprover", dec, err)
		}
	})
}
