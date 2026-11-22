package ghstackgit

import (
	"slices"
	"strings"
	"testing"
)

// instances is every argument list words stands for with one sample of each part: a literal as
// itself, a prefix with "x" appended, a format with value for each of its pieces, a value as
// value, each alternative, an optional part left out and put in, and a repeated part given once
// and twice.
func instances(words []word, value string) [][]string {
	out := [][]string{{}}
	extend := func(tails [][]string) {
		var next [][]string
		for _, head := range out {
			for _, tail := range tails {
				next = append(next, append(slices.Clone(head), tail...))
			}
		}
		out = next
	}
	for _, w := range words {
		switch w.kind {
		case 'L':
			extend([][]string{{w.s}})
		case 'P':
			extend([][]string{{w.s + "x"}})
		case 'F':
			filled := w.s
			for n := '1'; n <= '9'; n++ {
				filled = strings.ReplaceAll(filled, "%"+string(n), value)
			}
			extend([][]string{{filled}})
		case 'V':
			extend([][]string{{value}})
		case 'A':
			var tails [][]string
			for _, alt := range w.alts {
				tails = append(tails, []string{alt})
			}
			extend(tails)
		case 'O':
			extend(append([][]string{{}}, instances(w.group, value)...))
		case 'R':
			var tails [][]string
			for _, one := range instances(w.group, value) {
				tails = append(tails, one, append(slices.Clone(one), one...))
			}
			extend(tails)
		case 'X':
			var tails [][]string
			for _, seq := range w.seqs {
				tails = append(tails, instances(seq, value)...)
			}
			extend(tails)
		}
	}
	return out
}

// Every shape admits the calls it stands for, and refuses each of them with an option in place of
// any one name, whole or in a format: `--exec=touch x`, which git would run a program for, and
// `-f`, an option gh-stack passes elsewhere, and `.`.
func TestEveryShapeAdmitsItsCallsAndNoValueThatStartsWithADash(t *testing.T) {
	if len(parsed) < 40 {
		t.Fatalf("parsed %d shapes, want gh-stack's 60 or so", len(parsed))
	}
	for i, shape := range parsed {
		for _, args := range instances(shape, "feature") {
			if err := Check(args); err != nil {
				t.Errorf("shape %q: Check(%q) = %v, want nil", shapes[i], args, err)
			}
			for j, arg := range args {
				if arg != "feature" {
					continue
				}
				for _, name := range []string{"--exec=touch x", "-f", "."} {
					dashed := slices.Clone(args)
					dashed[j] = name
					if err := Check(dashed); err == nil {
						t.Errorf("shape %q: Check(%q) = nil, want a refusal", shapes[i], dashed)
					} else if name == "--exec=touch x" && !strings.Contains(err.Error(), `"--exec=touch x"`) {
						t.Errorf("shape %q: Check(%q) = %v, want a refusal naming the value", shapes[i], dashed, err)
					}
				}
			}
		}
	}
}

// `git checkout <name>` runs as `git checkout <name> --`, so git never reads the name as a path;
// every other call runs as gh-stack made it.
func TestArgsReadsACheckoutsNameAsNoPath(t *testing.T) {
	for _, tc := range []struct{ args, want []string }{
		{[]string{"checkout", "notes"}, []string{"checkout", "notes", "--"}},
		{[]string{"-C", "/work/repo", "checkout", "notes"}, []string{"-C", "/work/repo", "checkout", "notes", "--"}},
		{[]string{"rev-parse", "notes"}, []string{"rev-parse", "notes"}},
		{[]string{"branch", "notes", "main"}, []string{"branch", "notes", "main"}},
	} {
		if got, err := Args(tc.args); err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("Args(%q) = %q, %v; want %q", tc.args, got, err, tc.want)
		}
	}
	if got, err := Args([]string{"checkout", "-f"}); err == nil {
		t.Errorf("Args(checkout -f) = %q, want a refusal", got)
	}
}

// The calls gh-stack v0.3.0 makes in its everyday commands, written here from its
// internal/git/gitops.go and worktree.go, cli/cli's git client and go-gh's internal/git, not
// from shapes, all pass.
func TestGhStacksCallsPass(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, args := range [][]string{
		{"--version"},
		{"remote", "-v"},
		{"config", "--get-regexp", `^remote\..*\.gh-resolved$`},
		{"-C", "/work/repo", "rev-parse", "--path-format=absolute", "--git-common-dir"},
		{"rev-parse", "--path-format=absolute", "--absolute-git-dir"},
		{"--git-dir=/work/repo/.git", "config", "--null", "--path", "--get", "core.worktree"},
		{"worktree", "list", "--porcelain", "-z"},
		{"symbolic-ref", "--quiet", "HEAD"},
		{"rev-parse", "--verify", "--quiet", "refs/heads/feature"},
		{"rev-parse", "--verify", "--quiet", "--end-of-options", sha + "^{commit}"},
		{"rev-parse", "main", "feature", "top"},
		{"checkout", "feature"},
		{"branch", "feature", "main"},
		{"branch", "-f", "feature", sha},
		{"branch", "-m", "old", "new"},
		{"branch", "-D", "feature"},
		{"branch", "-dr", "origin/feature"},
		{"branch", "--set-upstream-to=origin/feature", "feature"},
		{"-c", "credential.helper=", "-c", `credential.helper=!"gh" auth git-credential`, "fetch", "origin"},
		{"fetch", "origin", "+refs/heads/a:refs/remotes/origin/a", "+refs/heads/b:refs/remotes/origin/b"},
		{"fetch", "origin", "+refs/heads/legion/INFRA-5217:refs/remotes/origin/legion/INFRA-5217"},
		{"fetch", "up/stream", "+refs/heads/feat/x/y:refs/remotes/up/stream/feat/x/y"},
		{"rev-parse", "--verify", "--quiet", "refs/remotes/origin/legion/INFRA-5217"},
		{"push", "origin", "--atomic", "refs/heads/a:refs/heads/a", "refs/heads/b:refs/heads/b"},
		{"push", "origin", "refs/heads/legion/INFRA-5217:refs/heads/legion/INFRA-5217"},
		{"-c", "push.pushOption=pull.stacked", "push", "origin", "--force-with-lease=refs/heads/a:" + sha, "--force-with-lease=refs/heads/b:", "--atomic", "refs/heads/a:refs/heads/a", "refs/heads/b:refs/heads/b"},
		{"-c", "rebase.updateRefs=false", "-c", "rebase.autoStash=false", "-c", "maintenance.auto=false", "rebase", "--onto", "main", sha, "feature"},
		{"-c", "rebase.updateRefs=false", "-c", "rebase.autoStash=false", "-c", "maintenance.auto=false", "rebase", "--merge", "--committer-date-is-author-date", "main"},
		{"-c", "rebase.updateRefs=false", "-c", "rebase.autoStash=false", "-c", "maintenance.auto=false", "rebase", "--continue"},
		{"merge-base", "--is-ancestor", sha, "feature"},
		{"log", "feature", "--format=%H\t%s\t%at", "-n", "20"},
		{"log", "--merges", "main..feature", "--format=%H%x01%B%x01%at%x00"},
		{"diff", "--no-relative", "--check", "--", ":(top,literal)src/a.go"},
		{"commit", "-m", "Add the layer"},
		{"-c", "merge.autoStash=false", "merge", "--ff-only", "origin/main"},
		{"add", "-A"},
		{"status", "--porcelain", "--untracked-files=all"},
		{"config", "gh-stack.remote", "origin"},
		{"config", "--get", "branch.feature.pushRemote"},
	} {
		if err := Check(args); err != nil {
			t.Errorf("Check(%q) = %v, want nil", args, err)
		}
	}
}

// A branch or revision name that starts with `-`, where gh-stack hands a name to git, is refused
// even where it equals an option gh-stack passes elsewhere, and so is `.`; a refspec that is not
// the one gh-stack builds of one name, another credential helper, and git calls gh-stack does not
// make are refused.
func TestNamesThatStartWithADashAndOtherCallsAreRefused(t *testing.T) {
	sha := strings.Repeat("a", 40)
	rebase := []string{"-c", "rebase.updateRefs=false", "-c", "rebase.autoStash=false", "-c", "maintenance.auto=false", "rebase"}
	for _, args := range [][]string{
		{"checkout", "-f"},
		{"checkout", "-m"},
		{"checkout", "--exec=touch x"},
		{"branch", "-m", "main"},
		{"branch", "-f", "-m", sha},
		{"branch", "-D", "-m"},
		{"branch", "--set-upstream-to=origin/x", "-f"},
		append(slices.Clone(rebase), "--onto", "main", sha, "--exec=touch x"),
		append(slices.Clone(rebase), "--onto", "main", sha, "-f"),
		{"merge-base", "--is-ancestor", sha, "-f"},
		{"log", "-n", "--format=%H", "-n", "1"},
		{"rev-parse", "main", "-f"},
		{"cherry-pick", "-m", "1"},
		{"push", "origin", "--delete", "-f"},
		{"reset", "--hard", "-p"},
		{"fetch", "--upload-pack=touch x", "origin"},
		{"-c", "core.sshCommand=touch x", "fetch", "origin"},
		{"-C", "/work/repo", "--exec-path=/tmp", "status", "--porcelain", "--untracked-files=all"},
		{"clean", "-fdx"},
		{"gc"},
		{"checkout", "--", "file"},
		{"checkout", "."},
		{"-c", "credential.helper=", "-c", "credential.helper=!touch x; false", "fetch", "origin"},
		{"push", "origin", "refs/heads/-f:refs/heads/-f"},
		{"push", "origin"},
		{"push", ".", "refs/heads/a:refs/heads/a"},
		{"push", "origin", ":refs/heads/main"},
		{"push", "origin", "+HEAD:refs/heads/main"},
		{"push", "origin", "refs/heads/a:refs/heads/main"},
		{"push", "origin", "refs/heads/a:refs/heads/a:refs/heads/main"},
		{"push", "origin", "--delete", "refs/heads/feature"},
		{"fetch", "origin", "+refs/heads/x:refs/heads/main"},
		{"fetch", "origin", "+refs/heads/x:refs/remotes/origin/y"},
		{"fetch", "origin", "+refs/heads/.:refs/remotes/origin/."},
		{"cherry-pick", "--quit"},
		{"config", "--unset", "gh-stack.remote"},
		{"cherry-pick"},
		{},
	} {
		if err := Check(args); err == nil {
			t.Errorf("Check(%q) = nil, want a refusal", args)
		}
	}
}
