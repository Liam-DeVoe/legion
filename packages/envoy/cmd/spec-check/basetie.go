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

// pathList is a flag that may be given more than once, each value a path.
type pathList []string

func (p *pathList) String() string { return strings.Join(*p, " ") }

func (p *pathList) Set(value string) error {
	*p = append(*p, value)
	return nil
}

// pointers is a pointer to each path, so the command can make each one absolute in place.
func (p pathList) pointers() []*string {
	out := make([]*string, len(p))
	for i := range p {
		out[i] = &p[i]
	}
	return out
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
			checks := ties[commit]
			switch {
			case !objectName(commit):
				return nil, fmt.Errorf("%s: commit %q: not a full object name of 40 or 64 lower-case hex digits", path, commit)
			case from[commit] != "":
				return nil, fmt.Errorf("%s: commit %s: %s holds its ties too", path, commit, from[commit])
			}
			all[commit], from[commit] = checks, path
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

// baseTies is the tie of each check at the base commit of --changed. A file that the change deleted
// or moved out of a tie is in no tie at the head, so a result carries from the base only while no
// file of the base's tie changed either (specgraph.Citations). With --ties the ties are those
// files' ties of the base commit. Else they are derived on the first ask, as the head's are:
// specgraph.GoTies with the base's own .github/scripts/go-test-reads.sh, in a checkout of the base
// commit that the command removes when it ends. Citations asks only for a check whose test did not
// run and whose tie at the head holds no changed path, so a run where each test ran makes no
// checkout.
type baseTies struct {
	repo, commit string
	files        specgraph.CommitTies // the ties of --ties, or nil
	dir          string               // the checkout's directory, once made
	err          error                // why the checkout could not be made
	answers      map[string]readsAnswer
}

// readsAnswer is one answer of go-test-reads.sh at the base, kept so each package asks once.
type readsAnswer struct {
	reads []string
	err   error
}

// tie is the tie of c at the base commit, Known false with the cause when it cannot be derived.
func (b *baseTies) tie(c specgraph.Check) specgraph.Tie {
	if b.files != nil {
		return fileTie(b.files, b.commit, c.ID)
	}
	if b.dir == "" && b.err == nil {
		b.dir, b.err = checkout(b.repo, b.commit)
		b.answers = map[string]readsAnswer{}
	}
	if b.err != nil {
		return specgraph.Tie{Why: b.err.Error()}
	}
	script := specgraph.ScriptTestReads(filepath.Join(b.dir, "tree"))
	reads := func(workdir string, pkgs, env []string) ([]string, error) {
		key := strings.Join(append(append([]string{workdir}, pkgs...), env...), "\x00")
		answer, asked := b.answers[key]
		if !asked {
			answer.reads, answer.err = script(workdir, pkgs, env)
			b.answers[key] = answer
		}
		return answer.reads, answer.err
	}
	return specgraph.GoTies([]specgraph.Check{c}, ".", nil, reads)[c.ID]
}

// close removes the checkout of the base commit.
func (b *baseTies) close() {
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
