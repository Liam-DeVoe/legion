package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/sjawhar/legion/daemon/internal/answerloop"
	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/ghtool"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
	"github.com/sjawhar/legion/daemon/internal/ompsessions"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// servedCheckReader is the Sandbox runtime's reader of the shared check as a test serves it
// (sandbox.LeakCheckReader): one packed check at any tag, a launcher that answers the install's
// `--help` run as the real check does and clears every call after.
type servedCheckReader struct{ body []byte }

func (r servedCheckReader) Get(_ context.Context, tag string) (leakguard.Served, error) {
	return leakguard.Served{Tag: tag, Body: r.body}, nil
}

func newServedCheckReader(t *testing.T) servedCheckReader {
	t.Helper()
	launcher := "#!/bin/sh\nif [ \"$1\" = --help ]; then\n" + leakguardtest.AnswersHelp(filepath.Join(t.TempDir(), "help-runs")) + "fi\n" + leakguardtest.Passes
	return servedCheckReader{body: leakguardtest.Tarball(t, map[string]leakguardtest.Member{
		leakguardtest.CheckName: {Body: launcher, Mode: 0o755},
	})}
}

// readTrouble is how the check's read fails while a test holds it failing: GitHub answering the
// tarball read with status, or with tarball in place of the check's; the read's transport failing
// with transport; or the read's mint failing with mint. forgets gives the source a Forget, as
// leakCheckSource gives the daemon's own where its App source forgets a lease.
type readTrouble struct {
	status    int
	tarball   []byte
	transport error
	mint      error
	forgets   bool
}

// troubledSource is the daemon's reader of the check, a *leakguard.Source as leakCheckSource
// builds one, over a stand-in of the check's repository: every read fails as its trouble says
// while failing holds, and the check is served once failing is cleared. status is the HTTP status
// the stand-in answers a failing read with, 0 for none, and tries counts every read asked of it.
type troubledSource struct {
	source  *leakguard.Source
	failing atomic.Bool
	status  atomic.Int32
	tries   atomic.Int32
}

func newTroubledSource(t *testing.T, trouble readTrouble) *troubledSource {
	t.Helper()
	raw := leakCheckTarball(t)
	s := &troubledSource{}
	s.failing.Store(true)
	s.status.Store(int32(trouble.status))
	url := githubtest.Server(t, githubtest.Spec{Token: leakCheckLease, Handle: func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/"+leakguard.CheckSlug+"/tarball/") {
			githubtest.Unexpected(t, w, r)
			return
		}
		switch status := int(s.status.Load()); {
		case s.failing.Load() && status != 0:
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, "{\"message\":%q}\n", http.StatusText(status))
		case s.failing.Load() && trouble.tarball != nil:
			_, _ = w.Write(trouble.tarball)
		default:
			_, _ = w.Write(raw)
		}
	}})
	s.source = &leakguard.Source{API: url, Token: func(context.Context) (string, error) {
		if s.failing.Load() && trouble.mint != nil {
			return "", trouble.mint
		}
		return leakCheckLease, nil
	}}
	if trouble.transport != nil {
		s.source.Client = &http.Client{Transport: failingTransport{failing: &s.failing, err: trouble.transport}}
	}
	if trouble.forgets {
		s.source.Forget = func() {}
	}
	return s
}

func (s *troubledSource) Get(ctx context.Context, tag string) (leakguard.Served, error) {
	s.tries.Add(1)
	return s.source.Get(ctx, tag)
}

// failingTransport fails every request with err while failing holds, as a connection GitHub
// never answers does, and makes it through Go's own transport once failing is cleared.
type failingTransport struct {
	failing *atomic.Bool
	err     error
}

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.failing.Load() {
		return nil, f.err
	}
	return http.DefaultTransport.RoundTrip(r)
}

// answerCalls is the gh tool's record of an answer's calls as these tests keep it
// (ghtool.Recorder), by seq.
type answerCalls struct {
	mu    sync.Mutex
	calls map[int]store.AnswerToolCall
}

func (c *answerCalls) StartAnswerToolCall(_ context.Context, call store.AnswerToolCall) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[int]store.AnswerToolCall{}
	}
	c.calls[call.Seq] = call
	return nil
}

func (c *answerCalls) FinishAnswerToolCall(_ context.Context, _ string, seq int, result string, exit *int, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	call := c.calls[seq]
	call.Result, call.Exit, call.FinishedAt = &result, exit, &at
	c.calls[seq] = call
	return nil
}

func (c *answerCalls) AnswerToolCall(_ context.Context, _ string, seq int) (store.AnswerToolCall, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	call, ok := c.calls[seq]
	return call, ok, nil
}

// installedCheck is the path c answers, failing t where it answers none.
func installedCheck(t *testing.T, c ghtool.LeakChecker) string {
	t.Helper()
	check, err := c.Check()
	if err != nil {
		t.Fatalf("the check is not installed: %v", err)
	}
	return check
}

// openAnswerTool is one answer's gh tool under a host whose check is check and whose state
// directory is stateDir. Its gh is a path nothing is at: a call past the check is refused at gh's
// help read, before anything of it could reach GitHub.
func openAnswerTool(t *testing.T, stateDir string, check ghtool.LeakChecker, log *slog.Logger) answerloop.Tool {
	t.Helper()
	tools := ghTools{host: ghtool.Host{
		GH: filepath.Join(t.TempDir(), "gh"), Legion: "/opt/legion/bin/legion", StateDir: stateDir, LeakCheck: check, Environ: []string{"PATH=/usr/bin:/bin"},
		Credential: appauth.GitHubCredential(outboxTokens{}, legionRepos, ghrepo.Renames{}, legionInstalled, nil, quietLogger()),
		Calls:      &answerCalls{}, Log: log, Now: time.Now,
	}, bounds: config.AnswerBounds{GHCall: 20 * time.Second, GHOutputChars: 60000, GHCalls: 20}}
	tool, err := tools.Open(context.Background(), ghtool.Agent{Issue: "LEGION-9", Role: claim.RoleImplementer, Session: "ses_implementer", Phase: "implement",
		Repository: ghrepo.MustParse("legion/legion"), AppID: "1001"}, "dispatch:m-1")
	if err != nil {
		t.Fatalf("open the answer's gh tool: %v", err)
	}
	t.Cleanup(func() { _ = tool.Close() })
	return tool
}

// answerCall is the gh call these tests make, a comment on a pull request of the agent's
// repository.
const answerCall = `["pr","comment","5","-R","legion/legion","--body","Done."]`

// pendingRefusal is the gh tool's refusal of a call before a try of the install lands, the last
// try's cause after it.
const pendingRefusal = "refused: LEAK CHECK SETUP ERROR: the gh call: the shared public leak check at " + leakCheckTag + ", the pin " +
	leakguard.TagVariable + " names, is not installed yet, and the daemon tries the install again until it is: "

// A Kubernetes daemon installs the check its gh tool runs from the Sandbox runtime's reader, at the
// pin boot read, under its state directory, and logs the tag and the path.
func TestAKubernetesDaemonInstallsItsGhToolsCheckFromTheRuntimesReader(t *testing.T) {
	stateDir := t.TempDir()
	var logs syncBuffer
	check, stop, err := installAnswersLeakCheck(context.Background(), stateDir, leakCheckTag, newServedCheckReader(t), slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("installAnswersLeakCheck: %v", err)
	}
	defer stop()
	want := filepath.Join(stateDir, "leak-check", leakguardtest.CheckName)
	if got := installedCheck(t, check); got != want {
		t.Fatalf("installAnswersLeakCheck installed %q, want %s", got, want)
	}
	if line := logs.String(); !strings.Contains(line, "answers: installed the shared public leak check the gh tool's leak guard runs") ||
		!strings.Contains(line, "tag="+leakCheckTag) || !strings.Contains(line, "check="+want) {
		t.Errorf("the install logged %q, want its line naming %s and %s", line, leakCheckTag, want)
	}
}

// A Kubernetes daemon's answer loop is handed the gh tool whose host names the check its boot
// installed through the Sandbox runtime's own reader (sandbox.Runtime.LeakCheck), at the pin boot
// read, under its state directory: the copy every answer's guard runs, whatever the machine holds.
// The runtime is a real one (sandbox.New) over the stand-in API, its reader the served check.
func TestAKubernetesDaemonsAnswerLoopRunsTheCheckItInstalledFromTheRuntimesReader(t *testing.T) {
	url := newIssueSandboxAPI().serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rt, err := sandbox.New(ctx, sandboxRESTConfig(url), sandbox.Options{
		Namespace: "legion", Project: "legion", Store: liveTrees{}, Image: "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64),
		StorageClass: "standard", IssueVolume: resource.MustParse("1Gi"), StreamURL: "tcp://127.0.0.1:13371", Resources: defaultReservations(t),
		SessionStore: ompsessions.Store{Kind: ompsessions.StorePostgres, DSNSecret: "SESSION_DSN"},
		Tools:        sandbox.Tools{GH: "/usr/bin/gh", Git: "/usr/bin/git", JJ: "/usr/bin/jj", Legion: "/opt/legion/bin/legion", AgentSecrets: "/opt/legion/bin/agent-secrets"},
		BootTimeout:  time.Second, RegistrationIntervals: 3, BusyBackoff: time.Millisecond, TerminationGrace: time.Second, ProbeInterval: time.Hour, AdoptTimeout: time.Second,
		Tokens: issueProvisionTokens{}, GitHubCredential: appauth.GitHubCredential(outboxTokens{}, legionRepos, ghrepo.Renames{}, legionInstalled, nil, quietLogger()),
		ControllerCredential: appauth.ControllerCredential(outboxTokens{}, legionRepos, ghrepo.Renames{}, legionInstalled, nil, quietLogger()),
		GitHubDirs:           legionDirs, Conns: fake.NewConns(), Log: quietLogger(),
		RepositoryCache: defaultRepositoryCache(t), Mapped: noRepositories, LeakCheck: newServedCheckReader(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	w := &workflowRuntime{log: quietLogger()}
	cfg := config.Config{StateDir: stateDir, Answers: &config.Answers{}}
	tools, stop, err := w.answersTools(ctx, cfg, plan{answers: answersReads{leakCheckTag: leakCheckTag}}, rt, nil)
	if err != nil {
		t.Fatalf("answersTools: %v", err)
	}
	defer stop()
	if want := filepath.Join(stateDir, "leak-check", leakguardtest.CheckName); tools.host.LeakCheck == nil || installedCheck(t, tools.host.LeakCheck) != want {
		t.Fatalf("the answer loop's gh tool names the check %v; want the copy installed at boot, %s", tools.host.LeakCheck, want)
	}
}

// A tmux daemon's gh tool resolves the machine's own check at each call, in the daemon's own
// environment, the one its host names (ghtool.MachineLeakCheck; internal/ghtool's
// TestEachCallRunsTheMachinesCheckAsItIsWhenTheCallIsMade and
// TestTheMachinesCheckIsResolvedInTheDaemonsEnvironmentAndNotTheGuards), and no second way reads
// it: the host every answer opens under is the one answersTools built.
func TestATmuxDaemonsGhToolResolvesTheMachinesCheckInTheDaemonsEnvironment(t *testing.T) {
	var logs syncBuffer
	w := &workflowRuntime{log: slog.New(slog.NewTextHandler(&logs, nil))}
	cfg := config.Config{StateDir: t.TempDir(), Answers: &config.Answers{}}
	machine := filepath.Join(t.TempDir(), "public-leak-check.sh")
	tools, stop, err := w.answersTools(context.Background(), cfg, plan{answers: answersReads{leakCheck: machine}}, nil, nil)
	if err != nil {
		t.Fatalf("answersTools: %v", err)
	}
	defer stop()
	resolved, ok := tools.host.LeakCheck.(ghtool.MachineLeakCheck)
	if !ok || !slices.Equal(resolved.Environ, tools.host.Environ) || !slices.Equal(tools.host.Environ, os.Environ()) {
		t.Fatalf("a tmux daemon's gh tool names the check %T, want the machine's, resolved in the daemon's own environment", tools.host.LeakCheck)
	}
	if line := logs.String(); !strings.Contains(line, "answers: the gh tool's leak guard runs the machine's shared public leak check, resolved at each call") ||
		!strings.Contains(line, "check="+machine) {
		t.Errorf("answersTools logged %q, want the line naming the check boot found, %s", line, machine)
	}
}

// A failure of the check's read that is GitHub's trouble (appTransient, over the read as
// transientReads reports it) does not stop a Kubernetes daemon's boot: the install step returns
// with no error and installs the check in the background. Here GitHub answers the tarball read
// 502, the read's transport fails, and GitHub's trouble fails the read's mint. Until the install
// lands, the loop's gh call is refused, gh never started, with the setup failure naming the pin
// and the last try's cause; every failed try is logged with its cause and the wait before the
// next; and once the read is served, the check is installed and the same answer's next call is no
// longer refused for it (internal/ghtool's
// TestACallBeforeTheInstallLandsIsRefusedAndTheNextAfterItRuns runs such a call under the check).
func TestATransientFailureOfTheChecksReadBootsTheDaemonAndInstallsTheCheckInTheBackground(t *testing.T) {
	quickAppMints(t)
	for _, tc := range []struct {
		name    string
		trouble readTrouble
		// says is how the cause sentence begins after leakguard.RefusalPrefix, and logged what
		// each failed try's line carries of it.
		says, logged string
	}{
		{"GitHub answers the tarball read 502", readTrouble{status: http.StatusBadGateway},
			"GitHub answered the read of " + leakguard.CheckSlug + " at " + leakCheckTag + ` with HTTP 502: {"message":"Bad Gateway"}`, "HTTP 502"},
		{"GitHub answers 401 to a token the source then forgets", readTrouble{status: http.StatusUnauthorized, forgets: true},
			"GitHub answered the read of " + leakguard.CheckSlug + " at " + leakCheckTag + ` with HTTP 401: {"message":"Unauthorized"}`, "HTTP 401"},
		{"the read's transport fails", readTrouble{transport: errors.New("connection reset by peer")},
			"the daemon could not read " + leakguard.CheckSlug + " at " + leakCheckTag + ": Get ", "connection reset by peer"},
		{"GitHub's trouble fails the read's mint", readTrouble{mint: fmt.Errorf("the daemon could not mint its read-only token for %s: %w",
			leakguard.CheckSlug, &appauth.TransientError{Err: errors.New("GitHub App token exchange: dial tcp: connection refused")})},
			"the daemon could not mint its read-only token for " + leakguard.CheckSlug + ": GitHub App token exchange: dial tcp: connection refused", "connection refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			reader := newTroubledSource(t, tc.trouble)
			stateDir := t.TempDir()
			var logs syncBuffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			check, stop, err := installAnswersLeakCheck(ctx, stateDir, leakCheckTag, reader, log)
			if err != nil {
				t.Fatalf("installAnswersLeakCheck = %v; want no error, the install left to the background", err)
			}
			t.Cleanup(stop)

			tool := openAnswerTool(t, stateDir, check, log)
			refused := tool.Run(ctx, 1, answerCall)
			if want := pendingRefusal + leakguard.RefusalPrefix + tc.says; refused.Ran || refused.Exit != -1 || !strings.HasPrefix(refused.Output, want) ||
				!strings.Contains(refused.Output, tc.logged) {
				t.Fatalf("the loop's gh call before the install = %+v; want the refusal %q…, gh never run", refused, want)
			}

			testwait.Eventually(t, "two failed tries logged", func() bool {
				return reader.tries.Load() >= 2 && strings.Count(logs.String(), "installing it again") >= 2
			})
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				if strings.Contains(line, "installing it again") && (!strings.Contains(line, "tag="+leakCheckTag) || !strings.Contains(line, "retryIn=10ms") ||
					!strings.Contains(line, tc.logged)) {
					t.Errorf("a failed try logged %q, want the pin, the wait before the next try and the cause", line)
				}
			}

			reader.failing.Store(false)
			testwait.Eventually(t, "the install in the background lands", func() bool {
				_, err := check.Check()
				return err == nil
			})
			if got, want := installedCheck(t, check), filepath.Join(stateDir, "leak-check", leakguardtest.CheckName); got != want {
				t.Fatalf("the background install landed at %s, want %s", got, want)
			}
			if !strings.Contains(logs.String(), "answers: installed the shared public leak check the gh tool's leak guard runs") {
				t.Errorf("the background install logged %q, want its installed line", logs.String())
			}
			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("GitHub's trouble stopped the install: %s", logs.String())
			}
			if after := tool.Run(ctx, 2, answerCall); strings.Contains(after.Output, "is not installed yet") {
				t.Fatalf("the call after the install = %+v; want it past the check's install", after)
			}
		})
	}
}

// A cause that is not GitHub's trouble, met by a try in the background, ends the install. The
// boot's try met GitHub's 502 and left the install to the background, whose next try meets the
// tarball read answered 404, a tag the repository does not hold. The daemon logs that once at
// Error, with the tag and the cause, and makes no further try, and the next gh call is refused
// with that cause, in the sentence that says the install is tried again only at the daemon's next
// boot.
func TestAPermanentCauseMetInTheBackgroundEndsTheInstall(t *testing.T) {
	quickAppMints(t)
	ctx := context.Background()
	reader := newTroubledSource(t, readTrouble{status: http.StatusBadGateway})
	stateDir := t.TempDir()
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	check, stop, err := installAnswersLeakCheck(ctx, stateDir, leakCheckTag, reader, log)
	if err != nil {
		t.Fatalf("installAnswersLeakCheck over a read GitHub answered 502 = %v; want no error, the install left to the background", err)
	}
	t.Cleanup(stop)
	reader.status.Store(http.StatusNotFound)

	const ended = "answers: the shared public leak check cannot be installed, so the gh tool refuses every call until the daemon's next boot; installing it no more"
	testwait.Eventually(t, "the install ended", func() bool { return strings.Contains(logs.String(), ended) })
	tries := reader.tries.Load()
	// Ten of the background's waits pass, and no try is made in them.
	time.Sleep(10 * appMintRetry.Max)
	if after := reader.tries.Load(); after != tries {
		t.Fatalf("the install made %d tries after it ended, want none", after-tries)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(line, ended) {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "level=ERROR") || !strings.Contains(lines[0], "tag="+leakCheckTag) || !strings.Contains(lines[0], "HTTP 404") {
		t.Fatalf("the end of the install logged %q, want one Error line naming the pin and the 404", lines)
	}

	cause := leakguard.RefusalPrefix + "GitHub answered the read of " + leakguard.CheckSlug + " at " + leakCheckTag + ` with HTTP 404: {"message":"Not Found"}`
	want := "refused: LEAK CHECK SETUP ERROR: the gh call: the shared public leak check at " + leakCheckTag + ", the pin " + leakguard.TagVariable +
		" names, could not be installed, and the daemon tries the install again only at its next boot: " + cause
	if refused := openAnswerTool(t, stateDir, check, log).Run(ctx, 1, answerCall); refused.Ran || refused.Exit != -1 || refused.Output != want {
		t.Fatalf("the loop's gh call after the install ended = %+v; want the refusal %q, gh never run", refused, want)
	}
}

// A cause that is not GitHub's trouble (appTransient) refuses a Kubernetes daemon's boot at once,
// the install left to no background: the pin unset, or not of the published shape, which boot's
// read of it refuses and the reader refuses too; a daemon with no GitHub App to read the check
// with; GitHub answering the tarball read 404 (the tag is missing), 401 or 403; a tarball that
// holds no check; a mint that failed some way other than GitHub's trouble, GitHub refusing the
// narrowing of the read's token and an App with no installation on the check's owner among them;
// and a served check whose `--help` run fails. Each is refused in its own sentence.
func TestACauseNoWaitChangesRefusesTheBoot(t *testing.T) {
	t.Run("no pin", func(t *testing.T) {
		_, _, err := answersLeakCheck("kubernetes", environLookup([]string{"PATH=/usr/bin"}))
		if err == nil || !strings.Contains(err.Error(), "which the daemon's environment does not set") {
			t.Fatalf("boot's read of no pin = %v, want the refusal naming %s", err, leakguard.TagVariable)
		}
	})
	t.Run("a pin not of the published shape, read at boot", func(t *testing.T) {
		_, _, err := answersLeakCheck("kubernetes", environLookup([]string{leakguard.TagVariable + "=leak-check-v015"}))
		if err == nil || !strings.Contains(err.Error(), `"leak-check-v015", not of the published shape leak-check-v<n>`) {
			t.Fatalf("boot's read of the pin leak-check-v015 = %v, want the refusal naming its shape", err)
		}
	})

	minted := func(context.Context) (string, error) { return "ghs_never_used", nil }
	narrowing := provisioningStandIn(t, provisioningInstallation{id: "73", owner: "theorem-labs", granted: leakCheckGrant,
		answer: func(w http.ResponseWriter, permissions map[string]string) {
			if maps.Equal(permissions, leakCheckReadSet) {
				provisioningRefusal(w)
				return
			}
			mintedToken(w, "ghs_the_owners_own_token", permissions)
		}})
	elsewhere := provisioningStandIn(t, provisioningInstallation{id: "74", owner: "acme", granted: leakCheckGrant,
		answer: func(w http.ResponseWriter, permissions map[string]string) {
			mintedToken(w, "ghs_acmes_token", permissions)
		}})
	silent := servedCheckReader{body: leakguardtest.Tarball(t, map[string]leakguardtest.Member{
		leakguardtest.CheckName: {Body: "#!/bin/sh\nexit 2\n", Mode: 0o755},
	})}
	noCheck := leakguardtest.Tarball(t, map[string]leakguardtest.Member{
		leakguardtest.TarballPrefix + "README.md": {Body: "the rest of the repository\n", Mode: 0o644},
	})
	answered := func(status int) string {
		return leakguard.RefusalPrefix + fmt.Sprintf("GitHub answered the read of %s at %s with HTTP %d: {\"message\":%q}", leakguard.CheckSlug, leakCheckTag, status, http.StatusText(status))
	}
	for _, tc := range []struct {
		name   string
		tag    string
		reader sandbox.LeakCheckReader
		want   string
	}{
		{"a pin not of the published shape, read by the reader", "leak-check-v015", &leakguard.Source{Token: minted},
			leakguard.RefusalPrefix + `the pod asked for the tag "leak-check-v015", which is not of the published shape leak-check-v<n>`},
		{"no GitHub App", leakCheckTag, nil,
			leakguard.RefusalPrefix + "the daemon runs with no GitHub App, so it cannot read " + leakguard.CheckSlug},
		{"GitHub answers the tarball read 404", leakCheckTag, newTroubledSource(t, readTrouble{status: http.StatusNotFound}), answered(http.StatusNotFound)},
		{"GitHub answers 401 to a source that cannot forget its token", leakCheckTag, newTroubledSource(t, readTrouble{status: http.StatusUnauthorized}), answered(http.StatusUnauthorized)},
		{"GitHub answers the tarball read 403", leakCheckTag, newTroubledSource(t, readTrouble{status: http.StatusForbidden}), answered(http.StatusForbidden)},
		{"a tarball that holds no check", leakCheckTag, newTroubledSource(t, readTrouble{tarball: noCheck}),
			leakguard.RefusalPrefix + "the tarball of " + leakguard.CheckSlug + " at " + leakCheckTag + " holds no leak-check directory"},
		{"a mint that failed and is not GitHub's trouble", leakCheckTag,
			newTroubledSource(t, readTrouble{mint: errors.New("the daemon could not mint its read-only token for " + leakguard.CheckSlug + ": the App's key does not parse")}),
			leakguard.RefusalPrefix + "the daemon could not mint its read-only token for " + leakguard.CheckSlug + ": the App's key does not parse"},
		{"GitHub refuses the narrowing", leakCheckTag, leakCheckSource(narrowing.tokens, serveLeakCheckRepo(t).url),
			leakguard.RefusalPrefix + "GitHub refused the daemon's implement App the reading permissions on theorem-labs"},
		{"the App has no installation on the check's owner", leakCheckTag, leakCheckSource(elsewhere.tokens, serveLeakCheckRepo(t).url),
			leakguard.RefusalPrefix + "the daemon could not mint its read-only token for " + leakguard.CheckSlug},
		{"the served check's --help fails", leakCheckTag, silent, leakguard.RefusalPrefix + "the served check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check, stop, err := installAnswersLeakCheck(context.Background(), t.TempDir(), tc.tag, tc.reader, quietLogger())
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("installAnswersLeakCheck = %v, want the boot refused with %q…", err, tc.want)
			}
			if check != nil || stop != nil {
				t.Fatalf("a refused install answered the check %v and a stop; want neither, no install left running", check)
			}
		})
	}
}
