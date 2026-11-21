package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubrest"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// TestTheBaseRulesLineNamesGitHubsContextsAsTheRuleReadsThem: GitHub, or whatever answers in its
// place, names among a ruleset's required contexts one that echoes the installation token the
// read carried and quotes the probe (echoedWords), and a classic protection's context that does
// too. The line the read logs names each context as the rule reads it: none of the token or the
// probe's credentials, and the rest of the context. The rules keep each context as it came, which
// R0 decides on (classify.ConflictNeedsMerge). The line printed the contexts as they came.
// Captures daemon.answer-words/base-rules.
func TestTheBaseRulesLineNamesGitHubsContextsAsTheRuleReadsThem(t *testing.T) {
	classic := "build " + echoedWords
	server, _ := rulesStandIn(t, map[string]rulesAnswer{
		"/repos/acme/widgets/rules/branches/main": {body: `[{"type":"pull_request","parameters":{"required_approving_review_count":1}},` +
			`{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":` + fmt.Sprintf("%q", echoedWords) + `},{"context":"lint"}],"strict_required_status_checks_policy":true}}]`},
		"/repos/acme/widgets/branches/main": {body: `{"name":"main","protected":true,"protection":{"enabled":true,"required_status_checks":` +
			`{"enforcement_level":"non_admins","contexts":[` + fmt.Sprintf("%q", classic) + `],"checks":[{"context":"lint","app_id":null}]}}}`},
	})
	var logs bytes.Buffer
	w := &workflowRuntime{log: slog.New(slog.NewJSONHandler(&logs, nil))}
	github := githubrest.Client{Token: echoedToken, API: githubrest.RepositoryAPI(server.URL, ghrepo.MustParse("acme/widgets"))}

	rules := w.readBaseRules(context.Background(), github, record.PullRequest{Repo: "acme/widgets", Number: 86}, "main")

	// Control: the rules keep each context as it came, the echoed ones among them, so the line's
	// contexts are the ones R0 decides on.
	if want := []string{classic, "lint", echoedWords}; !slices.Equal(rules.Required, want) || !rules.Strict || rules.Unread != "" {
		t.Fatalf("the read came to %+v, want required %q as they came, strict, and read", rules, want)
	}
	lines := loggedLines(t, logs.String(), rulesReadLine)
	if len(lines) != 1 {
		t.Fatalf("the read logged %v, want one line of the rules it read", lines)
	}
	line := fmt.Sprint(lines[0]["required"])
	requireHolds(t, line, "build", "refused;", "lint")
	requireNoEcho(t, "daemon.answer-words/base-rules", line)
}
