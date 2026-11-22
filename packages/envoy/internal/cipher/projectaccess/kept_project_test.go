package projectaccess_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/cipher/store"
	"github.com/sjawhar/envoy/internal/cipher/store/storetest"
)

// The projects the kept-project rows read: PUB public, and PRIV private, its list naming ada and
// not bob. SHA is a key Dispatch holds no project for, as the "SHA" of "SHA-256" is in a reason,
// and GPT another, which nobody asked about.
func keptProjects() map[string]projectaccesstest.Row {
	return map[string]projectaccesstest.Row{
		"PUB":  {Visibility: "public", Owner: keptCarol},
		"PRIV": {Visibility: "private", Owner: keptCarol, Listed: []string{keptAda}},
	}
}

// projectUnchecked is the broker_project_visibility_unchecked_total sample labelled kept.
func (r *keptRig) projectUnchecked(t *testing.T, kept string) int {
	t.Helper()
	return r.sample(t, "broker_project_visibility_unchecked_total", kept)
}

// shown fails t unless r's client answers person's read of project under OrKept visible, with
// visibility and a zero AskedAt: a kept answer that shows them the project.
func (r *keptRig) shown(t *testing.T, project, person, visibility, when string) {
	t.Helper()
	got, err := r.client.Access(projectaccess.OrKept(t.Context()), project, person)
	if err != nil || !got.Visible || got.Visibility != visibility || got.Project != project || !got.AskedAt.IsZero() {
		t.Fatalf("Access(OrKept, %s, %s) %s = %+v, %v; want %s and visible, asked at no time", project, person, when, got, err, visibility)
	}
}

// hidden fails t unless r's client refuses person's read of project under OrKept as a read
// Dispatch did not answer: the read's failure, which is Unanswered.
func (r *keptRig) hidden(t *testing.T, project, person, when string) {
	t.Helper()
	if got, err := r.client.Access(projectaccess.OrKept(t.Context()), project, person); err == nil || !projectaccess.Unanswered(err) {
		t.Fatalf("Access(OrKept, %s, %s) %s = %+v, %v; want the failure of a read Dispatch did not answer", project, person, when, got, err)
	}
}

// noProject fails t unless r's client answers person's read of project under OrKept as no
// project, with no time: a kept answer that Dispatch holds no such project.
func (r *keptRig) noProject(t *testing.T, project, person, when string) {
	t.Helper()
	_, err := r.client.Access(projectaccess.OrKept(t.Context()), project, person)
	var missing *projectaccess.ProjectNotFoundError
	if !errors.As(err, &missing) || missing.Project != project || !missing.AskedAt.IsZero() {
		t.Fatalf("Access(OrKept, %s, %s) %s = %v; want the ProjectNotFoundError kept, asked at no time", project, person, when, err)
	}
}

// readAll fails t unless r's client reads each person's projects while Dispatch answers, which
// keeps each answer.
func (r *keptRig) readAll(t *testing.T, reads map[string][]string) {
	t.Helper()
	for person, projects := range reads {
		for _, project := range projects {
			_, err := r.client.Access(t.Context(), project, person)
			if err != nil && !errors.Is(err, projectaccess.ErrProjectNotFound) {
				t.Fatalf("Access(%s, %s) while Dispatch answers: %v", project, person, err)
			}
		}
	}
}

// TestAProjectReadUnderOrKeptIsAnsweredFromTheKeptAnswer: while Dispatch answers, ada reads PUB
// and PRIV and the key SHA, and bob reads PRIV, and each answer is kept. Once Dispatch stops
// answering, a read under OrKept answers from what was kept: ada's PUB, public, and her PRIV,
// visible to her, are shown, each with one WARN line (KeptProjectMessage) and one count, labelled
// public and visible, a minute however often they are read, and a second of each a minute later.
// SHA, which Dispatch answered no project for, is no project again for anyone, carol included, as
// Dispatch's own answer is (labelled missing), so a reason naming "SHA-256" or "GPT-5" hides no
// row. bob's PRIV, hidden from him, carol's PUB, never asked, GPT, never asked by anyone, and
// dave's PUB on a store that cannot be read are each refused as a client with no keeper refuses
// them, counted not_visible, none and unreadable.
//
// Controls: the same read of ada's PUB with no OrKept is refused, so the fallback is the person
// rule's alone and the outage is real; and GPT, never answered, is refused while SHA is served,
// so the no-project row rests on the kept answer and not on the key's form.
func TestAProjectReadUnderOrKeptIsAnsweredFromTheKeptAnswer(t *testing.T) {
	url := storetest.URL(t)
	dispatch := projectaccesstest.NewServer(t, keptProjects(), keptKinds())
	r := keepingClientOf(dispatch, openStore(t, url))
	r.readAll(t, map[string][]string{keptAda: {"PUB", "PRIV", "SHA"}, keptBob: {"PRIV"}})
	dispatch.SetDown(true)
	r.clock.advance(projectaccess.CacheTTL)

	if got, err := r.client.Access(t.Context(), "PUB", keptAda); err == nil {
		t.Fatalf("Access(PUB, ada) with no OrKept while Dispatch is down = %+v; want a failure: the fallback is the person rule's alone", got)
	}
	for range 3 {
		r.shown(t, "PUB", keptAda, projectaccess.VisibilityPublic, "while Dispatch is down, kept public")
		r.shown(t, "PRIV", keptAda, projectaccess.VisibilityPrivate, "while Dispatch is down, kept visible to her")
	}
	r.noProject(t, "SHA", keptAda, "while Dispatch is down, kept as no project")
	r.noProject(t, "SHA", keptCarol, "while Dispatch is down, kept as no project for anyone")
	r.hidden(t, "PRIV", keptBob, "while Dispatch is down, kept hidden from him")
	r.hidden(t, "PUB", keptCarol, "while Dispatch is down, never asked")
	r.hidden(t, "GPT", keptAda, "while Dispatch is down, never answered")
	for kept, want := range map[string]int{"public": 1, "visible": 1, "missing": 2, "not_visible": 1, "none": 2} {
		if got := r.projectUnchecked(t, kept); got != want {
			t.Fatalf(`broker_project_visibility_unchecked_total{kept=%q} = %d; want %d`, kept, got, want)
		}
	}
	served := 0
	for _, line := range r.warned(projectaccess.KeptProjectMessage, keptAda) {
		if line["error"] == "" || line["asked_at"] == "" {
			t.Fatalf("a served read logged %v; want the error and asked_at", line)
		}
		served++
	}
	if served != 3 || len(r.warned(projectaccess.KeptProjectMessage, keptBob)) != 0 {
		t.Fatalf("serving ada's PUB, PRIV and SHA logged %d WARN lines, and bob's refusal %d; want 3 and 0",
			served, len(r.warned(projectaccess.KeptProjectMessage, keptBob)))
	}

	r.clock.advance(time.Minute)
	r.shown(t, "PUB", keptAda, projectaccess.VisibilityPublic, "a minute later")
	if got := r.projectUnchecked(t, "public"); got != 2 {
		t.Fatalf(`broker_project_visibility_unchecked_total{kept="public"} a minute later = %d; want 2`, got)
	}

	gone := openStore(t, url)
	gone.Close()
	unreadable := keepingClientOf(dispatch, gone)
	unreadable.hidden(t, "PUB", keptDave, "while Dispatch is down and the store is closed")
	if got := unreadable.projectUnchecked(t, "unreadable"); got != 1 {
		t.Fatalf(`broker_project_visibility_unchecked_total{kept="unreadable"} with the store closed = %d; want 1`, got)
	}
}

// TestTheKeptProjectAnswerSurvivesARestart: a client of a second broker process over the same
// database, or of this one restarted, whose Dispatch is down from its start, answers as the
// first: ada's PUB and PRIV shown, SHA no project, bob's PRIV refused.
//
// Control: a client with no keeper refuses ada's PUB under OrKept, so the outage is real.
func TestTheKeptProjectAnswerSurvivesARestart(t *testing.T) {
	url := storetest.URL(t)
	dispatch := projectaccesstest.NewServer(t, keptProjects(), keptKinds())
	keepingClientOf(dispatch, openStore(t, url)).readAll(t, map[string][]string{keptAda: {"PUB", "PRIV", "SHA"}, keptBob: {"PRIV"}})
	dispatch.SetDown(true)

	bare := projectaccess.New(dispatch.URL, dispatch.Account, nil)
	if got, err := bare.Access(projectaccess.OrKept(t.Context()), "PUB", keptAda); err == nil {
		t.Fatalf("Access(OrKept, PUB, ada) at a client with no keeper while Dispatch is down = %+v; want a failure: the outage is not real", got)
	}
	second := keepingClientOf(dispatch, openStore(t, url))
	second.shown(t, "PUB", keptAda, projectaccess.VisibilityPublic, "at a second process")
	second.shown(t, "PRIV", keptAda, projectaccess.VisibilityPrivate, "at a second process")
	second.noProject(t, "SHA", keptAda, "at a second process")
	second.hidden(t, "PRIV", keptBob, "at a second process")
}

// TestAProjectChangeTakesEffectWhenDispatchAnswersAgain: ada is kept as seeing PRIV. While
// Dispatch is down PRIV's owner takes her off its list, which the broker cannot know: she is
// still shown it. Once Dispatch answers again, her next read answers her off the list and is
// kept, so when Dispatch stops answering once more she is refused, counted not_visible. A project
// she was off and is put back on is shown again the same way.
//
// The fault: KeepProjectStanding's update leaving visible out of what it sets; her read once
// Dispatch is down the second time shows her PRIV.
func TestAProjectChangeTakesEffectWhenDispatchAnswersAgain(t *testing.T) {
	dispatch := projectaccesstest.NewServer(t, keptProjects(), keptKinds())
	r := keepingClientOf(dispatch, storetest.Open(t))
	r.readAll(t, map[string][]string{keptAda: {"PRIV"}})
	dispatch.SetDown(true)
	r.clock.advance(projectaccess.CacheTTL)
	dispatch.SetRow("PRIV", projectaccesstest.Row{Visibility: "private", Owner: keptCarol})
	r.shown(t, "PRIV", keptAda, projectaccess.VisibilityPrivate, "while Dispatch is down, taken off the list since")

	dispatch.SetDown(false)
	r.clock.advance(projectaccess.CacheTTL)
	if got, err := r.client.Access(projectaccess.OrKept(t.Context()), "PRIV", keptAda); err != nil || got.Visible {
		t.Fatalf("Access(OrKept, PRIV, ada) once Dispatch answers = %+v, %v; want Dispatch's answer: not visible", got, err)
	}
	dispatch.SetDown(true)
	r.clock.advance(projectaccess.CacheTTL)
	r.hidden(t, "PRIV", keptAda, "while Dispatch is down again, kept off the list")
	if got := r.projectUnchecked(t, "not_visible"); got != 1 {
		t.Fatalf(`broker_project_visibility_unchecked_total{kept="not_visible"} = %d; want 1`, got)
	}

	dispatch.SetDown(false)
	dispatch.SetRow("PRIV", keptProjects()["PRIV"])
	r.clock.advance(projectaccess.CacheTTL)
	r.readAll(t, map[string][]string{keptAda: {"PRIV"}})
	dispatch.SetDown(true)
	r.clock.advance(projectaccess.CacheTTL)
	r.shown(t, "PRIV", keptAda, projectaccess.VisibilityPrivate, "while Dispatch is down, put back on the list")
}

// TestAnOlderProjectAnswerDoesNotReplaceTheKeptOne: two broker processes ask about ada's PRIV, the
// one whose clock reads later after she was taken off its list, and the one whose clock reads
// earlier while she was on it, written after. The database keeps the answer asked later, so a
// process whose Dispatch is down refuses her.
//
// The fault: KeepProjectStanding writing every answer whatever the row holds (its where clause
// gone); the older answer replaces the later one, and she is shown PRIV.
func TestAnOlderProjectAnswerDoesNotReplaceTheKeptOne(t *testing.T) {
	url := storetest.URL(t)
	dispatch := projectaccesstest.NewServer(t, keptProjects(), keptKinds())
	later := keepingClientOf(dispatch, openStore(t, url))
	earlier := keepingClientOf(dispatch, openStore(t, url))
	later.clock.advance(time.Second)
	dispatch.SetRow("PRIV", projectaccesstest.Row{Visibility: "private", Owner: keptCarol})
	later.readAll(t, map[string][]string{keptAda: {"PRIV"}})
	dispatch.SetRow("PRIV", keptProjects()["PRIV"])
	earlier.readAll(t, map[string][]string{keptAda: {"PRIV"}})
	st := openStore(t, url)
	if vis, visible, at, found, err := st.KeptProjectStanding(t.Context(), keptAda, "PRIV"); err != nil || !found || vis != projectaccess.VisibilityPrivate ||
		visible || !at.Equal(later.clock.read()) {
		t.Fatalf("KeptProjectStanding(ada, PRIV) = %q visible %t as of %v (found %t, %v); want private, not visible, as asked at the later clock %v",
			vis, visible, at, found, err, later.clock.read())
	}
	dispatch.SetDown(true)
	keepingClientOf(dispatch, st).hidden(t, "PRIV", keptAda, "after an earlier-asked answer was written last")
}

// TestAProjectAnswerNoWriteKeptStandsOverTheKeptOne: ada is kept as seeing PRIV; the database then
// refuses writes, and Dispatch answers her off its list. The read answers that, as Dispatch did,
// and logs that the answer could not be kept; once Dispatch stops answering, the client refuses
// her on that answer rather than show her PRIV on the older kept one.
//
// The fault: keepProject remembering no answer it could not keep; her read once Dispatch is down
// shows her PRIV.
func TestAProjectAnswerNoWriteKeptStandsOverTheKeptOne(t *testing.T) {
	st := storetest.Open(t)
	dispatch := projectaccesstest.NewServer(t, keptProjects(), keptKinds())
	r := keepingClientOf(dispatch, st)
	r.readAll(t, map[string][]string{keptAda: {"PRIV"}})
	refuseProjectKeeps(t, st)
	dispatch.SetRow("PRIV", projectaccesstest.Row{Visibility: "private", Owner: keptCarol})
	r.clock.advance(projectaccess.CacheTTL)
	if got, err := r.client.Access(t.Context(), "PRIV", keptAda); err != nil || got.Visible {
		t.Fatalf("Access(PRIV, ada) once she is off the list = %+v, %v; want not visible", got, err)
	}
	if lines := r.warned("broker: Dispatch's answer of whether a person may see a project could not be kept", keptAda); len(lines) != 1 {
		t.Fatalf("the refused write logged %v; want one WARN line", lines)
	}
	dispatch.SetDown(true)
	r.clock.advance(projectaccess.CacheTTL)
	r.hidden(t, "PRIV", keptAda, "while Dispatch is down after an answer no write kept")
}

// refuseProjectKeeps makes project_standing refuse every insert and update, as a database that
// takes reads and no writes.
func refuseProjectKeeps(t *testing.T, st *store.Store) {
	t.Helper()
	for _, statement := range []string{
		`create or replace function refuse_project_standing_writes() returns trigger language plpgsql as $$
			begin raise exception 'this test refuses every write to project_standing'; end $$`,
		`create trigger project_standing_refused before insert or update on project_standing
			for each row execute function refuse_project_standing_writes()`,
	} {
		if _, err := st.Pool.Exec(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
}
