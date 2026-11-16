package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/runtime/shellprefix"
	"github.com/sjawhar/legion/daemon/internal/runtime/tmux"
	"github.com/sjawhar/legion/daemon/internal/settings"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/stream"
)

// tmuxLaunchRig is a tmux daemon's runtime as its boot holds it, short of the daemon around it,
// as sandboxRebuildRig is a Kubernetes daemon's: the settings holder over a legion.yaml
// (liveSettings) with its route arm (internal/settings' Keeper.Route), the one rebuild observer
// on it (rebuilder.observe, and rebuilder.route in front of the route arm, as run wires both),
// and the runtime holder built and registered as openSupervision builds and registers it, over
// the runtime build prepare leaves behind (p.runtimeFor).
//
// The host is the rig's own, so boot and every build resolve what a host with each harness
// installed would: a mise whose `mise where <tool>` answers a tool directory of the rig's holding
// bin/omp (and refuses a tool it does not hold, as mise refuses one that is not installed), and a
// Claude Code, a bun and a built claude-envoy plugin. Every build is handed to the rig
// (overrides.buildTmux), which keeps the Options it read, runs the runtime's construction on them
// (tmux.New, whose refusals are that build's own) and serves that build from a fake runtime, so
// no tmux is run: a test reads the launchers of the runtime the holder serves from now, and with
// them the command a pane that runtime opens would run (tmux.Launcher.Launch, which is what a
// tmux spawn splices in after the shim's `--`), and the provider-key directory its panes are told.
type tmuxLaunchRig struct {
	t       *testing.T
	live    *config.Live
	route   func(map[string]any) ([]config.Change, map[string]string)
	holder  runtimeHolding
	logs    *syncBuffer
	tools   string
	bin     string
	claude  string
	mu      sync.Mutex
	builds  int
	state   string
	legion  string
	project string
	// reread is the file watcher's settle after a wake: every credential holder reads its files
	// again (rebuilder.rereadCredentials).
	reread func()
}

// launchedTmux is one runtime a build of the rig made: the fake, beside the Options that build
// read from its configuration.
type launchedTmux struct {
	*fake.Runtime
	opts tmux.Options
}

// newTmuxLaunchRig boots a tmux daemon's runtime on liveSettings' file with lines added to it.
func newTmuxLaunchRig(t *testing.T, lines ...string) *tmuxLaunchRig {
	t.Helper()
	host := t.TempDir()
	rig := &tmuxLaunchRig{t: t, tools: filepath.Join(host, "tools"), bin: filepath.Join(host, "bin"), logs: &syncBuffer{}, legion: "/opt/legion/bin/legion"}
	for _, tool := range []string{"omp-a", "omp-b"} {
		writeExecutable(t, filepath.Join(rig.tools, tool, "bin", "omp"), "#!/bin/sh\n")
	}
	mise := filepath.Join(host, "bin", "mise")
	writeExecutable(t, mise, fmt.Sprintf("#!/bin/sh\n[ \"$1\" = where ] || exit 2\n"+
		"[ -d %[1]s/\"$2\" ] || { echo \"mise: $2 is not installed\" >&2; exit 1; }\necho %[1]s/\"$2\"\n",
		shellprefix.Word(rig.tools)))
	rig.claude = filepath.Join(host, "bin", "claude")
	writeExecutable(t, rig.claude, "#!/bin/sh\n")
	bun := filepath.Join(host, "bin", "bun")
	writeExecutable(t, bun, "#!/bin/sh\n")
	// The tmux the construction finds on PATH; nothing runs it.
	writeExecutable(t, filepath.Join(rig.bin, "tmux"), "#!/bin/sh\nexit 1\n")
	plugin := filepath.Join(host, "claude-envoy")
	for _, file := range []string{filepath.Join("dist", "envoy-channel.js"), filepath.Join("legion", ".claude-plugin", "plugin.json")} {
		writeExecutable(t, filepath.Join(plugin, file), "")
	}

	rig.live = liveSettings(t, lines...)
	logger := slog.New(slog.NewJSONHandler(rig.logs, nil))
	rebuild := newRebuilder(logger, rig.live)
	rig.live.OnChange(rebuild.observe)
	routed := rebuild.route(settings.New(rig.live, nil, logger).Route)
	rig.reread = func() { rig.live.WhileSettled(rebuild.rereadCredentials) }
	rig.route = func(changes map[string]any) ([]config.Change, map[string]string) {
		t.Helper()
		applied, refused, failed, err := routed(t.Context(), config.CallerOperator, "someone", changes)
		if err != nil || len(refused) != 0 {
			t.Fatalf("the route applied %v: %v, refused %+v; want every path applied", changes, err, refused)
		}
		return applied, failed
	}

	cfg := *rig.live.Snapshot()
	project, err := claim.ProjectToken(cfg.Project)
	if err != nil {
		t.Fatal(err)
	}
	rig.state, rig.project = cfg.StateDir, project
	o := overrides{
		// What the claude-envoy launch reads (resolveClaude) is the daemon's environment; the OMP
		// invocation is resolved against getenv (resolveOmp), which names the rig's mise and no
		// LEGION_OMP_PATH, so omp_invocation is what decides the Oh My Pi a pane runs.
		environ: []string{"PATH=/usr/bin:/bin", "LEGION_CLAUDE_PATH=" + rig.claude, "LEGION_BUN_PATH=" + bun, claudePluginDirEnv + "=" + plugin},
		getenv: func(name string) string {
			if name == "LEGION_MISE_PATH" {
				return mise
			}
			return ""
		},
		buildTmux: rig.record,
	}
	p, err := prepare(cfg, logger, o)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// openSupervision's buildRuntime: the factory for the configuration it is given, over one
	// stream address, on a context of the runtime's own.
	build := func(c config.Config) (runtime.Runtime, context.CancelFunc, error) {
		factory, err := p.runtimeFor(c)
		if err != nil {
			return nil, nil, err
		}
		living, stop := context.WithCancel(t.Context())
		next, err := factory(living, nil, p.stream, nil, nil, nil, noRepositories)
		if err != nil {
			stop()
			return nil, nil, err
		}
		return next, stop, nil
	}
	built, stop, err := build(cfg)
	if err != nil {
		t.Fatalf("build the boot's runtime: %v", err)
	}
	rig.holder = newRuntimeHolder(built, stop, logger, build)
	rebuild.register(rig.holder)
	return rig
}

// writeExecutable writes contents to path, an executable file, making its directory.
func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

// record is the rig's stand-in for the tmux runtime's construction (overrides.buildTmux): it
// keeps the Options the build read, constructs the runtime from them as tmuxRuntime does
// (tmux.New) — on the stream address the factory is handed, a connection directory, and a PATH
// holding the rig's tmux — so a build the construction refuses fails here as it fails on a host,
// and serves a build it accepts from a fake runtime.
func (r *tmuxLaunchRig) record(opts tmux.Options) runtimeFactory {
	return func(_ context.Context, _ *stream.Listener, address string, _ appauth.Tokens,
		_ func(context.Context, string) (ghrepo.Repository, string, error), _ *store.Store, _ func() []ghrepo.Repository) (runtime.Runtime, error) {
		constructed := opts
		constructed.StreamAddress, constructed.Conns, constructed.Environ = address, fake.NewConns(), []string{"PATH=" + r.bin}
		constructed.Executable = func() (string, error) { return r.legion, nil }
		if _, err := tmux.New(constructed); err != nil {
			return nil, err
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.builds++
		return &launchedTmux{Runtime: fake.NewRuntime(), opts: opts}, nil
	}
}

// built is how many times the runtime has been built, the boot's build among them.
func (r *tmuxLaunchRig) built() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.builds
}

// current is the runtime the holder serves every call from now.
func (r *tmuxLaunchRig) current() *launchedTmux {
	r.t.Helper()
	served, ok := r.holder.(heldRuntime).get().(*launchedTmux)
	if !ok {
		r.t.Fatalf("the holder serves %T, not a runtime the rig built", r.holder.(heldRuntime).get())
	}
	return served
}

// omp is the Oh My Pi `mise where tool` names, as the invocation names it (real path, quoted for
// the pane's shell).
func (r *tmuxLaunchRig) omp(tool string) string {
	r.t.Helper()
	real, err := filepath.EvalSymlinks(filepath.Join(r.tools, tool, "bin", "omp"))
	if err != nil {
		r.t.Fatal(err)
	}
	return shellprefix.Word(real)
}

// spec is a launch of role on the backend the settled configuration puts it on, as the launch
// specs build one (config.Config.BackendFor).
func (r *tmuxLaunchRig) spec(role claim.Role) runtime.SpawnSpec {
	r.t.Helper()
	token, err := claim.NewToken(r.project, "LEGION-7", role)
	if err != nil {
		r.t.Fatal(err)
	}
	return runtime.SpawnSpec{
		Claim: token, Tree: "LEGION-7", Issue: "LEGION-7", Role: role, Backend: r.live.Snapshot().BackendFor(role),
		Prompt: runtime.PromptParts{RolePromptPaths: []string{filepath.Join(r.state, "prompts", string(role)+".md")}},
	}
}

// paneCommand is the command a pane of spec, opened by the runtime the holder serves now, runs
// after its shim's `--`: the launcher that runtime holds for the spec's backend, launched for it,
// exactly as a tmux spawn finds and launches it — or that spawn's refusal of a backend the
// runtime holds no launcher for (tmux's launch).
func (r *tmuxLaunchRig) paneCommand(spec runtime.SpawnSpec) (string, error) {
	r.t.Helper()
	served := r.current()
	backend := runtime.Of(spec.Backend)
	launcher, ok := served.opts.Launchers[backend]
	if !ok {
		return "", fmt.Errorf("spawn %s: its role runs on %s, and the daemon resolved no %s launch (worker_backends)",
			spec.Claim, backend, runtime.Backends[backend].Name)
	}
	inner, _, err := launcher.Launch(spec, tmux.Host{StateDir: served.opts.StateDir, Legion: r.legion})
	return inner, err
}

// `omp_invocation` is what every Oh My Pi pane runs, through the tmux runtime's launcher
// (tmux.NewOmpLauncher, which a spawn splices in after the shim's `--`): the route answers a
// change of it next-launch and names the runtime, and the runtime built for it resolves the
// invocation again from the configuration that build is handed (paneLaunchers), so a pane opened
// after the change runs the Oh My Pi the new invocation names. Resolved once at boot into the
// launchers every build was handed, each pane opened after the change ran the boot's.
func TestAChangedOmpInvocationReachesTheNextPanesCommand(t *testing.T) {
	rig := newTmuxLaunchRig(t, `omp_invocation: "mise x omp-a -- omp"`)
	booted, err := rig.paneCommand(rig.spec(claim.RoleImplementer))
	if err != nil {
		t.Fatalf("a pane of the boot's runtime: %v", err)
	}
	if want := " x omp-a -- " + rig.omp("omp-a") + " "; !strings.Contains(booted, want) {
		t.Fatalf("a pane of the boot's runtime runs %q, want the Oh My Pi omp_invocation names (%q)", booted, want)
	}

	applied, failed := rig.route(map[string]any{"omp_invocation": "mise x omp-b -- omp"})

	change, held := changeOf(applied, "omp_invocation")
	if !held || change.Outcome != config.OutcomeNextLaunch || change.Holder != config.HolderRuntime {
		t.Fatalf("the route applied %+v, want omp_invocation answered next-launch naming the runtime", applied)
	}
	if len(failed) != 0 {
		t.Fatalf("the route was told the rebuild failed: %v", failed)
	}
	if builds := rig.built(); builds != 2 {
		t.Fatalf("the runtime was built %d times, want the boot's and the change's", builds)
	}
	next, err := rig.paneCommand(rig.spec(claim.RoleImplementer))
	if err != nil {
		t.Fatalf("a pane of the runtime built for the change: %v", err)
	}
	if want := " x omp-b -- " + rig.omp("omp-b") + " "; !strings.Contains(next, want) {
		t.Fatalf("the pane opened after the change runs %q, want the Oh My Pi the new omp_invocation names (%q)", next, want)
	}
	if strings.Contains(next, "omp-a") {
		t.Fatalf("the pane opened after the change runs %q, which still names the boot's omp-a", next)
	}
}

// `worker_backends` is read by two things: the launch specs, which carry each role's backend into
// its spawn spec, and, under tmux, the runtime, whose launcher for that backend is what a pane of
// the role runs — a tmux spawn refuses a spec whose backend it holds no launcher for. So a change
// of it builds both again (config.Classes' row, Also), and the runtime built for it resolves a
// launcher for every backend the configuration now puts a role on (paneLaunchers). Before, a role
// moved onto Claude Code under a daemon that booted with no role on it was refused at every
// launch: the specs said claude and the runtime, built from the boot's launchers and not built
// again at all, held Oh My Pi's alone.
func TestARoleMovedOntoABackendTheBootHadNoLauncherForIsSpawnedUnderTheRuntimeBuiltAfterTheChange(t *testing.T) {
	rig := newTmuxLaunchRig(t, `omp_invocation: "mise x omp-a -- omp"`)
	if _, held := rig.current().opts.Launchers[runtime.BackendClaude]; held {
		t.Fatal("the boot resolved a Claude Code launcher with no role on Claude Code: this proves nothing")
	}

	applied, failed := rig.route(map[string]any{"worker_backends": map[string]any{"implementer": "claude"}})

	change, held := changeOf(applied, "worker_backends")
	if !held || change.Outcome != config.OutcomeNextLaunch {
		t.Fatalf("the route applied %+v, want worker_backends answered next-launch", applied)
	}
	if len(failed) != 0 {
		t.Fatalf("the route was told the rebuild failed: %v", failed)
	}
	spec := rig.spec(claim.RoleImplementer)
	if spec.Backend != runtime.BackendClaude {
		t.Fatalf("the implementer's launch runs on %q after the change, want claude", spec.Backend)
	}
	command, err := rig.paneCommand(spec)
	if err != nil {
		t.Fatalf("the implementer's launch under the runtime the daemon serves after the change: %v", err)
	}
	if !strings.Contains(command, " claude-bridge -- ") || !strings.Contains(command, shellprefix.Word(rig.claude)) {
		t.Fatalf("the implementer's pane runs %q, want the claude-bridge over the Claude Code the host holds (%s)", command, rig.claude)
	}
	// The architect stays on Oh My Pi, on the invocation the boot resolved and nothing moved.
	if architect, err := rig.paneCommand(rig.spec(claim.RoleArchitect)); err != nil || !strings.Contains(architect, rig.omp("omp-a")) {
		t.Fatalf("the architect's pane after the change runs %q (%v), want the boot's Oh My Pi", architect, err)
	}
}

// A launcher is resolved from the host — `mise where` for an Oh My Pi installed by mise, the
// claude and bun binaries and the built plugin for Claude Code — so a build can be handed a
// configuration this host cannot launch. The boot refuses to start on that. A live rebuild must
// not leave the daemon without a runtime, so it fails as every rebuild fails: the daemon keeps
// the runtime it has, whose panes go on launching as they did, the rebuilder logs the failure
// naming the runtime, and the route answers the change failed with the resolver's own reason.
// Nothing of the failed resolution is kept: the next change this host can resolve is resolved,
// and reaches the next pane.
func TestALiveChangeThisHostCannotLaunchKeepsTheRuntimeAndIsAnsweredFailed(t *testing.T) {
	rig := newTmuxLaunchRig(t, `omp_invocation: "mise x omp-a -- omp"`)
	booted := rig.current()

	applied, failed := rig.route(map[string]any{"omp_invocation": "mise x omp-missing -- omp"})

	if _, held := changeOf(applied, "omp_invocation"); !held {
		t.Fatalf("the route applied %+v, want the change of omp_invocation applied", applied)
	}
	if why := failed["omp_invocation"]; !strings.HasPrefix(why, config.HolderRuntime+": mise could not resolve OMP tool omp-missing") {
		t.Fatalf("the route was told %v, want omp_invocation answered failed with the runtime's resolution of omp-missing", failed)
	}
	if !strings.Contains(rig.logs.String(), `"msg":"legion settings: rebuilding a holder failed; the daemon keeps the one it has","holder":"runtime"`) {
		t.Fatalf("the failed rebuild was not logged naming the runtime:\n%s", rig.logs)
	}
	if rig.current() != booted {
		t.Fatal("the holder serves another runtime after a build that failed")
	}
	if command, err := rig.paneCommand(rig.spec(claim.RoleImplementer)); err != nil || !strings.Contains(command, rig.omp("omp-a")) {
		t.Fatalf("a pane opened after the failed change runs %q (%v), want the Oh My Pi of the runtime the daemon kept", command, err)
	}

	_, failed = rig.route(map[string]any{"omp_invocation": "mise x omp-b -- omp"})
	if len(failed) != 0 {
		t.Fatalf("the route was told %v for a change this host can launch", failed)
	}
	if command, err := rig.paneCommand(rig.spec(claim.RoleImplementer)); err != nil || !strings.Contains(command, rig.omp("omp-b")) {
		t.Fatalf("a pane opened after the next change runs %q (%v), want the Oh My Pi it names", command, err)
	}
}
