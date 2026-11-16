package approval_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// privProject is the private project of the page's project tests: ada owns it and its access
// list names ben, whose machine every request comes from; carol is on no list.
const privProject = "PRIV"

// projectRig is newRig with a Dispatch stand-in holding PRIV, served to the request machine and
// the enrollment, and two secrets of PRIV beside the rig's four: PRIV_SECRET, ada's and human
// tier, which ada approves; PRIV_SHARED, shared and human tier, which anyone signed in may approve
// by owner and tier.
func projectRig(t *testing.T) (*rig, *projectaccesstest.Server) {
	t.Helper()
	dispatch := projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{
		privProject: {Visibility: "private", Owner: ada, Listed: []string{ben}},
	}, nil)
	tagged := func(name, owner string) secrets.LocalSecret {
		s := policytest.Secret(name, owner, policy.TierHuman, strings.ToLower(name)+"-v1")
		s.Tags[policy.TagProject] = privProject
		return s
	}
	r := newRig(t, func(s *rigSetup) {
		s.secrets = []secrets.LocalSecret{tagged("PRIV_SECRET", ada), tagged("PRIV_SHARED", policy.OwnerShared)}
		s.projects = projectaccess.New(dispatch.URL, dispatch.Account, nil)
	})
	return r, dispatch
}

// nameProjects sets the projects enrollmentID's session names, as its enrollment recorded them.
func (r *rig) nameProjects(t *testing.T, enrollmentID string, projects ...string) {
	t.Helper()
	if _, err := r.st.Pool.Exec(t.Context(), `update enrollments set projects=$2 where id=$1`, enrollmentID, projects); err != nil {
		t.Fatalf("name the session's projects: %v", err)
	}
}

// TestTheApprovalPageShowsAPrivateSecretsProject: the page of a request for PRIV's secret, from
// a session naming PRIV, says which project the secret belongs to, that it is private and that
// the session names it, and which projects the session names.
func TestTheApprovalPageShowsAPrivateSecretsProject(t *testing.T) {
	r, _ := projectRig(t)
	enrollmentID, key := r.benSession(t, "ben-priv-box")
	r.nameProjects(t, enrollmentID, privProject)
	p := r.request(t, enrollmentID, key, "deploy PRIV", nil, "", "PRIV_SECRET")

	b := r.browser(t)
	b.SignIn(t, p.recordID, person(ada))
	got := pageText(t, b.Page(t, p.recordID).Body)
	for _, want := range []string{
		"Project of PRIV_SECRET PRIV, private, which the session names",
		"Projects the session names PRIV",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page of %s does not say %q:\n%s", p.recordID, want, got)
		}
	}
}

// TestTheApprovalPageOfAnUntaggedRequestIsUnchanged: a request for a secret of no project, from a
// session naming none, prints no project row, and its page asks Dispatch nothing, though the
// broker holds a client of it. The enrollment itself asks Dispatch the person's member kind
// (enroll.Service.checkProjects), so the count is read once the request exists.
func TestTheApprovalPageOfAnUntaggedRequestIsUnchanged(t *testing.T) {
	r, dispatch := projectRig(t)
	enrollmentID, key := r.benSession(t, "ben-plain-box")
	p := r.request(t, enrollmentID, key, "deploy the billing service", nil, "", "TEST_SECRET")
	before := dispatch.Calls()

	b := r.browser(t)
	b.SignIn(t, p.recordID, person(ada))
	got := pageText(t, b.Page(t, p.recordID).Body)
	if strings.Contains(got, "Project") {
		t.Errorf("the page of an untagged request prints a project row:\n%s", got)
	}
	if calls := dispatch.Calls() - before; calls != 0 {
		t.Errorf("the page of an untagged request asked Dispatch %d times; want none", calls)
	}
}

// TestAnOutsiderSeesNoPrivateRequest: carol, outside PRIV, may approve PRIV_SHARED by owner and
// tier, yet its page answers her the 404 an id no record has, it is not among the other requests
// waiting for her, a decision's outcome line naming it says nothing of it, and her Approve leaves
// it pending. ben's request for an untagged secret whose reason names a PRIV issue is hidden the
// same way. A person on PRIV's list sees both.
func TestAnOutsiderSeesNoPrivateRequest(t *testing.T) {
	r, _ := projectRig(t)
	enrollmentID, key := r.benSession(t, "ben-shared-box")
	r.nameProjects(t, enrollmentID, privProject)
	tagged := r.request(t, enrollmentID, key, "rotate the shared key", nil, "", "PRIV_SHARED")
	plainSession, plainKey := r.benSession(t, "ben-reason-box")
	reasoned := r.request(t, plainSession, plainKey, "PRIV-12: rotate the shared key", nil, "", "SHARED_SECRET")
	// A session of its own: a second request of one session for the same secret and approver
	// joins the pending one rather than opening a card of its own.
	wikiSession, wikiKey := r.benSession(t, "ben-wiki-box")
	visible := r.request(t, wikiSession, wikiKey, "rotate the wiki key", nil, "", "SHARED_SECRET")

	b := r.browser(t)
	b.SignIn(t, visible.recordID, person(carol))
	for _, hidden := range []pending{tagged, reasoned} {
		if res := b.Get(t, "/approve/"+hidden.recordID); res.Status != http.StatusNotFound || !strings.Contains(pageText(t, res.Body), "No such request") {
			t.Errorf("carol's page of %s = %d:\n%s\nwant the 404 an id no record has", hidden.recordID, res.Status, pageText(t, res.Body))
		}
	}
	page := b.Get(t, "/approve/"+visible.recordID+"?decided="+tagged.recordID+","+reasoned.recordID)
	if page.Status != http.StatusOK {
		t.Fatalf("carol's page of %s = %d", visible.recordID, page.Status)
	}
	for _, hidden := range []pending{tagged, reasoned} {
		if strings.Contains(page.Body, hidden.recordID) {
			t.Errorf("carol's page names %s, a request of PRIV:\n%s", hidden.recordID, page.Body)
		}
	}
	if text := pageText(t, page.Body); strings.Contains(text, "PRIV") {
		t.Errorf("carol's page says PRIV:\n%s", text)
	}
	form := decisionForm(t, page.Body, visible.recordID)
	if form == nil {
		t.Fatalf("carol's page offers no form for %s", visible.recordID)
	}
	form.Set("record", tagged.recordID)
	b.FromPage(t, "/approve", decision(form, "approve"))
	r.requirePending(t, tagged.recordID)

	insider := r.browser(t)
	insider.SignIn(t, tagged.recordID, person(ben))
	for _, shown := range []pending{tagged, reasoned} {
		if res := insider.Get(t, "/approve/"+shown.recordID); res.Status != http.StatusOK {
			t.Errorf("ben's page of %s = %d, want 200: he is on PRIV's list", shown.recordID, res.Status)
		}
	}
}

// unansweredNote is what the page says of a request Dispatch cannot answer for: whether the
// person may see it is unknown, so the page shows nothing of it and nothing decides it.
const unansweredNote = "the secrets broker could not ask Dispatch whether you may see this request, so it shows nothing of it and cannot decide it until Dispatch answers. Try again shortly."

// TestADispatchOutageShowsOnlyItsRequestAsUnavailable: nothing is tagged, and ben's request for
// the shared secret names an issue, INFRA-12, beside one that names none. carol's page prints both
// while Dispatch answers. Dispatch then goes down and she approves all she was shown: the request
// naming no issue is granted, and the one naming INFRA-12 is decided by nobody and stays pending.
// The page she lands on answers 200 with each outcome, the INFRA-12 one saying only that it is
// unavailable; the INFRA-12 request's own page answers 503 showing that one request as
// unavailable and nothing of it, its reason included. Once Dispatch answers it is shown and
// decidable again.
func TestADispatchOutageShowsOnlyItsRequestAsUnavailable(t *testing.T) {
	up := projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{}, nil)
	access := &projectaccesstest.Swappable{}
	access.Serve(up)
	r := newRig(t, func(s *rigSetup) { s.projects = access })
	plainSession, plainKey := r.benSession(t, "ben-plain-box")
	plain := r.request(t, plainSession, plainKey, "rotate the wiki key", nil, "", "SHARED_SECRET")
	keyedSession, keyedKey := r.benSession(t, "ben-keyed-box")
	keyed := r.request(t, keyedSession, keyedKey, "INFRA-12: rotate the deploy key", nil, "", "SHARED_SECRET")

	b := r.browser(t)
	b.SignIn(t, plain.recordID, person(carol))
	all, label := approveAll(t, b.Page(t, plain.recordID).Body)
	if label != "Approve all 2 shown" {
		t.Fatalf("carol's page offers %q; want both requests shown while Dispatch answers", label)
	}
	down := projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{}, nil)
	down.SetDown(true)
	access.Serve(down)

	res := b.FromPage(t, "/approve", all)
	if res.Status != http.StatusSeeOther {
		t.Fatalf("approve all while Dispatch is down = %d, want 303:\n%s", res.Status, res.Body)
	}
	if state := r.state(t, plain.recordID); state != "approved" {
		t.Errorf("the request naming no issue = %s, want approved: Dispatch is asked nothing for it", state)
	}
	r.requirePending(t, keyed.recordID)
	landed := b.Get(t, res.Header.Get("Location"))
	text := pageText(t, landed.Body)
	if landed.Status != http.StatusOK || !strings.Contains(text, keyed.recordID+": unavailable: "+unansweredNote) {
		t.Fatalf("the page approve all lands on = %d; want 200 with the INFRA-12 request's outcome %q:\n%s", landed.Status, unansweredNote, text)
	}
	if strings.Contains(text, "INFRA-12") || strings.Contains(text, "rotate the deploy key") {
		t.Errorf("the page shows the reason of a request Dispatch could not answer for:\n%s", text)
	}

	own := b.Get(t, "/approve/"+keyed.recordID)
	text = pageText(t, own.Body)
	if own.Status != http.StatusServiceUnavailable || !strings.Contains(text, "Request "+keyed.recordID) || !strings.Contains(text, "The "+strings.TrimPrefix(unansweredNote, "the ")) {
		t.Fatalf("the INFRA-12 request's page while Dispatch is down = %d; want 503 showing it unavailable:\n%s", own.Status, text)
	}
	if strings.Contains(text, "INFRA-12") || strings.Contains(text, "SHARED_SECRET") || decisionForm(t, own.Body, keyed.recordID) != nil {
		t.Errorf("the INFRA-12 request's page while Dispatch is down shows its names, its reason or a button:\n%s", text)
	}
	if down.Calls() == 0 {
		t.Fatal("the down Dispatch was never asked: the test is blind")
	}

	access.Serve(projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{}, nil))
	if back := b.Page(t, keyed.recordID); back.Status != http.StatusOK || decisionForm(t, back.Body, keyed.recordID) == nil {
		t.Fatalf("the INFRA-12 request's page once Dispatch answers = %d with no button; want it decidable again:\n%s", back.Status, pageText(t, back.Body))
	}
}
