package textcheck

import (
	"strings"
	"testing"
)

// The operator texts say what the runtime does. A role's bundle, its Secret, holds a token for
// every owner its App is installed on, and an issue pod projects only the directories every role of
// the pod holds (runtime/sandbox's ghDirsShared), so the reach of the whole bundle is said of the
// Secret and not of a role in an issue pod. The worker image carries Claude Code's pin and the
// claude-envoy plugin, never Claude Code, which a pod installs at each launch of a Claude role
// (runtime/sandbox's claudeCodeBlock). And an issue runs in one pod: a role that dies is started
// again inside it as its next generation, through its launcher, after any earlier generation of it
// is stopped (runtime/sandbox's relaunch and Suspend), and no pod is named for a role.
func TestTheOperatorTextsSayWhereARoleWritesWhatTheImageCarriesAndWhereARoleRelaunches(t *testing.T) {
	for _, text := range []struct {
		path       string
		want, gone []string
	}{
		{
			path: "docs/kubernetes.md",
			want: []string{
				"A role's Secret therefore holds tokens to push to, open a pull request in and review a pull request of every repository its App reaches.",
				"In an issue pod a role sees only the directories every role of the pod holds, as the projection below says.",
				"A graceful stop of a role sends that role's launcher a stop frame for the recorded generation, with the runtime's stop grace",
				"It ends that role's process alone: the issue's other roles run on in the pod, which stays.",
				"Two generations of a role never run side by side",
				"The relaunch starts only that role, through its launcher in the issue's one pod",
			},
			gone: []string{
				"A role therefore pushes to, opens a pull request in and reviews a pull request of every repository its App reaches",
				"legion-<issue>-<role>-g<n+1>",
				"then deletes the per-pod Secret",
				"the previous generation's pod is deleted",
				"A phase worker or architect pod that dies mid-task",
			},
		},
		{
			path: "docs/site/src/content/docs/legion/concepts.md",
			want: []string{"No directory holds a read-only token, so the role's whole bundle can write in every repository its App reaches, whichever owner holds it."},
			gone: []string{"so a role writes in every repository its App reaches"},
		},
		{
			path: "deploy/kubernetes/daemon/legion.yaml.example",
			want: []string{"In a pod, as in this file, the worker image carries the claude-envoy plugin and Claude Code's pin, not Claude Code. Each launch there installs the pinned release. Under runtime: tmux it needs LEGION_CLAUDE_PLUGIN_DIR (and LEGION_CLAUDE_PATH, LEGION_BUN_PATH, else PATH) in the daemon's environment."},
			gone: []string{"which carries Claude Code and the claude-envoy plugin"},
		},
	} {
		prose := proseOf(shippedText(t, text.path))
		for _, phrase := range text.want {
			if !strings.Contains(prose, phrase) {
				t.Errorf("%s omits %q", text.path, phrase)
			}
		}
		for _, phrase := range text.gone {
			if strings.Contains(prose, phrase) {
				t.Errorf("%s still says %q", text.path, phrase)
			}
		}
	}
}

// The architect skill gives one order for a child and its release. A child's architect decides its
// children before it extends its spec, as its role prompt's list of first acts says. A child is
// released once its filed version has the architect's approving review and its plan (register_plan
// before release_children), and a person who files a child reviews no spec of it. The one release
// held back for a close is the child that builds on another repository's change, and it goes out
// before an issue above it is accepted, since from then until that issue closes the daemon refuses
// it (api's CHILD_UNDER_ACCEPTED_PARENT). A child has no architect before its first phase starts it
// (workflow's Engine.gateOpened), so the review before its release tells that architect once it runs.
func TestTheArchitectSkillReleasesAChildAfterItsReviewAndItsPlan(t *testing.T) {
	for _, text := range []struct {
		path       string
		want, gone []string
	}{
		{
			path: "skills/legion-architect/SKILL.md",
			want: []string{
				"A child: after step 4, for the part that stays.",
				"Release each child once its spec is approved and it has its plan:",
				"Never hold a release back for a close, except as `skill://legion-architect/references/decomposition.md` says: the daemon refuses one under an accepted issue.",
			},
			gone: []string{
				"A child: as `skill://legion-architect/references/decomposition.md`, \"Creating children\", says.",
				"Release each child once it is filed:",
				"a child released after the root's merge runs as an orphan root",
			},
		},
		{
			path: "skills/legion-architect/references/decomposition.md",
			want: []string{
				"Release a child a person created as it is, once you have reviewed its spec, as no person did, and given it its plan:",
				"then tell the child's architect with `envoy_publish` once it runs:",
				"Release it before an issue above it is accepted: the daemon then refuses it until that issue closes. If that pull request cannot merge in time, ask a person (\"Every turn\") rather than release late.",
			},
			gone: []string{
				"Release a child a person created at once and as it is",
				"then tell the child's architect with `envoy_publish`:",
			},
		},
	} {
		prose := proseOf(shippedText(t, text.path))
		for _, phrase := range text.want {
			if !strings.Contains(prose, phrase) {
				t.Errorf("%s omits %q", text.path, phrase)
			}
		}
		for _, phrase := range text.gone {
			if strings.Contains(prose, phrase) {
				t.Errorf("%s still says %q", text.path, phrase)
			}
		}
	}
}
