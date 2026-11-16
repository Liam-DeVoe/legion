package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/natsauth"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	workershim "github.com/sjawhar/legion/daemon/internal/shim"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/stream"
	"github.com/sjawhar/legion/daemon/internal/testnats"
)

// sandboxRebuildRig is a Kubernetes daemon's runtime as its boot holds it, short of the daemon
// around it: the settings holder over a legion.yaml the test edits (config.Live), the one
// rebuild observer on it (rebuilder.observe, registered as run registers it), and the runtime
// holder built and registered as openSupervision builds and registers it, over the runtime
// build prepare leaves behind (p.runtimeFor). A change through the route is an apply of the
// holder's route arm, and a rotation of a file in place is the reread the file watcher's settle
// runs (rebuilder.rereadCredentials).
//
// Built with recorded, every build hands what it read — the cluster client and the Options —
// to the rig (overrides.buildSandbox) and runs on a fake runtime, so a test reads what a pod
// launched on that build would carry. Built without it, every build is the real Sandbox runtime
// over the API server the kubeconfig names, on a stream listener and a store of the rig's own.
type sandboxRebuildRig struct {
	t       *testing.T
	dir     string
	live    *config.Live
	rebuild *rebuilder
	holder  runtimeHolding
	mu      sync.Mutex
	// reads are what each build read, the boot's first, while the rig records them.
	reads []sandboxReads
	// living is the context each build's runtime runs on, the boot's first: a runtime the
	// holder replaced is stopped, which ends its context.
	living []context.Context
}

// unreachedCluster is the API server of a rig whose builds are recorded: no build contacts it.
const unreachedCluster = "https://192.0.2.20:6443"

// kubeconfigOf is a kubeconfig of one cluster at server, whose certificate authority is ca (PEM,
// "" for the host's own roots), reached with bearer, whose one context is the one
// kubernetesSwapBlock names.
func kubeconfigOf(server, ca, bearer string) string {
	authority := ""
	if ca != "" {
		authority = ", certificate-authority-data: " + base64.StdEncoding.EncodeToString([]byte(ca))
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: example, cluster: {server: %q%s}}]
users: [{name: legion-daemon, user: {token: %s}}]
contexts: [{name: legion-daemon, context: {cluster: example, user: legion-daemon, namespace: legion}}]
current-context: legion-daemon
`, server, authority, bearer)
}

// newSandboxRebuildRig writes a Kubernetes deployment's files — its legion.yaml
// (runtimeSwapFile), its bearers, and kubeconfig as its cluster credential — and boots its
// runtime from them.
func newSandboxRebuildRig(t *testing.T, kubeconfig string, recorded bool) *sandboxRebuildRig {
	t.Helper()
	rig := &sandboxRebuildRig{t: t, dir: t.TempDir()}
	for name, contents := range map[string]string{
		"envoy-token": "envoy\n", "operator-token": "operator\n", "dispatch-token": "dispatch\n", "kubeconfig": kubeconfig,
	} {
		rig.write(name, contents)
	}
	path := rig.write("legion.yaml", runtimeSwapFile(t, rig.dir, kubernetesSwapBlock(rig.dir)))
	live, err := config.NewLive(path, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	rig.live = live
	rig.rebuild = newRebuilder(quietLogger(), live)
	live.OnChange(rig.rebuild.observe)

	o := overrides{environ: []string{}}
	var listener *stream.Listener
	var st *store.Store
	if recorded {
		o.buildSandbox = rig.record
	} else {
		refuse := func(string) (claim.Token, uint64, bool, bool, error) { return "", 0, false, false, nil }
		if listener, err = stream.Listen(t.Context(), "tcp://127.0.0.1:0", refuse, stream.Options{RPCTimeout: 5 * time.Second, Log: quietLogger()}); err != nil {
			t.Fatal(err)
		}
		if st, err = store.Open(t.Context(), testDSN(t)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(st.Close)
	}
	cfg := *live.Snapshot()
	p, err := prepare(cfg, quietLogger(), o)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// openSupervision's buildRuntime: the factory for the configuration it is given, over the
	// same listener and address, on a context of the runtime's own.
	build := func(c config.Config) (runtime.Runtime, context.CancelFunc, error) {
		factory, err := p.runtimeFor(c)
		if err != nil {
			return nil, nil, err
		}
		living, stop := context.WithCancel(t.Context())
		address := ""
		if listener != nil {
			address = listener.Addr()
		}
		next, err := factory(living, listener, address, outboxTokens{}, nil, st, noRepositories)
		if err != nil {
			stop()
			return nil, nil, err
		}
		rig.mu.Lock()
		rig.living = append(rig.living, living)
		rig.mu.Unlock()
		return next, stop, nil
	}
	built, stop, err := build(cfg)
	if err != nil {
		t.Fatalf("build the boot's runtime: %v", err)
	}
	rig.holder = newRuntimeHolder(built, stop, quietLogger(), build)
	rig.rebuild.register(rig.holder)
	return rig
}

// record is the rig's stand-in for the Sandbox runtime's construction (overrides.buildSandbox):
// it keeps what the build read and runs that build on a fake runtime.
func (r *sandboxRebuildRig) record(reads sandboxReads) runtimeFactory {
	r.mu.Lock()
	r.reads = append(r.reads, reads)
	r.mu.Unlock()
	return func(context.Context, *stream.Listener, string, appauth.Tokens,
		func(context.Context, string) (ghrepo.Repository, string, error), *store.Store, func() []ghrepo.Repository) (runtime.Runtime, error) {
		return fake.NewRuntime(), nil
	}
}

// write writes contents as the deployment's file name, in place when it is there, and answers
// its path.
func (r *sandboxRebuildRig) write(name, contents string) string {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// builds is how many times the runtime has been built, the boot's build among them, and last
// what the latest recorded build read.
func (r *sandboxRebuildRig) builds() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.living)
}

func (r *sandboxRebuildRig) last() sandboxReads {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reads) == 0 {
		r.t.Fatal("no build of the runtime was recorded")
	}
	return r.reads[len(r.reads)-1]
}

// reread is the file watcher's settle after a wake: every credential holder reads its files
// again, over the configuration the daemon is running on.
func (r *sandboxRebuildRig) reread() { r.rebuild.rereadCredentials(r.live.Snapshot()) }

// current is the runtime the holder serves every call from now.
func (r *sandboxRebuildRig) current() runtime.Runtime { return r.holder.(heldRuntime).get() }

// bootStopped is whether the runtime the boot built has been stopped.
func (r *sandboxRebuildRig) bootStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.living[0].Err() != nil
}

// A Kubernetes daemon's pods carry the Dispatch bearer in each claim's Secret (DISPATCH_TOKEN),
// which the Sandbox runtime is handed when it is built (sandbox.Options.DispatchToken). So the
// runtime holds `dispatch_token_file` beside the Dispatch client (config.Classes' row, Also),
// and each build reads the bearer from the file at that build (prepareSandbox's runtimeFor): a
// change of the file through the route rebuilds the runtime on the bearer the new file holds,
// and a rotation of the file in place, which changes no setting, rebuilds it at the file
// watcher's next reread, since the runtime holder reports the file among its credentials
// (runtimeHolder.credentials). Built from the boot's read instead, every pod launched after
// either went on carrying the bearer the daemon booted on.
//
// The last reread is the control: a wake with nothing moved rebuilds nothing, since a Sandbox
// rebuild restarts the runtime's informers.
func TestASandboxRuntimeRebuildHandsTheNextPodTheDispatchBearerItsFileHoldsNow(t *testing.T) {
	rig := newSandboxRebuildRig(t, kubeconfigOf(unreachedCluster, "", "kube-bearer"), true)
	if got := rig.last().opts.DispatchToken; got != "dispatch" {
		t.Fatalf("the boot's runtime hands pods the Dispatch bearer %q, want the file's", got)
	}

	routed := rig.write("dispatch-token-routed", "routed-bearer\n")
	apply(t, rig.live, map[string]any{"dispatch_token_file": routed})
	if builds, got := rig.builds(), rig.last().opts.DispatchToken; builds != 2 || got != "routed-bearer" {
		t.Fatalf("after a route change of dispatch_token_file the runtime was built %d times and hands pods %q, want twice and the new file's bearer", builds, got)
	}

	rig.write("dispatch-token-routed", "rotated-bearer\n")
	rig.reread()
	if builds, got := rig.builds(), rig.last().opts.DispatchToken; builds != 3 || got != "rotated-bearer" {
		t.Fatalf("after the file was rotated in place the runtime was built %d times and hands pods %q, want three times and the rotated bearer", builds, got)
	}

	rig.reread()
	if builds := rig.builds(); builds != 3 {
		t.Fatalf("a reread over files nothing moved built the runtime %d times, want the three before it", builds)
	}
}

// Every launch reads DISPATCH_URL and ENVOY_NATS_URL from the runtime, which holds both from
// the configuration it was built on (sandbox.Options.DispatchURL and NATSURLs; tmuxOptions'
// DispatchURL and NatsURLs). So the runtime holds `dispatch_url` and `nats_urls` beside the
// Dispatch client and NATS (config.Classes' rows, Also), and a change of either through the
// route rebuilds it: the next launch is told the new address, where before the change waited
// for some unrelated rebuild of the runtime.
func TestAChangedDispatchURLOrNatsURLsRebuildsTheRuntimeTheNextLaunchReads(t *testing.T) {
	rig := newSandboxRebuildRig(t, kubeconfigOf(unreachedCluster, "", "kube-bearer"), true)
	if got := rig.last().opts; got.DispatchURL != "https://dispatch.internal.example" || !slices.Equal(got.NATSURLs, []string{"nats://nats.internal.example:4222"}) {
		t.Fatalf("the boot's runtime tells launches DISPATCH_URL %q and NATS %v, want the file's", got.DispatchURL, got.NATSURLs)
	}

	const dispatchURL = "https://dispatch-2.internal.example"
	apply(t, rig.live, map[string]any{"dispatch_url": dispatchURL})
	if builds, got := rig.builds(), rig.last().opts.DispatchURL; builds != 2 || got != dispatchURL {
		t.Fatalf("after a route change of dispatch_url the runtime was built %d times and tells launches %q, want twice and %q", builds, got, dispatchURL)
	}

	natsURLs := []string{"nats://nats-2.internal.example:4222"}
	apply(t, rig.live, map[string]any{"nats_urls": []any{natsURLs[0]}})
	if builds, got := rig.builds(), rig.last().opts.NATSURLs; builds != 3 || !slices.Equal(got, natsURLs) {
		t.Fatalf("after a route change of nats_urls the runtime was built %d times and tells launches %v, want three times and %v", builds, got, natsURLs)
	}
}

// `instructions` is a launch key of both pod kinds, which the Sandbox runtime records in every
// pod's launch-settings annotation for an update round to read the pod's drift from
// (sandbox.Options.LaunchSettings, runtime/sandbox's LaunchSettings); each build of the runtime
// makes that record once. So the runtime holds the key beside the launch specs (config.Classes'
// row, Also), and a change of it through the route builds the runtime again on a record that
// names the new file. Held by the launch specs alone, the runtime kept the record it booted on,
// and no pod launched on the old instructions read as drifted.
func TestAChangedInstructionsPathRebuildsTheRuntimeThatRecordsEachPodsLaunch(t *testing.T) {
	rig := newSandboxRebuildRig(t, kubeconfigOf(unreachedCluster, "", "kube-bearer"), true)
	instructions := rig.write("instructions.md", "Never push to main.\n")

	apply(t, rig.live, map[string]any{"instructions": instructions})

	if builds := rig.builds(); builds != 2 {
		t.Fatalf("after a route change of instructions the runtime was built %d times, want twice", builds)
	}
	var recorded map[string]string
	if err := json.Unmarshal([]byte(rig.last().opts.LaunchSettings), &recorded); err != nil {
		t.Fatalf("decode the launch settings the rebuilt runtime records: %v", err)
	}
	if got := recorded["instructions"]; got != instructions {
		t.Fatalf("the rebuilt runtime records every pod's instructions as %q, want %q", got, instructions)
	}
}

// The plugin pair (`runtime.kubernetes.plugins`) is read by one launch, a stale-plugin relaunch
// into the pod it keeps, and the launch specs hand the pair there (specs.stalePluginInstall): no
// pod's own launch reads it, since a fresh pod loads the image's own copies. So its rows name the
// launch specs alone (config.Classes), the route answers a change of it `rebuilt` naming them, and
// the change builds no runtime. Nor does the runtime record the pair when some other change builds
// it from a configuration that names one: the record of each pod's launch it holds
// (sandbox.Options.LaunchSettings) carries no key of the pair. Held by the runtime as well, the
// change built the runtime again on a record naming the new pair, and every pod of the fleet read
// as drifted: an update round over each one, each pod replaced and its disk deleted, for a value
// no pod of it reads.
func TestAChangedPluginPairBuildsNoRuntimeAndIsRecordedForNoPod(t *testing.T) {
	rig := newSandboxRebuildRig(t, kubeconfigOf(unreachedCluster, "", "kube-bearer"), true)

	changes := apply(t, rig.live, map[string]any{"runtime.kubernetes.plugins": map[string]any{"pi_legion": "9.8.0", "pi_envoy": "9.7.2"}})

	if builds := rig.builds(); builds != 1 {
		t.Fatalf("after a route change of the plugin pair the runtime was built %d times, want once: every rebuild records the pods' launch settings again", builds)
	}
	answered := 0
	for _, change := range changes {
		if !strings.HasPrefix(change.Path, "runtime.kubernetes.plugins") {
			continue
		}
		answered++
		if change.Outcome != config.OutcomeRebuilt || change.Holder != config.HolderLaunchSpecs {
			t.Errorf("the route answers %s %s naming %q, want rebuilt naming the launch specs, the one holder a stale-plugin relaunch reads the pair from",
				change.Path, change.Outcome, change.Holder)
		}
	}
	if answered == 0 {
		t.Fatalf("the route answered %+v, none of it the plugin pair", changes)
	}

	// A key the runtime does hold builds it again, now from a configuration naming the pair.
	apply(t, rig.live, map[string]any{"dispatch_url": "https://dispatch-2.internal.example"})
	if builds := rig.builds(); builds != 2 {
		t.Fatalf("after a route change of dispatch_url the runtime was built %d times, want twice", builds)
	}
	var recorded map[string]string
	if err := json.Unmarshal([]byte(rig.last().opts.LaunchSettings), &recorded); err != nil {
		t.Fatalf("decode the launch settings the rebuilt runtime records: %v", err)
	}
	for path := range recorded {
		if strings.HasPrefix(path, "runtime.kubernetes.plugins") {
			t.Errorf("the runtime records %s in every pod's launch settings, which no pod's launch reads", path)
		}
	}
}

// Whether a Kubernetes daemon has a pane NATS seed decides where every pod reads it: a daemon
// with one names NATS_NKEY_SEED among the launch secrets and among the providers Secret's keys,
// so each pod mounts the deployment's copy and is pointed at it (NATS_NKEY_SEED_FILE); a daemon
// with none names neither (launchSecretNames, providersSecrets). Both are read from the
// configuration each build of the runtime is given (readSandbox), so the runtime holds
// `nats_nkey_seed_file` beside NATS and the launch specs (config.Classes' row, Also): a route
// change that sets the key where it was unset rebuilds the runtime, and the next pod reads the
// seed from the providers Secret. Clearing the key is the same read answering no, so this one
// direction drives the one code path both take. Without the runtime on the row, the runtime
// kept the boot's answer until some unrelated change rebuilt it.
func TestSettingTheNatsSeedFileRebuildsTheRuntimeThatPointsEveryPodAtTheSeed(t *testing.T) {
	rig := newSandboxRebuildRig(t, kubeconfigOf(unreachedCluster, "", "kube-bearer"), true)
	if got := rig.last().opts; len(got.ProvidersSecrets) != 0 || slices.Contains(got.LaunchSecrets, natsauth.SeedVariable) {
		t.Fatalf("the boot's runtime, with no seed configured, names providers secrets %v and launch secrets %v, want no seed in either", got.ProvidersSecrets, got.LaunchSecrets)
	}

	seed := rig.write("nats-seed", testnats.UserSeed(t)+"\n")
	apply(t, rig.live, map[string]any{"nats_nkey_seed_file": seed})

	got := rig.last().opts
	if builds := rig.builds(); builds != 2 {
		t.Fatalf("after a route change set nats_nkey_seed_file the runtime was built %d times, want twice", builds)
	}
	if !slices.Equal(got.ProvidersSecrets, []string{natsauth.SeedVariable}) || !slices.Contains(got.LaunchSecrets, natsauth.SeedVariable) {
		t.Fatalf("the runtime built after the seed was set names providers secrets %v and launch secrets %v, want %s in both", got.ProvidersSecrets, got.LaunchSecrets, natsauth.SeedVariable)
	}
}

// kubeAPI is the rig's Kubernetes API stand-in (issueSandboxAPI) on a TLS server of its own,
// recording the bearer of every request it is asked; ca is the server's certificate, PEM.
type kubeAPI struct {
	url, ca string
	mu      sync.Mutex
	bearers []string
}

func newKubeAPI(t *testing.T) *kubeAPI {
	t.Helper()
	k := &kubeAPI{}
	api := newIssueSandboxAPI()
	// TLS, since a kubeconfig's user credentials reach only a cluster served over it
	// (clientcmd reads the user's auth only for a TLS transport).
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.bearers = append(k.bearers, r.Header.Get("Authorization"))
		k.mu.Unlock()
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { api.requireServed(t) })
	k.url = server.URL
	k.ca = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	return k
}

// kubeconfig is a kubeconfig of this API, reached with bearer.
func (k *kubeAPI) kubeconfig(bearer string) string { return kubeconfigOf(k.url, k.ca, bearer) }

// asked is the bearer of every request the API was asked so far.
func (k *kubeAPI) asked() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.bearers)
}

// The daemon's cluster credential is a file (`runtime.kubernetes.kubeconfig`), and a rotation of
// it in place — the same path, the contents of a new credential — changes no setting, so no
// route change or settle of legion.yaml names the runtime. The runtime holder reports the
// kubeconfig among its credentials (runtimeHolder.credentials), so the file watcher's next
// reread (rebuilder.rereadCredentials) builds the runtime again, and the build reads the
// kubeconfig again (kubeClient): the new runtime's informers ask the cluster the rotated file
// names, with the rotated bearer, and the runtime on the boot's client is stopped. Without it a
// rotated kubeconfig reached the daemon only at a restart, and a revoked one left it asking the
// cluster with a credential that no longer answers.
//
// The runtime is the real one (sandbox.New, through prepare's own build) over two API servers,
// the boot's kubeconfig naming the first and the rotated one the second. The first reread, with
// nothing moved, is the control: it builds nothing.
func TestAKubeconfigRotatedInPlaceRebuildsTheRuntimeOnTheClusterItNamesNow(t *testing.T) {
	booted, rotated := newKubeAPI(t), newKubeAPI(t)
	rig := newSandboxRebuildRig(t, booted.kubeconfig("boot-kube-bearer"), false)
	first := rig.current()
	if asked := booted.asked(); len(asked) == 0 || asked[0] != "Bearer boot-kube-bearer" {
		t.Fatalf("the boot's runtime asked the cluster its kubeconfig names %v, want its informers' lists with the file's bearer", asked)
	}

	rig.reread()
	if rig.current() != first || rig.builds() != 1 {
		t.Fatalf("a reread over a kubeconfig nothing moved built the runtime %d times, want once: each rebuild restarts its informers", rig.builds())
	}

	rig.write("kubeconfig", rotated.kubeconfig("rotated-kube-bearer"))
	rig.reread()

	if rig.current() == first || rig.builds() != 2 {
		t.Fatalf("after the kubeconfig was rotated in place the runtime was built %d times, want twice: the daemon still runs on the cluster client it booted on", rig.builds())
	}
	asked := rotated.asked()
	if len(asked) == 0 {
		t.Fatal("the runtime built after the rotation asked the cluster the rotated kubeconfig names nothing")
	}
	for _, bearer := range asked {
		if bearer != "Bearer rotated-kube-bearer" {
			t.Errorf("the rotated kubeconfig's cluster was asked with %q, want the rotated bearer", bearer)
		}
	}
	if !rig.bootStopped() {
		t.Error("the runtime on the boot's cluster client is still running beside the one the rotation built")
	}
}

// A tmux daemon's panes read the Dispatch bearer from the one file the daemon writes under its
// state directory (`<state_dir>/secrets/dispatch-token`, every pane's DISPATCH_TOKEN_FILE). So
// the runtime holds `dispatch_token_file` beside the Dispatch client (config.Classes' row,
// Also), and each build of the runtime reads the bearer from the file the configuration names
// and writes the pane's file again (prepareTmux's runtimeFor): after a change of the file
// through the settings route, and after a rotation of the file in place that the file watcher's
// reread finds (rebuilder.rereadCredentials, runtimeHolder.credentials), the next pane reads the
// new bearer. Written once at boot instead, every pane went on reading the bearer the daemon
// booted on.
func TestATmuxRuntimeRebuildWritesThePaneDispatchBearerItsFileHoldsNow(t *testing.T) {
	natsURL := workflowNATS(t)
	cfg := workflowConfig(t, natsURL)
	dir := t.TempDir()
	routed := filepath.Join(dir, "routed-dispatch-token")
	if err := os.WriteFile(routed, []byte("routed-bearer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legion.yaml")
	body := fmt.Sprintf(`project: %[1]s
port: %[2]d
bind: 127.0.0.1
postgres_dsn: %[3]s
state_dir: %[4]s
runtime: tmux
daemon_url: http://127.0.0.1:%[2]d
operator_token_file: %[5]s
controller: operator
dispatch_url: %[6]s
dispatch_token_file: %[7]s
nats_urls: [%[8]s]
projects:
  %[1]s: {}
github_apps:
  implement: { app_id: "1", private_key: implement-test-key }
  review: { app_id: "2", private_key: review-test-key }
`, cfg.Project, cfg.Port, cfg.PostgresDSN, cfg.StateDir, cfg.OperatorTokenFile, cfg.DispatchURL, cfg.DispatchTokenFile, natsURL)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := config.NewLive(path, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	o := fakeRuntime(fake.NewRuntime(), &built{})
	o.workflowTokens = &workflowTokenRecorder{}
	var rebuild *rebuilder
	o.settingsRebuilder = func(r *rebuilder) { rebuild = r }
	d := startDaemonOn(t, live, o, quietLogger(), 30*time.Second)
	pane := runtime.SecretFilePath(cfg.StateDir, runtime.DispatchTokenFileName)
	paneBearer := func() string {
		t.Helper()
		contents, err := os.ReadFile(pane)
		if err != nil {
			t.Fatalf("read the panes' Dispatch token file: %v", err)
		}
		return string(contents)
	}
	if got := paneBearer(); got != "dispatch-test-token" {
		t.Fatalf("after boot the panes read the Dispatch bearer %q, want the file's", got)
	}

	request := api.SettingsChangeRequest{By: "ana@example.com", Changes: map[string]json.RawMessage{
		"dispatch_token_file": json.RawMessage(strconv.Quote(routed)),
	}}
	if status, answer := settingsRequest(t, d, testOperatorToken, request); status != http.StatusOK || answer["dispatch_token_file"].Applied != string(config.OutcomeRebuilt) {
		t.Fatalf("the settings route answered dispatch_token_file with %d %+v, want it rebuilt", status, answer["dispatch_token_file"])
	}
	if got := paneBearer(); got != "routed-bearer" {
		t.Fatalf("after a route change of dispatch_token_file the next pane reads the bearer %q, want the new file's", got)
	}

	if err := os.WriteFile(routed, []byte("rotated-bearer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rebuild == nil {
		t.Fatal("the daemon handed the test no rebuild observer")
	}
	d.live.WhileSettled(rebuild.rereadCredentials)
	if got := paneBearer(); got != "rotated-bearer" {
		t.Fatalf("after the file was rotated in place the next pane reads the bearer %q, want the rotated one", got)
	}
}

// Every launch's spec carries the pane NATS nkey seed as NATS_NKEY_SEED (launchSecrets), read
// when the launch specs are built. So the launch specs hold `nats_nkey_seed_file` beside NATS
// (config.Classes' row, Also), and a change of the file through the route rebuilds them: the
// next launch carries the seed the new file holds, rather than waiting for the file watcher's
// reread to notice that the specs' files moved.
func TestARouteChangeOfTheNatsSeedFileReachesTheNextLaunch(t *testing.T) {
	booted, routed := testnats.UserSeed(t), testnats.UserSeed(t)
	live := liveSettings(t, "nats_nkey_seed_file: "+testnats.SeedFile(t, booted+"\n"))
	rt := fake.NewRuntime()
	d := startDaemonOn(t, live, fakeRuntime(rt, &built{}), quietLogger(), 10*time.Second)
	if got := lastLaunch(t, rt, d.spawn(architect())).Secrets["NATS_NKEY_SEED"]; got != booted {
		t.Fatalf("the launch before the change carries NATS_NKEY_SEED %q, want the file's seed", got)
	}

	apply(t, live, map[string]any{"nats_nkey_seed_file": testnats.SeedFile(t, routed+"\n")})

	worker := d.spawn(api.SpawnRequest{Tree: "LEGION-1", Issue: "LEGION-2", Role: claim.RoleImplementer, Prompt: "Wait."})
	if got := lastLaunch(t, rt, worker).Secrets["NATS_NKEY_SEED"]; got != routed {
		t.Errorf("the launch after a route change of nats_nkey_seed_file carries NATS_NKEY_SEED %q, want the new file's seed", got)
	}
	if strings.Contains(fmt.Sprint(lastLaunch(t, rt, worker).Secrets), booted) {
		t.Error("the launch after the change still carries the seed the daemon booted on")
	}
}

// A tmux daemon's panes read every provider key from a set the daemon writes under its state
// directory (`<state_dir>/secrets/provider-env/<set>`, each pane's `--provider-env-dir`), one
// 0600 file per variable, never from the secret store or the operator's file themselves
// (config.MaterializeProviderKeys). `provider_keys` is held by the runtime, and each build of the
// tmux runtime is built on a set written for the keys it names, written again when they or their
// files have moved (prepareTmux's runtimeFor, providerKeyFiles): after a change of
// `provider_keys` through the route, and after a key's file is rotated in place, which the file
// watcher's reread finds since the runtime holder reports each key's file among its credentials
// (runtimeHolder.credentials), the next pane reads the new key. Written once at boot instead,
// every pane went on reading the keys the daemon booted on.
//
// The keys are of the file form (`file:/abs/path`), which the daemon reads itself; the secretsd
// form runs `secrets get`, which is that form's own test (TestRunResolvesProviderKeysAtBootAndKeepsThemThroughThePrune).
func TestATmuxRuntimeRebuildWritesTheProviderKeysTheNextPaneReads(t *testing.T) {
	dir := t.TempDir()
	booted, routed := filepath.Join(dir, "booted-key"), filepath.Join(dir, "routed-key")
	for file, key := range map[string]string{booted: "booted-provider-key\n", routed: "routed-provider-key\n"} {
		if err := os.WriteFile(file, []byte(key), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rig := newTmuxLaunchRig(t, `omp_invocation: "mise x omp-a -- omp"`, "provider_keys:\n  AGENT_PROVIDER_KEY: file:"+booted)
	if got, want := paneExports(t, rig.current()), []string{"AGENT_PROVIDER_KEY=booted-provider-key"}; !slices.Equal(got, want) {
		t.Fatalf("after boot a pane exports %q, want %q", got, want)
	}

	if _, failed := rig.route(map[string]any{"provider_keys": map[string]any{"AGENT_PROVIDER_KEY": "file:" + routed}}); len(failed) != 0 {
		t.Fatalf("the route was told %v for a change of provider_keys this host can build", failed)
	}
	if got, want := paneExports(t, rig.current()), []string{"AGENT_PROVIDER_KEY=routed-provider-key"}; !slices.Equal(got, want) {
		t.Fatalf("after a route change of provider_keys the next pane exports %q, want %q", got, want)
	}

	if err := os.WriteFile(routed, []byte("rotated-provider-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rig.reread()
	if got, want := paneExports(t, rig.current()), []string{"AGENT_PROVIDER_KEY=rotated-provider-key"}; !slices.Equal(got, want) {
		t.Fatalf("after the key's file was rotated in place the next pane exports %q, want %q", got, want)
	}
}

// A build of the tmux runtime can refuse a change of `provider_keys`: the runtime's construction
// refuses a key named for a variable every pane already carries (tmux.New), and a key changed
// beside a launch this host cannot resolve fails with that launch (paneLaunchers). A refused
// build changes nothing — the daemon keeps the runtime it has (runtimeHolder.rebuild) — and the
// keys that runtime's panes read are part of it: the next pane the kept runtime opens is told
// the set it was told before the change, and its shim exports the keys it exported before
// (workershim.ReadProviderEnv). Written where every pane reads before the build could refuse,
// the kept runtime's next pane exported the keys of the change the daemon refused.
//
// Nothing of the refusal outlives it but the set it wrote, which no pane is told: the next
// build of the same keys is handed that set rather than resolving every key again (a secretsd
// key of the human tier may cost a tap), and a build of other keys removes it.
//
// The keys are of the file form (`file:/abs/path`), which the daemon reads itself.
func TestARefusedProviderKeyChangeLeavesTheKeptRuntimesPanesTheKeysTheyRead(t *testing.T) {
	dir := t.TempDir()
	booted, routed, lang := filepath.Join(dir, "booted-key"), filepath.Join(dir, "routed-key"), filepath.Join(dir, "lang")
	for file, key := range map[string]string{booted: "booted-provider-key\n", routed: "routed-provider-key\n", lang: "C.UTF-8\n"} {
		if err := os.WriteFile(file, []byte(key), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := []string{"AGENT_PROVIDER_KEY=booted-provider-key"}
	for _, tc := range []struct {
		name    string
		refused map[string]any
		why     string
		next    map[string]any
		// reuses is whether the next change names the keys the refused one did, so its build is
		// handed the set the refused build wrote.
		reuses bool
	}{
		{
			name:    "the runtime refuses a key",
			refused: map[string]any{"provider_keys": map[string]any{"AGENT_PROVIDER_KEY": "file:" + routed, "LANG": "file:" + lang}},
			why:     config.HolderRuntime + ": tmux runtime: provider key LANG is a variable every pane already carries",
			next:    map[string]any{"provider_keys": map[string]any{"AGENT_PROVIDER_KEY": "file:" + routed}},
		},
		{
			name:    "the host cannot launch the change beside it",
			refused: map[string]any{"provider_keys": map[string]any{"AGENT_PROVIDER_KEY": "file:" + routed}, "omp_invocation": "mise x omp-missing -- omp"},
			why:     config.HolderRuntime + ": mise could not resolve OMP tool omp-missing",
			next:    map[string]any{"omp_invocation": "mise x omp-b -- omp"},
			reuses:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTmuxLaunchRig(t, `omp_invocation: "mise x omp-a -- omp"`, "provider_keys:\n  AGENT_PROVIDER_KEY: file:"+booted)
			kept := rig.current()
			if got := paneExports(t, kept); !slices.Equal(got, before) {
				t.Fatalf("a pane of the boot's runtime exports %q, want %q", got, before)
			}

			_, failed := rig.route(tc.refused)

			if why := failed["provider_keys"]; !strings.HasPrefix(why, tc.why) {
				t.Fatalf("the route was told %v, want provider_keys answered failed with %q: this proves nothing", failed, tc.why)
			}
			if rig.current() != kept {
				t.Fatal("the holder serves another runtime after a build that refused")
			}
			if got := paneExports(t, kept); !slices.Equal(got, before) {
				t.Errorf("the next pane of the runtime the daemon kept exports %q, want the keys it exported before the refused change, %q", got, before)
			}
			sets := keySets(t, rig)
			if len(sets) != 2 || !slices.Contains(sets, kept.opts.ProviderEnvDir) {
				t.Fatalf("after the refusal the provider-key directory holds %q, want the kept runtime's set (%s) and the one the refused build wrote", sets, kept.opts.ProviderEnvDir)
			}
			refusedSet := sets[0]
			if refusedSet == kept.opts.ProviderEnvDir {
				refusedSet = sets[1]
			}

			// The next change this host can build reaches the next pane.
			if _, failed := rig.route(tc.next); len(failed) != 0 {
				t.Fatalf("the route was told %v for a change this host can build", failed)
			}
			served := rig.current()
			if served == kept {
				t.Fatal("the holder still serves the boot's runtime after a change it could build")
			}
			if got, want := paneExports(t, served), []string{"AGENT_PROVIDER_KEY=routed-provider-key"}; !slices.Equal(got, want) {
				t.Errorf("a pane of the runtime built after the refusal exports %q, want %q", got, want)
			}
			if reused := served.opts.ProviderEnvDir == refusedSet; reused != tc.reuses {
				t.Errorf("the runtime built after the refusal is built on %s, the refused build's set %s: reused %t, want %t", served.opts.ProviderEnvDir, refusedSet, reused, tc.reuses)
			}
			if _, err := os.Stat(refusedSet); !tc.reuses && !os.IsNotExist(err) {
				t.Errorf("the set the refused build wrote outlived a build of other keys (stat: %v)", err)
			}
			if got := paneExports(t, kept); !slices.Equal(got, before) {
				t.Errorf("a pane the kept runtime opened before its stop exports %q, want %q: the set a runtime was built on outlives its stop", got, before)
			}
		})
	}
}

// A runtime holds the set it was built on past its own stop. The holder stops the runtime it
// replaced once the calls inside it have returned (runtimeHolder.rebuild), but a tmux Spawn
// answers once the pane's process runs its command, and that pane's shim reads its set after
// that, before it dials the daemon (workerShimConfig). So the set reads whole while the runtime
// drains, still reads whole after the runtime is stopped, and goes once the registration
// deadline the runtime was built from (`worker_boot_timeout_seconds` ×
// `worker_boot_registration_deadline_intervals`, here one second) has passed after its stop, by
// which every pane it opened has dialled or been retired. Removed at the stop, it could go from
// under a pane opened a moment before, whose shim would refuse to start.
func TestAKeySetOutlivesTheRuntimeBuiltOnItByItsRegistrationDeadline(t *testing.T) {
	dir := t.TempDir()
	booted, held, next := filepath.Join(dir, "booted-key"), filepath.Join(dir, "held-key"), filepath.Join(dir, "next-key")
	for file, key := range map[string]string{booted: "booted-provider-key\n", held: "held-provider-key\n", next: "next-provider-key\n"} {
		if err := os.WriteFile(file, []byte(key), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rig := newTmuxLaunchRig(t, `omp_invocation: "mise x omp-a -- omp"`, "provider_keys:\n  AGENT_PROVIDER_KEY: file:"+booted)
	if _, failed := rig.route(map[string]any{
		"worker_boot_timeout_seconds": 1, "worker_boot_registration_deadline_intervals": 1,
		"provider_keys": map[string]any{"AGENT_PROVIDER_KEY": "file:" + held},
	}); len(failed) != 0 {
		t.Fatalf("the route was told %v for a change this host can build", failed)
	}
	draining := rig.current()
	set, want := draining.opts.ProviderEnvDir, []string{"AGENT_PROVIDER_KEY=held-provider-key"}
	read := func() ([]string, error) {
		exported, err := workershim.ReadProviderEnv(set, func(string) (string, bool) { return "", false })
		slices.Sort(exported)
		return exported, err
	}

	// A launch inside the runtime holds the next rebuild's drain open until the set has been read.
	entered, release := make(chan struct{}), make(chan struct{})
	draining.OnResume = func(context.Context) error {
		close(entered)
		<-release
		return nil
	}
	launched := make(chan error, 1)
	go func() {
		_, err := rig.holder.Resume(t.Context(), nil, rig.spec(claim.RoleImplementer))
		launched <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the launch never entered the runtime")
	}
	type reading struct {
		exported []string
		err      error
	}
	whileDraining := make(chan reading, 1)
	go func() {
		defer close(release)
		for give := time.Now().Add(10 * time.Second); rig.holder.(heldRuntime).get() == runtime.Runtime(draining); time.Sleep(time.Millisecond) {
			if time.Now().After(give) {
				whileDraining <- reading{err: fmt.Errorf("the holder never swapped in the runtime the change built")}
				return
			}
		}
		exported, err := read()
		whileDraining <- reading{exported, err}
	}()

	if _, failed := rig.route(map[string]any{"provider_keys": map[string]any{"AGENT_PROVIDER_KEY": "file:" + next}}); len(failed) != 0 {
		t.Fatalf("the route was told %v for a change this host can build", failed)
	}
	if err := <-launched; err != nil {
		t.Fatalf("the launch inside the draining runtime: %v", err)
	}
	if got := <-whileDraining; got.err != nil || !slices.Equal(got.exported, want) {
		t.Fatalf("while the replaced runtime drained its set read %q (%v), want %q", got.exported, got.err, want)
	}

	// The rebuild has stopped the replaced runtime; half its deadline later its set still reads.
	time.Sleep(500 * time.Millisecond)
	if got, err := read(); err != nil || !slices.Equal(got, want) {
		t.Fatalf("half the registration deadline after its runtime was stopped the set read %q (%v), want %q: a pane opened just before the stop may not have read it yet", got, err, want)
	}
	for give := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(set); os.IsNotExist(err) {
			break
		}
		if time.Now().After(give) {
			t.Fatalf("the set of a runtime stopped well past its registration deadline is still there: %s", set)
		}
	}
	if got, want := paneExports(t, rig.current()), []string{"AGENT_PROVIDER_KEY=next-provider-key"}; !slices.Equal(got, want) {
		t.Errorf("a pane of the runtime the daemon serves exports %q, want %q", got, want)
	}
}

// keySets are the sets of provider keys the rig's state directory holds (config.ProviderEnvDir),
// each by its path, in name order.
func keySets(t *testing.T, rig *tmuxLaunchRig) []string {
	t.Helper()
	parent := config.ProviderEnvDir(rig.state)
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var sets []string
	for _, entry := range entries {
		sets = append(sets, filepath.Join(parent, entry.Name()))
	}
	return sets
}

// paneExports is what the shim of a pane served opens exports into Oh My Pi's environment from
// the provider-key directory the pane is told (`--provider-env-dir`), in a pane whose own
// environment carries none of the keys.
func paneExports(t *testing.T, served *launchedTmux) []string {
	t.Helper()
	if served.opts.ProviderEnvDir == "" {
		return nil
	}
	exported, err := workershim.ReadProviderEnv(served.opts.ProviderEnvDir, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("the provider keys a pane is told: %v", err)
	}
	slices.Sort(exported)
	return exported
}

// A build of the tmux runtime whose provider keys have not moved resolves none of them again
// (providerKeyFiles): a secretsd key of the human tier may cost a person a YubiKey tap, and the
// runtime is built again for every change of a row it holds, its boot build right after the
// plugin gate's resolution among them. So the boot asks secretsd once per key, as it did when the
// keys were written once, and a rebuild for a key that moved no provider key asks it nothing,
// while a change of `provider_keys` asks it again.
func TestATmuxRuntimeRebuildThatMovesNoProviderKeyAsksSecretsdNothing(t *testing.T) {
	bin, asked := t.TempDir(), filepath.Join(t.TempDir(), "asked")
	stub := "#!/bin/sh\necho \"$2 $3\" >> " + asked + "\ncase \"$2:$3\" in\n" +
		"  AGENT_KEY:--no-request) echo '{\"key\":\"AGENT_KEY\",\"tier\":\"human\"}' ;;\n" +
		"  AGENT_KEY:--value) echo 'agent-value' ;;\n" +
		"  OTHER_KEY:--no-request) echo '{\"key\":\"OTHER_KEY\",\"tier\":\"agent\"}' ;;\n" +
		"  OTHER_KEY:--value) echo 'other-value' ;;\n" +
		"  *) exit 9 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "secrets"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	requests := func() []string {
		t.Helper()
		contents, err := os.ReadFile(asked)
		if err != nil {
			t.Fatalf("read what secretsd was asked: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(contents)), "\n")
	}
	live := liveSettings(t, "provider_keys:\n  AGENT_ENV: AGENT_KEY")
	o := fakeRuntime(fake.NewRuntime(), &built{})
	o.environ = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	logs := &syncBuffer{}
	startDaemonOn(t, live, o, slog.New(slog.NewJSONHandler(logs, nil)), 10*time.Second)
	if got := requests(); !slices.Equal(got, []string{"AGENT_KEY --no-request", "AGENT_KEY --value"}) {
		t.Fatalf("the boot asked secretsd %q, want the key's tier and its value once", got)
	}

	apply(t, live, map[string]any{"probe_interval_seconds": 31})
	if !strings.Contains(logs.String(), `"msg":"legion settings: holder rebuilt","holder":"runtime"`) {
		t.Fatal("the change of probe_interval_seconds did not rebuild the runtime, so this proves nothing")
	}
	if got := requests(); len(got) != 2 {
		t.Fatalf("a runtime rebuild that moved no provider key asked secretsd %q, want nothing beyond the boot's two requests: a human-tier key would cost a tap", got)
	}

	apply(t, live, map[string]any{"provider_keys": map[string]any{"AGENT_ENV": "AGENT_KEY", "OTHER_ENV": "OTHER_KEY"}})
	if got := requests(); len(got) != 6 {
		t.Fatalf("a change of provider_keys asked secretsd %q, want every key it names asked again", got)
	}
}
