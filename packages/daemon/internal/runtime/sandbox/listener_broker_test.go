package sandbox

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// starts is how many start commands the tester's launcher has been sent.
func starts(g *rig) int {
	n := 0
	for _, frame := range g.sent(workerToken) {
		if _, ok := frame.(shimwire.LauncherStart); ok {
			n++
		}
	}
	return n
}

// TestNoLaunchSecretIsAnEnvoyBearer: the runtime refuses an ENVOY_TOKEN launch secret, which no
// daemon hands out since the listener takes no shared bearer. The control is the same options with
// no such secret, which configure.
func TestNoLaunchSecretIsAnEnvoyBearer(t *testing.T) {
	for name, tc := range map[string]struct {
		move func(*Options)
		want string
	}{
		"an Envoy bearer": {func(o *Options) {
			o.LaunchSecrets = []string{envoyTokenKey}
		}, "LaunchSecrets names ENVOY_TOKEN: the Envoy listener takes no shared bearer"},
		"control: no bearer": {func(*Options) {}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			opts := goldenOptions()
			tc.move(&opts)
			_, err := configure(opts)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("configure = %v, want it configured", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("configure = %v, want a refusal containing %q", err, tc.want)
			}
		})
	}
}

// TestNoSpecSecretIsAnEnvoyBearer: prepare refuses a launch whose spec names ENVOY_TOKEN among its
// secrets, as it refuses one that names the provisioning token's key: the listener takes no shared
// bearer, so no daemon hands one out, and a launch that carried one would point its role's Envoy
// client at it ahead of the broker token its session trades. The control is the spec with the pane
// NATS nkey seed alone, the launch secret the daemon does hand out, which prepares and carries it.
func TestNoSpecSecretIsAnEnvoyBearer(t *testing.T) {
	r, err := configure(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		secrets map[string]string
		want    string
	}{
		"an Envoy bearer beside the pane seed": {map[string]string{"NATS_NKEY_SEED": testPaneSeed, envoyTokenKey: "envoy-bearer"},
			"secret ENVOY_TOKEN is an Envoy bearer: the Envoy listener takes no shared bearer, so no launch carries one"},
		"control: the pane seed alone": {map[string]string{"NATS_NKEY_SEED": testPaneSeed}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			spec := workerSpec(t)
			spec.Secrets = tc.secrets
			l, err := r.prepare(spec)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("prepare = %v, want the launch prepared", err)
				}
				if got := l.secrets["NATS_NKEY_SEED"]; got != testPaneSeed {
					t.Errorf("the launch carries NATS_NKEY_SEED %q, want the spec's %q", got, testPaneSeed)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("prepare = %v, want a refusal containing %q", err, tc.want)
			}
		})
	}
}

// TestAKeptPodWithNoEnvoyCredentialWaitsForItsRound: a role relaunched into a pod built before
// the exchange identity would start with no identity it can trade, and so could not reach the
// listener at all until the pod's update round replaced the pod. listenerCredential refuses that
// launch before anything of the pod is touched, with runtime.ErrAwaitingPodRound naming the pod,
// which the supervisor waits out charging nothing, with or without dispatch_token_broker. The
// control is the same kept pod with the identity, whose launch starts.
func TestAKeptPodWithNoEnvoyCredentialWaitsForItsRound(t *testing.T) {
	t.Run("a pod built before the identity", func(t *testing.T) {
		g, loc := beforeTheIdentity(t)
		r2 := restartedOver(t, g, loc, func(*Options) {})
		before := starts(g)
		g.clearActions()
		spec := workerSpec(t)
		spec.Generation, spec.BootToken, spec.ResumeSessionFile = 2, "boot-tester-g2", resumeSession
		_, err := r2.Resume(g.ctx, &loc, spec)
		wantAwaitingRound(t, g, loc, err, before,
			"pod "+loc.Sandbox.Name+" carries no exchange identity", "the Envoy listener, which takes no shared bearer", "its update round replaces it")
	})

	t.Run("control: the same pod with the exchange identity", func(t *testing.T) {
		g := newRig(t, nil, withOptions(dispatchBearer))
		loc := g.spawn(workerSpec(t))
		r2 := restartedOver(t, g, loc, func(*Options) {})
		next := resumeTester(t, g, r2, loc)
		if next.Sandbox.PodUID != loc.Sandbox.PodUID {
			t.Fatalf("Resume replaced the pod; this case is about the pod a launch keeps")
		}
		if start := startSent(t, g, 2); start.Files[bootTokenKey] != "boot-tester-g2" {
			t.Fatalf("the launch into the kept pod carries files %v, want its own boot token", slices.Sorted(maps.Keys(start.Files)))
		}
	})
}

// TestAPodWithTheExchangeIdentityStartsWithNoEnvoyBearer: a launch that builds its pod, and one
// that keeps a pod built with the exchange identity, each start with no ENVOY_TOKEN among the start
// command's files and no ENVOY_TOKEN_FILE told to the role, which trades the pod's identity for
// its own broker token instead.
func TestAPodWithTheExchangeIdentityStartsWithNoEnvoyBearer(t *testing.T) {
	g := newRig(t, nil)
	loc := g.spawn(workerSpec(t))
	if found := exchangeTokenVolumes(g.pod(loc.Sandbox.Name).Spec, testExchangeAudience); len(found) != 1 {
		t.Fatalf("the fresh pod projects %+v for the exchange, want its one token", found)
	}
	wantNoEnvoyBearer := func(what string, start shimwire.LauncherStart) {
		t.Helper()
		if _, carried := start.Files["ENVOY_TOKEN"]; carried {
			t.Errorf("%s carries ENVOY_TOKEN among its files", what)
		}
		if pointer, told := startEnv(start)["ENVOY_TOKEN_FILE"]; told {
			t.Errorf("%s tells its role ENVOY_TOKEN_FILE=%s, want none", what, pointer)
		}
	}
	wantNoEnvoyBearer("the launch that built its pod", lastStart(t, g, workerToken))

	r2 := restartedOver(t, g, loc, func(*Options) {})
	spec := workerSpec(t)
	spec.Generation, spec.BootToken, spec.ResumeSessionFile = 2, "boot-tester-g2", resumeSession
	next, err := r2.Resume(g.ctx, &loc, spec)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if next.Sandbox.PodUID != loc.Sandbox.PodUID {
		t.Fatalf("Resume replaced the pod; this case is about the pod a launch keeps")
	}
	wantNoEnvoyBearer("the launch that kept its pod", startSent(t, g, 2))
}
