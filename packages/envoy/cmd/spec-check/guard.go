package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// workflowsDir is the directory of a repository, from its root, that holds its workflows.
const workflowsDir = ".github/workflows"

// ciDirs are the directories of a repository, from its root, that guard reads: the workflows and
// their actions, and the scripts of .github/scripts and scripts. A step can run a file outside
// them, such as a package script or a make target, and compare's refusal in a CI job covers that
// route (compare.go).
var ciDirs = []string{workflowsDir, ".github/actions", ".github/scripts", "scripts"}

// runGuard is "spec-check guard --root <dir>": it reads every regular file under the CI
// directories of the repository at dir, and answers 0 when no line of them runs the comparison of
// the files with the approved spec, 1 when one does or when it read no workflow, and 2 for a
// directory or a file it cannot read or arguments it does not take.
func runGuard(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("guard", flag.ContinueOnError)
	rootFlag := flags.String("root", "", "the root directory of the repository")
	if code, ok := parse(flags, args, stderr, "root"); !ok {
		return code
	}
	root, ok := absolute(stderr, *rootFlag)
	if !ok {
		return 2
	}
	switch info, err := os.Stat(root); {
	case errors.Is(err, fs.ErrNotExist):
		say(stderr, "%s: does not exist", root)
		return 2
	case err != nil:
		say(stderr, "%s: cannot be read: %s", root, err)
		return 2
	case !info.IsDir():
		say(stderr, "%s: is not a directory", root)
		return 2
	}

	files := map[string][]byte{}
	workflows := 0
	for _, dir := range ciDirs {
		top := filepath.Join(root, filepath.FromSlash(dir))
		err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist) && path == top:
				return nil // a repository need not have each CI directory
			case err != nil:
				return fmt.Errorf("%s: cannot be read: %w", path, err)
			case !d.Type().IsRegular():
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("%s: cannot be read: %w", path, err)
			}
			key, err := filepath.Rel(root, path)
			if err != nil {
				return fmt.Errorf("%s: cannot be read: %w", path, err)
			}
			files[filepath.ToSlash(key)] = content
			if dir == workflowsDir {
				workflows++
			}
			return nil
		})
		if err != nil {
			say(stderr, "%s", err)
			return 2
		}
	}

	hits := specgraph.CIFilesThatCompare(files)
	for _, hit := range hits {
		say(stderr, "%s: this line runs the comparison of the files with the approved spec, which no CI job does (R2)", filepath.Join(root, hit))
	}
	switch {
	case workflows == 0:
		// An instrument that read no workflow has not passed: it would say the same of a
		// repository whose workflows run the comparison.
		say(stderr, "%s: read no workflow", root)
		return 1
	case len(hits) > 0:
		verb := "run"
		if len(hits) == 1 {
			verb = "runs"
		}
		say(stdout, "%s: %s of CI files %s the comparison", root, count(len(hits), "line"), verb)
		return 1
	}
	say(stdout, "%s: no line of %s runs the comparison", root, count(len(files), "CI file"))
	return 0
}
