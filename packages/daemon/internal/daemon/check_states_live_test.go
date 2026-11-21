//go:build e2e

// The live proof that the tester's move is decided by the check results in Dispatch alone: the
// tester records its results with the real `dispatch result`, its real `legion` tool completes
// testing with no verdict, Dispatch publishes each change of a check's state as a
// check.state_changed event, and the daemon moves the issue to reviewing when every check reads
// yes at the head the tester's test record names, or back to implementing with a task that names
// every check that does not pass. scripts/e2e/check-states.sh builds this driver with `-tags e2e`
// and owns the nats-server and the PostgreSQL it runs against.
//
// The unit tests hold each half: Dispatch's own tests (packages/envoy/internal/dispatch/api's
// check_states_test.go) that a write publishes the state it changes, the intake's that the
// event decodes, and the workflow's that the move reads the states at the tested head. None of
// them runs the real Dispatch, the real daemon and a real tester's pane against each other, so
// none says that what a tester's pane does moves the issue so. This driver does.
//
// Nothing between the tester and the move is stood in for. Dispatch is the real server this
// checkout builds, on a database of its own, as handoff_records_live_test.go runs it, but with its
// events on: it publishes them on the run's own broker, LEGION_E2E_NATS_URL, where the daemon's
// durable consumer of Dispatch's issue events takes them, as in a deployment. The daemon is the
// whole daemon (run, through startDaemonWithin), booted with dispatch_url at that server. Each
// tester is a real `omp --mode rpc` under the real `legion worker-shim`, started by
// localruntime_live_test.go's runtime with this checkout's `@sjawhar/pi-envoy` and
// `@sjawhar/pi-legion` in an Oh My Pi profile of the run's own, and its model is
// late_completion_live_test.go's completionStandin, which answers the turn a case arms with one
// `tool_use` block calling the `legion` tool's handoff_complete, so the completion the daemon
// answers is the request the shipped plugin builds. Every result and every test record is written
// with the real `dispatch` CLI (packages/envoy-client/bin/dispatch.ts under bun) as the session
// the tester's pane registered, as that pane's own shell runs it.
//
// What is stood in for lies outside the move. The open pull request of each issue is staged in
// Dispatch's database as the `pull_request` envelope Envoy relays would have stored it (one
// pull_request_heads row, attributed to the issue and read just now), since the scratch server
// reads no GitHub; the trigger on that table marks the issue owed, as the envelope's write does.
// GitHub's REST API, as the daemon reads it, is a stand-in of this driver's own for those pull
// requests. Envoy's listener is the package's noticeListener, the Apps' tokens are
// workflowTokenRecorder's placeholders, and every pane but a tester's talks to a model stand-in
// that answers text alone. The issues, their pull requests, their trees' slots and each tester's
// claim mid-launch are seeded through the store, as every daemon test seeds its fixture.
//
// The cases are chosen with CHECK_STATES_E2E_CASES (a comma- or space-separated list; every case
// when it is empty), and each runs on issues of its own, in the order statesCaseOrder gives. Each
// prints `== <case>`, what it drove and read, and `ok <case>`; the first that fails ends the run,
// its failure naming the case and the assertion (`FAIL <case> at <assertion>: ...`). A read that
// finds nothing it could judge says UNKNOWN and what it could not read, and its case fails. Each
// bound of 10 seconds is measured from the operation that triggers the answer, never from the
// read, and an answer that comes later than the bound fails at `<assertion>-within`.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/stream"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
)

// The model stand-ins, by the provider name each is given in the run's models.yml: the testers'
// one, which calls the `legion` tool where a case arms it; the implementers' one, which a case
// holds while it reads the task the daemon queued for the implementer; and the spare, which every
// other pane talks to.
const (
	statesTesterProvider      = "statestester"
	statesImplementerProvider = "statesimplementer"
	statesSpareProvider       = "statesspare"
)

// statesCasesEnv names the cases a run makes; empty, it makes every one.
const statesCasesEnv = "CHECK_STATES_E2E_CASES"

// statesBrokerPIDEnv names the pid of the run's own nats-server: the variable
// scripts/e2e/lib/live-daemon-proof.sh holds it in, which scripts/e2e/check-states.sh exports
// before the run starts, so the driver stops exactly the broker the run started and no other.
const statesBrokerPIDEnv = "nats_pid"

// statesHeld is the workflow's line for each state event it takes (workflow's checkState).
const statesHeld = "workflow: a check state is held"

// statesWithin is the bound the approved checks give the daemon's answer: the line after a result,
// the move and the reviewer's start after the tester's completion, and the move after the broker
// comes back. It is measured from the operation that triggers the answer: the start of the
// result's write, the moment the turn that completes reached the tester's model (which comes
// before the tool's request), or the SIGCONT.
const statesWithin = 10 * time.Second

// statesLate is how long a case reads on for an answer past statesWithin, so an answer that comes
// late fails at the bound, naming how late it came, apart from one that never comes.
const statesLate = time.Minute

// statesCompletion, statesWrite and statesContinue name the operations a bound is measured from.
const (
	statesCompletion = "the tester's completion"
	statesWrite      = "the start of the result's write"
	statesContinue   = "the SIGCONT"
)

// statesVerification is the Verification document of every issue but the no-check case's: two
// `before_merge` checks, k1 and k2, that a role proves by its own result.
const statesVerification = `:::check{#k1 when="before_merge" negative_control="A server whose route records nothing."}
The server records a result against a check.
:::

:::check{#k2 when="before_merge" negative_control="A server whose read answers nothing."}
The server answers a read of the results.
:::
`

// statesNoBeforeMerge is the no-check case's Verification document: a document that exists and
// holds a check, k3, which is proved after the merge, so it holds no `before_merge` check. Dispatch
// reads its blocks and keeps none of them in the head's set. An issue with no document at all
// would not reach that path: Dispatch answers it before it reads a block.
const statesNoBeforeMerge = `Nothing of this issue is proved before its merge.

:::check{#k3 when="after_merge" negative_control="A released server whose route records nothing."}
The released server records a result against a check.
:::
`

// statesCaseOrder is every case, in the order a run makes them. completion-before-event is last,
// since it stops the broker every other case's events cross.
var statesCaseOrder = []string{
	"one-record", "daemon-records-nothing", "dispatch-tells", "pass-goes-to-review", "no-check",
	"fail-sends-back", "completion-before-event",
}

// statesCaseIssues is the issues each case drives, by the title each is filed under, and whether
// its Verification document holds the two before_merge checks k1 and k2 (statesVerification) or
// no before_merge check (statesNoBeforeMerge).
var statesCaseIssues = map[string][]statesIssueSpec{
	"one-record": {
		{title: "One record decides: the tester's fail of k2 sends the issue back", beforeMerge: true},
		{title: "One record decides: the tester's passes send the issue to review", beforeMerge: true},
	},
	"daemon-records-nothing": {
		{title: "The daemon records nothing: a round that fails k2", beforeMerge: true},
		{title: "The daemon records nothing: a round that passes both checks", beforeMerge: true},
	},
	"dispatch-tells":      {{title: "Dispatch tells the daemon of a fail of k1", beforeMerge: true}},
	"pass-goes-to-review": {{title: "Every check passes, recorded by the implementer and the tester", beforeMerge: true}},
	"no-check":            {{title: "No before_merge check in the Verification document", beforeMerge: false}},
	"fail-sends-back": {
		{title: "A fail of k2 is named in the implementer's task", beforeMerge: true},
		{title: "A pass and a fail of k1 are named in the implementer's task", beforeMerge: true},
	},
	"completion-before-event": {{title: "The completion arrives before Dispatch's events", beforeMerge: true}},
}

// statesIssueSpec is one issue a case drives, as statesCaseIssues writes it.
type statesIssueSpec struct {
	title       string
	beforeMerge bool
}

// statesIssue is one issue of the run: its key as Dispatch filed it, its title, its pull request's
// number and head, and the session its tester's pane registered.
type statesIssue struct {
	key, title, head string
	number           int
	tester           string
	// staged is whether the issue's pull request is staged in Dispatch's database (stageHead).
	staged bool
	// recorded is each result the case recorded on the issue, as statesRecordedAs spells it.
	recorded []string
}

// statesProof is one run of the proof: the booted daemon over the real Dispatch, the issues of the
// cases it makes, the stand-ins the panes talk to, and what the case being made has found it could
// not read.
type statesProof struct {
	liveAgentProof
	dispatch *handoffDispatch
	// dispatchDB is Dispatch's own database, which a case stages pull requests in and reads the
	// results and the states from.
	dispatchDB *pgxpool.Pool
	tokens     *workflowTokenRecorder
	cases      []string
	issues     map[string][]*statesIssue
	// root is the checkout the CLI runs from, and cliHome the home, configuration and session
	// state of every CLI run.
	root, cliHome string
	// github is the stand-in of GitHub's REST API (serveGitHub).
	github string
	// tester is the stand-in every tester's pane talks to, implementer every implementer's, and
	// spare every other pane's.
	tester      *completionStandin
	implementer *paneStandin
	spare       *paneStandin
	// home and profile are the Oh My Pi home and profile the panes run under; overlays is each
	// role's PI_CONFIG_FILES.
	home, profile string
	overlays      map[claim.Role][]string
	// current is the case being made, and blind what its reads could not see, which fails the
	// case once it has made its own judgement.
	current string
	blind   []string
	// stopped is the pid the case completion-before-event stopped, 0 while none is stopped.
	stopped int
}

// TestCheckStatesLive makes each chosen case, in statesCaseOrder. Its name is short on purpose: the
// daemon's worker socket is made under the test's own temporary directory, which carries the name,
// and a unix socket's path holds 107 bytes.
func TestCheckStatesLive(t *testing.T) {
	fixtureenv.Live(t)
	r := newStatesProof(t)
	bodies := map[string]func(){
		"one-record":              r.oneRecord,
		"daemon-records-nothing":  r.daemonRecordsNothing,
		"dispatch-tells":          r.dispatchTells,
		"pass-goes-to-review":     r.passGoesToReview,
		"no-check":                r.noCheck,
		"fail-sends-back":         r.failSendsBack,
		"completion-before-event": r.completionBeforeEvent,
	}
	for _, name := range r.cases {
		r.current, r.blind = name, nil
		r.step(name, func() {
			bodies[name]()
			if len(r.blind) > 0 {
				r.fail("unknown", "UNKNOWN: %s", strings.Join(r.blind, "; "))
			}
		})
	}
}

// statesSelected is the cases CHECK_STATES_E2E_CASES names, in statesCaseOrder, and every case when
// it names none. A name that is no case fails the run before anything is built, naming the cases.
func statesSelected(t *testing.T) []string {
	t.Helper()
	named := strings.FieldsFunc(os.Getenv(statesCasesEnv), func(r rune) bool { return r == ',' || r == ' ' })
	if len(named) == 0 {
		return slices.Clone(statesCaseOrder)
	}
	for _, name := range named {
		if !slices.Contains(statesCaseOrder, name) {
			t.Fatalf("%s names %q, which is no case of this proof; the cases are %s", statesCasesEnv, name, strings.Join(statesCaseOrder, ", "))
		}
	}
	var chosen []string
	for _, name := range statesCaseOrder {
		if slices.Contains(named, name) {
			chosen = append(chosen, name)
		}
	}
	return chosen
}

// newStatesProof readies everything the panes need before the daemon boots: the real Dispatch with
// its events on the run's broker, the project, its repository mapping and every chosen case's
// issues; the `legion` binary and the two plugins built from this checkout; an Oh My Pi profile
// routed to the stand-ins; GitHub's stand-in; the store seeded; and the daemon booted over the
// local runtime.
func newStatesProof(t *testing.T) *statesProof {
	t.Helper()
	omp := os.Getenv(liveOmpPathEnv)
	if omp == "" {
		t.Skipf("%s is unset, so there is no Oh My Pi for a pane to run; scripts/e2e/check-states.sh requires it", liveOmpPathEnv)
	}
	for _, tool := range []string{"bun", "jj", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is not on PATH: %v", tool, err)
		}
	}
	cases := statesSelected(t)
	takeOwnDatabase(t)
	proof := newLiveProof(t, "scripts/e2e/check-states.sh")
	// A Dispatch project key is at most ten characters (contracts ProjectKeyPattern), and the
	// daemon's project is its Dispatch project, so the run's project is a short one of its own.
	proof.cfg.Project = "CS" + randomSuffix(t)[:6]
	proof.cfg.Projects = map[string]config.Project{proof.cfg.Project: {}}
	// The loader's own default (config's resolve), which workflowConfig leaves at zero: a pane's
	// tool reads GET /legion/v1/state through the daemon API contract, which takes a positive cap
	// alone.
	proof.cfg.ReviewRoundCap = 3
	// Each issue is a tree of its own and takes a slot; ten is the most a run seeds.
	proof.cfg.AdmissionCap = 16
	var err error
	if proof.project, err = claim.ProjectToken(proof.cfg.Project); err != nil {
		t.Fatal(err)
	}
	r := &statesProof{
		liveAgentProof: liveAgentProof{liveProof: proof, omp: omp},
		tokens:         &workflowTokenRecorder{},
		cases:          cases,
		issues:         map[string][]*statesIssue{},
		cliHome:        t.TempDir(),
	}
	version, err := exec.Command(omp, "--version").Output()
	if err != nil {
		t.Fatalf("read the Oh My Pi version of %s: %v", omp, err)
	}
	r.note("Oh My Pi %s at %s; cases %s", strings.TrimSpace(string(version)), omp, strings.Join(cases, ", "))

	r.root = proofCheckoutRoot(t)
	r.dispatch = startStatesDispatch(t, r.root, os.Getenv(liveNATSURLEnv))
	if err := os.WriteFile(r.cfg.DispatchTokenFile, []byte(r.dispatch.token+"\n"), 0o600); err != nil {
		t.Fatalf("write the Dispatch token file: %v", err)
	}
	r.cfg.DispatchURL = r.dispatch.base
	if r.dispatchDB, err = pgxpool.New(context.Background(), r.dispatch.database); err != nil {
		t.Fatalf("open Dispatch's database: %v", err)
	}
	t.Cleanup(r.dispatchDB.Close)
	r.setUpDispatch()

	work := t.TempDir()
	r.legion = buildProofLegion(t, r.root, work)
	r.home = filepath.Join(work, "omp-home")
	r.profile = "legion-check-states-" + strings.ToLower(r.cfg.Project)
	installProofPlugins(t, r.root, work, r.home, r.profile, omp)
	r.tester = newCompletionStandin(t, statesTesterProvider)
	r.implementer = newPaneStandin(t, statesImplementerProvider)
	r.spare = newPaneStandin(t, statesSpareProvider)
	r.writeOmpProfile()
	r.writeOverlays(work)

	r.rt = newLocalRuntime(t, r.cfg.StateDir, r.legion, omp, r.home, r.profile)
	r.rt.daemonURL, r.rt.envoyURL, r.rt.natsURLs = r.cfg.DaemonURL, newPaneListener(t), r.cfg.NatsURLs
	r.rt.dispatchURL, r.rt.dispatchTokenFile = r.cfg.DispatchURL, r.cfg.DispatchTokenFile
	r.rt.overlays = func(token claim.Token) []string {
		role, ok := token.Role()
		if overlays, driven := r.overlays[role]; ok && driven {
			return overlays
		}
		return r.overlays[""]
	}
	r.serveGitHub()
	r.seed()
	r.daemon = newRecordingLog(r.log())
	r.d = startDaemonWithin(t, r.cfg, r.overrides(), slog.New(r.daemon), 2*time.Minute)
	r.note("Dispatch %s (database of its own, events on %s), project %s mapped to %s; daemon at dispatch_url %s, each pane told it as DISPATCH_URL",
		r.dispatch.base, os.Getenv(liveNATSURLEnv), r.cfg.Project, liveRepo, r.cfg.DispatchURL)
	return r
}

// startStatesDispatch starts the run's Dispatch server from root's packages/envoy on a database of
// its own, as startHandoffDispatch does, with its events published on natsURL, the run's broker,
// where the daemon consumes them. Dispatch's own log goes beside the daemon's in the evidence
// directory when the script names one.
func startStatesDispatch(t *testing.T, root, natsURL string) *handoffDispatch {
	t.Helper()
	if natsURL == "" {
		t.Fatalf("%s names no broker for Dispatch to publish its events on", liveNATSURLEnv)
	}
	options := scratchDispatchOptions{root: root, natsURL: natsURL}
	if daemonLog := os.Getenv(liveDaemonLogEnv); daemonLog != "" {
		file, err := os.OpenFile(filepath.Join(filepath.Dir(daemonLog), "dispatch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatalf("open Dispatch's log: %v", err)
		}
		t.Cleanup(func() { file.Close() })
		options.log = file
	}
	return &handoffDispatch{scratchDispatch: startScratchDispatchOn(t, options), client: &http.Client{Timeout: 30 * time.Second}}
}

// setUpDispatch creates the run's project on Dispatch, maps liveRepo to it as its default, and
// files each chosen case's issues, in progress and carrying the legion label, as a running daemon
// leaves them, each with the Verification document its case gives it. The duplicate gate is
// forced: the titles of one case share words on purpose.
func (r *statesProof) setUpDispatch() {
	t := r.t
	t.Helper()
	r.dispatch.user(http.MethodPost, "/api/v1/projects", map[string]string{"key": r.cfg.Project, "name": "Check states live proof"}, http.StatusCreated, nil)
	r.dispatch.user(http.MethodPut, "/api/v1/settings/repo-projects/"+liveRepo, map[string]any{"project": r.cfg.Project, "default": true}, http.StatusOK, nil)
	base, err := strconv.ParseInt(randomSuffix(t)[:5], 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range r.cases {
		for _, spec := range statesCaseIssues[name] {
			var created struct {
				Key string `json:"key"`
			}
			r.dispatch.user(http.MethodPost, "/api/v1/issues", map[string]any{"project": r.cfg.Project, "title": spec.title, "force": true}, http.StatusCreated, &created)
			if created.Key == "" {
				t.Fatalf("Dispatch created the issue %q with no key", spec.title)
			}
			r.dispatch.user(http.MethodPatch, "/api/v1/issues/"+created.Key, map[string]any{"status": "in_progress", "labels": []string{"legion"}}, http.StatusOK, nil)
			content, document := statesNoBeforeMerge, "a Verification document whose one check, k3, is after_merge: no before_merge check"
			if spec.beforeMerge {
				content, document = statesVerification, "a Verification document of two before_merge checks, k1 and k2"
			}
			r.dispatch.user(http.MethodPost, "/api/v1/issues/"+created.Key+"/artifacts", map[string]any{"name": "verification.md", "content": content}, http.StatusCreated, nil)
			base++
			issue := &statesIssue{key: created.Key, title: spec.title, head: headCommit(created.Key, "test"), number: int(base)}
			r.issues[name] = append(r.issues[name], issue)
			r.note("%s filed %s %q with %s; its pull request #%d is open at %s", name, issue.key, issue.title, document, issue.number, issue.head)
		}
	}
}

// seed records each issue as the daemon would hold it while its tester works: a root handed to
// Legion, in testing, on an open pull request at its head that GitHub would merge, holding its
// tree's admission slot with its lifecycle open, and its tester's claim mid-launch, which this
// boot relaunches, holding the task its phase's start left pending, written as the daemon writes
// a phase's task line (admission's repair of a slotted worker passes over a claim that holds one).
// Every pane's role prompt is an operator's short prompt file (writePrompt).
func (r *statesProof) seed() {
	t := r.t
	t.Helper()
	ctx := context.Background()
	all := r.allIssues()
	if err := r.st.Tx(ctx, func(tx pgx.Tx) error {
		for _, issue := range all {
			listed := r.dispatch.issue(issue.key)
			if err := r.records.PutIssue(ctx, tx, record.Issue{
				Key: issue.key, Repo: liveRepo, DefaultBranch: "main", GitHubRepo: liveRepo, Tree: issue.key, Project: r.cfg.Project,
				Title: listed.Title, Phase: phase.Testing, Generation: 1, Status: "in_progress", DispatchStatus: "in_progress",
				Rank: listed.Rank, LastDispatchSeq: listed.LastSeq,
			}); err != nil {
				return err
			}
			if err := r.records.PutPullRequest(ctx, tx, record.PullRequest{
				State: record.PullRequestOpen, Issue: issue.key, Repo: liveRepo, Number: issue.number, Branch: claim.IssueBranch(issue.key),
				HeadSHA: issue.head, Base: "main", Failing: []string{},
				Mergeability: record.MergeabilityMergeable, MergeState: record.MergeStateClean,
			}); err != nil {
				return err
			}
			if err := r.admit(ctx, tx, issue.key); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed the issues: %v", err)
	}
	for _, issue := range all {
		if _, err := r.st.OpenTreeLifecycle(ctx, r.project, issue.key, treelifecycle.AuthorityWorkflow); err != nil {
			t.Fatalf("open the lifecycle of %s: %v", issue.key, err)
		}
		tester := r.token(issue.key, claim.RoleTester)
		if _, err := r.st.AdmitClaim(ctx, supervise.Claim{
			Token: tester, Project: r.project, Tree: issue.key, Issue: issue.key, Role: claim.RoleTester,
			Generation: 1, State: supervise.StateLaunching,
			BootTokenHash: supervise.HashBootToken("interrupted-" + randomSuffix(t)), ServingGeneration: 1,
		}); err != nil {
			t.Fatalf("record the tester's unfinished launch on %s: %v", issue.key, err)
		}
		task := fmt.Sprintf("Continue %s. Issue: %s. Phase: testing. Pull request: #%d.", issue.title, issue.key, issue.number)
		if err := r.st.PutDelivery(ctx, tester, supervise.Delivery{
			ID: "seeded-start-" + randomSuffix(t), Task: task, Generation: 1, Phase: phase.Testing, QueuedAt: time.Now(),
		}); err != nil {
			t.Fatalf("record the task the tester's start left pending on %s: %v", issue.key, err)
		}
		for _, role := range []claim.Role{claim.RoleTester, claim.RoleImplementer, claim.RoleReviewer} {
			writePrompt(t, r.cfg, r.token(issue.key, role))
		}
	}
	r.note("seeded %d issues in testing, each on its open pull request (mergeable, clean) with its tester mid-launch", len(all))
}

// allIssues is every issue of the run, in the order of the cases that drive them.
func (r *statesProof) allIssues() []*statesIssue {
	var all []*statesIssue
	for _, name := range r.cases {
		all = append(all, r.issues[name]...)
	}
	return all
}

// token is the claim token of role on issue, as the daemon spells it.
func (r *statesProof) token(issue string, role claim.Role) claim.Token {
	r.t.Helper()
	token, err := claim.NewToken(r.project, issue, role)
	if err != nil {
		r.t.Fatal(err)
	}
	return token
}

// overrides is the daemon this proof boots: the local runtime over the real worker stream, the
// API listener the fixture holds, the workflow's App tokens stood in for, and GitHub's stand-in.
func (r *statesProof) overrides() overrides {
	return overrides{
		runtime: func(_ context.Context, listener *stream.Listener, address string, _ appauth.Source,
			_ func(context.Context, string) (ghrepo.Repository, string, error), _ *store.Store, _ func() []ghrepo.Repository) (runtime.Runtime, error) {
			r.rt.conns, r.rt.stream = listener, address
			return r.rt, nil
		},
		listen:         heldListen,
		workflowTokens: r.tokens,
		githubAPI:      r.github,
	}
}

// fail ends the run at the case being made, naming the assertion that did not hold.
func (r *statesProof) fail(assertion, format string, args ...any) {
	r.t.Helper()
	r.t.Fatalf("FAIL %s at %s: %s", r.current, assertion, fmt.Sprintf(format, args...))
}

// ---- the cases -------------------------------------------------------------------------------------

// one-record: the tester records a pass of k1 and a fail of k2, and its `legion` tool completes
// testing with the summary "Every check passes." and no verdict; the issue goes back to
// implementing, and the implementer's task the move queued names k2 and not k1. In a second run,
// on an issue of its own, the tester records a pass of both checks and completes the same way; the
// issue goes to reviewing, its reviewer starts, and the implementer is given no task: the results
// in Dispatch decide, whatever the completion's summary says. Each run reads back the results
// Dispatch holds of its issue.
func (r *statesProof) oneRecord() {
	issues := r.issues["one-record"]
	r.oneRecordRuns(issues[0], issues[1])
}

// oneRecordRuns makes the two runs of the case one-record, on failing and passing.
func (r *statesProof) oneRecordRuns(failing, passing *statesIssue) {
	r.sentBack(failing, []statesResult{{"k1", "pass"}, {"k2", "fail"}}, "back-to-implementing", "k2: no (fail)", "k1")
	done := r.testerRun(passing, []statesResult{{"k1", "pass"}, {"k2", "pass"}}, "Every check passes.")
	r.movesWithin(passing, phase.Reviewing, "to-reviewing", done, statesCompletion)
	r.reviewerLaunched(passing, "to-reviewing", done, statesCompletion)
	r.noImplementerTask(passing)
	r.resultsHeld(passing)
}

// daemon-records-nothing: the two runs of the case one-record, on issues of its own; then no
// request Dispatch's access log holds of a result's record or retraction was made by anyone but a
// tester or an implementer of the run, the daemon's own session above all, and every result and
// retraction Dispatch stores names such a session.
func (r *statesProof) daemonRecordsNothing() {
	issues := r.issues["daemon-records-nothing"]
	r.oneRecordRuns(issues[0], issues[1])
	allowed := r.roleSessions()
	daemonSession := dispatch.DaemonSession(issues[0].key)

	writes := r.resultWrites()
	r.note("read back: Dispatch's access log holds %d requests that record or retract a result", len(writes))
	if len(writes) == 0 {
		r.fail("access-log", "UNKNOWN: Dispatch's access log holds no request that records or retracts a result, so it cannot say who wrote the results the runs recorded")
	}
	for _, write := range writes {
		r.note("  %s %s status=%s caller_kind=%s caller=%s user_agent=%q", write["method"], write["path"], write["status"], write["caller_kind"], write["caller"], write["user_agent"])
		if write["caller_kind"] != "session" || !slices.Contains(allowed, write["caller"]) {
			r.fail("access-log", "Dispatch served %s %s for %s %q, which is no tester's or implementer's session of the run (they are %v; the daemon's is %s)",
				write["method"], write["path"], write["caller_kind"], write["caller"], allowed, daemonSession)
		}
	}

	results := r.storedResults("check_results", "")
	r.note("read back: check_results holds %d results", len(results))
	for _, issue := range issues {
		if mine := slices.IndexFunc(results, func(s statesStored) bool { return s.issue == issue.key }); mine < 0 {
			r.fail("results", "UNKNOWN: check_results holds no result of %s, whose runs recorded two, so it cannot say who wrote them", issue.key)
		}
	}
	for _, table := range []string{"ci_check_results", "check_result_retractions", "ci_check_result_retractions"} {
		stored := r.storedResults(table, "")
		r.note("read back: %s holds %d rows", table, len(stored))
		results = append(results, stored...)
	}
	for _, stored := range results {
		r.note("  %s %s", stored.table, stored)
		if stored.actorKind != "session" || stored.actorID != stored.session || !slices.Contains(allowed, stored.actorID) {
			r.fail("results", "%s holds %s, written by %s %q under session %q: not a tester's or implementer's session of the run (they are %v; the daemon's is %s)",
				stored.table, stored, stored.actorKind, stored.actorID, stored.session, allowed, daemonSession)
		}
	}
	r.note("every result and retraction Dispatch served and stores names a tester's or an implementer's session; none names the daemon's %s", daemonSession)
}

// dispatch-tells: the tester records a fail of k1 with the real `dispatch` command, and within 10
// seconds of the start of that write, by the record's own time, the daemon's log holds `workflow:
// a check state is held` for the issue, k1, the head, the state no and the reason fail: Dispatch's
// event, which nothing but its outbox and the daemon's consumer carry. The tester has not
// completed, so the issue stays in testing and the implementer is given no task; the case reads
// both, and the results Dispatch holds of the issue.
func (r *statesProof) dispatchTells() {
	issue := r.issues["dispatch-tells"][0]
	r.readyTester(issue)
	r.stageHead(issue)
	r.statesBefore(issue)
	from := r.daemon.count()
	sent := time.Now()
	r.record(issue, issue.tester, "tester", statesResult{"k1", "fail"})
	var line *loggedRecord
	for line == nil && time.Since(sent) < statesLate {
		line = r.daemon.firstAfter(from, func(l loggedRecord) bool {
			return l.Message == statesHeld && l.Attrs["issue"] == issue.key && l.Attrs["check"] == "k1" && l.Attrs["head"] == issue.head &&
				l.Attrs["state"] == "no" && l.Attrs["reason"] == "fail"
		})
		time.Sleep(50 * time.Millisecond)
	}
	if line == nil {
		r.fail("line", "UNKNOWN: the daemon's log holds no %q line for %s, k1, %s, no, fail within %s of %s: Dispatch's event of that state did not reach it",
			statesHeld, issue.key, issue.head, statesLate, statesWrite)
	}
	took := line.at.Sub(sent)
	r.note("daemon, %s after %s (bound %s): %s issue=%v check=%v repo=%v head=%v state=%v reason=%v",
		took.Round(time.Millisecond), statesWrite, statesWithin, line.Message, line.Attrs["issue"], line.Attrs["check"], line.Attrs["repo"],
		line.Attrs["head"], line.Attrs["state"], line.Attrs["reason"])
	if took > statesWithin {
		r.fail("line-within", "the daemon logged %q for %s, k1, no, fail at %s, %s after %s, past the bound of %s",
			statesHeld, issue.key, line.at.UTC().Format(time.RFC3339Nano), took.Round(time.Millisecond), statesWrite, statesWithin)
	}
	if got := r.phaseOf(issue, "phase"); got != phase.Testing {
		r.fail("phase", "GET /legion/v1/state shows %s in %s after the daemon held k1's fail, want %s: its tester has not completed", issue.key, got, phase.Testing)
	}
	r.note("read back: GET /legion/v1/state shows %s in %s after the daemon held k1's fail: its tester has not completed", issue.key, phase.Testing)
	r.noImplementerTask(issue)
	r.resultsHeld(issue)
}

// pass-goes-to-review: the implementer and the tester record a pass of k1 and of k2, the tester's
// `legion` tool completes testing with no verdict, and within 10 seconds of the completion the
// issue is in reviewing and the daemon has started its reviewer. The implementer is given no task,
// and Dispatch holds the four results.
func (r *statesProof) passGoesToReview() {
	issue := r.issues["pass-goes-to-review"][0]
	r.readyTester(issue)
	r.stageHead(issue)
	implementer := "implementer-" + strings.ToLower(issue.key)
	r.record(issue, implementer, "implementer", statesResult{"k1", "pass"}, statesResult{"k2", "pass"})
	done := r.testerRun(issue, []statesResult{{"k1", "pass"}, {"k2", "pass"}}, "Every check passes.")
	r.movesWithin(issue, phase.Reviewing, "reviewing", done, statesCompletion)
	r.reviewerStarted(issue, done, statesCompletion)
	r.noImplementerTask(issue)
	r.resultsHeld(issue)
}

// no-check: an issue whose Verification document exists and holds no before_merge check (its one
// check, k3, is after_merge), as Dispatch answers it. Its tester completes with no verdict and no
// result. Within 10 seconds of the completion the issue is in reviewing and its reviewer started;
// the implementer is given no task; Dispatch holds no result of the issue; and Dispatch published
// the head's set with no check, computed from that document.
func (r *statesProof) noCheck() {
	issue := r.issues["no-check"][0]
	document := r.noBeforeMergeCheck(issue)
	done := r.testerRun(issue, nil, "No check to run: the Verification document holds no before_merge check.")
	r.movesWithin(issue, phase.Reviewing, "reviewing", done, statesCompletion)
	r.reviewerStarted(issue, done, statesCompletion)
	r.noImplementerTask(issue)
	r.resultsHeld(issue)
	r.emptySet(issue, document)
}

// fail-sends-back: the tester records a pass of k1 and a fail of k2 and completes with no verdict;
// the issue goes back to implementing, and the implementer's task, read from the daemon's store,
// names k2, the state no and the reason fail, and does not name k1. On a second issue the tester
// records a pass and a fail of k1 and a pass of k2, and the task names k1 with the reason
// pass_and_fail, and not k2. Each run reads back the results Dispatch holds of its issue.
func (r *statesProof) failSendsBack() {
	issues := r.issues["fail-sends-back"]
	r.sentBack(issues[0], []statesResult{{"k1", "pass"}, {"k2", "fail"}}, "implementing", "k2: no (fail)", "k1")
	r.sentBack(issues[1], []statesResult{{"k1", "pass"}, {"k1", "fail"}, {"k2", "pass"}}, "implementing", "k1: unknown (pass_and_fail)", "k2")
}

// sentBack makes one run that sends issue back: the tester's results, its completion, the move
// back to implementing within statesWithin of the completion (assertion names it), the
// implementer's task naming named and not unnamed, and the results Dispatch holds of issue. The
// implementers' stand-in holds every turn while the task is read, so the task is still the
// claim's pending delivery.
func (r *statesProof) sentBack(issue *statesIssue, results []statesResult, assertion, named, unnamed string) {
	r.implementer.holdTurn()
	defer r.implementer.releaseTurn()
	done := r.testerRun(issue, results, "Every check passes.")
	r.movesWithin(issue, phase.Implementing, assertion, done, statesCompletion)
	task := r.implementerTask(issue)
	r.note("read back: the implementer's task in the daemon's store: %q", task)
	if !strings.Contains(task, named) {
		r.fail("task-names", "the implementer's task of %s does not name %q: %q", issue.key, named, task)
	}
	if strings.Contains(task, unnamed) {
		r.fail("task-names", "the implementer's task of %s names %s, which passes: %q", issue.key, unnamed, task)
	}
	r.note("the task names %q and not %s", named, unnamed)
	r.resultsHeld(issue)
}

// completion-before-event: with the states from before the results held, the run's own
// nats-server is stopped (SIGSTOP, by the pid the run started it with). The tester records a pass
// of k1 and of k2, writes its test record and completes testing. For 10 seconds the issue stays in
// testing and the daemon starts no worker, and the architect is told once, in a phase-finished
// notice, that the move waits for Dispatch's check states at the head. Then the broker is continued
// (SIGCONT), Dispatch's outbox and the daemon's consumer deliver what waited, and the issue goes to
// reviewing within 10 seconds of the SIGCONT. Once its reviewer starts, the implementer has been
// given no task, and Dispatch holds the tester's two results.
func (r *statesProof) completionBeforeEvent() {
	issue := r.issues["completion-before-event"][0]
	r.readyTester(issue)
	r.stageHead(issue)
	r.statesBefore(issue)
	r.stopBroker()
	defer r.continueBroker()
	r.record(issue, issue.tester, "tester", statesResult{"k1", "pass"}, statesResult{"k2", "pass"})
	r.writeTestRecord(issue)
	launches := len(r.rt.Launches())
	claims := r.issueClaims(issue)
	r.complete(issue, "Every check passes.")
	deadline := time.Now().Add(statesWithin)
	for time.Now().Before(deadline) {
		if got := r.phaseOf(issue, "stays-testing"); got != phase.Testing {
			r.fail("stays-testing", "%s moved to %s while Dispatch's events could not reach the daemon, on the states from before the tester's results", issue.key, got)
		}
		if now := len(r.rt.Launches()); now != launches {
			r.fail("starts-no-worker", "the daemon started %d workers while the move waited: %v", now-launches, r.rt.Launches()[launches:])
		}
		if now := r.issueClaims(issue); !slices.Equal(now, claims) {
			r.fail("starts-no-worker", "the daemon holds the claims %v of %s while the move waited, and held %v before the completion", now, issue.key, claims)
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.note("read back: for %s %s stayed in %s, the daemon started no worker (launches %d, claims %v)", statesWithin, issue.key, phase.Testing, launches, claims)
	notices := r.architectNotices(issue.key, "phase-finished")
	if len(notices) != 1 {
		r.fail("notice", "%s's architect was told %d phase-finished notices while the move waited, want one: %v", issue.key, len(notices), notices)
	}
	reason := notices[0].Payload
	r.note("read back: the architect's one phase-finished notice: %q", reason)
	if !strings.Contains(reason, "the move waits for Dispatch's check states") || !strings.Contains(reason, issue.head) {
		r.fail("notice", "the phase-finished notice says %q, which is not that the move waits for Dispatch's check states at %s", reason, issue.head)
	}
	continued := r.continueBroker()
	if continued.IsZero() {
		r.fail("broker", "the run's nats-server was not continued, so nothing can be measured from the SIGCONT")
	}
	r.movesWithin(issue, phase.Reviewing, "moves-after", continued, statesContinue)
	r.reviewerLaunched(issue, "moves-after", continued, statesContinue)
	r.noImplementerTask(issue)
	r.resultsHeld(issue)
}

// ---- what a case drives and reads ----------------------------------------------------------------

// statesResult is one result a case records: the check and the verdict.
type statesResult struct {
	check, verdict string
}

// testerRun is one tester's run on issue: its pane ready, the issue's pull request staged in
// Dispatch, each of results recorded as the tester's session, its test record written, and its
// `legion` tool's completion of testing with summary and no verdict, which the tool must take. It
// answers the moment complete answers, which a bound after the completion is measured from.
func (r *statesProof) testerRun(issue *statesIssue, results []statesResult, summary string) time.Time {
	r.readyTester(issue)
	r.stageHead(issue)
	r.record(issue, issue.tester, "tester", results...)
	r.writeTestRecord(issue)
	return r.complete(issue, summary)
}

// readyTester waits for issue's tester's pane, which the boot relaunched, to register, take the
// testing task its claim held, and end that turn, which the testers' stand-in answers with a line
// starting WAITING; it holds the session the pane registered, which every write of the tester's is
// made as. The case's own task then starts a turn of a worker that holds no task of its phase but
// is serving its run, as a tester that said it waits is.
func (r *statesProof) readyTester(issue *statesIssue) {
	r.t.Helper()
	if issue.tester != "" {
		return
	}
	token := r.token(issue.key, claim.RoleTester)
	task := "Issue: " + issue.key + ". Phase: testing."
	r.until("tester", "the tester's pane to take its testing task "+strconv.Quote(task), paneReady, func() bool {
		if state := r.claim(token).State; state == string(supervise.StateFailed) {
			r.fail("tester", "%s failed before its pane took its task; its workers' output is under %s", token, r.rt.paneLogs)
		}
		return r.tester.requestsHolding(task) > 0
	})
	r.until("tester", "the tester's pane to end its first turn", time.Minute, func() bool {
		return r.claim(token).State == string(supervise.StateIdle)
	})
	held := r.claim(token)
	if issue.tester = held.Session; issue.tester == "" {
		r.fail("tester", "UNKNOWN: %s is idle and the daemon names no session of it, so nothing can be written as the tester", token)
	}
	r.note("%s took its testing task %q and ended the turn: session %s, plugin contract %d, process %s",
		token, task, issue.tester, held.PluginContract, held.Locator.Label())
}

// until polls want until it holds, and fails the case at assertion, naming what it waited for,
// once within has passed.
func (r *statesProof) until(assertion, what string, within time.Duration, want func() bool) {
	r.t.Helper()
	for deadline := time.Now().Add(within); !want(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			r.fail(assertion, "waited %s for %s", within, what)
		}
	}
}

// stageHead stores issue's open pull request in Dispatch's database as the `pull_request` envelope
// Envoy relays would have stored it: one pull_request_heads row at the issue's head, attributed to
// the issue by its title and read just now, so Dispatch's reconcile has no reason to read it from
// GitHub again. The table's trigger marks the issue owed, as the envelope's own write does.
func (r *statesProof) stageHead(issue *statesIssue) {
	r.t.Helper()
	if issue.staged {
		return
	}
	issue.staged = true
	if _, err := r.dispatchDB.Exec(context.Background(), `
		insert into pull_request_heads (repo, number, head_sha, head_ref, base_ref, state, issue_key, attribution_source,
			title, url, event_at, checked_at)
		values ($1, $2, $3, $4, 'main', 'open', $5, 'title_body', $6, $7, now(), now())`,
		liveRepo, issue.number, issue.head, claim.IssueBranch(issue.key), issue.key, issue.key+" "+issue.title,
		fmt.Sprintf("https://github.com/%s/pull/%d", liveRepo, issue.number)); err != nil {
		r.fail("pull-request", "stage %s's pull request #%d at %s in Dispatch's database: %v", issue.key, issue.number, issue.head, err)
	}
	r.note("staged %s's pull request #%d, open at %s on %s, in Dispatch's database", issue.key, issue.number, issue.head, claim.IssueBranch(issue.key))
}

// statesBefore waits for the daemon to hold Dispatch's first state of each check at issue's head,
// no for no_result, which the staged pull request made Dispatch publish: the states from before any
// result. A read that finds them missing is UNKNOWN, which fails the case once its own judgement is
// made, so a run where no state reaches the daemon fails where the case says it does.
func (r *statesProof) statesBefore(issue *statesIssue) {
	r.t.Helper()
	deadline := time.Now().Add(time.Minute)
	for _, check := range []string{"k1", "k2"} {
		var line *loggedRecord
		for line == nil && time.Now().Before(deadline) {
			line = r.lastLog(func(l loggedRecord) bool {
				return l.Message == statesHeld && l.Attrs["issue"] == issue.key && l.Attrs["check"] == check && l.Attrs["head"] == issue.head
			})
			if line == nil {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if line == nil {
			r.blind = append(r.blind, fmt.Sprintf("the daemon held no state of %s at %s of %s within a minute of its pull request's staging", check, issue.head, issue.key))
			r.note("UNKNOWN: the daemon holds no state of %s at %s yet", check, issue.head)
			continue
		}
		r.note("daemon: %s issue=%v check=%v head=%v state=%v reason=%v", line.Message, line.Attrs["issue"], line.Attrs["check"], line.Attrs["head"],
			line.Attrs["state"], line.Attrs["reason"])
	}
}

// record records each of results on issue at its head with the real `dispatch result`, as session
// in role, reads each back from Dispatch's database, and keeps it in issue.recorded.
func (r *statesProof) record(issue *statesIssue, session, role string, results ...statesResult) {
	r.t.Helper()
	for _, result := range results {
		args := []string{"result", "--issue", issue.key, "--check", result.check, "--role", role, "--verdict", result.verdict,
			"--commit", issue.head, "--surface", "cli", "--observed", "The " + role + "'s run of " + result.check + " at the head: " + result.verdict + "."}
		if result.verdict == "fail" {
			args = append(args, "--failed", "1")
		}
		out, code := r.cli(session, args...)
		id := statesResultID.FindStringSubmatch(out)
		if code != 0 || id == nil {
			r.fail("result", "dispatch result %s %s of %s as %s exited %d: %s", result.verdict, result.check, issue.key, role, code, strings.TrimSpace(out))
		}
		stored := r.storedResults("check_results", "id::text = $1", id[1])
		if len(stored) != 1 {
			r.fail("result", "UNKNOWN: Dispatch's check_results holds %d rows of the result %s the command reported", len(stored), id[1])
		}
		issue.recorded = append(issue.recorded, statesRecordedAs(role, result.check, result.verdict, issue.head))
		r.note("dispatch result as %s session %s: %s; Dispatch stores %s", role, session, firstLine(strings.TrimSpace(out)), stored[0])
	}
}

// statesRecordedAs is one result as resultsHeld compares what a case recorded with what Dispatch
// holds: its role, check, verdict and commit.
func statesRecordedAs(role, check, verdict, commit string) string {
	return fmt.Sprintf("%s %s %s at %s", role, check, verdict, commit)
}

// statesResultID is the result id `dispatch result` prints: `Result <id>.`.
var statesResultID = regexp.MustCompile(`Result ([0-9a-f-]{36})`)

// writeTestRecord writes issue's test record with the real `dispatch handoff-write` as the tester's
// session, its body naming the head the tester tested under `head`, and prints the check states
// serial Dispatch stored with it, "none" where it stores none.
func (r *statesProof) writeTestRecord(issue *statesIssue) {
	r.t.Helper()
	path := filepath.Join(r.cliHome, issue.key+"-test.json")
	writeProofFile(r.t, path, string(headBody(issue.key, "test", issue.head)))
	out, code := r.cli(issue.tester, "handoff-write", "--issue", issue.key, "--phase", "test", "--body-file", path)
	written := parseWritten(r.t, out, code)
	var read struct {
		CheckStatesSerial *int64 `json:"check_states_serial"`
	}
	r.dispatch.agentGet("/api/v1/handoffs/"+written.id, &read)
	serial := "none"
	if read.CheckStatesSerial != nil {
		serial = strconv.FormatInt(*read.CheckStatesSerial, 10)
	}
	r.note("dispatch handoff-write as the tester: %s; head %s, check states serial %s", written.line, issue.head, serial)
}

// complete has the tester's pane complete testing through its real `legion` tool: an operator's
// task (`legion claims deliver`) starts a turn the tester's stand-in answers with one call of
// handoff_complete carrying summary and no verdict. The tool must take it. It answers the moment
// the turn that makes the call reached the tester's model, which comes before the tool's request
// reaches the daemon: a bound measured from it is never shorter than one measured from the
// completion itself.
func (r *statesProof) complete(issue *statesIssue, summary string) time.Time {
	r.t.Helper()
	token := r.token(issue.key, claim.RoleTester)
	r.until("completion", "the tester's pane to be between turns", time.Minute, func() bool {
		state := r.claim(token).State
		return state == string(supervise.StateIdle) || state == string(supervise.StateReady)
	})
	marker := "CHECK-STATES " + issue.key + ": complete testing"
	call := r.tester.arm(marker, map[string]any{"op": "handoff_complete", "summary": summary}, false)
	task := marker + " with the legion tool."
	from := r.daemon.count()
	r.note("legion claims deliver --claim %s --task %q:\n%s", token, task,
		indentOutput(r.legionClaims("deliver", "--claim", string(token), "--task", task)))
	var result toolResult
	var reached time.Time
	select {
	case <-call.reached:
		reached = time.Now()
	case <-time.After(2 * time.Minute):
		r.fail("completion", "no turn of the tester's pane carried the task %q within two minutes; %s", task, r.tester.lastRequest())
	}
	select {
	case result = <-call.result:
	case <-time.After(2 * time.Minute):
		r.fail("completion", "the tester's pane sent no result of its legion call within two minutes")
	}
	r.note("the tester's turn called legion %s; the tool answered (error %t): %s", inputOf(call), result.isError, result.text)
	if line := r.daemon.firstAfter(from, func(l loggedRecord) bool {
		return l.Message == lateCompletionAnswered && l.Attrs["claim"] == string(token)
	}); line != nil {
		r.note("daemon, %s after the turn reached the model: %s", line.at.Sub(reached).Round(time.Millisecond), answeredLine(*line))
	}
	if result.isError {
		r.fail("completion", "the tester's legion tool refused the completion of testing with no verdict: %s", result.text)
	}
	return reached
}

// phaseOf is issue's phase as GET /legion/v1/state shows it now. An answer that holds no such issue
// is UNKNOWN and fails the case at assertion: the daemon's phase of it cannot be read.
func (r *statesProof) phaseOf(issue *statesIssue, assertion string) phase.Phase {
	r.t.Helper()
	held, ok := r.d.state().Issues[issue.key]
	if !ok {
		r.fail(assertion, "UNKNOWN: GET /legion/v1/state holds no issue %s, so its phase cannot be read", issue.key)
	}
	return held.Phase
}

// movesWithin holds that GET /legion/v1/state shows issue in want within statesWithin of from, the
// moment of trigger, the operation that moves it. It fails at assertion at once when the issue
// leaves testing for another phase, and when it is still in testing statesLate after from. An
// issue that reaches want later than the bound fails at `<assertion>-within`, naming how late.
func (r *statesProof) movesWithin(issue *statesIssue, want phase.Phase, assertion string, from time.Time, trigger string) {
	r.t.Helper()
	got := r.phaseOf(issue, assertion)
	for got == phase.Testing && time.Since(from) < statesLate {
		time.Sleep(100 * time.Millisecond)
		got = r.phaseOf(issue, assertion)
	}
	seen := time.Since(from).Round(time.Millisecond)
	if got != want {
		r.fail(assertion, "GET /legion/v1/state shows %s in %s %s after %s, want %s", issue.key, got, seen, trigger, want)
	}
	if seen > statesWithin {
		r.fail(assertion+"-within", "GET /legion/v1/state shows %s in %s only %s after %s, past the bound of %s", issue.key, want, seen, trigger, statesWithin)
	}
	r.note("read back: GET /legion/v1/state shows %s in %s %s after %s (bound %s)", issue.key, got, seen, trigger, statesWithin)
	for _, state := range r.publishedStates(issue) {
		r.note("  Dispatch's published state: %s", state)
	}
}

// reviewerLaunched waits, until statesLate has passed from from, the moment of trigger, for the
// daemon to start issue's reviewer: to hold its claim and to have asked the runtime to launch its
// pane. It answers how long after from it saw both, and fails the case at assertion when the
// reviewer has not started by then.
func (r *statesProof) reviewerLaunched(issue *statesIssue, assertion string, from time.Time, trigger string) time.Duration {
	r.t.Helper()
	reviewer := r.token(issue.key, claim.RoleReviewer)
	started := func() bool {
		return len(r.rt.LaunchesOf(reviewer)) > 0 && slices.ContainsFunc(r.d.claims(), func(c api.OperatorClaim) bool { return c.Token == reviewer })
	}
	held := started()
	for !held && time.Since(from) < statesLate {
		time.Sleep(100 * time.Millisecond)
		held = started()
	}
	seen := time.Since(from).Round(time.Millisecond)
	launches := r.rt.LaunchesOf(reviewer)
	if !held {
		r.fail(assertion, "the daemon did not start %s within %s of %s: %d launches of its pane", reviewer, statesLate, trigger, len(launches))
	}
	r.note("read back: the daemon started the reviewer %s %s after %s (%d launches of its pane, generation %d)",
		reviewer, seen, trigger, len(launches), launches[len(launches)-1].Generation)
	return seen
}

// reviewerStarted holds that the daemon starts issue's reviewer within statesWithin of from, the
// moment of trigger; a reviewer that starts later fails at reviewer-started-within.
func (r *statesProof) reviewerStarted(issue *statesIssue, from time.Time, trigger string) {
	r.t.Helper()
	if seen := r.reviewerLaunched(issue, "reviewer-started", from, trigger); seen > statesWithin {
		r.fail("reviewer-started-within", "the daemon started %s only %s after %s, past the bound of %s",
			r.token(issue.key, claim.RoleReviewer), seen, trigger, statesWithin)
	}
}

// implementerTask is the task the daemon queued for issue's implementer when the move sent the
// issue back, read from the claim's pending delivery in the daemon's store, which the implementers'
// held turn keeps pending.
func (r *statesProof) implementerTask(issue *statesIssue) string {
	r.t.Helper()
	token := r.token(issue.key, claim.RoleImplementer)
	deadline := time.Now().Add(time.Minute)
	for {
		var task string
		err := r.st.Pool().QueryRow(context.Background(),
			`select task from pending_task_deliveries where claim_token = $1`, string(token)).Scan(&task)
		switch {
		case err == nil && task != "":
			return task
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			r.fail("task", "read the pending task of %s from the daemon's store: %v", token, err)
		}
		if time.Now().After(deadline) {
			r.fail("task", "UNKNOWN: the daemon's store holds no pending task of %s within a minute of the move, so what the task names cannot be read", token)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// noImplementerTask holds that the daemon gave issue's implementer nothing: its store holds no
// pending task of the implementer's claim, and the runtime was never asked to launch the
// implementer's pane, which a task taken already would have needed.
func (r *statesProof) noImplementerTask(issue *statesIssue) {
	r.t.Helper()
	token := r.token(issue.key, claim.RoleImplementer)
	var task string
	err := r.st.Pool().QueryRow(context.Background(),
		`select task from pending_task_deliveries where claim_token = $1`, string(token)).Scan(&task)
	switch {
	case err == nil:
		r.fail("no-task", "the daemon's store holds a pending task of %s, whose issue did not go back: %q", token, task)
	case !errors.Is(err, pgx.ErrNoRows):
		r.fail("no-task", "read the pending task of %s from the daemon's store: %v", token, err)
	}
	if launches := r.rt.LaunchesOf(token); len(launches) > 0 {
		r.fail("no-task", "the daemon launched the pane of %s %d times, whose issue did not go back", token, len(launches))
	}
	r.note("read back: the daemon's store holds no pending task of %s, and the runtime never launched its pane", token)
}

// resultsHeld reads every result Dispatch's database holds of issue, in check_results and
// ci_check_results, and holds that they are exactly the results the case recorded on it, each by
// the role that recorded it at the issue's head. An issue the case recorded nothing on must hold
// none, and the case says so.
func (r *statesProof) resultsHeld(issue *statesIssue) {
	r.t.Helper()
	var held []string
	for _, table := range []string{"check_results", "ci_check_results"} {
		for _, stored := range r.storedResults(table, "issue_key = $1", issue.key) {
			held = append(held, statesRecordedAs(stored.role, stored.check, stored.verdict, stored.commit))
		}
	}
	want := slices.Clone(issue.recorded)
	slices.Sort(held)
	slices.Sort(want)
	if !slices.Equal(held, want) {
		r.fail("results", "Dispatch's check_results and ci_check_results hold %q of %s, and the case recorded %q", held, issue.key, want)
	}
	if len(held) == 0 {
		r.note("read back: Dispatch's check_results and ci_check_results hold no result of %s, and the case recorded none", issue.key)
		return
	}
	r.note("read back: Dispatch's check_results and ci_check_results hold of %s exactly the %d results the case recorded: %s", issue.key, len(held), strings.Join(held, "; "))
}

// noBeforeMergeCheck reads issue's checks as Dispatch answers them (GET /api/v1/issues/<key>/results)
// once it has indexed the document's blocks, and holds that the issue has a Verification document
// whose checks hold no before_merge one. It answers that document's id.
func (r *statesProof) noBeforeMergeCheck(issue *statesIssue) string {
	r.t.Helper()
	type answer struct {
		VerificationArtifactID string `json:"verification_artifact_id"`
		Checks                 []struct {
			BlockID string `json:"block_id"`
			When    string `json:"when"`
		} `json:"checks"`
	}
	var read answer
	for deadline := time.Now().Add(statesLate); ; time.Sleep(100 * time.Millisecond) {
		read = answer{}
		r.dispatch.agentGet("/api/v1/issues/"+issue.key+"/results", &read)
		if len(read.Checks) > 0 || time.Now().After(deadline) {
			break
		}
	}
	if read.VerificationArtifactID == "" || len(read.Checks) == 0 {
		r.fail("document", "UNKNOWN: Dispatch answers %s with the Verification document %q and %d checks %s after the case asked, so it cannot be read as a document that holds checks, none before_merge",
			issue.key, read.VerificationArtifactID, len(read.Checks), statesLate)
	}
	var checks []string
	for _, check := range read.Checks {
		if check.When == "before_merge" {
			r.fail("document", "Dispatch answers %s's check %s as before_merge: the case's document must hold none", issue.key, check.BlockID)
		}
		checks = append(checks, check.BlockID+" "+check.When)
	}
	r.note("read back: Dispatch answers %s's Verification document %s with the checks [%s], none before_merge", issue.key, read.VerificationArtifactID, strings.Join(checks, ", "))
	return read.VerificationArtifactID
}

// emptySet holds that Dispatch's check_states holds of issue only the set row at its head, with no
// check, computed from the Verification document document: the published state of a head whose
// issue has a document with no before_merge check, and not of an issue with no document.
func (r *statesProof) emptySet(issue *statesIssue, document string) {
	r.t.Helper()
	published := r.publishedStates(issue)
	if len(published) != 1 || published[0].check != "" || published[0].head != issue.head || len(published[0].checks) != 0 || published[0].document != document {
		r.fail("empty-set", "Dispatch's check_states holds %v of %s, want only the set at %s with no check, from the Verification document %s", published, issue.key, issue.head, document)
	}
	r.note("read back: Dispatch's check_states holds of %s only the set at %s, with no check, computed from the Verification document %s", issue.key, issue.head, document)
}

// issueClaims is the tokens of every claim the daemon holds of issue, sorted.
func (r *statesProof) issueClaims(issue *statesIssue) []string {
	var tokens []string
	for _, held := range r.d.claims() {
		if held.Issue == issue.key {
			tokens = append(tokens, string(held.Token))
		}
	}
	slices.Sort(tokens)
	return tokens
}

// roleSessions is every session a tester's or an implementer's result of the run may be written
// under: each tester's pane's own, and the implementer's session pass-goes-to-review records as.
func (r *statesProof) roleSessions() []string {
	var sessions []string
	for _, issue := range r.allIssues() {
		if issue.tester != "" {
			sessions = append(sessions, issue.tester)
		}
		sessions = append(sessions, "implementer-"+strings.ToLower(issue.key))
	}
	return sessions
}

// cli runs the real `dispatch` CLI as session (handoff_records_live_test.go's cliShell) and answers
// both streams together and its exit code.
func (r *statesProof) cli(session string, args ...string) (string, int) {
	r.t.Helper()
	out, code := cliShell{t: r.t, dir: r.cliHome, session: session, base: r.dispatch.base, token: r.dispatch.token}.bun(nil, dispatchCLI(r.root), args...)
	return string(out.stdout) + string(out.stderr), code
}

// ---- Dispatch's own records ------------------------------------------------------------------------

// statesStored is one row of check_results, ci_check_results or one of their retraction tables:
// the issue and check of a result (empty for a retraction, which names its result alone), its role
// and verdict, the session it was written under and the actor Dispatch took it from.
type statesStored struct {
	table, id, issue, check, role, verdict, commit, session, actorKind, actorID string
}

func (s statesStored) String() string {
	if s.issue == "" {
		return fmt.Sprintf("retraction of %s by %s %s (session %q)", s.id, s.actorKind, s.actorID, s.session)
	}
	return fmt.Sprintf("%s: %s %s of %s at %s as %s by %s %s (session %q)", s.id, s.check, s.verdict, s.issue, s.commit, s.role, s.actorKind, s.actorID, s.session)
}

// storedResults is every row of table in Dispatch's database, or of those where holds: a condition
// on the table's own columns, with args.
func (r *statesProof) storedResults(table, where string, args ...any) []statesStored {
	r.t.Helper()
	var query string
	switch table {
	case "check_results":
		query = `select id::text, issue_key, check_block_id, role, verdict, commit_sha, session, actor->>'kind', actor->>'id' from check_results`
	case "ci_check_results":
		query = `select id::text, issue_key, check_block_id, 'ci', verdict, commit_sha, session, actor->>'kind', actor->>'id' from ci_check_results`
	default:
		query = `select result_id::text, '', '', '', '', '', session, actor->>'kind', actor->>'id' from ` + table
	}
	if where != "" {
		query += ` where ` + where
	}
	rows, err := r.dispatchDB.Query(context.Background(), query+` order by 1`, args...)
	if err != nil {
		r.fail("results", "read Dispatch's %s: %v", table, err)
	}
	defer rows.Close()
	var stored []statesStored
	for rows.Next() {
		row := statesStored{table: table}
		var kind, actor *string
		if err := rows.Scan(&row.id, &row.issue, &row.check, &row.role, &row.verdict, &row.commit, &row.session, &kind, &actor); err != nil {
			r.fail("results", "read a row of Dispatch's %s: %v", table, err)
		}
		if kind != nil {
			row.actorKind = *kind
		}
		if actor != nil {
			row.actorID = *actor
		}
		stored = append(stored, row)
	}
	if err := rows.Err(); err != nil {
		r.fail("results", "read Dispatch's %s: %v", table, err)
	}
	return stored
}

// statesPublished is one row of Dispatch's check_states: the newest state Dispatch published of a
// check at a head, or, where check is empty, the head's set of checks. document is the
// Verification document the row was computed from, empty where the issue had none.
type statesPublished struct {
	check, head, state, reason, document string
	checks                               []string
}

func (p statesPublished) String() string {
	if p.check == "" {
		return fmt.Sprintf("the set at %s: [%s] (document %q)", p.head, strings.Join(p.checks, ","), p.document)
	}
	return fmt.Sprintf("%s at %s: %s (%s)", p.check, p.head, p.state, p.reason)
}

// publishedStates is each state Dispatch has published of issue's checks, as its check_states
// table holds the newest, and the set row of each head.
func (r *statesProof) publishedStates(issue *statesIssue) []statesPublished {
	r.t.Helper()
	rows, err := r.dispatchDB.Query(context.Background(), `
		select check_block_id, head_sha, coalesce(state, ''), reason, coalesce(check_artifact_id::text, ''), checks
		from check_states where issue_key = $1 order by head_sha, check_block_id`, issue.key)
	if err != nil {
		r.fail("published-states", "read Dispatch's check_states of %s: %v", issue.key, err)
	}
	defer rows.Close()
	var states []statesPublished
	for rows.Next() {
		var state statesPublished
		if err := rows.Scan(&state.check, &state.head, &state.state, &state.reason, &state.document, &state.checks); err != nil {
			r.fail("published-states", "read a state of %s: %v", issue.key, err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		r.fail("published-states", "read Dispatch's check_states of %s: %v", issue.key, err)
	}
	if len(states) == 0 {
		r.fail("published-states", "UNKNOWN: Dispatch's check_states holds no state of %s, whose move the states decided", issue.key)
	}
	return states
}

// resultWrites is each request Dispatch's access log holds (accesslog.Message) that records or
// retracts a result, its fields by name.
func (r *statesProof) resultWrites() []map[string]string {
	r.t.Helper()
	routes := regexp.MustCompile(`^/api/v1/(issues/[^/]+/results|projects/[^/]+/results|results/[^/]+/retract)$`)
	var writes []map[string]string
	scanner := bufio.NewScanner(strings.NewReader(r.dispatch.output.String()))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if !strings.Contains(scanner.Text(), `msg="dispatch: request"`) {
			continue
		}
		fields := logfmtFields(scanner.Text())
		if fields["method"] == http.MethodPost && routes.MatchString(fields["path"]) {
			writes = append(writes, fields)
		}
	}
	if err := scanner.Err(); err != nil {
		r.fail("access-log", "read Dispatch's access log: %v", err)
	}
	return writes
}

// logfmtFields is one line of slog's text handler, which cmd/dispatch logs through, as its fields
// by key: a value is bare, or quoted as strconv.Quote quotes it.
func logfmtFields(line string) map[string]string {
	fields := map[string]string{}
	rest := strings.TrimSpace(line)
	for rest != "" {
		key, after, found := strings.Cut(rest, "=")
		if !found || strings.ContainsAny(key, " \"") {
			break
		}
		var value string
		if strings.HasPrefix(after, `"`) {
			quoted, err := strconv.QuotedPrefix(after)
			if err != nil {
				break
			}
			value, _ = strconv.Unquote(quoted)
			rest = strings.TrimLeft(after[len(quoted):], " ")
		} else {
			value, rest, _ = strings.Cut(after, " ")
			rest = strings.TrimLeft(rest, " ")
		}
		fields[key] = value
	}
	return fields
}

// ---- the run's own broker ----------------------------------------------------------------------------

// stopBroker stops the run's own nats-server with SIGSTOP, by the pid the run started it with
// (statesBrokerPIDEnv), once that pid is shown to be a nats-server listening on the port the run
// gave the daemon. A watchdog of its own continues it after a minute whatever happens to this
// driver, since a stopped broker holds the TERM the script's cleanup sends it, and that cleanup
// waits for it.
func (r *statesProof) stopBroker() {
	r.t.Helper()
	pid, why := statesBroker()
	if pid == 0 {
		r.fail("broker", "UNKNOWN: %s", why)
	}
	watchdog := exec.Command("sh", "-c", fmt.Sprintf("sleep 60; kill -CONT %d", pid))
	watchdog.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := watchdog.Start(); err != nil {
		r.fail("broker", "start the watchdog that continues the broker: %v", err)
	}
	r.t.Cleanup(func() {
		r.continueBroker()
		_ = watchdog.Process.Kill()
		_ = watchdog.Wait()
	})
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		r.fail("broker", "SIGSTOP the run's nats-server (pid %d): %v", pid, err)
	}
	r.stopped = pid
	state := ""
	for deadline := time.Now().Add(5 * time.Second); state != "T" && time.Now().Before(deadline); {
		state = processState(pid)
		time.Sleep(20 * time.Millisecond)
	}
	if state != "T" {
		r.fail("broker", "the run's nats-server (pid %d) reads state %q after SIGSTOP, not stopped", pid, state)
	}
	r.note("stopped the run's nats-server (pid %d, %s) with SIGSTOP: its state reads %s", pid, why, state)
}

// continueBroker sends SIGCONT to the broker stopBroker stopped, once, and answers the moment just
// before it sent it, which the move after the broker comes back is measured from; the zero time
// when no broker was stopped or the signal failed.
func (r *statesProof) continueBroker() time.Time {
	if r.stopped == 0 {
		return time.Time{}
	}
	pid := r.stopped
	r.stopped = 0
	sent := time.Now()
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		r.t.Errorf("SIGCONT the run's nats-server (pid %d): %v", pid, err)
		return time.Time{}
	}
	r.note("continued the run's nats-server (pid %d) with SIGCONT: its state reads %s", pid, processState(pid))
	return sent
}

// statesBroker is the pid statesBrokerPIDEnv names when it is a nats-server listening on the port
// LEGION_E2E_NATS_URL names, with what was read of it; 0 and why it could not be held to be.
func statesBroker() (int, string) {
	text := os.Getenv(statesBrokerPIDEnv)
	if text == "" {
		return 0, statesBrokerPIDEnv + " is unset: scripts/e2e/check-states.sh exports the pid of the nats-server lib/live-daemon-proof.sh starts, and without it no broker can be stopped by the pid the run started it with"
	}
	pid, err := strconv.Atoi(text)
	if err != nil || pid <= 1 {
		return 0, fmt.Sprintf("%s=%q is no pid", statesBrokerPIDEnv, text)
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil || strings.TrimSpace(string(comm)) != "nats-server" {
		return 0, fmt.Sprintf("pid %d is not a nats-server (comm %q, %v)", pid, strings.TrimSpace(string(comm)), err)
	}
	broker, err := url.Parse(os.Getenv(liveNATSURLEnv))
	if err != nil || broker.Port() == "" {
		return 0, fmt.Sprintf("%s=%q names no port (%v)", liveNATSURLEnv, os.Getenv(liveNATSURLEnv), err)
	}
	port, err := strconv.Atoi(broker.Port())
	if err != nil {
		return 0, fmt.Sprintf("%s names the port %q (%v)", liveNATSURLEnv, broker.Port(), err)
	}
	inodes := listeningInodes(port)
	if len(inodes) == 0 {
		return 0, fmt.Sprintf("no socket listens on port %d in /proc/net/tcp or tcp6", port)
	}
	descriptors, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0, fmt.Sprintf("read pid %d's descriptors: %v", pid, err)
	}
	for _, descriptor := range descriptors {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, descriptor.Name()))
		if err == nil && slices.Contains(inodes, target) {
			return pid, fmt.Sprintf("listening on port %d as %s", port, target)
		}
	}
	return 0, fmt.Sprintf("pid %d is a nats-server and holds none of the sockets %v that listen on port %d", pid, inodes, port)
}

// listeningInodes is each socket of /proc/net/tcp and tcp6 that listens on port, as a descriptor's
// link names it (`socket:[<inode>]`).
func listeningInodes(port int) []string {
	suffix := fmt.Sprintf(":%04X", port)
	var inodes []string
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(table)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			// local_address, st and inode are the second, fourth and tenth columns; 0A is LISTEN.
			if len(fields) > 9 && strings.HasSuffix(fields[1], suffix) && fields[3] == "0A" {
				inodes = append(inodes, "socket:["+fields[9]+"]")
			}
		}
		file.Close()
	}
	return inodes
}

// processState is the state letter /proc/<pid>/stat reads for pid (T for a stopped one), "" when it
// cannot be read.
func processState(pid int) string {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	_, rest, found := strings.Cut(string(stat), ") ")
	if !found || rest == "" {
		return ""
	}
	return rest[:1]
}

// ---- GitHub, the Oh My Pi profile and the overlays ----------------------------------------------------

// serveGitHub starts the stand-in of GitHub's REST API for the run's pull requests, as the daemon
// reads them: each open on base main at its issue's head, mergeable and clean, with no reviews, the
// repository with default branch main, every other repository the same, and a GraphQL root that
// knows no pull request. Anything else is answered 404, so a read the proof did not foresee fails
// the case that waited on it rather than reaching github.com.
func (r *statesProof) serveGitHub() {
	pulls := map[string]map[string]any{}
	var listed []any
	for _, issue := range r.allIssues() {
		pull := map[string]any{
			"number": issue.number, "state": "open", "mergeable": true, "mergeable_state": "clean",
			"base": map[string]string{"ref": "main"},
			"head": map[string]string{"ref": claim.IssueBranch(issue.key), "sha": issue.head},
		}
		pulls[fmt.Sprintf("/pulls/%d", issue.number)] = pull
		pulls[fmt.Sprintf("/pulls/%d/reviews", issue.number)] = nil
		listed = append(listed, pull)
	}
	r.github = githubtest.Server(r.t, githubtest.Spec{Handle: func(w http.ResponseWriter, req *http.Request) {
		write := func(value any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(value)
		}
		if req.Method == http.MethodPost && req.URL.Path == "/graphql" {
			write(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": nil}}})
			return
		}
		rest, ours := strings.CutPrefix(req.URL.Path, "/repos/"+liveRepo)
		switch pull, isPull := pulls[rest]; {
		case req.Method != http.MethodGet:
		case ours && rest == "":
			write(map[string]any{"full_name": liveRepo, "default_branch": "main"})
			return
		case ours && rest == "/pulls":
			write(listed)
			return
		case ours && isPull && pull == nil:
			write([]any{})
			return
		case ours && isPull:
			write(pull)
			return
		case !ours:
			if other, isRepository := strings.CutPrefix(req.URL.Path, "/repos/"); isRepository && len(strings.Split(other, "/")) == 2 {
				write(map[string]any{"full_name": other, "default_branch": "main"})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}})
}

// writeOmpProfile writes the profile the panes share: the three stand-in providers, one model
// each, and the settings a pane needs to run unattended.
func (r *statesProof) writeOmpProfile() {
	t := r.t
	t.Helper()
	agent := filepath.Join(r.home, ".omp", "profiles", r.profile, "agent")
	if err := os.MkdirAll(agent, 0o700); err != nil {
		t.Fatalf("make the profile's agent directory %s: %v", agent, err)
	}
	var models, enabled strings.Builder
	models.WriteString("# Written by packages/daemon/internal/daemon/check_states_live_test.go.\nproviders:\n")
	for _, stand := range []*modelStandin{r.tester.modelStandin, r.implementer.modelStandin, r.spare.modelStandin} {
		fmt.Fprintf(&models, "  %s:\n    baseUrl: %s\n    api: anthropic-messages\n    apiKey: a-literal-key-no-endpoint-checks\n"+
			"    models:\n      - id: %s\n        name: %s\n        contextWindow: 200000\n        maxTokens: 8192\n",
			stand.name, stand.url, stand.name+standinModelID, stand.name)
		fmt.Fprintf(&enabled, "  - %s\n", standinModel(stand.name))
	}
	writeProofFile(t, filepath.Join(agent, "models.yml"), models.String())
	writeProofFile(t, filepath.Join(agent, "config.yml"),
		"# Written by packages/daemon/internal/daemon/check_states_live_test.go.\n"+
			"enabledModels:\n"+enabled.String()+
			// A fresh profile opens Oh My Pi's setup wizard, and a pane waiting on it never starts.
			"startup:\n  setupWizard: false\n")
}

// writeOverlays writes the Oh My Pi overlays each pane is told through PI_CONFIG_FILES: the
// operator's own, which every pane names first and which turns `retry.enabled` off so the host
// walks no fallback chain of its own, then one naming the single model of the role's stand-in:
// the testers', the implementers', and the spare's for every other role (keyed "").
func (r *statesProof) writeOverlays(work string) {
	t := r.t
	t.Helper()
	dir := filepath.Join(work, "overlays")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	operator := filepath.Join(dir, "operator.yml")
	writeProofFile(t, operator, "# The operator's pane overlay (runtime.tmux.pi_config_files).\nretry:\n  enabled: false\n")
	r.overlays = map[claim.Role][]string{}
	for role, provider := range map[claim.Role]string{
		claim.RoleTester: statesTesterProvider, claim.RoleImplementer: statesImplementerProvider, "": statesSpareProvider,
	} {
		path := filepath.Join(dir, provider+".yml")
		writeRoleOverlay(t, path, standinModel(provider))
		r.overlays[role] = []string{operator, path}
	}
}
