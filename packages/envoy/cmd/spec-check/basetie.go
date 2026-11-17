package main

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// baseLine starts the first line of the list that changed prints and citations --changed reads,
// "base <object name>": the commit that the paths of the list changed since.
const baseLine = "base "

// objectName is whether s is a full git object name: 40 lower-case hex digits, or 64 in a
// repository of SHA-256. citations names commits so in its own files, since a short or a symbolic
// name can name another commit tomorrow.
func objectName(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0123456789abcdef") == ""
}

// commitName is the full object name of the commit that rev names in the git clone at repo, and
// whether git could read one. --end-of-options makes git read a rev that starts with "-" as a
// revision, never as an option, as GitHistory does.
func commitName(repo, rev string) (string, bool) {
	name, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}").Output()
	return strings.TrimSpace(string(name)), err == nil
}

// readTiesFiles reads the files of --ties, each in the form of specgraph.ReadCommitTies, as one
// set of ties by commit. It refuses a commit that is no full object name, and a commit that two of
// the files hold, since the command could not say which file's ties are the commit's; of a file's
// commits it judges each in sorted order, so a refusal names the same commit on each run.
func readTiesFiles(paths []string) (specgraph.CommitTies, error) {
	all := specgraph.CommitTies{}
	from := map[string]string{}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("%s: cannot be read: %w", path, err)
		}
		ties, err := specgraph.ReadCommitTies(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, commit := range slices.Sorted(maps.Keys(ties)) {
			switch {
			case !objectName(commit):
				return nil, fmt.Errorf("%s: commit %q: not a full object name of 40 or 64 lower-case hex digits", path, commit)
			case from[commit] != "":
				return nil, fmt.Errorf("%s: commit %s: %s holds its ties too", path, commit, from[commit])
			}
			all[commit], from[commit] = ties[commit], path
		}
	}
	return all, nil
}

// fileTie is the tie of check at commit in the files of --ties, Known false when they hold none.
func fileTie(files specgraph.CommitTies, commit, check string) specgraph.Tie {
	checks, held := files[commit]
	if !held {
		return specgraph.Tie{Why: fmt.Sprintf("--ties holds no ties of commit %s", commit)}
	}
	tie, held := checks[check]
	if !held {
		return specgraph.Tie{Why: fmt.Sprintf("--ties holds no tie of the check at commit %s", commit)}
	}
	return tie
}

// derivedTies is the tie of each check that the .github/scripts/go-test-reads.sh of a repository
// derives (specgraph.GoTies), one check at a time on the first ask, each set of the script's
// arguments asked once. specgraph.Citations asks for a check's tie only when its test did not run,
// so a run where each test ran runs no script.
type derivedTies struct {
	reads   specgraph.TestReads
	answers map[string]readsAnswer
}

// readsAnswer is one answer of go-test-reads.sh.
type readsAnswer struct {
	reads []string
	err   error
}

// newDerivedTies is the derived ties of the repository at root, whose go-test-reads.sh runs in
// root, where the root go.work takes the import paths of the repository's modules.
func newDerivedTies(root string) *derivedTies {
	return &derivedTies{reads: specgraph.ScriptTestReads(root), answers: map[string]readsAnswer{}}
}

// tie is the tie of c, Known false with the cause when it cannot be derived.
func (d *derivedTies) tie(c specgraph.Check) specgraph.Tie {
	return specgraph.GoTies([]specgraph.Check{c}, ".", nil, d.once)[c.ID]
}

// once is the script's answer for its arguments, asked the first time only.
func (d *derivedTies) once(workdir string, pkgs, env []string) ([]string, error) {
	key := strings.Join(append(append([]string{workdir}, pkgs...), env...), "\x00")
	answer, asked := d.answers[key]
	if !asked {
		answer.reads, answer.err = d.reads(workdir, pkgs, env)
		d.answers[key] = answer
	}
	return answer.reads, answer.err
}

// baseCheckout is the tie of each check at the base commit of --changed, derived as the ties at the
// head are, by the base's own .github/scripts/go-test-reads.sh, in a checkout of the base commit
// that the first ask makes and close removes. A file that the change deleted or moved out of a tie
// is in no tie at the head, so a result carries from the base only while no file of the base's
// tie changed either (specgraph.Citations), which asks only for a check whose test did not run and
// whose tie at the head covers no changed path: a run where each test ran makes no checkout, and
// citations makes none for a base that is the head, whose tie is the one at the head.
type baseCheckout struct {
	repo, commit string
	dir          string // the checkout's directory, once made
	err          error  // why the checkout could not be made
	ties         *derivedTies
}

// tie is the tie of c at the base commit, Known false with the cause when it cannot be derived.
func (b *baseCheckout) tie(c specgraph.Check) specgraph.Tie {
	if b.dir == "" && b.err == nil {
		if b.dir, b.err = checkout(b.repo, b.commit); b.err == nil {
			b.ties = newDerivedTies(filepath.Join(b.dir, "tree"))
		}
	}
	if b.err != nil {
		return specgraph.Tie{Why: b.err.Error()}
	}
	return b.ties.tie(c)
}

// close removes the checkout of the base commit.
func (b *baseCheckout) close() {
	if b.dir != "" {
		os.RemoveAll(b.dir)
	}
}

// checkout writes the tree of commit of the clone at repo to <dir>/tree of a new directory, as git
// checks it out, links and modes included, and changes nothing of the clone: git read-tree into an
// index file of the new directory, then git checkout-index of that index.
func checkout(repo, commit string) (string, error) {
	dir, err := os.MkdirTemp("", "spec-check-base-")
	if err != nil {
		return "", fmt.Errorf("no checkout of the base commit %s: %w", commit, err)
	}
	for _, args := range [][]string{
		{"read-tree", commit},
		{"checkout-index", "--all", "--prefix=" + filepath.Join(dir, "tree") + string(filepath.Separator)},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "index"))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("no checkout of the base commit %s: git %s: %w: %s", commit, args[0], err, strings.Join(strings.Fields(stderr.String()), " "))
		}
	}
	return dir, nil
}
