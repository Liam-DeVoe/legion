package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// A pull request event on a branch that is no issue's decodes to the sighting of an outside pull
// request at its head, with GitHub's updated_at of the pull request, by which a sighting delivered
// late is ordered among the others: opened, reopened, or moved by a push. Every other action of
// such a pull request, and one that names no head, is no fact. The decoder reads no verdict
// repository: the consumer acknowledges the sighting of a repository no verdict entry names with
// no transaction (TestLegionVerdictAnOutsidePullRequestIsAFactOnAVerdictRepositoryAlone).
func TestLegionVerdictAPullRequestOnNoIssueBranchDecodesAsOutside(t *testing.T) {
	const subject = "notifications.github.sjawhar.legion.pr.42"
	envelope := func(action, head string) []byte {
		t.Helper()
		payload, err := json.Marshal(map[string]string{
			"kind": "pr", "action": action, "repo": "sjawhar/legion", "number": "42", "title": "a person's change",
			"author": "a-writer", "head_sha": head, "head_ref": "feature/faster-builds", "base_ref": "main", "merged": "false",
			"updated_at": "2026-11-22T18:04:05Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		return envelopeJSON(t, "outside-"+action, "github", string(payload))
	}
	outside := OutsidePullRequest{Repo: "sjawhar/legion", Number: 42, Branch: "feature/faster-builds", HeadSHA: "head-outside",
		UpdatedAt: time.Date(2026, 11, 22, 18, 4, 5, 0, time.UTC)}
	for _, tc := range []struct {
		action, head string
		want         Fact
	}{
		{"opened", "head-outside", outside},
		{"reopened", "head-outside", outside},
		{"synchronize", "head-outside", outside},
		{"synchronize", "", nil},
		{"closed", "head-outside", nil},
		{"dequeued", "head-outside", nil},
		{"edited", "head-outside", nil},
	} {
		t.Run(tc.action+" at "+tc.head, func(t *testing.T) {
			got, err := decodeMessage(subject, "CAPTURE", capturedRepositories, envelope(tc.action, tc.head))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(got.Fact, tc.want) {
				t.Fatalf("fact = %#v, want %#v", got.Fact, tc.want)
			}
		})
	}
}

// reviewInState is the captured review envelope with its payload rewritten to carry a review id
// and state, as reviewRequestingChanges rewrites it.
func reviewInState(t *testing.T, id, state string) []byte {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(capturedGitHubEnvelope(t, "review.json"), &envelope); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(envelope["payload"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	payload["review_id"], payload["state"] = id, state
	text, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope["payload"], envelope["event_id"], envelope["dedupe_key"] = string(text), "review-"+id, "review-"+id
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// On a repository the daemon writes legion-verdict on (ConsumerSpec.VerdictRepositories), a pull
// request no recorded issue owns is a fact: its sighting, and each review of it, an approval's
// author read for write access as a request for changes' is, since a person's approval is that
// head's verdict. A checks settlement of it is still acknowledged with no transaction, as every
// pull request event of a repository no verdict entry names is.
func TestLegionVerdictAnOutsidePullRequestIsAFactOnAVerdictRepositoryAlone(t *testing.T) {
	pool := migratedPool(t)
	js, stream := testJetStream(t)
	spec := consumerSpec(&lockedBuffer{})
	spec.AckWait = time.Minute
	var mu sync.Mutex
	var asked []string
	var applied []Fact
	spec.ReviewPermission = func(_ context.Context, review PullRequestReview) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, review.State)
		return true, nil
	}
	recorder := handlerFunc(func(_ context.Context, _ pgx.Tx, fact Fact) (Result, error) {
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, fact)
		return Result{}, nil
	})
	onOtherBranch := func(name string) []byte {
		t.Helper()
		data := capturedGitHubEnvelope(t, name)
		other := bytes.ReplaceAll(data, []byte("legion/LEGION-208"), []byte("dependabot/go_modules/x"))
		if bytes.Equal(other, data) {
			t.Fatalf("%s names no issue branch to replace", name)
		}
		return other
	}
	const (
		prSubject     = "notifications.github.sjawhar.legion.pr.42"
		reviewSubject = "notifications.github.sjawhar.legion.pr.42.review"
		checksSubject = "notifications.github.sjawhar.legion.pr.42.checks"
	)
	seen := func() ([]Fact, []string) {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(applied), slices.Clone(asked)
	}

	t.Run("a repository no verdict entry names", func(t *testing.T) {
		stop := startConsume(t, js, spec, pool, recorder)
		defer stop()
		assertAcknowledgedThrough(t, stream, githubConsumerName(spec.Project), publish(t, js, prSubject, onOtherBranch("pr-opened.json")))
		assertAcknowledgedThrough(t, stream, githubConsumerName(spec.Project), publish(t, js, reviewSubject, reviewInState(t, "70", "approved")))
		if facts, reads := seen(); len(facts) != 0 || len(reads) != 0 {
			t.Fatalf("applied %#v and read the permission of %v, want nothing of an outside pull request there", facts, reads)
		}
	})

	t.Run("a verdict repository", func(t *testing.T) {
		withVerdicts := spec
		withVerdicts.VerdictRepositories = []ghrepo.Repository{ghrepo.MustParse("SJawhar/Legion")}
		stop := startConsume(t, js, withVerdicts, pool, recorder)
		defer stop()
		for _, event := range []struct {
			subject string
			body    []byte
		}{
			{prSubject, onOtherBranch("pr-synchronized.json")},
			{reviewSubject, reviewInState(t, "71", "approved")},
			{reviewSubject, reviewInState(t, "72", "changes_requested")},
			{reviewSubject, reviewInState(t, "73", "commented")},
			{checksSubject, capturedGitHubEnvelope(t, "checks.json")},
		} {
			assertAcknowledgedThrough(t, stream, githubConsumerName(spec.Project), publish(t, js, event.subject, event.body))
		}
		facts, reads := seen()
		if len(facts) != 4 {
			t.Fatalf("applied %#v, want the sighting and the three reviews, and no checks settlement", facts)
		}
		if sighting, ok := facts[0].(OutsidePullRequest); !ok || sighting.Repo != "sjawhar/legion" || sighting.Number != 42 || sighting.Branch != "dependabot/go_modules/x" || sighting.HeadSHA == "" {
			t.Fatalf("the first fact = %#v, want the outside pull request at its head", facts[0])
		}
		for i, state := range []string{"approved", "changes_requested", "commented"} {
			review, ok := facts[i+1].(OutsideReview)
			if !ok || review.State != state || review.Number != 42 {
				t.Fatalf("fact %d = %#v, want the outside review in state %s", i+1, facts[i+1], state)
			}
			if want := state != "commented"; review.AuthorCanWrite != want {
				t.Fatalf("the %s review's AuthorCanWrite = %v, want %v", state, review.AuthorCanWrite, want)
			}
		}
		if !slices.Equal(reads, []string{"approved", "changes_requested"}) {
			t.Fatalf("the permission was read for %v, want the approval and the request for changes, and no comment", reads)
		}
	})
}
