package natscallout

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
)

func TestTNOneProjectRefusingBrokerRefusesEveryHeldDaemon(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{
		{Kind: KindDaemon, Account: pubDaemonAccount},
		{Kind: KindDaemon, Account: slowDaemonAccount},
	}
	dispatch := &dispatchAnswers{answers: map[string][]string{
		"PUB": {"acme/pub"}, "OTHER": {"acme/other"},
	}}
	deps := heldDeps(t,
		map[string]platformToken{"token": {
			account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour),
		}},
		map[string][]string{pubDaemonAccount: {"PUB"}, slowDaemonAccount: {"OTHER"}},
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

func TestTNMappingRefusalBesideFailedProjectMustRefuse(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	dispatch := &dispatchAnswers{answers: map[string][]string{"PUB": {"acme/pub"}}}
	deps := heldDeps(t,
		map[string]platformToken{"token": {
			account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour),
		}},
		map[string][]string{pubDaemonAccount: {"PUB"}}, dispatch, services)
	deps.held.repoProjects = func(context.Context) ([]projectaccess.RepoProject, error) {
		return nil, projectaccess.ErrBrokerRefused
	}
	deps.held.access = func(context.Context, string, string) (projectaccess.Answer, error) {
		return projectaccess.Answer{}, errors.New("connection reset")
	}
	deps.held.refresh(t.Context())
	_, err := Decide(t.Context(), deps, services, serviceRequest("token"))
	requireRefused(t, err, labelAccessRefused, "Dispatch refuses the broker's own account token")
}
