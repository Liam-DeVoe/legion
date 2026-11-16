package natscallout

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
)

// A pass in which Dispatch refuses the broker's own token on one question and another question
// fails is held as refused (every held daemon is refused project_access_refused), so no line of
// it may tell the operator that the last complete pass is kept: that is the opposite of what the
// broker does. Seen on the real stack at a2b85a4579: a 401 about one project beside a mapping
// read whose connection closed logged "Dispatch's repository mapping could not be read, so the
// last complete pass is kept" at WARNING while every held daemon was refused.
func TestARefusedPassLogsNoKeptPass(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}}}
	tokens := map[string]platformToken{"token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	var logged bytes.Buffer
	deps := Deps{
		ServiceToken: serviceTokens(tokens), Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB"}}),
		Access: dispatch.access, RepoProjects: dispatch.repoProjects,
		Log: slog.New(slog.NewTextHandler(&logged, nil)),
	}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	deps.held.refresh(t.Context())
	logged.Reset()

	deps.held.access = func(context.Context, string, string) (projectaccess.Answer, error) {
		return projectaccess.Answer{}, projectaccess.ErrBrokerRefused
	}
	deps.held.repoProjects = func(context.Context) ([]projectaccess.RepoProject, error) {
		return nil, errors.New("projectaccess: ask dispatch: EOF")
	}
	deps.held.refresh(t.Context())

	_, err := Decide(t.Context(), deps, services, serviceRequest("token"))
	requireRefused(t, err, labelAccessRefused, "Dispatch refuses the broker's own account token")
	require.NotContains(t, logged.String(), "last complete pass is kept",
		"a refused pass was logged as keeping the last complete pass:\n%s", logged.String())
}
