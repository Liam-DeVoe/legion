package sandbox

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// A pod's reviewer is told its own clone and its own git (workspace/reviewgit.go): its
// LEGION_WORKSPACE is the reviewer's clone of the issue on the issue's volume
// (workspace.ReviewLocation), which its role-init makes from the pod's shared clone at the head
// the daemon gives it (workspace.ReviewHeadEnv, from the spec's ReviewHead), or, where the daemon
// gives none, at the issue's branch or else the default branch it is told
// (LEGION_DEFAULT_BRANCH); its git reads no
// global or system configuration, and, after the pairs every role's git reads, the fsmonitor off
// and the hooks at /dev/null, which git takes since it reads them last. Every other role works in
// the issue's workspace and is told none of it, and a spec that names any of these is refused, as
// it is for every name the runtime sets.
func TestAPodReviewerIsToldItsOwnCloneAndGit(t *testing.T) {
	r, err := configure(goldenOptions())
	if err != nil {
		t.Fatal(err)
	}
	const head = "0123456789abcdef0123456789abcdef01234567"
	reviewer := testSpec(t, otherToken, claim.RoleReviewer, testTree)
	reviewer.DefaultBranch = "release/v7"
	reviewer.ReviewHead = head
	env := map[string]string{}
	for _, entry := range r.mainEnvironment(launchOf(t, r, reviewer)) {
		env[entry.Name] = entry.Value
	}
	review, err := workspace.ReviewLocation(TreeRoot, reviewer.Repository, reviewer.Issue)
	if err != nil {
		t.Fatal(err)
	}
	pairs := slices.Concat(gitConfigPairs, [][2]string{{"core.fsmonitor", "false"}, {"core.hooksPath", "/dev/null"}})
	want := map[string]string{
		"LEGION_WORKSPACE": review, "LEGION_DEFAULT_BRANCH": "release/v7", workspace.ReviewHeadEnv: head,
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_COUNT": strconv.Itoa(len(pairs)),
	}
	for i, pair := range pairs {
		want["GIT_CONFIG_KEY_"+strconv.Itoa(i)], want["GIT_CONFIG_VALUE_"+strconv.Itoa(i)] = pair[0], pair[1]
	}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("the reviewer's %s = %q, want %q", name, env[name], value)
		}
	}
	reviewer.ReviewHead = ""
	for _, entry := range r.mainEnvironment(launchOf(t, r, reviewer)) {
		if entry.Name == workspace.ReviewHeadEnv {
			t.Errorf("a reviewer given no head is told %s=%q", entry.Name, entry.Value)
		}
	}

	tester := workerSpec(t)
	tester.ReviewHead = head
	env = map[string]string{}
	for _, entry := range r.mainEnvironment(launchOf(t, r, tester)) {
		env[entry.Name] = entry.Value
	}
	located, err := workspace.Location(TreeRoot, tester.Repository, tester.Issue)
	if err != nil {
		t.Fatal(err)
	}
	if env["LEGION_WORKSPACE"] != located.Dir {
		t.Errorf("the tester's LEGION_WORKSPACE = %q, want the issue's workspace %s", env["LEGION_WORKSPACE"], located.Dir)
	}
	if env["GIT_CONFIG_COUNT"] != strconv.Itoa(len(gitConfigPairs)) {
		t.Errorf("the tester's GIT_CONFIG_COUNT = %q, want the %d pairs every role's git reads", env["GIT_CONFIG_COUNT"], len(gitConfigPairs))
	}
	for _, name := range []string{"LEGION_DEFAULT_BRANCH", workspace.ReviewHeadEnv, "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"} {
		if value, told := env[name]; told {
			t.Errorf("the tester is told %s=%q, which is the reviewer's alone", name, value)
		}
	}

	for _, name := range []string{"LEGION_DEFAULT_BRANCH", workspace.ReviewHeadEnv, "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_KEY_" + strconv.Itoa(len(pairs)-1)} {
		spec := testSpec(t, otherToken, claim.RoleReviewer, testTree)
		spec.Env[name] = "x"
		if _, err := r.prepare(spec); err == nil || !strings.Contains(err.Error(), "Env sets "+name) {
			t.Errorf("prepare of a reviewer's spec that sets %s = %v, want a refusal naming it", name, err)
		}
	}
}
