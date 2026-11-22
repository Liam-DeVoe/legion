package sandbox

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// secondImplementer is the agent of a plan's second implementer step on the rig's issue: a name
// that is no role of the runtime.
var secondImplementer = claim.Token("legion-legion-legion-208-implementer-2")

// withPlanAgents is a store whose plan in force of the rig's issue names agents, each with the role
// of its first step (Store.PlanAgents).
func withPlanAgents(agents map[claim.Agent]claim.Role) rigOption {
	return func(_ *rig, o *Options) { o.Store.(*fakeStore).plan(testTree, agents) }
}

// An issue pod has a slot for each role of the runtime and for each agent name of the issue's
// plan that no role has: the first launch builds `--role implementer-2` with that slot's launcher,
// gh, private and state volumes and its Secret, which holds the App of the agent's first step, and a
// hello of that slot is taken as the claim `…-implementer-2`. A plan whose names are all roles
// builds the pod a plan of no agents builds, byte for byte.
func TestAnIssuePodHasASlotForEachAgentNameOfItsPlan(t *testing.T) {
	g := newRig(t, nil, withPlanAgents(map[claim.Agent]claim.Role{
		"implementer": claim.RoleImplementer, "reviewer": claim.RoleReviewer, "implementer-2": claim.RoleImplementer,
	}))
	loc := g.spawn(testSpec(t, deploymentImplementer, claim.RoleImplementer, testTree))
	name := loc.Sandbox.Name
	pod := g.pod(name)
	if pod == nil {
		t.Fatalf("the launch built no pod %s", name)
	}
	command := containerNamed(t, pod.Spec, runtime.IssuePodContainer).Command
	if got, want := builtRoles(pod.Spec), append(slices.Clone(claim.Roles), "implementer-2"); !slices.Equal(got, want) {
		t.Errorf("the pod's slots = %v, want %v (the launcher's command %q)", got, want, command)
	}
	for _, volume := range []string{roleVolume("launcher", "implementer-2"), roleVolume("private", "implementer-2"),
		roleVolume(stateVolume, "implementer-2"), roleVolume(ghVolume, "implementer-2")} {
		if !slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == volume }) {
			t.Errorf("the pod has no volume %s", volume)
		}
	}
	secret := g.secret(roleSecretName(name, "implementer-2"))
	if secret == nil {
		t.Fatalf("the Sandbox has no Secret %s", roleSecretName(name, "implementer-2"))
	}
	if !slices.ContainsFunc(secret.OwnerReferences, func(o metav1.OwnerReference) bool { return o.UID == g.sandbox(name).UID }) {
		t.Errorf("the Secret %s is not the Sandbox's", secret.Name)
	}
	owner := string(secret.Data[GitHubDefaultKey])
	if token, err := ghconfig.TokenFromHosts(secret.Data[GitHubHostsKey(owner)]); err != nil || token != staticCredentialToken(claim.RoleImplementer) {
		t.Errorf("the implementer-2 Secret's %s hosts.yml holds %q (%v), want the implement App's %q", owner, token, err, staticCredentialToken(claim.RoleImplementer))
	}
	hello := shimwire.LauncherHello{Token: string(secret.Data[LauncherTokenFile]), Sandbox: name, Role: "implementer-2", PodUID: loc.Sandbox.PodUID, LauncherID: "l-implementer-2"}
	if handler, _, reason := g.r.LauncherResolver()(hello); handler == nil {
		t.Errorf("the launcher of the slot implementer-2 was refused: %s", reason)
	}
	g.r.launchers.mu.Lock()
	_, accepted := g.r.launchers.sessions[secondImplementer]
	g.r.launchers.mu.Unlock()
	if !accepted {
		t.Errorf("the launcher of the slot implementer-2 is not held as %s", secondImplementer)
	}

	plain := newRig(t, nil)
	plainPod := plain.pod(plain.spawn(testSpec(t, deploymentImplementer, claim.RoleImplementer, testTree)).Sandbox.Name)
	roles := newRig(t, nil, withPlanAgents(map[claim.Agent]claim.Role{"implementer": claim.RoleImplementer, "reviewer": claim.RoleReviewer}))
	rolesPod := roles.pod(roles.spawn(testSpec(t, deploymentImplementer, claim.RoleImplementer, testTree)).Sandbox.Name)
	if !reflect.DeepEqual(plainPod.Spec, rolesPod.Spec) {
		t.Errorf("a plan whose agents are all roles built the pod\n%+v\nwant the pod a plan of no agents builds\n%+v", rolesPod.Spec, plainPod.Spec)
	}
}

// The gh refresh passes over the slot of an agent of the plan in which no stored claim runs yet: its
// name is no role's, so no App names it, and nothing in it reads its files. Once a claim of that
// agent is stored, the refresh renders its slot for that claim's role as it does every other slot.
func TestTheRefreshOfAPlanSlotWaitsForAClaimOfItsAgent(t *testing.T) {
	g := newRig(t, nil, withPlanAgents(map[claim.Agent]claim.Role{"implementer-2": claim.RoleImplementer}))
	loc := g.spawn(testSpec(t, deploymentImplementer, claim.RoleImplementer, testTree))
	slot := roleSecretName(loc.Sandbox.Name, "implementer-2")
	readSecrets := func() map[string]bool {
		read := map[string]bool{}
		for _, a := range g.requests() {
			if a.verb == "get" && a.resource == "secrets" {
				read[a.name] = true
			}
		}
		return read
	}
	g.clearActions()
	g.r.refreshGitHubCredentials(g.ctx)
	read := readSecrets()
	if !read[roleSecretName(loc.Sandbox.Name, claim.RoleImplementer)] {
		t.Fatalf("the refresh read the Secrets %v, want the implementer's among them", read)
	}
	if read[slot] {
		t.Errorf("the refresh read %s, want no read of the slot of an agent no stored claim is of", slot)
	}
	g.store.mu.Lock()
	g.store.claims = append(g.store.claims, supervise.Claim{Token: secondImplementer, Issue: testTree, Role: claim.RoleImplementer, Generation: 1})
	g.store.mu.Unlock()
	g.clearActions()
	g.r.refreshGitHubCredentials(g.ctx)
	if !readSecrets()[slot] {
		t.Errorf("the refresh after a claim of implementer-2 was stored read %v, want %s among them", readSecrets(), slot)
	}
}

// The launcher credential a runtime holds for the slot of an agent of the plan goes with its
// Sandbox, as each role's does, and the Secret of another Sandbox whose name begins with the
// forgotten one's stays held.
func TestTheLauncherCredentialOfAPlanSlotIsForgottenWithItsSandbox(t *testing.T) {
	r, err := configure(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	name := SandboxName(secondImplementer)
	s := &sandbox{ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-sandbox"}}
	other := &sandbox{ObjectMeta: metav1.ObjectMeta{Name: name + "1", UID: "uid-other"}}
	held := map[*sandbox]*corev1.Secret{
		s:     {ObjectMeta: metav1.ObjectMeta{Name: roleSecretName(name, "implementer-2")}, Data: map[string][]byte{LauncherTokenFile: []byte("token")}},
		other: {ObjectMeta: metav1.ObjectMeta{Name: roleSecretName(other.Name, claim.RoleTester)}, Data: map[string][]byte{LauncherTokenFile: []byte("other")}},
	}
	for sandbox, secret := range held {
		r.cacheLauncherCredential(sandbox, secret)
	}
	r.forgetLauncherCredentials(name)
	if _, kept, _ := r.launcherAuth.lookup(held[s].Name, s.UID); kept {
		t.Errorf("the implementer-2 launcher credential is still held after its Sandbox %s was forgotten", name)
	}
	if _, kept, _ := r.launcherAuth.lookup(held[other].Name, other.UID); !kept {
		t.Errorf("the credential of %s was dropped with the Sandbox %s", held[other].Name, name)
	}
}

// The slot of a claim's process is its agent's name, which can be no role (`implementer-2`), and
// the controller's for the controller's token. Such a claim's locator is an issue pod's like any
// other claim of the issue, and its observation reads its own slot: the address record of its
// generation is under its name, never under a role read off the token, so an address that moved
// since that generation started is observed.
func TestALocatorOfANameThatIsNoRoleIsAnIssuePodsAndIsObserved(t *testing.T) {
	issue, _, ok := workerToken.Cut()
	if !ok {
		t.Fatalf("Cut(%s) names no issue", workerToken)
	}
	agent := claim.Token(issue + "-implementer-2")
	for token, want := range map[claim.Token]claim.Role{
		agent:           "implementer-2",
		workerToken:     claim.RoleTester,
		controllerToken: claim.RoleController,
	} {
		if got := slotOf(token); got != want {
			t.Errorf("slotOf(%s) = %q, want %q", token, got, want)
		}
	}
	if _, issuePod := podKindFor(slotOf(agent)).(issuePod); !issuePod {
		t.Errorf("podKindFor(slotOf(%s)) = %T, want the issue pod", agent, podKindFor(slotOf(agent)))
	}
	loc := sandboxLocator(agent, recorded)
	if err := loc.Validate(); err != nil {
		t.Fatalf("validate the locator of %s: %v", agent, err)
	}

	name := SandboxName(agent)
	labels := claimLabels(claim.RoleImplementer)
	opts := testOptions()
	r, err := configure(opts)
	if err != nil {
		t.Fatal(err)
	}
	containers := podOf(t, r, workerSpec(t)).Containers
	moved := testOptions()
	moved.DaemonURL = movedDaemonURL
	earlier, err := configure(moved)
	if err != nil {
		t.Fatal(err)
	}
	// recordOf is slot's record of generation 1 as a runtime under the moved daemon address wrote
	// it when that generation started.
	recordOf := func(slot claim.Role) string {
		record, err := earlier.recordFor(slot, 1, earlier.enrollsIn(nil, slot))
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	running := corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: workerContainer, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}}
	objectsWith := func(records map[string]string) []k8sruntime.Object {
		s := sandboxObject(t, name, sandboxUID, modeRunning, labels)
		s.SetAnnotations(records)
		pod := podObject(name, recorded, sandboxUID, labels, running)
		pod.Spec.Containers = containers
		return []k8sruntime.Object{s, pod}
	}
	for _, tc := range []struct {
		row     string
		objects []k8sruntime.Object
		want    runtime.ObservationKind
		detail  string
	}{
		{
			row:     "the record under its own slot names the moved address",
			objects: objectsWith(map[string]string{addressesAnnotation("implementer-2"): recordOf("implementer-2")}),
			want:    runtime.StaleAddress,
			detail:  movedAddress{"LEGION_DAEMON_URL", movedDaemonURL, opts.DaemonURL}.String(),
		},
		{
			row:     "another slot's record is not its own",
			objects: objectsWith(map[string]string{addressesAnnotation(claim.RoleTester): recordOf(claim.RoleTester)}),
			want:    runtime.Alive,
			detail:  "runs generation 1",
		},
	} {
		t.Run(tc.row, func(t *testing.T) {
			g := newRig(t, tc.objects, withoutController())
			g.reportLauncher(agent, shimwire.LauncherState{Child: &shimwire.LauncherChild{Generation: 1, PID: 42}})
			obs, err := g.r.Probe(g.ctx, loc)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if obs.Kind != tc.want {
				t.Fatalf("kind %s, want %s (detail: %s)", obs.Kind, tc.want, obs.Detail)
			}
			if !strings.Contains(obs.Detail, tc.detail) {
				t.Errorf("detail %q lacks %q", obs.Detail, tc.detail)
			}
		})
	}
}
