package oauth

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"authelia.com/provider/oauth2"

	"github.com/sjawhar/envoy/internal/broker/exchange"
	"github.com/sjawhar/envoy/internal/broker/oauthstore"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
)

// TestEveryProjectScopeIsWrittenAsItsOwnWordAndNoOtherIsWritten holds writeProjectScope, the one
// switch decide writes a token's project scope through, to one word per value of
// exchange.ProjectScope. Each known value writes its word, the project only for a named token and
// a results token, the run only for a results token, the OAuth scope read only for the none
// scope and results only for the results scope; each session starts holding a project and a run
// from an earlier decision, so a scope that names none is seen to drop them. A value the switch
// does not know, the zero value and one past the last included, is a server error that names the
// value, and so are a run beside any scope but results and a results decision with no run; after
// each refusal the session is left as it was: no project scope, no project, no run and no scope
// granted. No path through decide can hand it such a decision today (ExchangeConfig.Policy is a
// concrete *exchange.Set, every decision of which sets one of the four values, and a run exactly
// beside results), so this test is the only thing that reaches the refusals, and it is what stops
// one from falling back to the person scope as the switch did before the none scope existed.
func TestEveryProjectScopeIsWrittenAsItsOwnWordAndNoOtherIsWritten(t *testing.T) {
	const project, stale = "ACME", "STALE"
	run := oauthwire.CIRun{
		Repository: "example-org/example-repo", WorkflowRef: "example-org/example-repo/.github/workflows/ci-results.yaml@refs/heads/topic",
		Ref: "refs/heads/topic", SHA: "0123456789abcdef0123456789abcdef01234567", EventName: "push",
		RunID: "7", RunAttempt: "1", RunnerEnvironment: "github-hosted", CheckRunID: "97",
	}
	for _, c := range []struct {
		name    string
		scope   exchange.ProjectScope
		word    string
		project string
		run     *oauthwire.CIRun
		granted []string
	}{
		{name: "named", scope: exchange.ProjectScopeNamed, word: oauthwire.ProjectScopeNamed, project: project},
		{name: "person", scope: exchange.ProjectScopePerson, word: oauthwire.ProjectScopePerson},
		{name: "none", scope: exchange.ProjectScopeNone, word: oauthwire.ProjectScopeNone, granted: []string{oauthwire.ScopeRead}},
		{name: "results", scope: exchange.ProjectScopeResults, word: oauthwire.ProjectScopeResults, project: project, run: &run,
			granted: []string{oauthwire.ScopeResults}},
	} {
		t.Run(c.name, func(t *testing.T) {
			session := oauthstore.NewExchangeSession()
			session.Extra[ClaimProject] = stale
			session.Extra[ClaimCI] = run
			request := &oauth2.Request{Session: session}
			claims := exchange.Claims{Scope: c.scope, CI: c.run}
			if c.project != "" {
				claims.Project = project
			}
			if err := writeProjectScope(request, session, claims); err != nil {
				t.Fatalf("writeProjectScope(%v) = %v, want it written", c.scope, err)
			}
			if got := session.Extra[ClaimProjectScope]; got != c.word {
				t.Errorf("%s = %v, want %q", ClaimProjectScope, got, c.word)
			}
			got, named := session.Extra[ClaimProject]
			if c.project == "" && named {
				t.Errorf("%s = %v, want it absent: the %s scope names no project", ClaimProject, got, c.word)
			}
			if c.project != "" && got != c.project {
				t.Errorf("%s = %v, want %q", ClaimProject, got, c.project)
			}
			written, carried := session.Extra[ClaimCI]
			if c.run == nil && carried {
				t.Errorf("%s = %v, want it absent: the %s scope carries no run", ClaimCI, written, c.word)
			}
			if c.run != nil && written != *c.run {
				t.Errorf("%s = %v, want the run %+v", ClaimCI, written, *c.run)
			}
			if granted := []string(request.GetGrantedScopes()); !slices.Equal(granted, c.granted) {
				t.Errorf("granted scopes = %q, want %q: the read and the result entries' decisions grant their one scope and nothing else does", granted, c.granted)
			}
		})
	}
	for _, c := range []struct {
		name   string
		claims exchange.Claims
		debug  string
	}{
		{name: "unknown 0", claims: exchange.Claims{Scope: exchange.ProjectScopeUnset, Project: project}, debug: "project scope 0"},
		{name: "unknown 99", claims: exchange.Claims{Scope: exchange.ProjectScope(99), Project: project}, debug: "project scope 99"},
		{name: "a run beside the named scope", claims: exchange.Claims{Scope: exchange.ProjectScopeNamed, Project: project, CI: &run},
			debug: fmt.Sprintf("project scope %d (%s) with a run (true)", int(exchange.ProjectScopeNamed), exchange.ProjectScopeNamed)},
		{name: "the results scope with no run", claims: exchange.Claims{Scope: exchange.ProjectScopeResults, Project: project},
			debug: fmt.Sprintf("project scope %d (%s) with a run (false)", int(exchange.ProjectScopeResults), exchange.ProjectScopeResults)},
	} {
		t.Run(c.name, func(t *testing.T) {
			session := oauthstore.NewExchangeSession()
			request := &oauth2.Request{Session: session}
			err := writeProjectScope(request, session, c.claims)
			if !errors.Is(err, oauth2.ErrServerError) {
				t.Fatalf("writeProjectScope(%s) = %v, want a server error", c.name, err)
			}
			var refusal *oauth2.RFC6749Error
			if !errors.As(err, &refusal) || !strings.Contains(refusal.Debug(), c.debug) {
				t.Errorf("writeProjectScope(%s) refused with %v, whose debug text does not say %q", c.name, err, c.debug)
			}
			for _, claim := range []string{ClaimProjectScope, ClaimProject, ClaimCI} {
				if got, written := session.Extra[claim]; written {
					t.Errorf("%s = %v after the refusal, want nothing written", claim, got)
				}
			}
			if granted := request.GetGrantedScopes(); len(granted) != 0 {
				t.Errorf("granted scopes = %q after the refusal, want none", granted)
			}
		})
	}
}
