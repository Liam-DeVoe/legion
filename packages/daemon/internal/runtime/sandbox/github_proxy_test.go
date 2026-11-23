package sandbox

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// proxiedSecrets is the broker enrollment of the golden runtime with the tester's GitHub behind
// the broker's proxy, under one secret, and one configured rename.
func proxiedSecrets() *AgentSecrets {
	return &AgentSecrets{
		URL: "https://secrets.internal.example", Audience: "agent-secrets", TokenExpiry: time.Hour,
		GitHub:        map[claim.Role][]string{claim.RoleTester: {"LEGION_REVIEW_APP"}},
		GitHubRenames: []ghrepo.Rename{ghrepo.MustRename("sjawhar/legion", "cybermonklabs/legion")},
	}
}

// proxiedRuntime is the golden runtime with proxiedSecrets.
func proxiedRuntime(t *testing.T) *Runtime {
	t.Helper()
	r, err := configure(goldenOptions())
	if err != nil {
		t.Fatal(err)
	}
	r.agentSecrets = proxiedSecrets()
	return r
}

// forwarderFlags is the forwarder's flags a start command writes into the generation's
// directory, and whether the shim is told to run them.
func forwarderFlags(t *testing.T, r *Runtime, l launch) ([]string, bool) {
	t.Helper()
	start := launcherCommand(l, r)
	told := slices.Contains(start.Argv, "--github-forwarder")
	raw, written := start.Files[gitHubForwarderFile]
	if !written {
		if told {
			t.Fatalf("the shim is told --github-forwarder, but the start writes no %s", gitHubForwarderFile)
		}
		return nil, false
	}
	at := slices.Index(start.Argv, "--github-forwarder")
	if at < 0 || start.Argv[at+1] != generationDir(l.spec.Generation)+"/"+gitHubForwarderFile || at > slices.Index(start.Argv, "--") {
		t.Fatalf("the shim's argv %v does not name %s before --", start.Argv, gitHubForwarderFile)
	}
	var flags []string
	if err := json.Unmarshal([]byte(raw), &flags); err != nil {
		t.Fatalf("%s holds %q, no JSON list: %v", gitHubForwarderFile, raw, err)
	}
	return flags, true
}

// flagValues is every value of name in flags.
func flagValues(flags []string, name string) []string {
	var values []string
	for i := 0; i+1 < len(flags); i++ {
		if flags[i] == name {
			values = append(values, flags[i+1])
		}
	}
	return values
}

// A role the deployment's agent_secrets.github names reaches GitHub through its forwarder: its
// shim is told to run `legion forwarder` with the role's secret names, a reason naming the issue,
// a loopback port of the role's own, the gh bundle under the generation's private directory, the
// issue's owner as the default and the configured renames; its HTTPS_PROXY and https_proxy name
// that port; its gh variables name that bundle; and its gh volume projects nothing of its Secret.
// A role of the same pod the map does not name is launched exactly as before.
//
// The named revert: drop the proxy decision from the launch, and the tester's start names no
// forwarder.
func TestARoleBehindTheProxyIsStartedWithItsForwarder(t *testing.T) {
	r := proxiedRuntime(t)
	spec := workerSpec(t)
	l := launchOf(t, r, spec)
	flags, told := forwarderFlags(t, r, l)
	if !told {
		t.Fatal("the tester's start runs no forwarder")
	}
	dir := generationDir(spec.Generation) + "/gh"
	listen := "127.0.0.1:47804"
	for name, want := range map[string][]string{
		"--role": {"tester"}, "--listen": {listen}, "--secret": {"LEGION_REVIEW_APP"}, "--gh-dir": {dir},
		"--default-owner": {"sjawhar"}, "--rename": {"sjawhar/legion=cybermonklabs/legion"},
	} {
		if got := flagValues(flags, name); !slices.Equal(got, want) {
			t.Errorf("the forwarder's %s = %v, want %v", name, got, want)
		}
	}
	if reason := flagValues(flags, "--reason"); len(reason) != 1 || !strings.Contains(reason[0], spec.Issue) || !strings.Contains(reason[0], "tester") {
		t.Errorf("the forwarder's --reason = %v, want one naming the tester and issue %s", reason, spec.Issue)
	}
	env := envOf(workerOf(t, r, spec))
	for name, want := range map[string]string{
		"HTTPS_PROXY": "http://" + listen, "https_proxy": "http://" + listen,
		"LEGION_GH_OWNERS_DIR": dir, "GH_CONFIG_DIR": dir + "/sjawhar",
	} {
		if env[name] != want {
			t.Errorf("the tester is told %s=%q, want %q", name, env[name], want)
		}
	}
	pod := podOf(t, r, spec)
	if gh := volumeNamed(pod, roleVolume(ghVolume, claim.RoleTester)); gh == nil || gh.Secret != nil || gh.EmptyDir == nil {
		t.Errorf("the tester's gh volume is %+v, want an empty directory projecting nothing of its Secret", gh)
	}
	if gh := volumeNamed(pod, roleVolume(ghVolume, claim.RoleArchitect)); gh == nil || gh.Secret == nil {
		t.Errorf("the architect's gh volume is %+v, want its Secret's projection, as before", gh)
	}

	root := rootSpec(t)
	rootLaunch := launchOf(t, r, root)
	if _, told := forwarderFlags(t, r, rootLaunch); told {
		t.Error("the architect's start runs a forwarder the map does not give it")
	}
	rootEnv := envOf(workerOf(t, r, root))
	if _, set := rootEnv["HTTPS_PROXY"]; set || rootEnv["LEGION_GH_OWNERS_DIR"] != GHConfigDir {
		t.Errorf("the architect is told HTTPS_PROXY=%q LEGION_GH_OWNERS_DIR=%q, want no proxy and its projected bundle", rootEnv["HTTPS_PROXY"], rootEnv["LEGION_GH_OWNERS_DIR"])
	}
}

// volumeNamed is pod's volume of that name, or nil.
func volumeNamed(pod corev1.PodSpec, name string) *corev1.Volume {
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == name {
			return &pod.Volumes[i]
		}
	}
	return nil
}

// Each role's forwarder listens on a loopback port of its own, since every role of an issue pod
// shares the pod's network namespace.
func TestEveryRolesForwarderHasAPortOfItsOwn(t *testing.T) {
	want := map[claim.Role]string{
		claim.RoleArchitect: "127.0.0.1:47801", claim.RolePlanner: "127.0.0.1:47802", claim.RoleImplementer: "127.0.0.1:47803",
		claim.RoleTester: "127.0.0.1:47804", claim.RoleReviewer: "127.0.0.1:47805", claim.RoleMerger: "127.0.0.1:47806",
		claim.RoleController: "127.0.0.1:47807",
	}
	for role, address := range want {
		if got := forwarderAddress(role); got != address {
			t.Errorf("forwarderAddress(%s) = %s, want %s", role, got, address)
		}
	}
}

// With no agent_secrets.github, no role is started with a forwarder or a proxy, and every gh
// volume projects the role's Secret as before.
func TestNoMapStartsNoForwarder(t *testing.T) {
	r, err := configure(goldenOptions())
	if err != nil {
		t.Fatal(err)
	}
	r.agentSecrets = &AgentSecrets{URL: "https://secrets.internal.example", Audience: "agent-secrets", TokenExpiry: time.Hour}
	spec := workerSpec(t)
	if _, told := forwarderFlags(t, r, launchOf(t, r, spec)); told {
		t.Fatal("a runtime with no github map starts a forwarder")
	}
	if _, set := envOf(workerOf(t, r, spec))["HTTPS_PROXY"]; set {
		t.Fatal("a runtime with no github map tells a role HTTPS_PROXY")
	}
	if gh := volumeNamed(podOf(t, r, spec), roleVolume(ghVolume, claim.RoleTester)); gh == nil || gh.Secret == nil {
		t.Fatalf("the tester's gh volume is %+v, want its Secret's projection", gh)
	}
}

// A launch spec may not set HTTPS_PROXY or https_proxy where the deployment puts a role's GitHub
// behind the proxy: the runtime sets both for such a role.
func TestTheProxyVariablesAreTheRuntimesWhereTheMapIsSet(t *testing.T) {
	r := proxiedRuntime(t)
	for _, name := range []string{"HTTPS_PROXY", "https_proxy"} {
		if !r.specOwned()[name] {
			t.Errorf("specOwned leaves %s to a launch spec where the github map is set", name)
		}
	}
	r.agentSecrets.GitHub = nil
	if r.specOwned()["HTTPS_PROXY"] {
		t.Error("specOwned takes HTTPS_PROXY where no github map is set")
	}
}

// The tester's Secret holds no GitHub token where its GitHub is behind the proxy: no hosts.yml key
// is written, its recorded default is empty so the refresher writes none, and a refresh tick adds
// none. The architect beside it still gets its App's bundle.
func TestARoleBehindTheProxyGetsNoAppToken(t *testing.T) {
	g := newRig(t, nil, func(_ *rig, o *Options) { o.AgentSecrets = proxiedSecrets() })
	loc := g.spawn(workerSpec(t))
	g.r.refreshGitHubCredentials(g.ctx)
	secret := g.secret(roleSecretName(loc.Sandbox.Name, claim.RoleTester))
	if secret == nil {
		t.Fatal("the tester has no Secret")
	}
	for key, value := range secret.Data {
		if strings.Contains(string(value), "ghs_") {
			t.Errorf("the tester's Secret holds a GitHub token under %s, want none", key)
		}
	}
	if got, ok := secret.Data[GitHubDefaultKey]; !ok || len(got) != 0 {
		t.Errorf("the tester's Secret records default %q (%v), want an empty one", got, ok)
	}
	architect := g.secret(roleSecretName(loc.Sandbox.Name, claim.RoleArchitect))
	if architect == nil || string(architect.Data[GitHubHostsKey("sjawhar")]) == "" {
		t.Errorf("the architect's Secret holds no sjawhar hosts.yml, want its App's bundle as before")
	}
	start := lastStart(t, g, workerToken)
	if !slices.Contains(start.Argv, "--github-forwarder") {
		t.Errorf("the tester's start %v names no forwarder", start.Argv)
	}
}

// A pod whose tester projects its App's tokens has drifted once the deployment puts the tester's
// GitHub behind the proxy, so the update round replaces it; a pod built with the tester behind the
// proxy has not, and a launch that keeps such a pod starts the tester with its forwarder, where a
// launch that keeps the older pod starts it as that pod was built.
func TestAPodWhoseRoleProjectsTokensDriftsOnceTheRoleIsBehindTheProxy(t *testing.T) {
	before, err := configure(goldenOptions())
	if err != nil {
		t.Fatal(err)
	}
	before.agentSecrets = &AgentSecrets{URL: "https://secrets.internal.example", Audience: "agent-secrets", TokenExpiry: time.Hour}
	after := proxiedRuntime(t)
	spec := workerSpec(t)
	for name, tc := range map[string]struct {
		built   *Runtime
		drifted bool
	}{
		"a pod built before the map": {before, true},
		"a pod built under the map":  {after, false},
	} {
		t.Run(name, func(t *testing.T) {
			podSpec := podOf(t, tc.built, spec)
			pod := &corev1.Pod{Spec: podSpec}
			moved, drifted := after.movedGhDirs(pod, claim.RoleTester)
			if drifted != tc.drifted {
				t.Fatalf("movedGhDirs = %v, %v; want drifted %v", moved, drifted, tc.drifted)
			}
			if after.proxiesGitHubIn(&podSpec, claim.RoleTester) == tc.drifted {
				t.Fatalf("proxiesGitHubIn of %s = %v, want %v", name, !tc.drifted, !tc.drifted)
			}
		})
	}
}
