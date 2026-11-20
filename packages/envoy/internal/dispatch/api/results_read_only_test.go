package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/brokertoken/brokertokentest"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/githubtest"
)

// TestAReadOnlyResultsReadAnswersAsAWriterInEveryState is the results row's state table (plan
// D26): GET /api/v1/issues/{key}/results driven through each state on two stacks fed the same
// stand-in GitHub script, one read by a writing token and one by a read-only token of the same
// identity, read after read on one clock. Each read asserts that the two stacks answer the same
// body and make the same GitHub calls, that the read-only stack holds no pull_request_heads or
// branch_commits row the writer's stack did not hold before its first read and records no event,
// and that the writer answers what the table's last column says, which is today's writer
// (parityPair.read).
//
// Rows 2 to 4 are one issue read three times, and rows 7 and 8 another read three times: each
// of those rows is the state the row before it left.
func TestAReadOnlyResultsReadAnswersAsAWriterInEveryState(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate fixture key: %v", err)
	}
	appKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))

	t.Run("1 the result's commit is the head of a pull request Dispatch stores", func(t *testing.T) {
		pair := newParityPair(t, appKey)
		issue := pair.issue(t, "ROA", parityBeforeMergeCheck)
		learned := pair.clock.now().Add(-time.Hour)
		pair.each(func(stack *parityStack) {
			storeParityHead(t, stack.rig.database, 41, headCommit, issue, learned)
		})
		pair.record(t, issue, "k1", headCommit)

		read := pair.read(t, issue, "the first read")
		stands := standsAt(t, read.writer, "k1", pair.writer.ids[0], "the writer's result at a stored head", model.CommitAtBasis, false)
		if stands.Basis == nil || stands.Basis.PullRequest == nil || *stands.Basis.PullRequest != 41 {
			t.Errorf("the writer stands the result at %+v, want pull request 41", stands.Basis)
		}
		read.callsNone(t)
	})

	// The issue's check k1 holds a result at behindHeadCommit, which no stored pull request heads,
	// and its sibling check k3 a result at headCommit, the head of the issue's pull request 61,
	// which no stored row holds. GitHub names pull request 61 for either commit.
	pushed := newParityPair(t, appKey)
	pushedIssue := pushed.issue(t, "ROB", siblingChecks)
	pushed.record(t, pushedIssue, "k1", behindHeadCommit)
	pushed.record(t, pushedIssue, "k3", headCommit)
	pushed.github.answersPulls(func(string) string { return parityOpenPull(61, headCommit, pushedIssue) })
	var pushedSecond parityStep

	t.Run("2 a result at a commit no stored pull request heads, beside the issue's head no stored row holds", func(t *testing.T) {
		read := pushed.read(t, pushedIssue, "the first read")
		// The writer asks GitHub for behindHeadCommit and stores pull request 61, whose head is
		// another commit, so it reads the bases no further; headCommit is then read from the
		// stored rows alone, which answer that a pull request heads it, so it asks GitHub
		// nothing for it. Both results are at another commit.
		for index, check := range []string{"k1", "k3"} {
			standsAt(t, read.writer, check, pushed.writer.ids[index], "the writer's result of "+check, model.CommitOtherCommit, false)
			if compared := checkOf(t, read.writer, check); compared.ComparedNothing != model.ReasonNoPullRequestOrBranch {
				t.Errorf("the writer's check %s compared with %+v (%q), want nothing, %q",
					check, compared.ComparedWith, compared.ComparedNothing, model.ReasonNoPullRequestOrBranch)
			}
		}
		read.calls(t, parityPulls, 1)
	})

	t.Run("3 as row 2, read again inside the window", func(t *testing.T) {
		pushed.clock.advance(time.Minute)
		pushedSecond = pushed.read(t, pushedIssue, "the second read")
		pushedSecond.callsNone(t)
	})

	t.Run("4 as row 3, read after delivery.CommitPullSettledFor", func(t *testing.T) {
		pushed.clock.advance(delivery.CommitPullSettledFor)
		read := pushed.read(t, pushedIssue, "the third read")
		if read.writerBody != pushedSecond.writerBody {
			t.Errorf("the writer's third read answered\n%s\nwant its second read's answer\n%s", read.writerBody, pushedSecond.writerBody)
		}
		// The settled span of behindHeadCommit has ended, so the writer asks GitHub for it
		// again, and the repository's own read has gone stale with it.
		read.calls(t, parityPulls, 1)
		read.calls(t, parityRepository, 1)
	})

	t.Run("5 an after_merge check whose first read found the commit on the named branch", func(t *testing.T) {
		pair := newParityPair(t, appKey)
		issue := pair.issue(t, "ROC", parityAfterMergeCheck)
		pair.each(func(stack *parityStack) { recordMerge(t, stack.rig.database, parityRepo, 51, issue, "main") })
		pair.github.answersCompare(func(base, head string) (int, string) {
			if base == "main" && head == mergeCommit {
				return http.StatusOK, "behind"
			}
			return http.StatusOK, "diverged"
		})
		pair.record(t, issue, "m1", mergeCommit)

		first := pair.read(t, issue, "the first read")
		standsAtBranch(t, first.writer, "m1", pair.writer.ids[0], "main")
		first.calls(t, parityCompare, 1)

		pair.clock.advance(time.Minute)
		second := pair.read(t, issue, "the second read")
		standsAtBranch(t, second.writer, "m1", pair.writer.ids[0], "main")
		second.callsNone(t)
	})

	t.Run("6 an after_merge check of a branch not the default, held as unseen after a kept yes", func(t *testing.T) {
		pair := newParityPair(t, appKey)
		issue := pair.issue(t, "ROD", parityAfterMergeCheck)
		pair.each(func(stack *parityStack) { recordMerge(t, stack.rig.database, parityRepo, 52, issue, "integration/x") })
		pair.github.answersCompare(func(base, head string) (int, string) {
			if base == "integration/x" && head == integrationCommit {
				return http.StatusOK, "behind"
			}
			return http.StatusOK, "diverged"
		})
		pair.record(t, issue, "m1", integrationCommit)

		first := pair.read(t, issue, "the first read")
		standsAtBranch(t, first.writer, "m1", pair.writer.ids[0], "integration/x")
		first.calls(t, parityCompare, 1)

		// integration/x is deleted, so GitHub answers every compare read against it 404. A newer
		// result at another commit is read first, and its compare read against integration/x
		// holds that branch as unseen for the rest of the window (markUnseen). A writer's
		// stored yes is read before that hold, so integrationCommit is asked of GitHub nothing.
		pair.github.answersCompare(func(base, head string) (int, string) {
			if base == "integration/x" {
				return http.StatusNotFound, ""
			}
			return http.StatusOK, "diverged"
		})
		pair.record(t, issue, "m1", strayCommit)
		pair.clock.advance(time.Minute)
		second := pair.read(t, issue, "the second read")
		standsAtBranch(t, second.writer, "m1", pair.writer.ids[0], "integration/x")
		standsAt(t, second.writer, "m1", pair.writer.ids[1], "the writer's newer result on no branch", model.CommitOtherCommit, false)
		// The newer result's two compare reads, and none for integrationCommit.
		second.calls(t, parityCompare, 2)
	})

	// A default_branch check whose repository's own read is made by a read before any result is
	// recorded, so that read goes stale two minutes before the answers the next read keeps.
	trunk := newParityPair(t, appKey)
	trunkIssue := trunk.issue(t, "ROE", parityDefaultBranchCheck)
	trunk.github.answersCompare(func(base, head string) (int, string) {
		if base == "main" && head == mergeCommit {
			return http.StatusOK, "behind"
		}
		return http.StatusOK, "diverged"
	})

	t.Run("7 a default_branch check: a kept yes, then the repository's own read unreadable inside the window", func(t *testing.T) {
		trunk.read(t, trunkIssue, "the read before any result").calls(t, parityRepository, 1)
		trunk.record(t, trunkIssue, "d1", mergeCommit)
		trunk.record(t, trunkIssue, "d1", strayCommit)
		trunk.clock.advance(2 * time.Minute)
		first := trunk.read(t, trunkIssue, "the first read")
		standsAtBranch(t, first.writer, "d1", trunk.writer.ids[0], "main")
		standsAt(t, first.writer, "d1", trunk.writer.ids[1], "the writer's result off the trunk", model.CommitOtherCommit, false)
		first.calls(t, parityCompare, 2)

		// The App loses the repository. The repository's own read, made three minutes before
		// the first read's compare reads, goes stale and is asked again, and is refused.
		trunk.github.loseRepository()
		trunk.clock.advance(3 * time.Minute)
		second := trunk.read(t, trunkIssue, "the second read")
		standsAtBranch(t, second.writer, "d1", trunk.writer.ids[0], "main")
		// The stored no is still inside its window, and is read before the unreadable exit too.
		standsAt(t, second.writer, "d1", trunk.writer.ids[1], "the writer's result off the trunk", model.CommitOtherCommit, false)
		second.calls(t, parityRepository, 1)
		second.calls(t, parityCompare, 0)
	})

	t.Run("8 a default_branch check: a kept yes read after BranchCommitStaleAfter, the repository unreadable", func(t *testing.T) {
		trunk.clock.advance(3 * time.Minute)
		third := trunk.read(t, trunkIssue, "the third read")
		// A writer's stored yes never goes stale (delivery.Branches.readCommitOnBranch), so the
		// yes the first read learned five minutes and more ago still places its commit; the no
		// has gone stale, and with the repository unreadable nothing can be asked.
		standsAtBranch(t, third.writer, "d1", trunk.writer.ids[0], "main")
		stale := standsAt(t, third.writer, "d1", trunk.writer.ids[1], "the writer's result whose no went stale", model.CommitUnknown, false)
		if stale.Reason != model.ReasonRepositoryUnreadable {
			t.Errorf("the writer's result whose no went stale is unknown for %q, want %q", stale.Reason, model.ReasonRepositoryUnreadable)
		}
		third.calls(t, parityCompare, 0)
	})

	t.Run("9 a default_branch check: nothing kept, the compare read refused as unseen", func(t *testing.T) {
		for _, refused := range []struct {
			what       string
			unreadable bool
			reason     string
			compares   int
		}{
			{"the repository readable", false, model.ReasonReadFailed, 1},
			{"the repository unreadable", true, model.ReasonRepositoryUnreadable, 0},
		} {
			pair := newParityPair(t, appKey)
			issue := pair.issue(t, "ROF", parityDefaultBranchCheck)
			pair.github.answersCompare(func(string, string) (int, string) { return http.StatusNotFound, "" })
			pair.record(t, issue, "d1", mergeCommit)
			if refused.unreadable {
				pair.github.loseRepository()
			}
			read := pair.read(t, issue, "the first read, "+refused.what)
			stands := standsAt(t, read.writer, "d1", pair.writer.ids[0], "the writer's result, "+refused.what, model.CommitUnknown, false)
			if stands.Reason != refused.reason {
				t.Errorf("the writer's result, %s, is unknown for %q, want %q", refused.what, stands.Reason, refused.reason)
			}
			read.calls(t, parityCompare, refused.compares)
		}
	})

	t.Run("10 a default_branch check: nothing kept, the compare read fails", func(t *testing.T) {
		pair := newParityPair(t, appKey)
		issue := pair.issue(t, "ROG", parityDefaultBranchCheck)
		pair.github.answersCompare(func(string, string) (int, string) { return http.StatusBadGateway, "" })
		pair.record(t, issue, "d1", mergeCommit)
		read := pair.read(t, issue, "the first read")
		stands := standsAt(t, read.writer, "d1", pair.writer.ids[0], "the writer's result", model.CommitUnknown, false)
		if stands.Reason != model.ReasonReadFailed {
			t.Errorf("the writer's result is unknown for %q, want %q", stands.Reason, model.ReasonReadFailed)
		}
		read.calls(t, parityCompare, 1)
	})

	t.Run("11 a default_branch check: a kept no inside the window", func(t *testing.T) {
		pair := newParityPair(t, appKey)
		issue := pair.issue(t, "ROH", parityDefaultBranchCheck)
		pair.record(t, issue, "d1", strayCommit)
		first := pair.read(t, issue, "the first read")
		standsAt(t, first.writer, "d1", pair.writer.ids[0], "the writer's result off the trunk", model.CommitOtherCommit, false)
		first.calls(t, parityCompare, 1)

		pair.clock.advance(time.Minute)
		second := pair.read(t, issue, "the second read")
		standsAt(t, second.writer, "d1", pair.writer.ids[0], "the writer's result off the trunk", model.CommitOtherCommit, false)
		second.callsNone(t)
	})
}

// parityRepo is the one repository the stand-in GitHub holds; its default branch is main.
const parityRepo = "acme/widgets"

// behindHeadCommit is a commit a push left behind: GitHub names a pull request for it whose head
// is another commit.
const behindHeadCommit = "7777777777777777777777777777777777777777"

// The table's Verification documents: one check of each `when`.
const (
	parityBeforeMergeCheck   = ":::check{#k1 surface=\"cli\" run=\"go test ./...\"}\nThe server records a result against a check.\n:::\n"
	parityAfterMergeCheck    = ":::check{#m1 when=\"after_merge\"}\nThe branch the work merged into carries the commit.\n:::\n"
	parityDefaultBranchCheck = ":::check{#d1 when=\"default_branch\"}\nThe trunk carries the commit.\n:::\n"
)

// The stand-in GitHub's routes, which name the calls a read makes.
const (
	parityInstallation = "GET /repos/{owner}/{repo}/installation"
	parityToken        = "POST /app/installations/{id}/access_tokens"
	parityRepository   = "GET /repos/{owner}/{repo}"
	parityCommit       = "GET /repos/{owner}/{repo}/commits/{sha}"
	parityPulls        = "GET /repos/{owner}/{repo}/commits/{sha}/pulls"
	parityPullCommits  = "GET /repos/{owner}/{repo}/pulls/{number}/commits"
	parityCompare      = "GET /repos/{owner}/{repo}/compare/{basehead}"
)

// parityTestSession is the session both tokens act for.
const parityTestSession = "ses_parity"

// parityClock is the clock both stacks' results reads take each moment from (Deps.ResultsClock).
// It stands still until the test moves it.
type parityClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *parityClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *parityClock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(by)
}

// parityGitHub is the script both stacks' stand-in GitHubs answer from: whether the App can read
// parityRepo, the pull requests GitHub names for a commit (none until the test sets any), and a
// compare read's HTTP status and status field (200 and diverged until the test sets them).
type parityGitHub struct {
	mu         sync.Mutex
	unreadable bool
	pulls      func(sha string) string
	compare    func(base, head string) (int, string)
}

func (g *parityGitHub) answersPulls(pulls func(sha string) string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pulls = pulls
}

func (g *parityGitHub) answersCompare(compare func(base, head string) (int, string)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.compare = compare
}

// loseRepository makes every read of parityRepo answer 404 from now on, as GitHub answers a
// repository the App is no longer installed on. The client keeps the token it minted, so the
// repository's own read is what says so.
func (g *parityGitHub) loseRepository() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unreadable = true
}

// parityCalls counts one stand-in's requests by route.
type parityCalls struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *parityCalls) add(route string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[route]++
}

func (c *parityCalls) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.counts)
}

// since is the requests made after before was taken, by route, each route asked at least once.
func (c *parityCalls) since(before map[string]int) map[string]int {
	made := map[string]int{}
	for route, count := range c.snapshot() {
		if count > before[route] {
			made[route] = count - before[route]
		}
	}
	return made
}

// parityStandIn serves script to one stack and counts that stack's requests in calls.
func parityStandIn(t *testing.T, script *parityGitHub, calls *parityCalls) string {
	t.Helper()
	mux := http.NewServeMux()
	route := func(pattern string, serve func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			calls.add(pattern)
			serve(w, r)
		})
	}
	// unread answers 404 for a repository the App cannot read, and reports whether it did.
	unread := func(w http.ResponseWriter, r *http.Request) bool {
		script.mu.Lock()
		unreadable := script.unreadable
		script.mu.Unlock()
		if !unreadable && r.PathValue("owner")+"/"+r.PathValue("repo") == parityRepo {
			return false
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
		return true
	}
	route(parityInstallation, func(w http.ResponseWriter, r *http.Request) {
		if !unread(w, r) {
			fmt.Fprint(w, `{"id":77,"app_slug":"dispatch-test","permissions":{"contents":"read","pull_requests":"read"}}`)
		}
	})
	route(parityToken, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_fake_77","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	route(parityRepository, func(w http.ResponseWriter, r *http.Request) {
		if !unread(w, r) {
			fmt.Fprintf(w, `{"full_name":%q,"default_branch":"main"}`, parityRepo)
		}
	})
	route(parityCommit, func(w http.ResponseWriter, r *http.Request) {
		if !unread(w, r) {
			fmt.Fprintf(w, `{"sha":%q}`, r.PathValue("sha"))
		}
	})
	route(parityPulls, func(w http.ResponseWriter, r *http.Request) {
		if unread(w, r) {
			return
		}
		script.mu.Lock()
		pulls := script.pulls
		script.mu.Unlock()
		if pulls == nil {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, pulls(r.PathValue("sha")))
	})
	route(parityPullCommits, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	route(parityCompare, func(w http.ResponseWriter, r *http.Request) {
		if unread(w, r) {
			return
		}
		base, head, _ := strings.Cut(r.PathValue("basehead"), "...")
		script.mu.Lock()
		compare := script.compare
		script.mu.Unlock()
		status, comparison := http.StatusOK, "diverged"
		if compare != nil {
			status, comparison = compare(base, head)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"message":%q}`, http.StatusText(status))
			return
		}
		fmt.Fprintf(w, `{"status":%q}`, comparison)
	})
	return githubtest.Server(t, githubtest.Spec{Handle: mux.ServeHTTP})
}

// parityOpenPull is GitHub's answer naming one open pull request of parityRepo whose head is
// head, titled after issueKey, whichever commit was asked about.
func parityOpenPull(number int, head, issueKey string) string {
	return fmt.Sprintf(`[{"number":%d,"title":%q,"body":"","html_url":"https://github.com/%s/pull/%d",
		"state":"open","merged":false,"updated_at":"2026-11-05T07:15:08Z",
		"head":{"ref":"legion/x","sha":%q,"repo":{"full_name":%q}},"base":{"ref":"main"}}]`,
		number, issueKey+" the work", parityRepo, number, head, parityRepo)
}

// parityStack is one of the pair's two stacks: its server, its stand-in's calls, the token its
// reads present, and the id of each result recorded on it, in the order recorded.
type parityStack struct {
	who   string
	rig   *brokerTokenRig
	calls *parityCalls
	token string
	ids   []string
}

// parityPair is the two stacks one row reads, on one clock and one GitHub script.
type parityPair struct {
	clock    *parityClock
	github   *parityGitHub
	caller   *brokerCaller
	writer   *parityStack
	readOnly *parityStack
	// heldBefore is every pull_request_heads and branch_commits row the writer's stack held
	// before its first read (parityRows).
	heldBefore map[string]bool
}

// newParityPair builds the two stacks: each the package's test server with the stub broker, a
// GitHub App whose key is appKey and a stand-in of its own, and the pair's clock. The writer's
// stack is read with a machine login's token holding the person scope and not narrowed, the
// read-only stack with the same login's token narrowed to reading (read-person): the same
// session and the same person (TestANarrowedTokenActsAsItsIdentityAndOnlyReads).
func newParityPair(t *testing.T, appKey string) *parityPair {
	t.Helper()
	pair := &parityPair{
		clock:  &parityClock{at: time.Now().UTC().Truncate(time.Second)},
		github: &parityGitHub{},
		caller: newBrokerCaller(t),
	}
	narrowed := brokertokentest.NarrowedAnswers(t, pair.caller.jkt, parityTestSession, time.Now().Add(time.Hour).Unix())["machine-login"]
	writing := narrowed
	writing.ProjectScope, writing.Scope = brokertoken.ProjectScopePerson, ""
	stack := func(who, token string, answer brokertoken.Introspection) *parityStack {
		calls := &parityCalls{counts: map[string]int{}}
		rig := newBrokerTokenRigOn(t, testServerOptions{
			app:           &auth.AppConfig{ClientID: "Iv1.test", PEM: appKey},
			githubAPIBase: parityStandIn(t, pair.github, calls),
		})
		rig.served.deps.ResultsClock = pair.clock.now
		rig.broker.Says(token, answer)
		return &parityStack{who: who, rig: rig, calls: calls, token: token}
	}
	pair.writer = stack("the writer's read", "a-token-that-writes", writing)
	pair.readOnly = stack("the read-only read", "a-token-narrowed-to-reading", narrowed)
	return pair
}

// each runs setup on both stacks, the writer's first.
func (p *parityPair) each(setup func(stack *parityStack)) {
	setup(p.writer)
	setup(p.readOnly)
}

// issue makes the same issue of project in parityRepo on both stacks, with verification as its
// Verification document, and answers its key.
func (p *parityPair) issue(t *testing.T, project, verification string) string {
	t.Helper()
	keys := map[string]bool{}
	p.each(func(stack *parityStack) {
		issue := issueInRepository(t, stack.rig.handler, project, parityRepo, "Read as a writer reads")
		uploadDocument(t, stack.rig.handler, "/api/v1/issues/"+issue.Key, "verification.md", verification)
		keys[issue.Key] = true
	})
	if len(keys) != 1 {
		t.Fatalf("the two stacks made the issues %v, want one key", slices.Collect(maps.Keys(keys)))
	}
	return slices.Collect(maps.Keys(keys))[0]
}

// record records a pass of check at commit on both stacks, as a person does.
func (p *parityPair) record(t *testing.T, issueKey, check, commit string) {
	t.Helper()
	p.each(func(stack *parityStack) {
		response := recordResult(t, stack.rig.handler, issueKey, resultBody(check, "pass", commit, nil))
		if response.Code != http.StatusCreated {
			t.Fatalf("record %s at %s on %s's stack: status=%d body=%s", check, commit, stack.who, response.Code, response.Body.String())
		}
		stack.ids = append(stack.ids, decodeBody[recordedResult](t, response).Result.ID)
	})
}

// parityStep is what one read of both stacks gave: the writer's answer, decoded and as compared,
// and the GitHub calls the writer's read made, by route.
type parityStep struct {
	writer      issueResults
	writerBody  string
	writerCalls map[string]int
}

// calls asserts that the writer's read made count calls of route, the table's call count.
func (s parityStep) calls(t *testing.T, route string, count int) {
	t.Helper()
	if s.writerCalls[route] != count {
		t.Errorf("the writer's read made %d calls of %s, want %d (all its calls: %v)", s.writerCalls[route], route, count, s.writerCalls)
	}
}

// callsNone asserts that the writer's read made no GitHub call at all.
func (s parityStep) callsNone(t *testing.T) {
	t.Helper()
	if len(s.writerCalls) != 0 {
		t.Errorf("the writer's read made the GitHub calls %v, want none", s.writerCalls)
	}
}

// read reads issueKey's results on both stacks at the clock's moment, the writer's first, and
// asserts what every row asserts: the two answers are one body, but for each stack's own ids and
// record times (normalized); the two reads made the same GitHub calls; and the read-only stack
// holds no pull_request_heads or branch_commits row the writer's stack did not hold before its
// first read, and its read recorded no event.
func (p *parityPair) read(t *testing.T, issueKey, what string) parityStep {
	t.Helper()
	if p.heldBefore == nil {
		p.each(func(stack *parityStack) { awaitSettledDocuments(t, stack.rig.database) })
		p.heldBefore = parityRows(t, p.writer.rig.database)
	}
	path := "/api/v1/issues/" + issueKey + "/results"
	serve := func(stack *parityStack) (string, map[string]int) {
		before := stack.calls.snapshot()
		response := stack.rig.serve(p.caller.presenting(t, http.MethodGet, path, stack.token, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s, %s: status=%d body=%s", what, stack.who, response.Code, response.Body.String())
		}
		return response.Body.String(), stack.calls.since(before)
	}
	writerRaw, writerCalls := serve(p.writer)
	eventsBefore := parityEvents(t, p.readOnly.rig.database)
	readOnlyRaw, readOnlyCalls := serve(p.readOnly)

	step := parityStep{writerBody: p.writer.normalized(t, writerRaw), writerCalls: writerCalls}
	if err := json.Unmarshal([]byte(writerRaw), &step.writer); err != nil {
		t.Fatalf("decode %s, the writer's: %v", what, err)
	}
	t.Logf("%s: the writer answered %s, with the GitHub calls %v", what, p.writer.summary(step.writer), writerCalls)
	if got := p.readOnly.normalized(t, readOnlyRaw); got != step.writerBody {
		t.Errorf("%s: the read-only read answered\n%s\nwant the writer's answer\n%s", what, got, step.writerBody)
	}
	if !maps.Equal(readOnlyCalls, writerCalls) {
		t.Errorf("%s: the read-only read made the GitHub calls %v, want the writer's %v", what, readOnlyCalls, writerCalls)
	}
	for row := range parityRows(t, p.readOnly.rig.database) {
		if !p.heldBefore[row] {
			t.Errorf("%s: the read-only stack holds %s, which the writer's stack did not hold before its first read", what, row)
		}
	}
	if events := parityEvents(t, p.readOnly.rig.database); events != eventsBefore {
		t.Errorf("%s: the read-only read recorded %d events, want none", what, events-eventsBefore)
	}
	return step
}

// normalized is a results answer of this stack as JSON, with every member that names this
// stack's own rows rather than the answer replaced: each result's id by its place in the order
// the test recorded it, and the artifact ids, the record times and the event sequence numbers
// cleared. Every other member, the moment each head was learned at among them, is as answered.
func (s *parityStack) normalized(t *testing.T, body string) string {
	t.Helper()
	var answer any
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("decode %s: %v", s.who, err)
	}
	var walk func(value any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for member, inner := range value {
				switch member {
				case "verification_artifact_id", "artifact_id", "check_artifact_id", "created_at", "event_seq":
					value[member] = "cleared"
				case "id":
					if id, isString := inner.(string); isString {
						if index := slices.Index(s.ids, id); index >= 0 {
							value[member] = fmt.Sprintf("result %d", index)
						}
					}
				default:
					walk(inner)
				}
			}
		case []any:
			for _, inner := range value {
				walk(inner)
			}
		}
	}
	walk(answer)
	encoded, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("encode %s: %v", s.who, err)
	}
	return string(encoded)
}

// summary is a results answer of this stack in short, for the log: what each check compared
// with, and where each result's commit stands, each result named by its place in the order the
// test recorded it.
func (s *parityStack) summary(read issueResults) string {
	var checks []string
	for _, check := range read.Checks {
		bases := make([]string, 0, len(check.ComparedWith))
		for _, basis := range check.ComparedWith {
			bases = append(bases, parityBasisName(&basis))
		}
		compared := "[" + strings.Join(bases, ", ") + "]"
		if check.ComparedNothing != "" {
			compared = "nothing (" + check.ComparedNothing + ")"
		}
		results := make([]string, 0, len(check.Results))
		for _, result := range check.Results {
			stands := "no answer"
			if result.CommitStands != nil {
				stands = result.CommitStands.Answer
				if result.CommitStands.Basis != nil {
					stands += " " + parityBasisName(result.CommitStands.Basis)
				}
				if result.CommitStands.Reason != "" {
					stands += " (" + result.CommitStands.Reason + ")"
				}
			}
			results = append(results, fmt.Sprintf("result %d at %.7s %s", slices.Index(s.ids, result.ID), result.CommitSHA, stands))
		}
		checks = append(checks, fmt.Sprintf("%s compared with %s: %s", check.BlockID, compared, strings.Join(results, "; ")))
	}
	return strings.Join(checks, " | ")
}

// parityBasisName is a basis in short: a pull request with its head, a branch, or a commit.
func parityBasisName(basis *model.CheckBasis) string {
	switch {
	case basis.PullRequest != nil:
		return fmt.Sprintf("pull request %d at %.7s", *basis.PullRequest, basis.Commit)
	case basis.Branch != "":
		return "branch " + basis.Branch
	default:
		return fmt.Sprintf("commit %.7s", basis.Commit)
	}
}

// standsAtBranch asserts that the writer's result stands at the branch basis.
func standsAtBranch(t *testing.T, read issueResults, check, resultID, branch string) {
	t.Helper()
	stands := standsAt(t, read, check, resultID, "the writer's result on "+branch, model.CommitAtBasis, false)
	if stands.Basis == nil || stands.Basis.Branch != branch {
		t.Errorf("the writer's result stands at %+v, want the branch %s", stands.Basis, branch)
	}
}

// storeParityHead stores the open pull request number of parityRepo with its head at sha,
// attributed to issueKey and learned at learned, as the intake's write of a `pull_request`
// envelope would leave it.
func storeParityHead(t *testing.T, database *store.Store, number int, sha, issueKey string, learned time.Time) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(), `
		insert into pull_request_heads (repo, number, head_sha, head_ref, base_ref, state, issue_key, event_at, checked_at)
		values ($1, $2, $3, 'legion/work', 'main', 'open', $4, $5, $5)
	`, parityRepo, number, sha, issueKey, learned); err != nil {
		t.Fatalf("store the head of %s#%d: %v", parityRepo, number, err)
	}
}

// parityRows is every pull_request_heads and branch_commits row database holds, each named by its
// key and what it says, without the moments it was written at.
func parityRows(t *testing.T, database *store.Store) map[string]bool {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `
		select format('pull request head %s#%s at %s, base %s, %s, issue %s', repo, number, head_sha, base_ref, state, coalesce(issue_key, 'none'))
		from pull_request_heads
		union all
		select format('branch answer %s %s %s on the branch: %s', repo, branch, sha, on_branch)
		from branch_commits
	`)
	if err != nil {
		t.Fatalf("read the stored rows: %v", err)
	}
	defer rows.Close()
	held := map[string]bool{}
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatalf("read a stored row: %v", err)
		}
		held[row] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the stored rows: %v", err)
	}
	return held
}

// parityEvents is how many events database holds.
func parityEvents(t *testing.T, database *store.Store) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(context.Background(), `select count(*) from events`).Scan(&count); err != nil {
		t.Fatalf("count the events: %v", err)
	}
	return count
}
