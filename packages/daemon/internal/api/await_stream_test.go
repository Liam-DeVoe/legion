package api

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/credential"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// An agent's own request that ends its step waits for its claim's machine to handle what the
// worker stream read before it (Options.AwaitStream), and only then applies its fact, so the record
// of the step names the model whose answer came ahead of the request: the tester's completion, the
// architect's sign-off, a phase worker's move back and the architect's move back out of accepting,
// its own step, each wait for the grant's own claim, with no fact of theirs applied yet. A request
// refused before it applies anything waits for nothing: the tester's completion with no verdict,
// and the architect's move back of an issue of another tree.
func TestAnAgentsRequestThatEndsItsStepWaitsForItsStreamFirst(t *testing.T) {
	h := newHarness(t)
	facts := &factRecorder{}
	statuses := &statusRecorder{issues: map[string]dispatch.Issue{"LEGION-208": {Key: "LEGION-208", LastSeq: 7}}}
	type wait struct {
		claim   claim.Token
		applied int
	}
	var mu sync.Mutex
	var waits []wait
	h.handoffs = statuses
	h.handler = NewServer("127.0.0.1", 8437, Options{
		Steps:      config.DesignGateRootIssues.Steps(),
		Supervisor: h.supervisor, BootTokens: h.tokens, Project: testProject, OperatorToken: testOperatorToken,
		Controller: h.store, Grants: credential.New(nil), Pool: h.store.Pool(), Record: record.NewStore(),
		Handlers: []intake.Handler{facts}, Dispatch: statuses, PluginRelaunchable: true,
		Tokens: handoffTokens{}, GitHubAPI: newHandoffGitHub(t).url,
		AwaitStream: func(_ context.Context, token claim.Token) {
			mu.Lock()
			defer mu.Unlock()
			waits = append(waits, wait{claim: token, applied: len(facts.recorded())})
		},
	}).Handler
	seedIssueAt(t, h, "LEGION-208", phase.Testing)
	tester := newLiveClaim(t, h, "LEGION-208", claim.RoleTester)
	written := tester.writeHandoff("test", 1)
	assertFailure(t, h.complete(HandoffCompleteRequest{GrantID: tester.grant(t), Summary: "ran the suite", HandoffID: written, HandoffVersion: 1}),
		http.StatusBadRequest, "TESTER_VERDICT_REQUIRED")
	completion(t, h.complete(HandoffCompleteRequest{GrantID: tester.grant(t), Summary: "tests pass", Verdict: "pass", HandoffID: written, HandoffVersion: 1}))

	architect := newLiveClaim(t, h, "LEGION-208", claim.RoleArchitect)
	if signoff := h.request(http.MethodPost, "/legion/v1/signoff", map[string]any{"grantId": architect.grant(t), "issue": "LEGION-208"}, nil); signoff.Code != http.StatusOK {
		t.Fatalf("signoff = %d: %s", signoff.Code, signoff.Body)
	}
	if backward := h.request(http.MethodPost, "/legion/v1/phase/backward", map[string]any{
		"grantId": tester.grant(t), "to": "implementing", "reason": "the suite needs a fix",
	}, nil); backward.Code != http.StatusOK {
		t.Fatalf("phase backward = %d: %s", backward.Code, backward.Body)
	}
	seedIssueAt(t, h, "LEGION-209", phase.Accepting)
	assertFailure(t, h.request(http.MethodPost, "/legion/v1/phase/backward", map[string]any{
		"grantId": architect.grant(t), "issue": "LEGION-209", "to": "implementing", "reason": "not this architect's tree",
	}, nil), http.StatusForbidden, "ISSUE_OUTSIDE_TREE")
	seedIssueAt(t, h, "LEGION-208", phase.Accepting)
	if backward := h.request(http.MethodPost, "/legion/v1/phase/backward", map[string]any{
		"grantId": architect.grant(t), "issue": "LEGION-208", "to": "implementing", "reason": "the acceptance found a gap",
	}, nil); backward.Code != http.StatusOK {
		t.Fatalf("the architect's phase backward = %d: %s", backward.Code, backward.Body)
	}

	mu.Lock()
	defer mu.Unlock()
	if want := []wait{{tester.token, 0}, {architect.token, 1}, {tester.token, 2}, {architect.token, 3}}; !slices.Equal(waits, want) {
		t.Fatalf("the waits made = %+v, want the tester's before its completion applied, the architect's before its sign-off, the tester's before its move back, the architect's before its move back out of accepting", waits)
	}
}
