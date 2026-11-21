package natscallout

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
)

// otherFails is the error OTHER's question answers with in these tests.
var otherFails = errors.New(`projectaccess: dispatch answered /api/v1/access/projects/OTHER 500 with the code ""`)

// Each project's answer is held on its own, so a refusal applies as soon as it is answered: the
// token exchange refuses a removed person's token within 30 s whatever else Dispatch answers, so
// the connection may not wait on an unrelated question. PUB's daemon is admitted on a first
// answer; then PUB is taken from its person (not visible) or from Dispatch (not found), while
// another entry's project, OTHER, answers 500. The next decision is refused on PUB's own answer.
// The control holds nothing of a refresh any question of which fails: each row is admitted.
func TestARefusalAppliesWhileAnotherProjectFails(t *testing.T) {
	for _, row := range []struct {
		name   string
		revoke func(d *dispatchAnswers)
		label  string
		reason string
	}{
		{"PUB hidden from its person", func(d *dispatchAnswers) { d.hide("PUB", true) },
			"project_not_visible", "project not visible to the account's person: PUB"},
		{"PUB gone from Dispatch", func(d *dispatchAnswers) { d.drop("PUB") },
			"project_not_found", "project not found: PUB"},
	} {
		t.Run(row.name, func(t *testing.T) {
			now := time.Now()
			services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: otherDaemonAccount}}
			tokens := map[string]platformToken{"pub-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
			dispatch := &dispatchAnswers{
				projects: map[string]bool{"PUB": true, "OTHER": true},
				private:  map[string]bool{"PUB": true},
			}
			deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB"}, otherDaemonAccount: {"OTHER"}}, dispatch, services)
			_, err := Decide(t.Context(), deps, services, serviceRequest("pub-token"))
			require.NoError(t, err, "PUB's daemon was refused on its first answer")

			row.revoke(dispatch)
			deps.held.access = func(ctx context.Context, project, person string) (projectaccess.Answer, error) {
				if project == "OTHER" {
					return projectaccess.Answer{}, otherFails
				}
				return dispatch.access(ctx, project, person)
			}
			deps.held.refresh(t.Context())

			_, err = Decide(t.Context(), deps, services, serviceRequest("pub-token"))
			requireRefused(t, err, row.label, row.reason)
		})
	}
}

// No rule joins the answers of one refresh: a project answered visible admits its daemon while
// another entry's project keeps failing. OTHER fails from the broker's start, so its daemon is
// refused as not yet known, while PUB's daemon is admitted on PUB's first answer. Then PUB is
// hidden from its person and refused, and once its person is put back, PUB's daemon is admitted on
// that answer, OTHER failing throughout. The control holds a refresh's answers only when every
// question of it is answered: PUB's daemon is refused on the first refresh, and the test fails
// naming PUB.
func TestAProjectIsAdmittedOnItsOwnAnswerWhileAnotherFails(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: otherDaemonAccount}}
	tokens := map[string]platformToken{
		"pub-token":   {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
		"other-token": {account: otherDaemonAccount, issued: now, expiry: now.Add(time.Hour)},
	}
	dispatch := &dispatchAnswers{
		projects:   map[string]bool{"PUB": true, "OTHER": true},
		private:    map[string]bool{"PUB": true},
		failingFor: map[string]error{"OTHER": otherFails},
	}
	deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB"}, otherDaemonAccount: {"OTHER"}}, dispatch, services)
	_, err := Decide(t.Context(), deps, services, serviceRequest("pub-token"))
	require.NoError(t, err, "PUB's daemon was refused on PUB's answer while OTHER failed: PUB")
	_, err = Decide(t.Context(), deps, services, serviceRequest("other-token"))
	requireRefused(t, err, labelProjectAccessUnknown, "project access not yet known: OTHER")

	dispatch.hide("PUB", true)
	deps.held.refresh(t.Context())
	_, err = Decide(t.Context(), deps, services, serviceRequest("pub-token"))
	requireRefused(t, err, labelProjectNotVisible, "project not visible to the account's person: PUB")

	dispatch.hide("PUB", false)
	deps.held.refresh(t.Context())
	_, err = Decide(t.Context(), deps, services, serviceRequest("pub-token"))
	require.NoError(t, err, "PUB's daemon was refused after its person was put back, while OTHER failed: PUB")
}
