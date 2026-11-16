package natscallout

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
)

// A pass one question of which fails holds no addition, but every refusal it got applies at once:
// the token exchange refuses a removed person's token within 30 s whatever else Dispatch answers,
// so the connection may not wait on an unrelated question. PUB's daemon is admitted on a first
// complete pass; then PUB is taken from its person (not visible) or from Dispatch (not found), or
// one of its repositories leaves it, while the mapping read fails (past its 8 MiB, say) or
// another entry's project, OTHER, answers 500. The next decision is refused in that same pass, and
// a repository PUB no longer maps is withheld. The control holds nothing of an incomplete pass:
// each row is admitted on the first pass, with every repository.
func TestAnIncompletePassStillAppliesItsRefusals(t *testing.T) {
	for _, row := range []struct {
		name    string
		revoke  func(d *dispatchAnswers)
		failing string // the question that fails in the second pass: "mapping" or "OTHER"
		label   string // the refusal's label, "" for a daemon still admitted
		reason  string
	}{
		{"PUB hidden from its person, the mapping read failing", func(d *dispatchAnswers) { d.hide("PUB", true) }, "mapping",
			"project_not_visible", "project not visible to the account's person: PUB"},
		{"PUB hidden from its person, OTHER answering 500", func(d *dispatchAnswers) { d.hide("PUB", true) }, "OTHER",
			"project_not_visible", "project not visible to the account's person: PUB"},
		{"PUB gone from Dispatch, the mapping read failing", func(d *dispatchAnswers) { d.drop("PUB") }, "mapping",
			"project_not_found", "project not found: PUB"},
		{"PUB gone from Dispatch, OTHER answering 500", func(d *dispatchAnswers) { d.drop("PUB") }, "OTHER",
			"project_not_found", "project not found: PUB"},
		{"a repository PUB no longer maps, the mapping read failing", func(d *dispatchAnswers) { d.set("PUB", "acme/pub") }, "mapping", "", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			now := time.Now()
			services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
			tokens := map[string]platformToken{"pub-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
			dispatch := &dispatchAnswers{
				answers: map[string][]string{"PUB": {"acme/pub", "acme/pub2"}, "OTHER": {"acme/other"}},
				private: map[string]bool{"PUB": true},
			}
			deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB"}, slowDaemonAccount: {"OTHER"}}, dispatch, services)
			decision, err := Decide(t.Context(), deps, services, serviceRequest("pub-token"))
			require.NoError(t, err, "PUB's daemon was refused on the first complete pass")
			require.True(t, holdsLane(decision.Grant, "acme/pub2"))

			row.revoke(dispatch)
			switch row.failing {
			case "mapping":
				dispatch.mu.Lock()
				dispatch.mappingFailing = errors.New("projectaccess: dispatch's answer to /api/v1/settings/repo-projects is over 8388608 bytes")
				dispatch.mu.Unlock()
			case "OTHER":
				deps.held.access = func(ctx context.Context, project, person string) (projectaccess.Answer, error) {
					if project == "OTHER" {
						return projectaccess.Answer{}, errors.New(`projectaccess: dispatch answered /api/v1/access/projects/OTHER 500 with the code ""`)
					}
					return dispatch.access(ctx, project, person)
				}
			}
			deps.held.refresh(t.Context())

			decision, err = Decide(t.Context(), deps, services, serviceRequest("pub-token"))
			if row.label == "" {
				require.NoError(t, err)
				require.True(t, holdsLane(decision.Grant, "acme/pub"), "PUB's daemon lost acme/pub, which PUB still maps")
				require.False(t, holdsLane(decision.Grant, "acme/pub2"), "PUB's daemon is still served acme/pub2, which PUB no longer maps")
				return
			}
			requireRefused(t, err, row.label, row.reason)
		})
	}
}

// A lane withheld as shared stays withheld until a complete pass: an incomplete pass's mapping
// may withhold more (TestTNKnownMappingRevocationAppliesDuringProjectFailure) but gives back
// nothing, since serving a lane again is an addition. PUB maps acme/a and acme/shared, which
// OTHER maps too, so a first complete pass withholds acme/shared from PUB's daemon; then OTHER
// drops it and answers 500 in the same pass. The lane stays withheld, and the next complete pass
// serves it. The control lets the later mapping replace the held one: the incomplete pass serves
// the lane.
func TestAnIncompletePassUnsharesNoLane(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
	tokens := map[string]platformToken{"pub-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/a", "acme/shared"}, "OTHER": {"acme/other", "acme/shared"}}}
	deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB"}, slowDaemonAccount: {"OTHER"}}, dispatch, services)
	decision, err := Decide(t.Context(), deps, services, serviceRequest("pub-token"))
	require.NoError(t, err)
	require.False(t, holdsLane(decision.Grant, "acme/shared"), "the first complete pass served the lane OTHER shares")

	dispatch.set("OTHER", "acme/other")
	access := deps.held.access
	deps.held.access = func(ctx context.Context, project, person string) (projectaccess.Answer, error) {
		if project == "OTHER" {
			return projectaccess.Answer{}, errors.New(`projectaccess: dispatch answered /api/v1/access/projects/OTHER 500 with the code ""`)
		}
		return access(ctx, project, person)
	}
	deps.held.refresh(t.Context())
	decision, err = Decide(t.Context(), deps, services, serviceRequest("pub-token"))
	require.NoError(t, err)
	require.False(t, holdsLane(decision.Grant, "acme/shared"), "an incomplete pass gave back a lane its mapping no longer shows shared")
	require.True(t, holdsLane(decision.Grant, "acme/a"))

	deps.held.access = access
	deps.held.refresh(t.Context())
	decision, err = Decide(t.Context(), deps, services, serviceRequest("pub-token"))
	require.NoError(t, err)
	require.True(t, holdsLane(decision.Grant, "acme/shared"), "the complete pass after OTHER dropped acme/shared did not serve it")
}
