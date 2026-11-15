package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/omplaunch"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/tmux"
)

// launchReads is what a resolution reads to launch panes on one harness: the pane's Launcher, the
// log line that names what was resolved, and, for Oh My Pi, the invocation boot's gate runs too.
type launchReads struct {
	launcher   tmux.Launcher
	log        func(*slog.Logger)
	invocation string // Oh My Pi's resolved invocation; "" for every other harness
}

// launchResolver resolves one harness's launch from the configuration and the daemon's environment
// (lookup, and getenv for the OMP invocation), writing nothing.
type launchResolver func(cfg config.Config, lookup func(string) (string, bool), getenv func(string) string) (launchReads, error)

// launchResolvers is each harness's resolution, by the backend it launches (runtime.Backends): at
// boot (readBoot), and at every build of the tmux runtime whose launch keys moved (paneLaunchers).
var launchResolvers = map[runtime.Backend]launchResolver{
	runtime.BackendOmp:    resolveOmp,
	runtime.BackendClaude: resolveClaude,
}

// resolveLaunchers is every launch the configuration's backends need (config.Backends), resolved
// in that order; Oh My Pi's only when boot runs the host's Oh My Pi (hostOMP).
func resolveLaunchers(cfg config.Config, lookup func(string) (string, bool), getenv func(string) string, hostOMP bool) (map[runtime.Backend]launchReads, error) {
	reads := map[runtime.Backend]launchReads{}
	for _, backend := range cfg.Backends() {
		if backend == runtime.BackendOmp && !hostOMP {
			continue
		}
		resolve, ok := launchResolvers[backend]
		if !ok {
			return nil, fmt.Errorf("the daemon has no launch for backend %q under runtime: tmux", backend)
		}
		read, err := resolve(cfg, lookup, getenv)
		if err != nil {
			return nil, err
		}
		reads[backend] = read
	}
	return reads, nil
}

// paneLaunchers are the tmux runtime's launchers as its builds keep them (prepareTmux's
// runtimeFor): each build's are the launches of the configuration that build is given
// (resolveLaunchers), so a change of `omp_invocation`, `omp_launch_prefix` or `worker_backends`
// reaches the next pane the runtime built for it opens, a role moved onto a backend no earlier
// build launched among them. The boot's resolution (readBoot) is the first one held, and a build
// whose launch keys have not moved since the resolution held takes that one again rather than
// reading the host once more: the runtime is built again for every change of a row it holds, a
// rotated credential file among them, and resolving an Oh My Pi that mise installs runs `mise
// where`.
//
// A resolution reads the host, so it can fail where the host holds nothing the configuration now
// names: a mise tool not installed, no `claude` or bun, no built claude-envoy plugin. That is the
// build's error and nothing of it is held. The boot refuses to start on it, as readBoot already
// has; a rebuild keeps the runtime the daemon has, whose panes go on launching as they did, and
// the rebuilder logs the failure and the settings route answers the change failed, as for any
// rebuild that fails (runtimeHolder.rebuild, rebuilder.rebuild). Leaving the daemon with no
// runtime instead would stop every launch over a key no pane running now reads, and the next
// build resolves again whatever the keys are then.
//
// Builds never overlap (providerKeyFiles), so it takes no lock of its own.
type paneLaunchers struct {
	lookup func(string) (string, bool)
	getenv func(string) string
	log    *slog.Logger
	// keys are what launchers was resolved from.
	keys      launchKeys
	launchers map[runtime.Backend]tmux.Launcher
}

// launchKeys are what of a configuration its launches are resolved from: the backends its roles
// run on (config.Config.Backends), and Oh My Pi's invocation and launch prefix (resolveOmp).
// Claude Code's is resolved from the daemon's environment alone (resolveClaude).
type launchKeys struct {
	backends   []runtime.Backend
	invocation string
	prefix     []string
}

func launchKeysOf(cfg config.Config) launchKeys {
	return launchKeys{backends: cfg.Backends(), invocation: cfg.OmpInvocation, prefix: cfg.OmpLaunchPrefix}
}

func (k launchKeys) equal(other launchKeys) bool {
	return k.invocation == other.invocation && slices.Equal(k.backends, other.backends) && slices.Equal(k.prefix, other.prefix)
}

// newPaneLaunchers holds booted, the launches boot resolved for cfg (readBoot), as the resolution
// the next build of the same launch keys takes; lookup and getenv are the daemon's environment
// every later resolution reads, as readBoot read it.
func newPaneLaunchers(cfg config.Config, booted map[runtime.Backend]launchReads, lookup func(string) (string, bool), getenv func(string) string, log *slog.Logger) *paneLaunchers {
	return &paneLaunchers{lookup: lookup, getenv: getenv, log: log, keys: launchKeysOf(cfg), launchers: launchersOf(booted)}
}

// resolve answers cfg's launchers: the ones held when cfg's launch keys are the ones they were
// resolved from, else a resolution of cfg's own, logged as boot logs its own, which it then
// holds.
func (l *paneLaunchers) resolve(cfg config.Config) (map[runtime.Backend]tmux.Launcher, error) {
	keys := launchKeysOf(cfg)
	if keys.equal(l.keys) {
		return l.launchers, nil
	}
	reads, err := resolveLaunchers(cfg, l.lookup, l.getenv, true)
	if err != nil {
		return nil, err
	}
	for _, backend := range keys.backends {
		reads[backend].log(l.log)
	}
	l.keys, l.launchers = keys, launchersOf(reads)
	return l.launchers, nil
}

// launchersOf is each resolved launch's Launcher, by backend.
func launchersOf(reads map[runtime.Backend]launchReads) map[runtime.Backend]tmux.Launcher {
	launchers := make(map[runtime.Backend]tmux.Launcher, len(reads))
	for backend, read := range reads {
		launchers[backend] = read.launcher
	}
	return launchers
}

// invocationEnvironment is o.getenv, or the process's own environment when a test replaced none:
// what the OMP invocation is resolved against (resolveOmp), by boot and by every build of the tmux
// runtime that resolves it again (paneLaunchers).
func (o overrides) invocationEnvironment() func(string) string {
	if o.getenv == nil {
		return os.Getenv
	}
	return o.getenv
}

// resolveOmp is the OMP invocation every pane and the boot gate run, whose one command is `mise
// where <tool>` when the invocation names a mise tool (omplaunch.ResolveInvocation).
func resolveOmp(cfg config.Config, _ func(string) (string, bool), getenv func(string) string) (launchReads, error) {
	invocation, err := omplaunch.ResolveInvocation(cfg.OmpInvocation, getenv)
	if err != nil {
		return launchReads{}, err
	}
	return launchReads{
		launcher:   tmux.NewOmpLauncher(cfg.OmpLaunchPrefix, invocation),
		invocation: invocation,
		log: func(log *slog.Logger) {
			log.Info("legion daemon resolved OMP invocation for boot probes and panes", "invocation", invocation)
		},
	}, nil
}

// claudePluginDirEnv names the claude-envoy plugin's package root a Claude Code pane loads.
const claudePluginDirEnv = "LEGION_CLAUDE_PLUGIN_DIR"

// resolveClaude is how a pane runs Claude Code, for a configuration whose worker_backends puts a role
// on claude: `claude` and the `bun` its MCP server runs on, each by its LEGION_<TOOL>_PATH override
// or on PATH, and the claude-envoy plugin at LEGION_CLAUDE_PLUGIN_DIR, which must hold its built
// channel server and the generated Legion plugin. Nothing in a checkout carries them: unpack an
// `npm pack @sjawhar/claude-envoy` of the published release, or run `bun scripts/build.ts --out
// <dir>` in packages/claude-envoy to build them from any tree. Every gap is named in one refusal.
func resolveClaude(_ config.Config, lookupEnv func(string) (string, bool), _ func(string) string) (launchReads, error) {
	var launch tmux.ClaudeLaunch
	var missing []string
	for tool, into := range map[string]*string{"claude": &launch.Binary, "bun": &launch.Bun} {
		found, err := resolveTool(lookupEnv, tool)
		if err != nil {
			return launchReads{}, err
		}
		if found == "" {
			missing = append(missing, fmt.Sprintf("%s (set %s to an absolute executable path)", tool, toolEnv(tool)))
		}
		*into = found
	}
	dir, _ := lookupEnv(claudePluginDirEnv)
	switch {
	case dir == "":
		missing = append(missing, fmt.Sprintf("the claude-envoy plugin (set %s to its package directory)", claudePluginDirEnv))
	case !filepath.IsAbs(dir):
		return launchReads{}, fmt.Errorf("%s is not an absolute path: %q", claudePluginDirEnv, dir)
	default:
		for _, file := range []string{filepath.Join("dist", "envoy-channel.js"), filepath.Join("legion", ".claude-plugin", "plugin.json")} {
			if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
				return launchReads{}, fmt.Errorf("%s %s holds no %s: build the plugin (unpack an npm pack of the published @sjawhar/claude-envoy, or run bun scripts/build.ts --out <dir> in packages/claude-envoy)", claudePluginDirEnv, dir, file)
			}
		}
		launch.PluginDir = dir
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return launchReads{}, fmt.Errorf("worker_backends runs a role on claude, and boot is missing %s", strings.Join(missing, ", "))
	}
	return launchReads{
		launcher: tmux.NewClaudeLauncher(launch),
		log: func(log *slog.Logger) {
			log.Info("legion daemon resolved Claude Code for the roles worker_backends puts on claude",
				"claude", launch.Binary, "plugin", launch.PluginDir, "bun", launch.Bun)
		},
	}, nil
}
