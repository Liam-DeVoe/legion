// The rig the daemon's two whole-tree kind tests share (stage4b_kind_live_test.go and
// claude_backend_kind_live_test.go), which the kind job of ci.yaml runs on the rig of
// internal/kindtest: the whole daemon, booted in this process from a legion.yaml its own loader
// reads, with the Agent Sandbox runtime it configures launching real issue pods in the kind
// cluster under gVisor, every role of them a real agent of the worker image under test.
//
// Where each part comes from:
//
//   - the cluster, Agent Sandbox, gVisor, the image and the fake GitHub (internal/fakegithub),
//     which the pods reach as github.com and api.github.com: kindtest.Start;
//   - PostgreSQL on the kind network (kindtest.Rig.StartPostgres): the daemon's store, Dispatch's
//     database and the session store every pod keeps its sessions in (`session_store: postgres`);
//   - NATS, the Envoy listener and Dispatch, each built from this checkout and run as a process of
//     its own: natsd (packages/envoy/internal/testnats/cmd/natsd), cmd/listener and cmd/dispatch.
//     natsd and Dispatch listen on this host's loopback, and a relay at the kind network's gateway
//     carries a pod's connection to each; the listener listens at the gateway itself;
//   - GitHub: the fake's repository and two Apps, each App's installation tokens released by
//     kindtest.Rig.AppTokenCommand (appauth's token mode), the shared public leak check served
//     from the fake (kindtest.Rig.ServeLeakCheck), the daemon's own REST and GraphQL calls made
//     through a loopback proxy to the fake, and the fake's push and pull request events delivered
//     to the listener's /webhook/github as signed webhooks (fakegithub.Server.Webhooks), so they
//     reach the daemon by the production path, through NATS;
//   - the model: one Messages stand-in at the gateway (kindStandin), which the operator route's
//     models.yml names for every Oh My Pi role and ANTHROPIC_BASE_URL names for Claude Code. It
//     answers every turn with a WAITING line carrying the run's marker, and a turn a step scripted
//     with a call of the tool the step names.
//
// What the rig stands in for lies outside the daemon and its pods: the model, GitHub, and the
// operator's own reads, which kubectl and the role namespaces' entry of
// scripts/e2e/lib/role-namespaces.sh make, as an operator reads a pod.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/fakegithub"
	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/kindtest"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testbuild"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
)

// The rig's names in the cluster and the credentials of its own processes. Every value is a
// throwaway of one run's own cluster, which nothing but this host reaches.
const (
	kindTreeNamespace     = "legion"
	kindTreeWorkerAccount = "legion-worker"
	kindTreeEnvoyToken    = "kind-envoy-token-not-a-secret"
	kindTreeWebhookSecret = "kind-webhook-secret-not-a-secret"
	kindTreeAnthropicKey  = "kind-anthropic-key-not-a-secret"
	// kindTreeModel is the model the Claude Code lane's pod.env names; the stand-in answers any.
	kindTreeModel = "claude-sonnet-5"
	// kindTreeIssueResources is the issue pod's reservation: three quarters of a CPU and 1.5Gi,
	// each requested in full, so the pod is Guaranteed and three of them fit the kind job's node,
	// one runner of four CPUs and 16Gi, beside a repository cache and the cluster's own pods.
	kindTreeIssueResources = "    resources:\n      issue: {cpu: 750m, memory: 1536Mi, ephemeral_storage: 6Gi, ephemeral_storage_request: 2Gi}\n"
)

// kindTreeWait is how long a step waits for a pod to start a role and register: a pod start under
// gVisor on the runner's one node, its init containers' clone, and Oh My Pi's start.
const kindTreeWait = 10 * time.Minute

// kindStandinURL, in a value of kindTreeOptions.podEnv, is replaced with the stand-in's URL.
const kindStandinURL = "${KIND_STANDIN_URL}"

// kindTreeOptions is what one test asks of the rig.
type kindTreeOptions struct {
	// prefix starts the run's Dispatch project key, and admissionCap is the file's
	// admission_cap.
	prefix       string
	admissionCap int
	// topLevel is more of the file's top level (worker_backends, controller), and podEnv more of
	// runtime.kubernetes.pod.env.
	topLevel string
	podEnv   map[string]string
	// providerKeys are the providers Secret's keys beside SESSION_DSN, each one a provider_keys
	// entry of the file under the same name.
	providerKeys map[string]string
	// tokenLifetime, when set, is how long every App installation token the fake GitHub mints
	// lives (fakegithub.Server.SetTokenLifetime).
	tokenLifetime time.Duration
}

// kindTreeRig is one run of the rig: the booted daemon and everything it runs against.
type kindTreeRig struct {
	liveAgentProof
	// parent is the test the rig belongs to, which owns every process and cleanup; liveProof.t
	// is the test or subtest a step runs as now (use).
	parent *testing.T
	opts   kindTreeOptions
	kind   *kindtest.Rig
	root   string
	work   string
	// repo is the run's private repository of the fake GitHub; implement and review are the two
	// Apps, both installed on its owner, implement also on the leak check's.
	repo              fakegithub.Repository
	implement, review fakegithub.App
	// natsURL, listenerURL, dispatchURL and model's url are each as a pod and this process reach
	// them. listenerAlias is a second address of the same listener (address-moved-env-same-pod).
	natsURL, listenerURL, listenerAlias, dispatchURL string
	dispatch                                         *handoffDispatch
	model                                            *kindStandin
	pg                                               kindtest.Postgres
	githubAPI                                        string
	apiPort, streamPort                              int
	// envoyURL is the listener address the current file hands the pods, and configPath the file.
	envoyURL, configPath string
	controller           bool
	tokens               *appauth.Manager
	enter                string
	watch                *kindPodWatch
}

// newKindTreeRig brings the rig up and readies everything the daemon boots against: the cluster,
// PostgreSQL, NATS, the listener, Dispatch with the run's project and its repository mapping, the
// fake GitHub's repository, Apps, leak check and webhook, the stand-in, the operator route's
// ConfigMap and the providers Secret, the `legion` this checkout builds, and the store, migrated.
// The daemon is booted by boot, after the test seeds what it boots on.
func newKindTreeRig(t *testing.T, opts kindTreeOptions) *kindTreeRig {
	t.Helper()
	fixtureenv.Live(t)
	kind := kindtest.Start(t)
	root := kindtest.RepositoryRoot(t)
	r := &kindTreeRig{parent: t, opts: opts, kind: kind, root: root, work: t.TempDir()}
	r.liveProof.t = t
	r.records = record.NewStore()

	r.pg = kind.StartPostgres(t)
	// Every database of the run is the rig's server's: the store takes one of its own
	// (takeOwnDatabase), and so does Dispatch (testdb.Database).
	t.Setenv("LEGION_TEST_PG_DSN", r.pg.Host)
	takeOwnDatabase(t)

	r.natsURL = startKindNATS(t, kind.Gateway)
	r.repo = kind.GitHub.NewRepository(t, "legion-kind", "tree", false)
	r.implement = kind.GitHub.AddApp(t, "legion-kind-implement", r.repo.Owner, "theorem-labs")
	r.review = kind.GitHub.AddApp(t, "legion-kind-review", r.repo.Owner)
	if opts.tokenLifetime > 0 {
		kind.GitHub.SetTokenLifetime(opts.tokenLifetime)
	}
	kind.ServeLeakCheck(t)
	r.listenerURL = startKindListener(t, r.natsURL, kind.Gateway, strconv.FormatInt(r.review.ID, 10))
	listenerHost, _ := strings.CutPrefix(r.listenerURL, "http://")
	r.listenerAlias = "http://" + kindRelay(t, kind.Gateway, listenerHost)
	r.envoyURL = r.listenerURL
	kind.GitHub.Webhooks(r.listenerURL+"/webhook/github", kindTreeWebhookSecret)
	r.githubAPI = kindGitHubAPI(t, kind.GitHub)

	options := scratchDispatchOptions{natsURL: r.natsURL, root: root, log: kindLogFile(t, kind, "dispatch.log")}
	r.dispatch = &handoffDispatch{scratchDispatch: startScratchDispatchOn(t, options), client: &http.Client{Timeout: 10 * time.Second}}
	dispatchHost, _ := strings.CutPrefix(r.dispatch.base, "http://")
	r.dispatchURL = "http://" + kindRelay(t, kind.Gateway, dispatchHost)

	r.cfg.Project = opts.prefix + randomSuffix(t)[:6]
	var err error
	if r.project, err = claim.ProjectToken(r.cfg.Project); err != nil {
		t.Fatal(err)
	}
	r.dispatch.user(http.MethodPost, "/api/v1/projects", map[string]string{"key": r.cfg.Project, "name": "kind tree " + r.cfg.Project}, http.StatusCreated, nil)
	r.dispatch.user(http.MethodPut, "/api/v1/settings/repo-projects/"+r.repo.Slug(), map[string]any{"project": r.cfg.Project, "default": true}, http.StatusOK, nil)

	r.model = newKindStandin(t, kind.Gateway, "kind-marker-"+randomSuffix(t)[:8])
	r.operatorRoute()
	r.legion = buildProofLegion(t, root, r.work)
	r.apiPort, r.streamPort = kindFreePort(t), kindFreePort(t)
	r.writeConfig()
	if r.st, err = store.Open(context.Background(), os.Getenv("LEGION_TEST_PG_DSN")); err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(r.st.Close)
	if _, err := r.st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate the store: %v", err)
	}
	enter, err := exec.Command("bash", "-c", `. "$1" && printf %s "$role_enter_sh"`, "role-enter",
		filepath.Join(root, "scripts", "e2e", "lib", "role-namespaces.sh")).Output()
	if err != nil || len(enter) == 0 {
		t.Fatalf("read role_enter_sh from scripts/e2e/lib/role-namespaces.sh: %v", err)
	}
	r.enter = string(enter)
	r.watch = startKindPodWatch(t, r)
	r.note("project %s on %s; Dispatch %s, listener %s, NATS %s, the stand-in %s, all reached by the pods at the kind network's gateway",
		r.cfg.Project, r.repo.Slug(), r.dispatchURL, r.listenerURL, r.natsURL, r.model.url)
	return r
}

// use makes t the test every read and wait of the rig fails, the subtest a checkpoint runs as.
func (r *kindTreeRig) use(t *testing.T) {
	r.liveProof.t = t
	r.dispatch.t = t
	if r.d != nil {
		r.d.t = t
	}
}

// checkpoint runs body as the subtest name and stops the test at the first one that fails: every
// checkpoint reads the state the ones before it left.
func (r *kindTreeRig) checkpoint(name string, body func()) {
	if !r.parent.Run(name, func(t *testing.T) {
		r.use(t)
		defer r.use(r.parent)
		started := time.Now()
		body()
		fmt.Printf("CHECK %s: PASS (%ds)\n", name, int(time.Since(started).Seconds()))
	}) {
		fmt.Printf("CHECK %s: FAIL\n", name)
		r.parent.FailNow()
	}
}

// operatorRoute creates what the operator route's pod block names (deploy/kubernetes/
// operator-route): the ServiceAccount the pods run as, the ConfigMap holding models.yml, whose
// anthropic provider is the stand-in, and overlay.yml; and the project's providers Secret, whose
// SESSION_DSN is the rig's PostgreSQL as a pod dials it, beside the test's provider keys.
func (r *kindTreeRig) operatorRoute() {
	t := r.parent
	route := filepath.Join(r.root, "deploy", "kubernetes", "operator-route")
	models, err := os.ReadFile(filepath.Join(route, "models.yml"))
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.ReplaceAll(string(models), "${MODEL_BASE_URL}", r.model.url)
	modelsFile := filepath.Join(r.work, "models.yml")
	if err := os.WriteFile(modelsFile, []byte(filled), 0o600); err != nil {
		t.Fatal(err)
	}
	r.kind.Apply(t, fmt.Sprintf("apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: %s, namespace: %s}\n", kindTreeWorkerAccount, kindTreeNamespace))
	r.kind.Kubectl(t, "-n", kindTreeNamespace, "create", "configmap", "legion-operator-route",
		"--from-file=models.yml="+modelsFile, "--from-file=overlay.yml="+filepath.Join(route, "overlay.yml"))
	secret := map[string]string{"SESSION_DSN": r.pg.InCluster}
	maps.Copy(secret, r.opts.providerKeys)
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	r.kind.Apply(t, fmt.Sprintf("apiVersion: v1\nkind: Secret\nmetadata: {name: %s, namespace: %s}\nstringData: %s\n",
		sandbox.ProvidersSecretName(r.project), kindTreeNamespace, encoded))
}

// writeConfig writes the run's legion.yaml from the rig's addresses as they are now: the daemon
// binds every interface and advertises the kind network's gateway, which the pods dial.
func (r *kindTreeRig) writeConfig() {
	t := r.parent
	for name, value := range map[string]string{"operator-token": testOperatorToken, "envoy-token": kindTreeEnvoyToken, "dispatch-token": r.dispatch.token} {
		if err := os.WriteFile(filepath.Join(r.work, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	route, err := os.ReadFile(filepath.Join(r.root, "deploy", "kubernetes", "operator-route", "pod.yml"))
	if err != nil {
		t.Fatal(err)
	}
	pod := strings.NewReplacer("${MODEL_TOKEN_AUDIENCE}", "legion-model-gateway", "${POD_SERVICE_ACCOUNT}", kindTreeWorkerAccount).Replace(string(route))
	var podLines []string
	for _, line := range strings.Split(strings.TrimRight(pod, "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		podLines = append(podLines, "      "+line)
		if line == "env:" {
			for _, name := range slices.Sorted(maps.Keys(r.opts.podEnv)) {
				// A value may name the stand-in, whose address the rig picks: kindStandinURL.
				value := strings.ReplaceAll(r.opts.podEnv[name], kindStandinURL, r.model.url)
				podLines = append(podLines, fmt.Sprintf("        %s: %q", name, value))
			}
		}
	}
	var providerKeys string
	if len(r.opts.providerKeys) > 0 {
		providerKeys = "    provider_keys:\n"
		for _, name := range slices.Sorted(maps.Keys(r.opts.providerKeys)) {
			providerKeys += fmt.Sprintf("      %s: %s\n", name, name)
		}
	}
	controller := ""
	if r.controller {
		controller = "controller: daemon\n"
	}
	body := fmt.Sprintf(`project: %[1]s
bind: 0.0.0.0
advertise_host: %[2]s
port: %[3]d
worker_stream_port: %[4]d
daemon_url: http://%[2]s:%[3]d
postgres_dsn: %[5]s
state_dir: %[6]s
operator_token_file: ./operator-token
envoy_url: %[7]s
envoy_token_file: ./envoy-token
nats_urls: [%[8]s]
dispatch_url: %[9]s
dispatch_token_file: ./dispatch-token
projects:
  %[1]s: {}
gates:
  design: "off"
admission_cap: %[10]d
worker_boot_timeout_seconds: 900
github_apps:
  implement: {app_id: "%[11]d", token_command: %[12]q}
  review: {app_id: "%[13]d", token_command: %[14]q}
%[15]s%[16]sruntime:
  kubernetes:
    namespace: %[17]s
    image: %[18]s
    storage_class: standard
    kubeconfig: %[19]s
    context: %[20]s
    session_store: postgres
    session_dsn_secret: SESSION_DSN
%[21]s%[22]s%[23]s    pod:
%[24]s
`, r.cfg.Project, r.kind.Gateway, r.apiPort, r.streamPort, os.Getenv("LEGION_TEST_PG_DSN"), filepath.Join(r.work, "state"),
		r.envoyURL, r.natsURL, r.dispatchURL, r.opts.admissionCap,
		r.implement.ID, r.kind.AppTokenCommand(t, r.implement, r.repo.Owner, "theorem-labs"),
		r.review.ID, r.kind.AppTokenCommand(t, r.review, r.repo.Owner),
		r.opts.topLevel, controller, kindTreeNamespace, r.kind.Image, r.kind.Kubeconfig, r.kind.Context,
		kindTreeIssueResources, kindLiveCache, providerKeys, strings.Join(podLines, "\n"))
	r.configPath = filepath.Join(r.work, "legion.yaml")
	if err := os.WriteFile(r.configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if r.cfg, err = config.Load(r.configPath, nil); err != nil {
		t.Fatalf("the daemon's loader refused the run's legion.yaml: %v\n%s", err, body)
	}
}

// seedRoot files a root issue of the run in Dispatch, in progress and labelled legion, and seeds
// the daemon's store with it in at, admitted, its lifecycle open and each of mid's claims
// mid-launch, as a daemon that stopped with them launching leaves it: the boot launches each into
// the issue's pod, beside the architect every root runs. It returns the issue's key.
func (r *kindTreeRig) seedRoot(title string, at phase.Phase, mid ...claim.Role) string {
	t := r.t
	t.Helper()
	key := r.newIssue(title, "")
	r.dispatch.user(http.MethodPatch, "/api/v1/issues/"+key, map[string]any{"status": "in_progress"}, http.StatusOK, nil)
	listed := r.dispatch.issue(key)
	ctx := context.Background()
	if err := r.st.Tx(ctx, func(tx pgx.Tx) error {
		if err := r.records.PutIssue(ctx, tx, record.Issue{
			Key: key, Repo: r.repo.Slug(), DefaultBranch: "main", GitHubRepo: r.repo.Slug(), Tree: key, Project: r.cfg.Project,
			Title: listed.Title, Phase: at, Generation: 1, Status: "in_progress", DispatchStatus: "in_progress",
			Rank: listed.Rank, LastDispatchSeq: listed.LastSeq,
		}); err != nil {
			return err
		}
		return r.admit(ctx, tx, key)
	}); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
	if _, err := r.st.OpenTreeLifecycle(ctx, r.project, key, treelifecycle.AuthorityWorkflow); err != nil {
		t.Fatalf("open the lifecycle of %s: %v", key, err)
	}
	for _, role := range mid {
		token := r.token(key, role)
		if _, err := r.st.AdmitClaim(ctx, supervise.Claim{
			Token: token, Project: r.project, Tree: key, Issue: key, Role: role, Generation: 1, State: supervise.StateLaunching,
			BootTokenHash: supervise.HashBootToken("interrupted-" + randomSuffix(t)), ServingGeneration: 1,
		}); err != nil {
			t.Fatalf("record %s's unfinished launch: %v", token, err)
		}
		writePrompt(t, r.cfg, token)
	}
	r.note("seeded %s in %s, %v mid-launch", key, at, mid)
	return key
}

// branch makes the issue's branch, legion/<issue>, on the fake GitHub at main's commit: the branch
// an issue the store was seeded past planning has on GitHub, which its pod's workspace fetches.
func (r *kindTreeRig) branch(issue string) {
	r.t.Helper()
	command := exec.Command("git", "--git-dir="+r.repo.Bare, "update-ref", "refs/heads/legion/"+issue, "refs/heads/main")
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := command.CombinedOutput(); err != nil {
		r.t.Fatalf("make legion/%s on the fake GitHub: %v: %s", issue, err, out)
	}
}

// roleArgv is the argv of the first process in role's namespaces of pod whose argv holds needle.
func (r *kindTreeRig) roleArgv(pod string, role claim.Role, needle string) ([]string, error) {
	out, err := r.roleExec(pod, role, "sh", "-c", `for p in /proc/[0-9]*; do c=$(tr '\0' '\n' <"$p/cmdline" 2>/dev/null) || continue; case "$c" in *"$1"*) printf '%s\n' "$c"; exit 0;; esac; done; exit 1`, "argv", needle)
	if err != nil {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// roleEnviron is the environment, one NAME=value a line, of the first process in role's
// namespaces of pod whose argv[0] ends with suffix: the agent's own, as the agent reads it.
func (r *kindTreeRig) roleEnviron(pod string, role claim.Role, suffix string) ([]string, error) {
	out, err := r.roleExec(pod, role, "sh", "-c", `for p in /proc/[0-9]*; do a=$(tr '\0' '\n' <"$p/cmdline" 2>/dev/null | head -n 1); case "$a" in *"$1") tr '\0' '\n' <"$p/environ"; exit 0;; esac; done; exit 1`, "environ", suffix)
	if err != nil {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// awaitPodGone waits until no pod named name holds uid: the pod deleted, or replaced by one of
// another uid.
func (r *kindTreeRig) awaitPodGone(name, uid string) {
	r.t.Helper()
	r.await("pod "+name+" (uid "+uid+") to be gone", kindTreeWait, func() bool {
		pod, err := r.pod(name)
		return (err != nil && strings.Contains(err.Error(), "NotFound")) || (err == nil && pod.Metadata.UID != uid)
	})
}

// argValue is the argument after flag in argv, "" when argv has no flag.
func argValue(argv []string, flag string) string {
	if i := slices.Index(argv, flag); i >= 0 && i+1 < len(argv) {
		return argv[i+1]
	}
	return ""
}

// newIssue files an issue of the run's project in Dispatch, as the proof human: a root carrying
// the legion label, or a child of parent.
func (r *kindTreeRig) newIssue(title, parent string) string {
	body := map[string]any{"project": r.cfg.Project, "title": title, "force": true}
	if parent == "" {
		body["labels"] = []string{"legion"}
		body["spec"] = "## Summary\n\nA kind tree proof: add one Markdown file under `smoke/` naming this issue.\n"
	} else {
		body["parent"] = parent
	}
	var created struct {
		Key string `json:"key"`
	}
	r.dispatch.user(http.MethodPost, "/api/v1/issues", body, http.StatusCreated, &created)
	if created.Key == "" {
		r.t.Fatalf("Dispatch created %q with no key", title)
	}
	return created.Key
}

// setStatus sets the issue's Dispatch status, as the proof human.
func (r *kindTreeRig) setStatus(key, status string) {
	r.dispatch.user(http.MethodPatch, "/api/v1/issues/"+key, map[string]any{"status": status}, http.StatusOK, nil)
}

// token is the claim token of role on issue, as the daemon spells it.
func (r *kindTreeRig) token(issue string, role claim.Role) claim.Token {
	token, err := claim.NewToken(r.project, issue, role)
	if err != nil {
		r.t.Fatal(err)
	}
	return token
}

// boot starts the daemon on the run's file, with its App tokens minted by a manager of the
// daemon's own over the fake GitHub, and its REST root the rig's proxy to it.
func (r *kindTreeRig) boot() {
	r.parent.Helper()
	if r.daemon == nil {
		r.daemon = newRecordingLog(slog.New(slog.NewJSONHandler(kindLogFile(r.parent, r.kind, "daemon.log"), &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
	r.tokens = appauth.New(r.cfg.GitHubApps, appauth.Options{BaseURL: r.githubAPI, Log: slog.New(r.daemon)})
	current := r.t
	r.d = startDaemonWithin(r.parent, r.cfg, overrides{githubAPI: r.githubAPI, workflowTokens: r.tokens}, slog.New(r.daemon), 10*time.Minute)
	r.use(current)
}

// stopDaemon stops the running daemon and its token manager.
func (r *kindTreeRig) stopDaemon() {
	r.d.stop()
	r.tokens.Close()
}

// restart stops the daemon, writes the file again from the rig's addresses and boots on it.
func (r *kindTreeRig) restart() {
	r.stopDaemon()
	r.writeConfig()
	r.boot()
}

// kindState is the part of GET /legion/v1/state the checkpoints read.
type kindState struct {
	Admission struct {
		Cap     int      `json:"cap"`
		Active  []string `json:"active"`
		Waiting []string `json:"waiting"`
	} `json:"admission"`
	Capabilities []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	} `json:"capabilities"`
	ControllerLocator *struct {
		SessionID string `json:"sessionId"`
	} `json:"controllerLocator"`
	Issues map[string]struct {
		Phase string `json:"phase"`
	} `json:"issues"`
}

func (r *kindTreeRig) state() kindState {
	r.t.Helper()
	status, body := r.d.request(http.MethodGet, "/legion/v1/state", nil, true)
	if status != http.StatusOK {
		r.t.Fatalf("GET /legion/v1/state = %d %s", status, body)
	}
	var state kindState
	if err := json.Unmarshal(body, &state); err != nil {
		r.t.Fatalf("decode the state: %v", err)
	}
	return state
}

// claims is every claim the operator's listing shows now.
func (r *kindTreeRig) claims() []api.OperatorClaim {
	r.t.Helper()
	status, body := r.d.request(http.MethodGet, "/legion/v1/operator/claims", nil, true)
	if status != http.StatusOK {
		r.t.Fatalf("the operator's claim listing = %d %s", status, body)
	}
	var list api.OperatorClaims
	if err := json.Unmarshal(body, &list); err != nil {
		r.t.Fatalf("decode the claim listing: %v", err)
	}
	return list.Claims
}

// claimOf is role's claim on issue as the listing shows it now, and whether there is one.
func (r *kindTreeRig) claimOf(issue string, role claim.Role) (api.OperatorClaim, bool) {
	for _, held := range r.claims() {
		if held.Issue == issue && held.Role == role {
			return held, true
		}
	}
	return api.OperatorClaim{}, false
}

// kindLive is whether a claim's process runs and registered: ready, working or idle.
func kindLive(c api.OperatorClaim) bool {
	return c.State == string(supervise.StateReady) || c.State == string(supervise.StateWorking) || c.State == string(supervise.StateIdle)
}

// awaitLive waits for role's claim on issue to run in a pod and register there, and returns it.
func (r *kindTreeRig) awaitLive(issue string, role claim.Role) api.OperatorClaim {
	r.t.Helper()
	var held api.OperatorClaim
	r.await(fmt.Sprintf("%s's %s to register from its pod", issue, role), kindTreeWait, func() bool {
		var ok bool
		held, ok = r.claimOf(issue, role)
		if ok && held.State == string(supervise.StateFailed) {
			r.t.Fatalf("%s failed before it registered: %+v", held.Token, held)
		}
		return ok && kindLive(held) && held.Locator != nil && held.Locator.Sandbox != nil && held.Session != ""
	})
	return held
}

// liveClaims is every claim whose process runs now, by token.
func (r *kindTreeRig) liveClaims() map[claim.Token]api.OperatorClaim {
	live := map[claim.Token]api.OperatorClaim{}
	for _, held := range r.claims() {
		if kindLive(held) && held.Locator != nil && held.Locator.Sandbox != nil {
			live[held.Token] = held
		}
	}
	return live
}

// kubectl runs kubectl against the rig's cluster in the run's namespace, as the operator.
func (r *kindTreeRig) kubectl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", r.kind.Kubeconfig, "--context", r.kind.Context, "-n", kindTreeNamespace}, args...)...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return stdout.String(), fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// podExec runs a command in the issue pod's one container, in the container's own view.
func (r *kindTreeRig) podExec(pod string, command ...string) (string, error) {
	out, err := r.kubectl(append([]string{"exec", pod, "-c", runtime.IssuePodContainer, "--"}, command...)...)
	return strings.TrimSpace(out), err
}

// roleExec runs a command inside one running role of the pod, in its own namespaces, as its
// shim's user: the view the role's agent has (role_enter_sh).
func (r *kindTreeRig) roleExec(pod string, role claim.Role, command ...string) (string, error) {
	return r.podExec(pod, append([]string{"sh", "-c", r.enter, "role-enter", string(role)}, command...)...)
}

// pod is the pod as the API server holds it now.
func (r *kindTreeRig) pod(name string) (kindPod, error) {
	out, err := r.kubectl("get", "pod", name, "-o", "json")
	if err != nil {
		return kindPod{}, err
	}
	var pod kindPod
	return pod, json.Unmarshal([]byte(out), &pod)
}

// send delivers message to session through the listener, as a sender's envoy_send does.
func (r *kindTreeRig) send(session, message string) {
	r.t.Helper()
	body, _ := json.Marshal(map[string]any{"target_session": session, "message": message, "source": "agent",
		"source_session": "ses_kind_proof_driver", "idempotency_key": "kind-" + randomSuffix(r.t)})
	request, err := http.NewRequest(http.MethodPost, r.listenerURL+"/v1/messages/send", bytes.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+kindTreeEnvoyToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		r.t.Fatalf("send to %s: %v", session, err)
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(response.Body)
	if response.StatusCode >= 300 {
		r.t.Fatalf("the listener refused the message to %s: %d %s", session, response.StatusCode, answer)
	}
}

// writeRecord writes issue's word handoff record as session, as `dispatch handoff-write` of that
// session writes one, and returns it as Dispatch answers it.
func (r *kindTreeRig) writeRecord(issue, session, word string) handoffRecord {
	r.t.Helper()
	status, answer := r.dispatch.agentPost("/api/v1/issues/"+issue+"/handoffs/"+word, map[string]any{
		"actor": map[string]string{"kind": "session", "id": session}, "body": "The " + word + " of " + issue + ": one Markdown file under smoke/.",
	})
	if status != http.StatusCreated {
		r.t.Fatalf("the write of %s's %s record as %s = %d %s", issue, word, session, status, answer)
	}
	return r.dispatch.latest(issue, word)
}

// logged is every record of the daemon's own log whose message is msg and whose attributes hold
// each of attrs.
func (r *kindTreeRig) logged(msg string, attrs map[string]string) []loggedRecord {
	return r.logs(func(record loggedRecord) bool {
		if record.Message != msg {
			return false
		}
		for key, want := range attrs {
			if fmt.Sprint(record.Attrs[key]) != want {
				return false
			}
		}
		return true
	})
}

// kindPod is what the checkpoints read of a pod.
type kindPod struct {
	Metadata struct {
		Name   string            `json:"name"`
		UID    string            `json:"uid"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		NodeName              string          `json:"nodeName"`
		RuntimeClassName      string          `json:"runtimeClassName"`
		Affinity              json.RawMessage `json:"affinity"`
		ShareProcessNamespace *bool           `json:"shareProcessNamespace"`
		Containers            []kindContainer `json:"containers"`
		InitContainers        []kindContainer `json:"initContainers"`
	} `json:"spec"`
	Status struct {
		Phase             string                `json:"phase"`
		QOSClass          string                `json:"qosClass"`
		ContainerStatuses []kindContainerStatus `json:"containerStatuses"`
	} `json:"status"`
}

// kindContainerStatus is what the checkpoints read of a container's status.
type kindContainerStatus struct {
	Name        string `json:"name"`
	ContainerID string `json:"containerID"`
	Ready       bool   `json:"ready"`
}

// kindContainer is what the checkpoints read of a container.
type kindContainer struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
	Resources struct {
		Requests map[string]string `json:"requests"`
		Limits   map[string]string `json:"limits"`
	} `json:"resources"`
}

// rolesContainer is the pod's roles container's id, which a role's kill and relaunch keeps.
func (p kindPod) rolesContainer() string {
	for _, status := range p.Status.ContainerStatuses {
		if status.Name == runtime.IssuePodContainer {
			return status.ContainerID
		}
	}
	return ""
}

// connect is the --connect the pod's launcher dials, from the roles container's command.
func (p kindPod) connect() string {
	for _, container := range p.Spec.Containers {
		argv := append(slices.Clone(container.Command), container.Args...)
		if i := slices.Index(argv, "--connect"); i >= 0 && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// kindPodWatch records every Sandbox pod of the run the API server held Running with every
// container ready, by uid, as it last read it then: the record pod-shape judges, deleted pods
// included.
type kindPodWatch struct {
	mu   sync.Mutex
	pods map[string]kindPod
	// streams is the worker stream each recorded pod was handed, by uid, as the run's file named
	// it when the pod was first read Running.
	streams map[string]string
}

func startKindPodWatch(t *testing.T, r *kindTreeRig) *kindPodWatch {
	w := &kindPodWatch{pods: map[string]kindPod{}, streams: map[string]string{}}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { stop(); <-done })
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			out, err := r.kubectl("get", "pods", "-l", "legion.dev/project="+r.project, "-o", "json")
			var list struct {
				Items []kindPod `json:"items"`
			}
			if err == nil && json.Unmarshal([]byte(out), &list) == nil {
				w.mu.Lock()
				for _, pod := range list.Items {
					if pod.Status.Phase == "Running" && len(pod.Status.ContainerStatuses) > 0 &&
						!slices.ContainsFunc(pod.Status.ContainerStatuses, func(s kindContainerStatus) bool { return !s.Ready }) {
						if _, seen := w.streams[pod.Metadata.UID]; !seen {
							w.streams[pod.Metadata.UID] = fmt.Sprintf("tcp://%s:%d", r.kind.Gateway, r.streamPort)
						}
						w.pods[pod.Metadata.UID] = pod
					}
				}
				w.mu.Unlock()
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return w
}

// recorded is every pod the watch has recorded, and the stream each was handed.
func (w *kindPodWatch) recorded() (map[string]kindPod, map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.pods), maps.Clone(w.streams)
}

// startKindNATS runs natsd, built from this checkout, with JetStream on this host's loopback, and
// a relay at host, and answers the URL every process and pod of the run dials it at.
func startKindNATS(t *testing.T, host string) string {
	t.Helper()
	binary := testbuild.EnvoyBinary(t, "./internal/testnats/cmd/natsd")
	port := kindFreePortOn(t, "127.0.0.1")
	command := exec.Command(binary, "-p", strconv.Itoa(port), "-sd", t.TempDir())
	var output syncBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatalf("start natsd: %v", err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if !testwait.Holds(30*time.Second, 100*time.Millisecond, func() bool {
		conn, err := net.DialTimeout("tcp", target, time.Second)
		if err == nil {
			conn.Close()
		}
		return err == nil
	}) {
		t.Fatalf("natsd did not accept connections on %s:\n%s", target, output.String())
	}
	return "nats://" + kindRelay(t, host, target)
}

// startKindListener runs the Envoy listener, built from this checkout, at host, on natsURL, with
// its API behind kindTreeEnvoyToken and GitHub's webhook verified against kindTreeWebhookSecret,
// and answers its URL once /healthz says healthy.
func startKindListener(t *testing.T, natsURL, host, reviewerAppID string) string {
	t.Helper()
	binary := testbuild.EnvoyBinary(t, "./cmd/listener")
	home := t.TempDir()
	port := kindFreePortOn(t, host)
	command := exec.Command(binary)
	command.Dir = home
	command.Env = append(os.Environ(), "HOME="+home, "PORT="+strconv.Itoa(port), "ENVOY_LISTEN_HOST="+host,
		"ENVOY_MACHINE_ID=kind", "NATS_URLS="+natsURL, "ENVOY_API_TOKEN="+kindTreeEnvoyToken,
		"ENVOY_WEBHOOKS=github", "ENVOY_GITHUB_WEBHOOK_SECRET="+kindTreeWebhookSecret, "ENVOY_REVIEWER_APP_ID="+reviewerAppID)
	log := kindLogFile(t, nil, "listener.log")
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatalf("start the Envoy listener: %v", err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	base := "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	if !testwait.Holds(2*time.Minute, 250*time.Millisecond, func() bool {
		response, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		var health struct {
			Status string `json:"status"`
		}
		return json.NewDecoder(response.Body).Decode(&health) == nil && health.Status == "healthy"
	}) {
		t.Fatalf("the Envoy listener at %s did not answer /healthz healthy", base)
	}
	return base
}

// kindRelay carries every connection to host's listener on a port of its own to target, until the
// test ends, and answers that listener's host:port.
func kindRelay(t *testing.T, host, target string) string {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("relay at %s: %v", host, err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				upstream, err := net.DialTimeout("tcp", target, 10*time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				go func() { _, _ = io.Copy(upstream, conn); _ = upstream.(*net.TCPConn).CloseWrite() }()
				_, _ = io.Copy(conn, upstream)
			}()
		}
	}()
	return listener.Addr().String()
}

// kindGitHubAPI is GitHub's REST and GraphQL root as this process's daemon calls it: a loopback
// server that hands each request to the fake GitHub as api.github.com, over TLS that trusts the
// fake's authority alone (fakegithub.Server.Client).
func kindGitHubAPI(t *testing.T, github *fakegithub.Server) string {
	t.Helper()
	target, err := url.Parse(fakegithub.API)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = github.Client().Transport
	direct := proxy.Director
	proxy.Director = func(request *http.Request) {
		direct(request)
		request.Host = fakegithub.APIHost
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	return server.URL
}

// kindFreePort is a port nothing holds on any interface now, for a listener the daemon binds on
// every interface.
func kindFreePort(t *testing.T) int {
	return kindFreePortOn(t, "0.0.0.0")
}

func kindFreePortOn(t *testing.T, host string) int {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// kindLogFile is a log file of the run's in the rig's evidence directory, which the kind job
// uploads when the test fails (a directory of the test's own where there is no rig yet).
func kindLogFile(t *testing.T, rig *kindtest.Rig, name string) *os.File {
	t.Helper()
	dir := t.TempDir()
	if rig != nil && rig.Evidence != "" {
		dir = rig.Evidence
	} else if env := os.Getenv(kindtest.LogDirEnv); env != "" {
		dir = filepath.Join(env, strings.ReplaceAll(t.Name(), "/", "_"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

// kindStandin is the run's one Messages endpoint, at the kind network's gateway: Oh My Pi's
// anthropic provider (the operator route's models.yml) and Claude Code's ANTHROPIC_BASE_URL. It
// keeps every Messages request it was sent; it answers a request a step scripted (callOn) with a
// call of the step's tool, the plugin's side question and a compaction's summary as the package's
// other stand-ins do, and every other turn with one WAITING line carrying marker, so a session's
// earlier turns are told apart from a new one by what its context holds.
type kindStandin struct {
	url, marker string
	mu          sync.Mutex
	rules       []*kindRule
	seen        []kindSeen
}

// kindRule is one scripted answer: the first request match takes, answered with a call of tool
// with input.
type kindRule struct {
	match func(kindSeen) bool
	tool  string
	input map[string]any
	spent bool
}

// kindSeen is one Messages request: when it came, its whole body, its last message's text and
// whether it streams.
type kindSeen struct {
	at     time.Time
	body   string
	last   string
	stream bool
}

func newKindStandin(t *testing.T, host, marker string) *kindStandin {
	t.Helper()
	s := &kindStandin{marker: marker}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(s)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	s.url = server.URL
	return s
}

// callOn answers the first request match takes with a call of tool with input, the one-line
// intent every Oh My Pi tool takes among it.
func (s *kindStandin) callOn(match func(kindSeen) bool, tool string, input map[string]any) {
	called := map[string]any{"i": "Running the proof's step"}
	maps.Copy(called, input)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, &kindRule{match: match, tool: tool, input: called})
}

// requests is every Messages request since since whose body carries each of needles.
func (s *kindStandin) requests(since time.Time, needles ...string) []kindSeen {
	s.mu.Lock()
	defer s.mu.Unlock()
	var held []kindSeen
	for _, seen := range s.seen {
		if seen.at.Before(since) {
			continue
		}
		if !slices.ContainsFunc(needles, func(needle string) bool { return !strings.Contains(seen.body, needle) }) {
			held = append(held, seen)
		}
	}
	return held
}

func (s *kindStandin) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[],"has_more":false}`)
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(req.URL.Path, "/v1/messages") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":1000}`)
		return
	}
	var request struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &request)
	seen := kindSeen{at: time.Now(), body: string(body), last: readStandinRequest(body, time.Now()).last, stream: request.Stream}
	text := string(body)
	switch {
	case strings.Contains(text, compactSummaryMarker):
		s.text(w, request.Stream, "The session so far, written as text: the task, the decisions and what is still open.")
		return
	case isSideQuestion(text):
		s.text(w, request.Stream, "PROCEEDING")
		return
	}
	s.mu.Lock()
	s.seen = append(s.seen, seen)
	var rule *kindRule
	for _, candidate := range s.rules {
		if !candidate.spent && candidate.match(seen) {
			candidate.spent, rule = true, candidate
			break
		}
	}
	id := fmt.Sprintf("toolu_kind_%d", len(s.seen))
	s.mu.Unlock()
	if rule != nil && request.Stream {
		streamAnthropicToolUse(w, "kind", id, rule.tool, rule.input)
		return
	}
	s.text(w, request.Stream, "WAITING for the proof's next instruction ("+s.marker+").")
}

// text answers with one text block: streamed when the request streams, as one message when it
// does not, which is how Claude Code asks for some of its own side requests.
func (s *kindStandin) text(w http.ResponseWriter, streamed bool, text string) {
	if streamed {
		streamAnthropicText(w, "kind", text)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "answered", "type": "message", "role": "assistant", "model": kindTreeModel,
		"content": []map[string]string{{"type": "text", "text": text}}, "stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": 1000, "output_tokens": 16},
	})
}
