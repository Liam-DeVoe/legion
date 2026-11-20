package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
	"github.com/sjawhar/legion/daemon/internal/procgroup"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/stream"
)

// The controller orphan sweep's live proof (scripts/e2e/controller-sweep-kind.sh, whose driver is
// controller_sweep_live_test.go, built with -tags e2e) loads its legion.yaml and builds its Sandbox
// runtime with the functions here. They are untagged so `go test` holds them to the loader's and
// the runtime's current contracts on every run: a change that refuses the proof's file or its
// runtime's options fails this package's tests, not the live proof the next time a kind cluster
// runs it.

// controllerSweepKindProject is the proof's project. Dispatch maps it no repository.
const controllerSweepKindProject = "CSWEEP"

// controllerSweepKindStub is the agent the controller's shim wraps: it sleeps, so the pod stays up
// and nothing dials a model, Dispatch or NATS.
var controllerSweepKindStub = []string{"/bin/sh", "-c", "exec sleep infinity", "controller-sweep-stub"}

// writeControllerSweepKindConfig writes the proof's legion.yaml, and the token files it names,
// under dir: `controller: daemon` on runtime.kubernetes, the store at dsn, the worker stream bound
// at streamHost, the worker image, and the cluster as kubeconfig's kubeContext. The hosts no stub
// dials are documentation placeholders.
func writeControllerSweepKindConfig(t *testing.T, dir, dsn, streamHost, image, kubeconfig, kubeContext string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"envoy-token", "operator-token", "dispatch-token"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("placeholder-"+file+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf(`project: %[1]s
controller: daemon
postgres_dsn: %[2]s
state_dir: %[3]s
bind: %[4]s
daemon_url: http://%[4]s:13370
envoy_url: http://envoy-listener.internal.example:9020
envoy_token_file: ./envoy-token
operator_token_file: ./operator-token
dispatch_url: https://dispatch.internal.example
dispatch_token_file: ./dispatch-token
nats_urls: [nats://nats.internal.example:4222]
worker_boot_timeout_seconds: 300
projects:
  %[1]s: {}
github_apps:
  implement: { app_id: "1", private_key_command: "exit 1" }
  review: { app_id: "2", private_key_command: "exit 1" }
runtime:
  kubernetes:
    namespace: legion
    image: %[5]s
    storage_class: standard
    kubeconfig: %[6]s
    context: %[7]s
    session_store: postgres
    session_dsn_secret: SESSION_DSN
`, controllerSweepKindProject, dsn, filepath.Join(dir, "state"), streamHost, image, kubeconfig, kubeContext)
	path := filepath.Join(dir, "legion.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// controllerSweepKindRuntime builds the Sandbox runtime from cfg as boot does: its cluster client
// (kubeClient), sandboxOptions over the loaded configuration, then boot's own factory
// (sandboxRuntime) on listener and st, which also registers the runtime's launcher acceptor. What
// the proof stands in for: the agent is a sleep stub, the App tokens are placeholders
// (outboxTokens), no repository is mapped, so the controller's gh files name no owner, and GitHub
// is githubAPI, the caller's stand-in (controllerSweepKindGitHub), which the runtime reads the
// shared public leak check and its index of sandbox lines under.
func controllerSweepKindRuntime(ctx context.Context, cfg config.Config, listener *stream.Listener, st *store.Store, githubAPI string, log *slog.Logger) (runtime.Runtime, error) {
	k := *cfg.Runtime.Kubernetes
	project, err := claim.ProjectToken(cfg.Project)
	if err != nil {
		return nil, err
	}
	rc, err := kubeClient(k)
	if err != nil {
		return nil, err
	}
	opts, err := sandboxOptions(cfg, k, project, "placeholder-dispatch-token", lookup(nil), log)
	if err != nil {
		return nil, fmt.Errorf("sandboxOptions: %w", err)
	}
	opts.Agent = controllerSweepKindStub
	repos := func() []ghrepo.Repository { return nil }
	// The proof maps no repository and configures no rename, so the mapping's rule
	// (repositories.serving) answers every provisioning token's repository as it is recorded.
	serving := newRepositories(cfg.Project, ghrepo.Renames{}).serving
	// The proof applies no fact of a pod's volume and no memory guard kill, so the trimmer's
	// reports and the guard's reach holders nothing filled, which drop them
	// (volumeTrimReports.report, memoryGuardKills.killed).
	return sandboxRuntime(rc, opts, repos, serving, ghrepo.Renames{}, appauth.NewInstalled(), appauth.NewLeftOut(), githubAPI, &memoryGuardKills{}, &volumeTrimReports{})(ctx, listener, listener.Addr(), outboxTokens{}, nil, st, noRepositories)
}

// controllerSweepKindReadLease is the read lease the proof's placeholder App tokens mint
// (outboxTokens.ReadToken), which the runtime reads the check and its index with: the one bearer
// the proof's GitHub admits.
const controllerSweepKindReadLease = "outbox-token-read"

// controllerSweepKindGitHub is the GitHub the proof's runtime reads under: a stand-in of the
// check's repository (leakguardtest.ServeReleases) admitting only the read lease, so no request of
// the proof leaves the machine. Its release of the index holds no asset, so the runtime's read of
// the index as it starts is answered GitHub's 404, as before the builder's first publish. Its
// tarball route serves, at every tag of the published shape (shimwire.ValidLeakCheckTag), a check
// that answers --help as the real one does (leakguardtest.AnswersHelp), so the controller's
// launch, which installs the check its worker image pins before its start frame
// (launchers.serveLeakCheck), is served whatever that pin is. The check runs in the pod, whose
// files the driver cannot read, so it counts its runs nowhere (os.DevNull). A request of any other
// route fails the test.
func controllerSweepKindGitHub(t *testing.T) *leakguardtest.Releases {
	t.Helper()
	github := leakguardtest.ServeReleases(t, leakguard.CheckSlug, leakguard.IndexRelease, controllerSweepKindReadLease)
	github.ServeTarball(shimwire.ValidLeakCheckTag, leakguardtest.GitHubTarball(t, map[string]leakguardtest.Member{
		leakguardtest.CheckName: {Body: "#!/bin/sh\n" + leakguardtest.AnswersHelp(os.DevNull), Mode: 0o755},
	}))
	return github
}

// controllerSweepKindCheckServed is the tags github served the check's tarball at, in the order it
// served them: the runtime's reads of the check a launch installs, as the stand-in recorded them.
// It is an error unless github served the tarball once or more, each time to the read lease.
func controllerSweepKindCheckServed(github *leakguardtest.Releases) ([]string, error) {
	served := github.TarballsServed()
	if len(served) == 0 {
		return nil, errors.New("the proof's GitHub served the check's tarball to no read")
	}
	tags := make([]string, 0, len(served))
	for _, read := range served {
		if read.Authorization != "Bearer "+controllerSweepKindReadLease {
			return nil, fmt.Errorf("the proof's GitHub served %s to %q, not to the read lease", read.Path, read.Authorization)
		}
		tags = append(tags, strings.TrimPrefix(read.Path, "/repos/"+leakguard.CheckSlug+"/tarball/"))
	}
	return tags, nil
}

// controllerSweepKindProviders is the providers Secret (sandbox.ProvidersSecretName) of project
// that the proof's cluster holds in the runtime's namespace, as a deployment's operator keeps one.
// Under the proof's session store, postgres, every pod mounts that Secret's session_dsn_secret
// key, and the kubelet starts no pod whose providers Secret is missing. The key holds the store's
// own URL (postgres_dsn), as a database that keeps sessions: the proof's agent is a sleep stub,
// which opens no session.
func controllerSweepKindProviders(cfg config.Config, project string) *corev1.Secret {
	k := cfg.Runtime.Kubernetes
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: sandbox.ProvidersSecretName(project), Namespace: k.Namespace},
		Data:       map[string][]byte{k.SessionStore.DSNSecretKey(): []byte(cfg.PostgresDSN)},
	}
}

// controllerSweepKindSpec is the project controller's launch as the proof makes it: the
// controller's token and role, no issue, tree or repository, the launch secret the file's
// envoy_token_file names, and prompt as its role prompt.
func controllerSweepKindSpec(project, bootToken, prompt string) runtime.SpawnSpec {
	return runtime.SpawnSpec{
		Claim: claim.ControllerToken(project), Project: project, Role: claim.RoleController, Generation: 1, BootToken: bootToken,
		Env: map[string]string{}, Secrets: map[string]string{"ENVOY_TOKEN": "placeholder-envoy-token"},
		Prompt: runtime.PromptParts{RolePromptPaths: []string{prompt}},
	}
}

// The live proof's legion.yaml passes the daemon's loader as a `controller: daemon` deployment, the
// runtime it configures builds through boot's factory and sandbox.New, against an API server
// standing in for the kind cluster and a GitHub standing in for github.com
// (controllerSweepKindGitHub), and its controller launch passes the spawn-spec checks. Every step
// is the one the driver takes before it reaches the cluster. The GitHub stand-in is made first, so
// it closes after the runtime's context ends.
func TestTheControllerSweepKindProofBuildsItsRuntimeFromItsConfiguration(t *testing.T) {
	github := controllerSweepKindGitHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(newIssueSandboxAPI())
	t.Cleanup(func() { cancel(); server.Close() })
	dir := t.TempDir()
	path := writeControllerSweepKindConfig(t, dir, testDSN(t), "192.0.2.10",
		"ghcr.io/cybermonklabs/legion-worker@sha256:"+strings.Repeat("a", 64), writeKubeconfig(t, server.URL, ""), "test")
	cfg, err := config.LoadForValidation(path, nil)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	if cfg.ControllerLaunch != config.ControllerLaunchDaemon {
		t.Fatalf("the proof's file launches the controller as %q, want %q", cfg.ControllerLaunch, config.ControllerLaunchDaemon)
	}
	st, err := store.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	refuse := func(string) (claim.Token, uint64, bool, bool, error) { return "", 0, false, false, nil }
	listener, err := stream.Listen(ctx, "tcp://127.0.0.1:0", refuse, stream.Options{RPCTimeout: cfg.WorkerRPCTimeout, Log: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controllerSweepKindRuntime(ctx, cfg, listener, st, github.URL, quietLogger()); err != nil {
		t.Fatalf("the proof's runtime: %v", err)
	}
	project, err := claim.ProjectToken(cfg.Project)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ValidateSpawnSpec(controllerSweepKindSpec(project, "boot-token", filepath.Join(dir, "role.md")), nil); err != nil {
		t.Fatalf("the proof's controller launch: %v", err)
	}
}

// The proof's GitHub serves the shared public leak check at any tag of the published shape a worker
// image pins, to the read lease the proof's runtime reads it with: the daemon's own reader of the
// check (leakCheckSource), over the placeholder App tokens that runtime is built with, reads it at
// each tag, what it serves installs as a pod's launcher installs the check
// (leakguard.InstallServed), its one --help run naming the shapes the install reads, and the
// stand-in's record (controllerSweepKindCheckServed), which the live proof reads after its launch,
// names each read. The controller's launch waits for that install before its start frame
// (launchers.serveLeakCheck), so a GitHub that served no check would refuse the launch of every
// image that pins one.
func TestTheControllerSweepKindProofsGitHubServesTheCheckAnImagePins(t *testing.T) {
	github := controllerSweepKindGitHub(t)
	source := leakCheckSource(outboxTokens{}, github.URL)
	tags := []string{leakCheckTag, "leak-check-v1"}
	for _, tag := range tags {
		served, err := source.Get(context.Background(), tag)
		if err != nil {
			t.Fatalf("the daemon's read of the check at %s: %v", tag, err)
		}
		install := filepath.Join(t.TempDir(), "leak-check")
		if err := leakguard.InstallServed(context.Background(), leakguard.ServedInstall{Dir: install, Tag: tag, Body: served.Body, Start: procgroup.NoReaper}); err != nil {
			t.Fatalf("the check served at %s does not install: %v", tag, err)
		}
	}
	if served, err := controllerSweepKindCheckServed(github); err != nil || !slices.Equal(served, tags) {
		t.Fatalf("the proof's GitHub served the check at %q (%v), want %q, each to the read lease", served, err, tags)
	}
}

// The providers Secret the live proof makes (controllerSweepKindProviders) holds every key the
// proof's pods mount from it, listed as the runtime lists them (Runtime.providersKeys,
// internal/runtime/sandbox/manifest.go): each provider_keys key, each providers secret, and the
// session store's session_dsn_secret key. The kubelet starts no pod whose providers Secret is
// missing or lacks a key it mounts, so the controller's launch would time out waiting for its
// launcher.
func TestTheControllerSweepKindProofsProvidersSecretHoldsEveryKeyItsPodsMount(t *testing.T) {
	path := writeControllerSweepKindConfig(t, t.TempDir(), "postgres://proof:p@192.0.2.10:5432/postgres", "192.0.2.10",
		"ghcr.io/cybermonklabs/legion-worker@sha256:"+strings.Repeat("a", 64), writeKubeconfig(t, "https://192.0.2.10:6443", ""), "test")
	cfg, err := config.LoadForValidation(path, nil)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	project, err := claim.ProjectToken(cfg.Project)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := sandboxOptions(cfg, *cfg.Runtime.Kubernetes, project, "placeholder-dispatch-token", lookup(nil), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	mounted := slices.Concat(slices.Collect(maps.Values(opts.ProviderKeys)), opts.ProvidersSecrets)
	if key := opts.SessionStore.DSNSecretKey(); key != "" {
		mounted = append(mounted, key)
	}
	slices.Sort(mounted)
	secret := controllerSweepKindProviders(cfg, project)
	if held := slices.Sorted(maps.Keys(secret.Data)); len(mounted) == 0 || !slices.Equal(held, mounted) {
		t.Fatalf("the proof's providers Secret holds %q, and its pods mount %q from it", held, mounted)
	}
	if want := sandbox.ProvidersSecretName(project); secret.Name != want || secret.Namespace != cfg.Runtime.Kubernetes.Namespace {
		t.Fatalf("the proof's providers Secret is %s/%s, want %s/%s", secret.Namespace, secret.Name, cfg.Runtime.Kubernetes.Namespace, want)
	}
}
