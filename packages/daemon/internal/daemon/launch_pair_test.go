package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/prompts"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// pairMark is the secret every spec the rig's launch specs build carries: the generation of the
// specs it was built from, which the runtime that takes the launch reads back.
const pairMark = "LAUNCH_PAIR_GENERATION"

// landing is one launch as the runtime that took it saw it: the generation of the specs the
// launch was built from and the generation of the runtime it was spawned on. A launch reads one
// pair when the two are the same.
type landing struct{ specs, runtime string }

// pairedRuntime is one runtime a build of the rig made, of one generation: every spawn it takes
// is recorded as a landing beside the generation of the specs that spawn was built from.
type pairedRuntime struct {
	*fake.Runtime
	generation string
	rig        *launchPairRig
}

func (r *pairedRuntime) Spawn(ctx context.Context, spec runtime.SpawnSpec) (runtime.Locator, error) {
	r.rig.mu.Lock()
	r.rig.landings = append(r.rig.landings, landing{specs: spec.Secrets[pairMark], runtime: r.generation})
	r.rig.mu.Unlock()
	return r.Runtime.Spawn(ctx, spec)
}

// launchPairRig is the two holders a launch reads, built and registered as openSupervision builds
// and registers them: the runtime holder (newRuntimeHolder) and then the launch specs
// (newSpecsHolder), in that order, with the rebuilder every settings change goes through. A launch
// is what supervise.Machine.start does: the spec from the launch specs, then a Spawn on the
// runtime.
//
// The configuration names a Dispatch bearer and an Envoy bearer, each a file of the rig's: the
// first is a credential of the runtime's (runtimeHolder.credentials) and the second of the launch
// specs' (specsHolder.credentials), so rotating both in place moves both holders at one reread.
type launchPairRig struct {
	t       *testing.T
	cfg     config.Config
	rebuild *rebuilder
	runtime runtimeHolding
	specs   *specsHolder
	claim   supervise.Claim

	mu sync.Mutex
	// living is the context of every runtime a build made, the boot's first: a runtime the holder
	// replaced, or built and never swapped in, is stopped, which ends its context.
	living []context.Context
	// specsBuilds counts the launch specs' builds, the boot's first: a build's count is the
	// generation it marks its specs with.
	specsBuilds int
	landings    []landing
	// runtimeBuilding and specsBuilding, when set, run inside every build of their holder after
	// the boot's, and what they answer is that build's error: a test holds a build open there, or
	// refuses it.
	runtimeBuilding func() error
	specsBuilding   func() error
}

func newLaunchPairRig(t *testing.T) *launchPairRig {
	t.Helper()
	dir := t.TempDir()
	composer, err := prompts.New(t.TempDir())
	if err != nil {
		t.Fatalf("compose the shipped prompts: %v", err)
	}
	token, err := claim.NewToken("s1", "S1-2", claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	rig := &launchPairRig{
		t:     t,
		cfg:   config.Config{DispatchTokenFile: filepath.Join(dir, "dispatch-token"), EnvoyTokenFile: filepath.Join(dir, "envoy-token")},
		claim: supervise.Claim{Token: token, Project: "s1", Tree: "S1-1", Issue: "S1-2", Role: claim.RoleImplementer},
	}
	rig.write(rig.cfg.DispatchTokenFile, "dispatch\n")
	rig.write(rig.cfg.EnvoyTokenFile, "envoy\n")
	rig.rebuild = newRebuilder(quietLogger(), config.Settled(rig.cfg))
	buildRuntime := func(config.Config) (runtime.Runtime, context.CancelFunc, error) {
		rig.mu.Lock()
		building, generation := rig.runtimeBuilding, len(rig.living)+1
		rig.mu.Unlock()
		if building != nil && generation > 1 {
			if err := building(); err != nil {
				return nil, nil, err
			}
		}
		living, stop := context.WithCancel(t.Context())
		rig.mu.Lock()
		rig.living = append(rig.living, living)
		rig.mu.Unlock()
		return &pairedRuntime{Runtime: fake.NewRuntime(), generation: strconv.Itoa(generation), rig: rig}, stop, nil
	}
	buildSpecs := func(config.Config) (specs, error) {
		rig.mu.Lock()
		rig.specsBuilds++
		building, generation := rig.specsBuilding, rig.specsBuilds
		rig.mu.Unlock()
		if building != nil && generation > 1 {
			if err := building(); err != nil {
				return specs{}, err
			}
		}
		return specs{stateDir: dir, project: "s1", prompts: composer, repositories: noRepositoriesView,
			secrets: map[string]string{pairMark: strconv.Itoa(generation)}}, nil
	}
	built, stop, err := buildRuntime(rig.cfg)
	if err != nil {
		t.Fatal(err)
	}
	rig.runtime = newRuntimeHolder(built, stop, quietLogger(), buildRuntime)
	settled, err := buildSpecs(rig.cfg)
	if err != nil {
		t.Fatal(err)
	}
	rig.specs = newSpecsHolder(settled, buildSpecs)
	rig.rebuild.register(rig.runtime, rig.specs)
	return rig
}

func (r *launchPairRig) write(path, contents string) {
	r.t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// launch launches the rig's claim as supervise.Machine.start does — the spec from the launch
// specs, then a Spawn on the runtime — and answers the landing the runtime recorded. It reports a
// failure with Errorf, since a test calls it from a goroutine of its own too.
func (r *launchPairRig) launch() landing {
	spec, err := r.specs.SpawnSpec(r.t.Context(), r.claim)
	if err != nil {
		r.t.Errorf("build the launch: %v", err)
		return landing{}
	}
	if _, err := r.runtime.Spawn(r.t.Context(), spec); err != nil {
		r.t.Errorf("spawn the launch: %v", err)
		return landing{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.landings[len(r.landings)-1]
}

// stoppedBuilds says, for every runtime a build made, the boot's first, whether it is stopped.
func (r *launchPairRig) stoppedBuilds() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	stopped := make([]bool, len(r.living))
	for i, living := range r.living {
		stopped[i] = living.Err() != nil
	}
	return stopped
}

// launchBetween runs apply with one launch placed inside the launch specs' build of it, and
// answers that launch's landing: the launch runs once the build has started and the build
// returns once the launch has landed. The build reads the launch secrets and the operator's
// instructions and writes the deployment copy (openSupervision's buildSpecs), so a launch does
// fall inside it.
func (r *launchPairRig) launchBetween(apply func()) landing {
	r.t.Helper()
	building, launched := make(chan struct{}), make(chan struct{})
	r.mu.Lock()
	r.specsBuilding = func() error {
		close(building)
		<-launched
		return nil
	}
	r.mu.Unlock()
	var between landing
	go func() {
		<-building
		between = r.launch()
		close(launched)
	}()
	apply()
	select {
	case <-launched:
	default:
		r.t.Fatal("the apply never built the launch specs, so no launch was placed inside it")
	}
	return between
}

// One apply of a row the launch specs and the runtime both hold changes the pair every launch
// reads, and a launch made while that apply is under way reads one pair: the specs it is built
// from and the runtime it is spawned on are both the ones the daemon had, or both the ones the
// apply built.
//
// `projects` names the launch specs, with the runtime on its row's Also (config.Classes), as do
// `instructions`, `worker_backends`, `nats_nkey_seed_file` and the project's own members; an
// `envoy_url` beside an `envoy_token_file` reaches the two in one request. The rebuilder built
// them one after the other in registration order, the runtime first: it swapped the new runtime
// in, waited out the calls still inside the one it replaced (runtimeDrainBound, as long as that
// runtime's launch bound and a margin), and only then built the specs, so every launch in between
// paired the specs of the daemon's old configuration with the runtime of its new one — a pane told
// the new ENVOY_URL with the old ENVOY_TOKEN.
//
// The launch here is placed inside the launch specs' build, the part of that window this test can
// hold open. A launch whose own SpawnSpec and Spawn straddle the swap itself still pairs across
// it, which no holder can close (rebuilder.rebuildPair).
func TestALaunchBetweenTheTwoRebuildsOfOneApplyReadsOnePair(t *testing.T) {
	rig := newLaunchPairRig(t)

	between := rig.launchBetween(func() {
		rig.rebuild.observe(&rig.cfg, []config.Change{changeOfPath(t, "projects")})
	})

	if between.specs != between.runtime {
		t.Errorf("a launch made while the apply was under way read the specs of generation %s on the runtime of generation %s, want one pair",
			between.specs, between.runtime)
	}
	if after := rig.launch(); after != (landing{specs: "2", runtime: "2"}) {
		t.Errorf("the launch after the apply read the specs of generation %s on the runtime of generation %s, want both the apply's (2)",
			after.specs, after.runtime)
	}
}

// A file watcher's reread that finds both halves of the pair moved — the Dispatch bearer the
// runtime writes where every pane reads it and the Envoy bearer every spec carries, each rotated
// in place — builds the two as one change too: it is the same pair, built by another route.
func TestARereadThatMovesBothHalvesOfThePairReadsOnePair(t *testing.T) {
	rig := newLaunchPairRig(t)
	rig.write(rig.cfg.DispatchTokenFile, "rotated-dispatch\n")
	rig.write(rig.cfg.EnvoyTokenFile, "rotated-envoy\n")

	between := rig.launchBetween(func() { rig.rebuild.rereadCredentials(&rig.cfg) })

	if between.specs != between.runtime {
		t.Errorf("a launch made while the reread was under way read the specs of generation %s on the runtime of generation %s, want one pair",
			between.specs, between.runtime)
	}
	if after := rig.launch(); after != (landing{specs: "2", runtime: "2"}) {
		t.Errorf("the launch after the reread read the specs of generation %s on the runtime of generation %s, want both the reread's (2)",
			after.specs, after.runtime)
	}
}

// A rebuild that fails leaves the old pair serving, whichever half could not be built: a launch
// after it reads the specs and the runtime the daemon had, the runtime built for the refused pair
// is stopped where it was built while the one the daemon is supervising with keeps running, and
// the caller is told that neither holder took the change.
//
// Built one after the other, a launch specs build that failed left the runtime already swapped
// in, and a runtime build that failed left the specs to be swapped in after it: in both the daemon
// went on serving a mixed pair, not for the length of one apply but until the next change of the
// half that failed.
func TestABuildThatFailsLeavesThePairALaunchReadsAsItWas(t *testing.T) {
	refused := errors.New("read the operator's instructions: permission denied")
	for _, tc := range []struct {
		name   string
		refuse func(*launchPairRig)
		// stopped is, for every runtime built, the boot's first, whether it is stopped.
		stopped []bool
	}{
		{name: "the launch specs", refuse: func(r *launchPairRig) { r.specsBuilding = func() error { return refused } }, stopped: []bool{false, true}},
		{name: "the runtime", refuse: func(r *launchPairRig) { r.runtimeBuilding = func() error { return refused } }, stopped: []bool{false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newLaunchPairRig(t)
			tc.refuse(rig)
			applied := []config.Change{changeOfPath(t, "projects")}
			route := rig.rebuild.route(func(context.Context, config.Caller, string, map[string]any) ([]config.Change, []config.Refusal, error) {
				rig.rebuild.observe(&rig.cfg, applied)
				return applied, nil, nil
			})
			_, _, failed, err := route(t.Context(), config.CallerOperator, "someone", map[string]any{"projects": map[string]any{}})
			if err != nil {
				t.Fatalf("apply the change: %v", err)
			}

			if after := rig.launch(); after != (landing{specs: "1", runtime: "1"}) {
				t.Errorf("after %s could not be built a launch read the specs of generation %s on the runtime of generation %s, want the pair the daemon had (1, 1)",
					tc.name, after.specs, after.runtime)
			}
			if got := rig.stoppedBuilds(); !slices.Equal(got, tc.stopped) {
				t.Errorf("the runtimes built, the boot's first, are stopped %v, want %v: the boot's keeps serving and one built for the refused pair is ended",
					got, tc.stopped)
			}
			for _, holder := range []string{config.HolderRuntime, config.HolderLaunchSpecs} {
				if !strings.Contains(failed["projects"], holder+": ") {
					t.Errorf("the route was told %q for projects, want the %s named: neither half of the pair took the change", failed["projects"], holder)
				}
			}
			if !strings.Contains(failed["projects"], refused.Error()) {
				t.Errorf("the route was told %q for projects, want the build's own error %q", failed["projects"], refused)
			}
		})
	}
}
