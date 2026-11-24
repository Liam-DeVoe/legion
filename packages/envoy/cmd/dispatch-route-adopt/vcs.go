package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// jjOutput runs jj in dir and answers its stdout, failing with its stderr.
func jjOutput(dir string, args ...string) (string, error) {
	command := exec.Command("jj", args...)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("jj %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// jjLine runs jj in dir and answers its stdout's one line.
func jjLine(dir string, args ...string) (string, error) {
	output, err := jjOutput(dir, args...)
	return strings.TrimSpace(output), err
}

// mergeSides is the merge in progress as two commits and their names in the report.
type mergeSides struct {
	first, second         string
	firstName, secondName string
}

// sides finds the merge in progress: HEAD and MERGE_HEAD while `git merge` has stopped, or the
// two parents of jj's working-copy commit, which `jj new <branch> main@origin` makes.
func (g gitRepo) sides() (mergeSides, error) {
	if !g.jj {
		mergeHead, err := g.line("rev-parse", "-q", "--verify", "MERGE_HEAD")
		if err != nil || mergeHead == "" {
			return mergeSides{}, errors.New("no merge is in progress: run this while `git merge origin/main` has stopped")
		}
		head, err := g.line("rev-parse", "HEAD")
		if err != nil {
			return mergeSides{}, err
		}
		return mergeSides{head, mergeHead, "HEAD", "MERGE_HEAD"}, nil
	}
	output, err := jjOutput(g.root, "log", "--no-graph", "-r", "parents(@)", "-T", `commit_id ++ "\n"`)
	if err != nil {
		return mergeSides{}, err
	}
	parents := strings.Fields(output)
	if len(parents) != 2 {
		return mergeSides{}, fmt.Errorf("jj's working-copy commit has %d parents, and a merge has two: run this with the merge as @, after `jj new <branch> main@origin`", len(parents))
	}
	return mergeSides{parents[0], parents[1], "@'s first parent " + parents[0][:12], "@'s second parent " + parents[1][:12]}, nil
}

// stage records files of package api written or removed: git adds them to its index; jj takes the
// working copy as it is when its next command runs, so nothing is done for it.
func (g gitRepo) stage(names, removed []string) error {
	if g.jj {
		for _, name := range removed {
			if err := os.Remove(filepath.Join(g.root, apiDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	}
	if len(names) > 0 {
		if _, err := g.run(append([]string{"add", "--"}, prefixed(names)...)...); err != nil {
			return err
		}
	}
	// A route the branch removes is a file the incoming side added, so git's index holds it as a
	// staged addition, which a plain `git rm` refuses to drop. adopt removes only a file whose bytes
	// are the merge base's own, or the fixture the new layout no longer commits, so nobody's change
	// is lost: the index entry is forced out and the file deleted.
	if len(removed) > 0 {
		if _, err := g.run(append([]string{"rm", "-q", "-f", "--ignore-unmatch", "--"}, prefixed(removed)...)...); err != nil {
			return err
		}
	}
	return nil
}

// prefixed names each file of package api from the repository's root.
func prefixed(names []string) []string {
	paths := make([]string, len(names))
	for index, name := range names {
		paths[index] = path.Join(apiDir, name)
	}
	return paths
}

// hasConflictMarkers reports whether a file holds a conflict git or jj wrote into it: a line that
// begins with seven '<' (git's `<<<<<<< ours`, jj's `<<<<<<< conflict 1 of 1`).
func hasConflictMarkers(content []byte) bool {
	for line := range strings.Lines(string(content)) {
		if strings.HasPrefix(line, "<<<<<<< ") || strings.TrimRight(line, "\n") == "<<<<<<<" {
			return true
		}
	}
	return false
}
