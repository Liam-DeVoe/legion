package sandbox

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// A role whose GitHub the deployment puts behind the secrets broker's proxy
// (runtime.kubernetes.agent_secrets.github) reaches GitHub through its own forwarder, `legion
// forwarder` (internal/githubproxy), which its shim starts with the generation: the forwarder asks
// the broker, with the role's own key, for an hour's grant of the role's secret names, writes each
// account's placeholder into a gh bundle under the generation's private directory, and carries
// the role's CONNECTs to GitHub's hosts to the broker's proxy port. The daemon mints no App token
// for such a role and writes none into its pod; the role's environment names the forwarder in
// HTTPS_PROXY and https_proxy, and the bundle in LEGION_GH_OWNERS_DIR and GH_CONFIG_DIR.

// gitHubForwarderFile is the file of a generation's private directory holding the forwarder's
// flags as a JSON list, which the start command writes (launcherCommand) and the shim's
// --github-forwarder names.
const gitHubForwarderFile = "github-forwarder.json"

// forwarderPortBase is the loopback port before the first role's forwarder's: every role of an
// issue pod shares the pod's network namespace, so each role's forwarder listens on a port of its
// own, forwarderPortBase plus the role's place in forwarderRoles.
const forwarderPortBase = 47800

// forwarderRoles are the roles a forwarder may serve, each the place its port is counted from:
// every workflow role, then the controller.
var forwarderRoles = append(slices.Clone(claim.Roles), claim.RoleController)

// forwarderAddress is the loopback address role's forwarder listens on, which its HTTPS_PROXY
// names.
func forwarderAddress(role claim.Role) string {
	return "127.0.0.1:" + strconv.Itoa(forwarderPortBase+1+slices.Index(forwarderRoles, role))
}

// proxyOwned are the proxy variables the runtime sets for a role behind the broker's proxy
// (ghAgentEnv), which a launch spec may not set where the deployment puts any role there
// (specOwned).
var proxyOwned = map[string]bool{"HTTPS_PROXY": true, "https_proxy": true}

// proxiesGitHubIn is the one decision whether a generation of role started now in a pod reaches
// GitHub through its forwarder: the generation holds the broker (enrollsIn), whose key the
// forwarder asks with; the deployment names secrets for the role (AgentSecrets.GitHub); and the
// pod's gh volume for the role projects nothing of its Secret (ghProjectionOf), which is how a pod
// built with the role behind the proxy is told from one built with its App's tokens. A nil pod is
// a launch that builds the pod itself, which carries the configuration as it now reads. A role in
// a kept pod built with its App's tokens is started as that pod was built, its tokens refreshed,
// until the update round replaces the pod (movedGhDirs).
func (r *Runtime) proxiesGitHubIn(pod *corev1.PodSpec, role claim.Role) bool {
	if !r.enrollsIn(pod, role) || len(r.agentSecrets.GitHub[role]) == 0 {
		return false
	}
	if pod == nil {
		return true
	}
	projected, found := ghProjectionOf(&corev1.Pod{Spec: *pod}, role)
	return found && !projected.secret
}

// proxiesGitHub is this launch's reading of that decision: of the live pod it keeps, as the launch
// read that pod's stored template when it began (keptPod.proxied), or of the pod it builds itself.
func (l launch) proxiesGitHub() bool {
	if l.kept != nil {
		return l.kept.proxied
	}
	return l.proxied
}

// gitHubProxyDir is the gh bundle a generation's forwarder writes, under the generation's own
// private directory, which the launcher makes before the shim starts and removes when the
// generation ends: no placeholder of a grant outlives the generation that asked for it.
func gitHubProxyDir(generation uint64) string {
	return generationDir(generation) + "/gh"
}

// proxyRenames are the configured renames a forwarder's bundle routes by: every configured pair
// that moves a repository to another owner's installation. A pair whose two names are one owner's
// routes nothing, since an owner's directory answers for each of its repositories.
func (r *Runtime) proxyRenames() []ghrepo.Rename {
	return slices.DeleteFunc(slices.Clone(r.agentSecrets.GitHubRenames), func(pair ghrepo.Rename) bool {
		return strings.EqualFold(pair.From.Owner(), pair.To.Owner())
	})
}

// proxyDefault is the account directory a role behind the proxy is told as GH_CONFIG_DIR: the
// directory the issue's repository routes to over the forwarder's renames (ghconfig.RouteDir),
// and, for the controller, which works no repository, the first owner the daemon serves
// (Options.GitHubDirs), "" where it serves none. It mints nothing.
func (r *Runtime) proxyDefault(spec runtime.SpawnSpec) string {
	if spec.Role == claim.RoleController {
		owners := ghOwnerDirs(r.gitHubDirs(ghrepo.Repository{}, podAppRoles(controllerPod{})))
		if len(owners) == 0 {
			return ""
		}
		return slices.Min(owners)
	}
	dir, _, _ := ghconfig.RouteDir(r.proxyRenames(), spec.Repository.Owner(), spec.Repository.Name())
	return dir
}

// forwarderFlags are the flags l's forwarder is started with (`legion forwarder`): its role, its
// loopback address, the role's secret names, a reason naming the role and its issue (the grant's
// audit row names it), the bundle it writes, the directory GH_CONFIG_DIR names and the renames.
func (r *Runtime) forwarderFlags(l launch) []string {
	role := l.spec.Role
	reason := fmt.Sprintf("GitHub access of the %s of %s, through its forwarder", role, l.spec.Issue)
	if role == claim.RoleController {
		reason = fmt.Sprintf("GitHub access of project %s's controller, through its forwarder", l.spec.Project)
	}
	flags := []string{"--role", string(role), "--listen", forwarderAddress(role)}
	for _, name := range r.agentSecrets.GitHub[role] {
		flags = append(flags, "--secret", name)
	}
	flags = append(flags, "--reason", reason, "--gh-dir", gitHubProxyDir(l.spec.Generation))
	if l.proxyDefault != "" {
		flags = append(flags, "--default-owner", l.proxyDefault)
	}
	for _, pair := range r.proxyRenames() {
		flags = append(flags, "--rename", pair.From.String()+"="+pair.To.String())
	}
	return flags
}

// forwarderFile is forwarderFlags as the file the shim reads them from.
func (r *Runtime) forwarderFile(l launch) string {
	encoded, err := json.Marshal(r.forwarderFlags(l))
	if err != nil {
		panic(err) // a list of strings always encodes
	}
	return string(encoded)
}

// specOwnedWithProxy is owned with proxyOwned where the deployment puts any role's GitHub behind
// the proxy.
func (r *Runtime) specOwnedWithProxy(owned map[string]bool) map[string]bool {
	if r.agentSecrets == nil || len(r.agentSecrets.GitHub) == 0 {
		return owned
	}
	names := maps.Clone(owned)
	maps.Copy(names, proxyOwned)
	return names
}
