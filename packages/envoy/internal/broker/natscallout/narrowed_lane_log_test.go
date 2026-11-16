package natscallout

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
)

// A lane an incomplete pass's mapping shows a project outside the entry mapping is withheld at
// once (narrow), and the operator is told which lane and which projects, as a complete pass tells
// it ("logged at error with the lane and its projects once a pass", pr4197-held-services.yaml).
// Seen on the real stack at c40030fd3d: OTHERP answering 500 and its acme/oldshared renamed to
// PUBX's acme/shared, PUBX's daemon lost acme/shared in the first pass and
// broker_nats_callout_shared_lanes_dropped_total rose, while neither broker logged an error line
// naming the lane for the minute it stayed withheld.
func TestALaneAnIncompletePassWithholdsIsLogged(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: slowDaemonAccount}}
	tokens := map[string]platformToken{"pub": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/shared", "acme/kept"}, "OTHER": {"acme/other"}}}
	var logged bytes.Buffer
	deps := Deps{
		ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB"}, slowDaemonAccount: {"OTHER"}}),
		Access: dispatch.access, RepoProjects: dispatch.repoProjects,
		Log: slog.New(slog.NewTextHandler(&logged, nil)),
	}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	deps.held.refresh(t.Context())
	logged.Reset()

	renamed := "acme/shared"
	dispatch.mu.Lock()
	dispatch.mapping = []projectaccess.RepoProject{
		{Repo: "acme/shared", Project: "PUB"}, {Repo: "acme/kept", Project: "PUB"},
		{Repo: "acme/other", Project: "OTHER"}, {Repo: "acme/oldshared", Project: "OTHER", RenamedTo: &renamed},
	}
	dispatch.mu.Unlock()
	deps.held.access = func(ctx context.Context, project, person string) (projectaccess.Answer, error) {
		if project == "OTHER" {
			return projectaccess.Answer{}, errors.New("projectaccess: dispatch answered /api/v1/access/projects/OTHER 500")
		}
		return dispatch.access(ctx, project, person)
	}
	deps.held.refresh(t.Context())

	got, err := Decide(t.Context(), deps, services, serviceRequest("pub"))
	require.NoError(t, err)
	require.False(t, holdsLane(got.Grant, "acme/shared"), "the incomplete pass did not withhold acme/shared")
	errorLines := 0
	for line := range strings.Lines(logged.String()) {
		if strings.Contains(line, "level=ERROR") && strings.Contains(line, "notifications.github.acme.shared") {
			errorLines++
		}
	}
	require.Equal(t, 1, errorLines, "want one error line naming the withheld lane and its projects:\n%s", logged.String())
}
