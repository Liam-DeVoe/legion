package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// apiDir is package api, from the repository's root.
const apiDir = "packages/envoy/internal/dispatch/api"

// gitRepo runs git in one checkout.
type gitRepo struct {
	root string
}

// run runs git with args and answers its stdout, failing with its stderr.
func (g gitRepo) run(args ...string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = g.root
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// line runs git and answers its stdout's one line.
func (g gitRepo) line(args ...string) (string, error) {
	output, err := g.run(args...)
	return strings.TrimSpace(string(output)), err
}

// findRepo is the checkout that holds dir.
func findRepo(dir string) (gitRepo, error) {
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		return gitRepo{}, fmt.Errorf("%s is in no git checkout: %w", dir, err)
	}
	return gitRepo{root: strings.TrimSpace(string(output))}, nil
}

// gitTree is package api at one commit.
type gitTree struct {
	repo gitRepo
	rev  string
}

func (t gitTree) read(name string) ([]byte, error) {
	return t.repo.run("show", t.rev+":"+path.Join(apiDir, name))
}

func (t gitTree) filesNaming(word string) ([]string, error) {
	command := exec.Command("git", "grep", "-l", "-w", "-F", "-e", word, t.rev, "--", apiDir+"/")
	command.Dir = t.repo.root
	output, err := command.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && len(output) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("git grep %s at %s: %w", word, t.rev, err)
	}
	var names []string
	for line := range strings.Lines(string(output)) {
		_, file, _ := strings.Cut(strings.TrimSpace(line), ":")
		if path.Dir(file) == apiDir && strings.HasSuffix(file, ".go") {
			names = append(names, path.Base(file))
		}
	}
	return names, nil
}

// dirTree is package api as one directory holds it.
type dirTree string

func (d dirTree) read(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(string(d), name))
}

func (d dirTree) filesNaming(word string) ([]string, error) {
	entries, err := os.ReadDir(string(d))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(string(d), entry.Name()))
		if err != nil {
			return nil, err
		}
		if bytes.Contains(content, []byte(word)) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// routeFilesIn is every route file of a directory, by name, with its bytes.
func routeFilesIn(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, entry := range entries {
		if entry.IsDir() || !isRouteFile(entry.Name()) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = content
	}
	return files, nil
}

// isRouteFile reports whether a file name is one routeFileName writes.
func isRouteFile(name string) bool {
	return strings.HasPrefix(name, "route_") && strings.HasSuffix(name, ".go") && strings.Contains(name, "+")
}

// mergeFile is git merge-file's three-way merge of one file: the merged bytes, and whether they
// hold a conflict.
func (g gitRepo) mergeFile(ours, base, theirs []byte, labels [3]string) ([]byte, bool, error) {
	dir, err := os.MkdirTemp("", "dispatch-route-adopt-")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)
	paths := make([]string, 3)
	for index, content := range [][]byte{ours, base, theirs} {
		paths[index] = filepath.Join(dir, fmt.Sprint(index))
		if err := os.WriteFile(paths[index], content, 0o644); err != nil {
			return nil, false, err
		}
	}
	command := exec.Command("git", "merge-file", "-p",
		"-L", labels[0], "-L", labels[1], "-L", labels[2], paths[0], paths[1], paths[2])
	command.Dir = g.root
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), false, nil
	case errors.As(err, &exit) && exit.ExitCode() > 0 && exit.ExitCode() < 127:
		return stdout.Bytes(), true, nil
	}
	return nil, false, fmt.Errorf("git merge-file: %w: %s", err, strings.TrimSpace(stderr.String()))
}

// unmerged is every path git still holds in conflict, sorted.
func (g gitRepo) unmerged() ([]string, error) {
	output, err := g.run("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var paths []string
	for line := range strings.Lines(string(output)) {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}
