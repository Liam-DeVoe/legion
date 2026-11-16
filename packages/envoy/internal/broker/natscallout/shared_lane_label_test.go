package natscallout

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
)

// A repository no GitHub lane can be written for is refused under grant_not_holdable even when a
// shared lane was withheld beside it. Withholding a repository never makes a holdable scope
// unholdable: a held entry always names a project (len(projects) == 0 is refused before), and
// natsgrant.WithinProjects refuses a scope only for a bad project key, a repository that is not
// owner/name, or repositories with no project. So the refusal is the unholdable repository's, and
// counting it under shared_lane sends an operator to look for a second project's mapping that is
// not why the connection was refused.
func TestAnUnholdableRepositoryBesideASharedLaneIsGrantNotHoldable(t *testing.T) {
	now := time.Now()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}}
	tokens := map[string]platformToken{"pub-daemon-token": {account: pubDaemonAccount, issued: now, expiry: now.Add(time.Hour)}}
	dispatch := &dispatchAnswers{
		answers: map[string][]string{"PUB": {"acme/a.b", "not-a-repository"}},
		mapping: []projectaccess.RepoProject{
			{Repo: "acme/a.b", Project: "PUB"},
			{Repo: "acme/a_b", Project: "OTHER"},
			{Repo: "not-a-repository", Project: "PUB"},
		},
	}
	deps := heldDeps(t, tokens, map[string][]string{pubDaemonAccount: {"PUB"}}, dispatch, services)

	_, err := Decide(t.Context(), deps, services, serviceRequest("pub-daemon-token"))
	requireRefused(t, err, labelGrantNotHoldable, "grant cannot be held to its projects: service daemon/"+pubDaemonAccount+`: natsgrant: scope repository "not-a-repository" is not owner/name`)
}
