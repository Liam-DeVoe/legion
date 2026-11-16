package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/testnats"
)

// The pull request a `before_merge` check compares its results with is the one GitHub's own
// event reports, in a deployment that holds no delivery settings (dispatch://INFRA-3992).
//
// The case is a pull request opened after its issue's results were recorded: not a draft, in the
// issue's own repository, which is mapped to the issue's project but is not the project's
// default, with a branch and a body that name the issue. Nobody links it to the issue by hand.
// Envoy relays GitHub's `pull_request` event for it, as it does for every repository the App's
// webhook covers, and the delivery intake is the one writer that turns that event into the head
// row the results read compares with. A deployment that never configured the delivery timeline
// holds no delivery settings, and the head of every pull request is still the basis of every
// `before_merge` result there, so the intake must write it all the same.
//
// GitHub's commit-to-pull-request answer names nothing here, at the record and at every read,
// which is what the results read's own read-through got for the commit in the deployment this
// issue was found in: so the event is the only way the head can reach Dispatch, and the test
// proves that way and no other.
func TestAPullRequestEventGivesTheResultsReadItsHeadWithNoDeliverySettings(t *testing.T) {
	handler, database, served, fake := readThroughGitHub(t, "example/tools")
	if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects",
		map[string]string{"key": "ACME", "name": "Acme"}, "alice"); response.Code != http.StatusCreated {
		t.Fatalf("create project ACME: status=%d body=%s", response.Code, response.Body.String())
	}
	for repo, isDefault := range map[string]bool{"example/default": true, "example/tools": false} {
		if response := dispatchRequest(t, handler, http.MethodPut, "/api/v1/settings/repo-projects/"+repo,
			map[string]any{"project": "ACME", "default": isDefault}, "alice"); response.Code != http.StatusOK {
			t.Fatalf("map %s to ACME: status=%d body=%s", repo, response.Code, response.Body.String())
		}
	}
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues",
		map[string]any{"project": "ACME", "title": "The tools change", "spec": "A spec\n", "repo": "example/tools"}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("create the issue in example/tools: status=%d body=%s", created.Code, created.Body.String())
	}
	issue := decodeBody[model.Issue](t, created)
	if issue.ResolvedRepo == nil || *issue.ResolvedRepo != "example/tools" {
		t.Fatalf("the issue resolves to %v, want its own example/tools, which is not its project's default", issue.ResolvedRepo)
	}
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", twoChecks)
	recorded := decodeBody[recordedResult](t, recordResult(t, handler, issue.Key, resultBody("k1", "pass", headCommit, nil)))
	if _, err := delivery.GetSettings(context.Background(), database.Pool); err == nil {
		t.Fatal("the deployment holds delivery settings, want none: the case is a deployment that never configured the delivery timeline")
	}

	_, natsURL := testnats.Start(t)
	publisher, err := bus.ConnectOwningStream([]string{natsURL})
	if err != nil {
		t.Fatalf("connect the publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	callers, err := database.Pool.Leases()
	if err != nil {
		t.Fatalf("open the lease pool: %v", err)
	}
	leases, err := delivery.ReserveIntakeLeases(callers)
	if err != nil {
		t.Fatalf("reserve the intakes' callers of the lease pool: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	stopIntakes, err := delivery.StartIntakes(ctx, func(name string) (*bus.Client, error) {
		return bus.Connect([]string{natsURL}, bus.WithName(name))
	}, database.Pool, leases, served.deps.GitHub, served.deps.Events, nil)
	if err != nil {
		cancel()
		t.Fatalf("start the intakes: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		stopIntakes()
	})

	payload, err := json.Marshal(map[string]string{
		"kind": "pr", "action": "opened", "repo": "example/tools", "number": "7",
		"title":    "The tools change (" + issue.Key + ")",
		"body":     "Dispatch: " + issue.Key + "\n\nThe change.",
		"url":      "https://github.com/example/tools/pull/7",
		"author":   "implementer[bot]",
		"head_sha": headCommit, "head_ref": "legion/" + issue.Key, "head_repo": "example/tools",
		"base_ref": "main", "merged": "false", "updated_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("encode the pull request's payload: %v", err)
	}
	if err := publisher.Publish(contracts.Envelope{
		EventID: "example-tools-pr-7-opened", Source: "github", SourceEventID: "example-tools-pr-7-opened",
		TraceID: "example-tools-pr-7-opened", Topic: contracts.GithubResourceSubject("example", "tools", "pr", "7"),
		Payload: string(payload), IssuedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("publish the pull request's opening: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	read := readResults(t, handler, issue.Key)
	for len(checkOf(t, read, "k1").ComparedWith) == 0 {
		if time.Now().After(deadline) {
			check := checkOf(t, read, "k1")
			t.Fatalf("30 s after the pull request's event, check k1 compares with %+v for the reason %q and "+
				"the delivery durable is %s; want the head of example/tools#7, which the event reported",
				check.ComparedWith, check.ComparedNothing, durableState(publisher))
		}
		time.Sleep(100 * time.Millisecond)
		read = readResults(t, handler, issue.Key)
	}
	stands := standsAt(t, read, "k1", recorded.Result.ID,
		"a result at the head of the pull request the event reported", model.CommitAtBasis, false)
	if stands.Basis == nil || stands.Basis.PullRequest == nil || *stands.Basis.PullRequest != 7 {
		t.Fatalf("the result stands at %+v, want the head of pull request 7", stands.Basis)
	}
	if stood := checkOf(t, read, "k1").PassStands; stood.Answer != model.PassStandsYes {
		t.Fatalf("the check answers pass_stands %+v, want yes", stood)
	}
	if asked := fake.asks("example/tools"); asked > 2 {
		t.Fatalf("GitHub was asked %d times for the commit's pull requests, want the record's read-through and at most one read's: the head came from the event", asked)
	}
}

// durableState says what the NATS server holds of the delivery intake's durable, for the
// failure above: absent, unbound or bound, so a red run names whether the intake ever took the
// event at all.
func durableState(client *bus.Client) string {
	info, err := client.JS().ConsumerInfo(bus.Stream, bus.DispatchDeliveryDurable)
	if err != nil {
		return fmt.Sprintf("not on the server (%v)", err)
	}
	return fmt.Sprintf("bound=%v, delivered through stream sequence %d, acknowledged through %d",
		info.PushBound, info.Delivered.Stream, info.AckFloor.Stream)
}
