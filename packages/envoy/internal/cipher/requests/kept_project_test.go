package requests

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/cipher/proxyrules"
)

// projectUnchecked is the broker_project_visibility_unchecked_total sample labelled kept.
func (p keptProcess) projectUnchecked(t *testing.T, kept string) int {
	t.Helper()
	return p.sample(t, "broker_project_visibility_unchecked_total", kept)
}

// TestWhileDispatchCannotBeAskedARowIsShownByTheKeptProjectAnswer is the person rule (maySee) on
// a Dispatch client that keeps each answer in the machine's own store, as the broker's does. Four
// pending requests for SHARED_KEY, from pods that name no person so every signed-in person's list
// reads them (everyonesPod), name in their reasons TEAM, public; PRIV, whose list names insider
// and not outsider; "SHA-256", a key of Dispatch's form that is no project; and, made only once
// Dispatch is down, OTHER, a private project whose list names insider, which nobody has asked
// about. Both people read their lists while Dispatch answers, and the answers are kept.
//
// Once Dispatch is down, a client of a broker process restarted since reads the kept answers:
// insider's list holds the TEAM, PRIV and SHA-256 rows and not OTHER's, never asked; outsider's
// holds TEAM's and SHA-256's and not PRIV's, kept hidden from him, nor OTHER's; insider approves
// PRIV's request; and outsider's approval of it is ErrProjectAccessUnavailable, leaving it
// pending, as every such refusal is while Dispatch is down. Meanwhile TEAM turns private, listing
// neither: insider's list still holds its row until Dispatch answers, and once it has, the row is
// gone, and stays gone when Dispatch stops answering again.
//
// Controls: on a client with no keeper insider's list holds none of the four rows while Dispatch
// is down, so the outage is real; and the request whose reason names no key is listed to both
// wherever their kept member kinds serve them. The fault: maySee reading without OrKept; insider's
// list holds none of the rows that name a project.
func TestWhileDispatchCannotBeAskedARowIsShownByTheKeptProjectAnswer(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := newProjectFixture(t, proxyrules.Rules{}, publicTeam)
	rows := dispatchRows(publicTeam)
	rows["OTHER"] = projectaccesstest.Row{Visibility: "private", Owner: otherPerson, Listed: []string{insider}}
	dispatch := projectaccesstest.NewServer(t, rows, map[string]string{
		insider: projectaccess.MemberFull, outsider: projectaccess.MemberFull, fixtureOperator: projectaccess.MemberFull,
	})
	ask := func(name, reason string) string {
		t.Helper()
		pod, key := everyonesPod(t, m, name, "")
		req, err := m.Create(ctx, pod, signRequest(t, m, key, reason, "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil {
			t.Fatalf("Create(SHARED_KEY, %q) = %+v, %v; want pending", reason, req, err)
		}
		return *req.RecordID
	}
	team := ask("pod-k-team", "TEAM-1: rotate the team key")
	priv := ask("pod-k-priv", "PRIV-2: rotate the deploy key")
	hashed := ask("pod-k-hash", "check the SHA-256 of the bundle")
	plain := ask("pod-k-plain", "no issue named")
	listed := func(login string) []string {
		t.Helper()
		pending, err := m.PendingForApprover(ctx, login, PendingStates{})
		if err != nil {
			t.Fatalf("PendingForApprover(%s): %v", login, err)
		}
		var ids []string
		for _, p := range pending {
			ids = append(ids, p.RecordID)
		}
		return ids
	}
	requireListed := func(login, when string, want, not []string) {
		t.Helper()
		got := listed(login)
		for _, id := range want {
			if !slices.Contains(got, id) {
				t.Errorf("%s's list %s = %v; want %s", login, when, got, id)
			}
		}
		for _, id := range not {
			if slices.Contains(got, id) {
				t.Errorf("%s's list %s = %v; want no %s", login, when, got, id)
			}
		}
	}

	m.Projects = newKeptProcess(dispatch, m.Store).client
	requireListed(insider, "while Dispatch answers", []string{team, priv, hashed, plain}, nil)
	requireListed(outsider, "while Dispatch answers", []string{team, hashed, plain}, []string{priv})
	dispatch.SetDown(true)
	t.Cleanup(func() { dispatch.SetDown(false) })
	other := ask("pod-k-other", "OTHER-3: a project nobody asked about")

	m.Projects = projectaccess.New(dispatch.URL, dispatch.Account, nil)
	requireListed(insider, "on a client with no keeper while Dispatch is down", nil, []string{team, priv, hashed, other})

	after := newKeptProcess(dispatch, m.Store)
	m.Projects = after.client
	requireListed(insider, "while Dispatch is down", []string{team, priv, hashed, plain}, []string{other})
	requireListed(outsider, "while Dispatch is down", []string{team, hashed, plain}, []string{priv, other})
	for kept, want := range map[string]int{"public": 2, "visible": 1, "missing": 2, "not_visible": 1, "none": 2} {
		if got := after.projectUnchecked(t, kept); got != want {
			t.Errorf(`broker_project_visibility_unchecked_total{kept=%q} after both lists = %d; want %d`, kept, got, want)
		}
	}
	if decision, err := m.ApplyDecision(ctx, priv, true, outsider); !errors.Is(err, ErrProjectAccessUnavailable) {
		t.Fatalf("an approval of PRIV's request by %s while Dispatch is down = %+v, %v; want ErrProjectAccessUnavailable", outsider, decision, err)
	}
	if decision, err := m.ApplyDecision(ctx, priv, true, insider); err != nil || decision.GrantID == "" {
		t.Fatalf("an approval of PRIV's request by %s while Dispatch is down = %+v, %v; want a grant", insider, decision, err)
	}

	dispatch.SetRow(teamProject, privateTeam())
	m.Projects = newKeptProcess(dispatch, m.Store).client
	requireListed(insider, "while Dispatch is down, TEAM turned private since", []string{team}, nil)
	dispatch.SetDown(false)
	m.Projects = newKeptProcess(dispatch, m.Store).client
	requireListed(insider, "once Dispatch answers TEAM private", []string{hashed, plain}, []string{team})
	dispatch.SetDown(true)
	m.Projects = newKeptProcess(dispatch, m.Store).client
	requireListed(insider, "while Dispatch is down again", []string{hashed, plain}, []string{team})
}
