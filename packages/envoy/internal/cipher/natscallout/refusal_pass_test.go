package natscallout

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
)

// A refusal of the broker's token on one project's question refuses every held daemon, whatever
// Dispatch answered about the daemon's own projects: the token exchange refuses every trade that
// asks Dispatch then. Here OTHER's question is refused while PUB is answered visible, and PUB's
// daemon is refused project_access_refused.
func TestTNOneProjectRefusingBrokerRefusesEveryHeldDaemon(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{
		{Kind: KindDaemon, Account: pubDaemonAccount},
		{Kind: KindDaemon, Account: otherDaemonAccount},
	}
	dispatch := &dispatchAnswers{projects: map[string]bool{"PUB": true, "OTHER": true}}
	deps := heldDeps(t,
		map[string]platformToken{"token": {
			account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour),
		}},
		map[string][]string{pubDaemonAccount: {"PUB"}, otherDaemonAccount: {"OTHER"}},
		dispatch, services)
	deps.held.access = func(ctx context.Context, project, person string) (projectaccess.Answer, error) {
		if project == "OTHER" {
			return projectaccess.Answer{}, projectaccess.ErrBrokerRefused
		}
		return dispatch.access(ctx, project, person)
	}
	deps.held.refresh(t.Context())
	_, err := Decide(t.Context(), deps, services, serviceRequest("token"))
	requireRefused(t, err, labelAccessRefused, "Dispatch refuses the broker's own account token")
}

// The refusal of the broker's token is the one rule that joins a refresh, and it holds before
// any answer: a first refresh in which Dispatch refuses the token on every question refuses PUB's
// daemon project_access_refused, naming that Dispatch refuses the broker's token, and not
// project_access_unknown, which would tell the operator to wait for an answer that cannot come.
// The control checks the projects' answers before the refusal: the daemon is refused
// project_access_unknown, and the test fails naming that label.
func TestARefusalOfTheBrokersTokenBeforeAnyAnswerIsNamedSo(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	tokens := map[string]platformToken{"token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	deps := Deps{
		ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB"}}),
		Access: func(context.Context, string, string) (projectaccess.Answer, error) {
			return projectaccess.Answer{}, projectaccess.ErrBrokerRefused
		},
	}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	deps.held.refresh(t.Context())
	_, known := deps.held.answer(heldKey{project: "PUB", person: entryPerson})
	require.False(t, known, "a refused question was held as an answer")

	_, err := Decide(t.Context(), deps, services, serviceRequest("token"))
	requireRefused(t, err, labelAccessRefused, "Dispatch refuses the broker's own account token")
}
