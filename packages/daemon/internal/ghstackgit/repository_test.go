package ghstackgit

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRepository is a repository Refusal reads: the index ls-files prints, and files by path
// under /g, the git directory.
type fakeRepository struct {
	index string
	files map[string]string
	calls [][]string
}

func (r *fakeRepository) git(args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	if i := slices.Index(args, "ls-files"); i >= 0 {
		return []byte(r.index), nil
	}
	if slices.Contains(args, "rev-parse") {
		var out []string
		for i, arg := range args {
			if arg == "--git-path" {
				out = append(out, filepath.Join("/g", args[i+1]))
			}
		}
		return []byte(strings.Join(out, "\n") + "\n"), nil
	}
	return nil, errors.New("unexpected probe")
}

func (r *fakeRepository) read(path string) ([]byte, error) {
	if text, ok := r.files[path]; ok {
		return []byte(text), nil
	}
	return nil, fs.ErrNotExist
}

// A call that reads the work tree or the index is refused while the index holds a gitlink, and
// another call, or the same call with no gitlink, is not; the index is read in the call's `-C`
// directory.
func TestRefusalRefusesASubmoduleInTheIndexForCallsThatReadTheWorkTree(t *testing.T) {
	blob := "100644 " + strings.Repeat("a", 40) + " 0\tf\x00"
	gitlink := "160000 " + strings.Repeat("b", 40) + " 0\tsub\x00"
	for _, args := range [][]string{
		{"status", "--porcelain", "--untracked-files=all"}, {"add", "-A"}, {"add", "-u"}, {"commit", "-m", "x"},
		{"-C", "/w", "checkout", "b", "--"}, {"diff", "--no-relative", "--check"},
		{"-c", "rebase.updateRefs=false", "rebase", "--onto", "a", "b", "c"}, {"cherry-pick", "abc"},
		{"-c", "merge.autoStash=false", "merge", "--ff-only", "origin/main"},
	} {
		repo := &fakeRepository{index: blob + gitlink}
		if err := Refusal(args, repo.git, repo.read); err == nil || !strings.Contains(err.Error(), "the index holds the submodule sub") {
			t.Errorf("Refusal(%q) with a gitlink = %v, want a refusal naming sub", args, err)
		}
		clean := &fakeRepository{index: blob}
		if err := Refusal(args, clean.git, clean.read); err != nil {
			t.Errorf("Refusal(%q) with no gitlink = %v, want nil", args, err)
		}
	}
	at := &fakeRepository{index: gitlink}
	_ = Refusal([]string{"-C", "/w", "status", "--porcelain", "--untracked-files=all"}, at.git, at.read)
	if len(at.calls) != 1 || !slices.Equal(at.calls[0][:2], []string{"-C", "/w"}) {
		t.Errorf("the index was read with %q, want it read in -C /w", at.calls)
	}
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"fetch", "origin"}, {"push", "origin", "refs/heads/a:refs/heads/a"}, {"log", "a"}} {
		repo := &fakeRepository{index: gitlink}
		if err := Refusal(args, repo.git, repo.read); err != nil || len(repo.calls) != 0 {
			t.Errorf("Refusal(%q) = %v after %q, want nil and no probe", args, err, repo.calls)
		}
	}
}

// `rebase --continue` runs only where the paused rebase does nothing but pick commits: an exec,
// a break, a merge or any other line, a recorded strategy, strategy option or signing, and an
// apply-backend rebase are each refused; a todo of picks, comments and blank lines, and no paused
// rebase at all, are not. `cherry-pick --continue` the same, with its opts.
func TestRefusalRunsAContinueOnlyForAStateThatPicksCommits(t *testing.T) {
	sha := strings.Repeat("c", 40)
	rebase := []string{"-c", "rebase.updateRefs=false", "-c", "rebase.autoStash=false", "-c", "maintenance.auto=false", "rebase", "--continue"}
	picks := "pick " + sha + " two\n# a comment\n\np 1234567 three\n"
	for _, tc := range []struct {
		name   string
		args   []string
		files  map[string]string
		refuse string
	}{
		{name: "picks", args: rebase, files: map[string]string{"/g/rebase-merge/git-rebase-todo": picks}},
		{name: "no paused rebase", args: rebase},
		{name: "an exec", args: rebase, files: map[string]string{"/g/rebase-merge/git-rebase-todo": "exec touch x\n" + picks}, refuse: `holds "exec touch x"`},
		{name: "an x", args: rebase, files: map[string]string{"/g/rebase-merge/git-rebase-todo": picks + "x touch x\n"}, refuse: `holds "x touch x"`},
		{name: "a break", args: rebase, files: map[string]string{"/g/rebase-merge/git-rebase-todo": "break\n"}, refuse: `holds "break"`},
		{name: "a merge", args: rebase, files: map[string]string{"/g/rebase-merge/git-rebase-todo": "merge -s evil -C " + sha + " x\n"}, refuse: "holds"},
		{name: "a reword", args: rebase, files: map[string]string{"/g/rebase-merge/git-rebase-todo": "reword " + sha + "\n"}, refuse: "holds"},
		{name: "a pick of no commit", args: rebase, files: map[string]string{"/g/rebase-merge/git-rebase-todo": "pick HEAD\n"}, refuse: "holds"},
		{name: "a strategy", args: rebase, files: map[string]string{"/g/rebase-merge/git-rebase-todo": picks, "/g/rebase-merge/strategy": "evil\n"}, refuse: "records strategy"},
		{name: "a strategy option", args: rebase, files: map[string]string{"/g/rebase-merge/strategy_opts": "--x"}, refuse: "records strategy_opts"},
		{name: "signing", args: rebase, files: map[string]string{"/g/rebase-merge/gpg_sign_opt": "-S"}, refuse: "records gpg_sign_opt"},
		{name: "an apply-backend rebase", args: rebase, files: map[string]string{"/g/rebase-apply": ""}, refuse: "apply backend"},
		{name: "a cherry-pick of picks", args: []string{"cherry-pick", "--continue"}, files: map[string]string{"/g/sequencer/todo": picks}},
		{name: "a cherry-pick exec", args: []string{"cherry-pick", "--continue"}, files: map[string]string{"/g/sequencer/todo": picks + "exec touch x\n"}, refuse: `holds "exec touch x"`},
		{name: "a cherry-pick strategy", args: []string{"cherry-pick", "--continue"}, files: map[string]string{"/g/sequencer/todo": picks, "/g/sequencer/opts": "[options]\n\tStrategy = evil\n"}, refuse: "records a strategy"},
		{name: "a cherry-pick signing", args: []string{"cherry-pick", "--continue"}, files: map[string]string{"/g/sequencer/opts": "[options]\n\tgpg-sign = -S\n"}, refuse: "records a strategy"},
		{name: "a rebase that is no continue", args: []string{"-c", "rebase.updateRefs=false", "rebase", "--abort"}, files: map[string]string{"/g/rebase-merge/git-rebase-todo": "exec touch x\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepository{files: tc.files}
			err := Refusal(tc.args, repo.git, repo.read)
			switch {
			case tc.refuse == "" && err != nil:
				t.Errorf("Refusal(%q) = %v, want nil", tc.args, err)
			case tc.refuse != "" && (err == nil || !strings.Contains(err.Error(), tc.refuse)):
				t.Errorf("Refusal(%q) = %v, want a refusal containing %q", tc.args, err, tc.refuse)
			}
		})
	}
}

// A probe that fails is a refusal: what it would have read is unread.
func TestRefusalRefusesWhereItCannotRead(t *testing.T) {
	failing := func(...string) ([]byte, error) { return nil, errors.New("no git") }
	none := func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	for _, args := range [][]string{{"status", "--porcelain", "--untracked-files=all"}, {"cherry-pick", "--continue"}} {
		if err := Refusal(args, failing, none); err == nil || !strings.Contains(err.Error(), "cannot") {
			t.Errorf("Refusal(%q) with a failing probe = %v, want a refusal", args, err)
		}
	}
}
