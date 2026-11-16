package natscallout

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
)

func TestTNKnownMappingRevocationAppliesDuringProjectFailure(t *testing.T) {
	for _, moved := range []bool{false, true} {
		name := "new external shared lane"
		if moved {
			name = "whole mapping moved outside entry"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			services := []ServiceEntry{
				{Kind: KindDaemon, Account: pubDaemonAccount},
				{Kind: KindDaemon, Account: slowDaemonAccount},
			}
			dispatch := &dispatchAnswers{answers: map[string][]string{
				"PUB": {"acme/shared", "acme/kept"}, "OTHER": {"acme/other"},
			}}
			deps := heldDeps(t, map[string]platformToken{"pub": {
				account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour),
			}}, map[string][]string{
				pubDaemonAccount: {"PUB"}, slowDaemonAccount: {"OTHER"},
			}, dispatch, services)
			before, err := Decide(t.Context(), deps, services, serviceRequest("pub"))
			if err != nil || !holdsLane(before.Grant, "acme/shared") {
				t.Fatalf("no initial lane: %v", err)
			}
			dispatch.mu.Lock()
			dispatch.mapping = []projectaccess.RepoProject{
				{Repo: "acme/kept", Project: "PUB"},
				{Repo: "ACME/SHARED", Project: "OTHER"},
				{Repo: "acme/other", Project: "OTHER"},
			}
			if !moved {
				dispatch.mapping = append(dispatch.mapping,
					projectaccess.RepoProject{Repo: "acme/shared", Project: "PUB"})
			}
			dispatch.mu.Unlock()
			deps.held.access = func(ctx context.Context, project, person string) (projectaccess.Answer, error) {
				if (moved && project == "PUB") || (!moved && project == "OTHER") {
					return projectaccess.Answer{}, errors.New("project 500")
				}
				return dispatch.access(ctx, project, person)
			}
			deps.held.refresh(t.Context())
			got, err := Decide(t.Context(), deps, services, serviceRequest("pub"))
			if err != nil {
				t.Fatal(err)
			}
			if holdsLane(got.Grant, "acme/shared") {
				t.Fatal("the successful complete mapping read puts acme/shared outside PUB, but the incomplete pass still grants its lane")
			}
			if !holdsLane(got.Grant, "acme/kept") {
				t.Fatal("unrelated own lane was removed")
			}
		})
	}
}
