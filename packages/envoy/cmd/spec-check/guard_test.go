package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardHit is the end of guard's line for each place that it names, after "spec-check:
// <path>:<line>".
const guardHit = ": this line holds the command that compares the files with the approved spec, and no CI job runs that command (R2). If no shell runs this line, reword it.\n"

// testGuard runs guard over the CI-file fixtures of testdata/ci-files and over trees it makes: it
// names each place of a CI file that holds the command that compares the files with the approved
// spec (R2: "No CI job compares the files"), and an instrument that read no workflow does not
// pass.
func testGuard(t *testing.T, binary string) {
	t.Run("guard passes CI files that do not hold the comparison", func(t *testing.T) {
		t.Parallel()
		root := ciFiles(t, "clean")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{stdout: "spec-check: " + root + ": no line of 4 CI files holds the command that compares the files\n"})
	})
	t.Run("guard names each line that holds the comparison, from a workflow, an action and a script", func(t *testing.T) {
		t.Parallel()
		root := ciFiles(t, "planted")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 3 lines of CI files hold the command that compares the files\n",
			stderr: "spec-check: " + filepath.Join(root, ".github", "actions", "compare", "action.yml") + ":9" + guardHit +
				"spec-check: " + filepath.Join(root, ".github", "workflows", "compare.yaml") + ":13" + guardHit +
				"spec-check: " + filepath.Join(root, "scripts", "compare.sh") + ":4" + guardHit,
		})
	})
	t.Run("guard names one line in the singular", func(t *testing.T) {
		t.Parallel()
		root, workflow := workflowTree(t, "      - run: |\n          spec-check \\\n            compare --dir .dispatch/spec --approved a.json\n")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), named(root, workflow, 6))
	})
	// A quote, and a line break between the two words, are ignored: each row names the line that
	// holds spec-check.
	t.Run("guard names a quoted subcommand and a value over two lines", func(t *testing.T) {
		t.Parallel()
		for _, row := range []struct {
			name, step string
			line       int
		}{
			{"a quoted subcommand", "      - run: spec-check \"compare\" --dir .dispatch/spec --approved a.json\n", 5},
			{"a plain scalar over two lines", "      - run: spec-check\n          compare --dir .dispatch/spec --approved a.json\n", 5},
			{"a folded scalar over two lines", "      - run: >-\n          spec-check\n          compare --dir .dispatch/spec --approved a.json\n", 6},
		} {
			t.Run(row.name, func(t *testing.T) {
				t.Parallel()
				root, workflow := workflowTree(t, row.step)
				expect(t, runBinary(t, binary, "", "guard", "--root", root), named(root, workflow, row.line))
			})
		}
	})
	// sudo -n and docker run start the command in a CI step past the refusal of compare. The guard
	// names each such line in a workflow, which is the part of R2 that covers them.
	t.Run("guard names a sudo or a container step that runs the comparison", func(t *testing.T) {
		t.Parallel()
		root, workflow := workflowTree(t, "      - run: sudo -n spec-check compare --dir .dispatch/spec --approved a.json\n"+
			"      - run: docker run --rm -v \"$PWD:/w\" -w /w -v \"$RUNNER_TEMP/spec-check:/usr/local/bin/spec-check:ro\" debian:bookworm-slim spec-check compare --dir .dispatch/spec --approved a.json\n")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), named(root, workflow, 5, 6))
	})
	// A $ before a quote is ignored with the quote, so $'compare' is named behind env -u, which
	// takes the step past the refusal of compare. A subcommand that an escape builds, such as
	// $'com\x70are', is not found: compare's refusal stops it in a step that keeps GITHUB_ACTIONS.
	t.Run("guard names a $ quote and not a word that an escape builds", func(t *testing.T) {
		t.Parallel()
		root, workflow := workflowTree(t, "      - run: spec-check compare --dir .dispatch/spec --approved a.json\n"+
			"      - run: env -u GITHUB_ACTIONS spec-check $'compare' --dir .dispatch/spec --approved a.json\n"+
			"      - run: env -u GITHUB_ACTIONS spec-check $'com\\x70are' --dir .dispatch/spec --approved a.json\n"+
			"      - run: spec-check $'tree' --dir .dispatch/spec\n")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), named(root, workflow, 5, 6))
	})
	// A backslash and the line break after it are ignored, with the indentation after them, so a
	// $ and its quote that they split, or a quoted word that they split, is named. A word that an
	// escape builds is not.
	t.Run("guard names a subcommand that a line continuation splits", func(t *testing.T) {
		t.Parallel()
		root, workflow := workflowTree(t, "      - run: |\n"+
			"          env -u GITHUB_ACTIONS spec-check $\\\n"+
			"          'com\\x70are' --dir .dispatch/spec --approved a.json\n"+
			"      - run: |\n"+
			"          env -u GITHUB_ACTIONS spec-check $\\\n"+
			"          \"compare\" --dir .dispatch/spec --approved a.json\n"+
			"      - run: |\n"+
			"          spec-check 'com\\\n"+
			"          pare' --dir .dispatch/spec\n")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), named(root, workflow, 9, 12))
	})
	// A quote in a comment or in a here-document changes nothing for a rule that reads text, so the
	// audit's two inputs, which run the split comparison behind env -u, are named. The plain
	// comparison is named, and the same comment before spec-check tree is not.
	t.Run("guard names a comparison after a quote in a comment or a here-document", func(t *testing.T) {
		t.Parallel()
		root, workflow := workflowTree(t, "      - run: |\n"+
			"          # \" \\\n"+
			"          : \"it's\"; env -u GITHUB_ACTIONS spec-check com\\\n"+
			"          pare --dir .dispatch/spec --approved a.json\n"+
			"      - run: |\n"+
			"          cat <<'END\\'\n"+
			"          \" \\\n"+
			"          END\\\n"+
			"          : \"it's\"; env -u GITHUB_ACTIONS spec-check com\\\n"+
			"          pare --dir .dispatch/spec --approved a.json\n"+
			"      - run: spec-check compare --dir .dispatch/spec --approved a.json\n"+
			"      - run: |\n"+
			"          # \" \\\n"+
			"          : \"it's\"; env -u GITHUB_ACTIONS spec-check tree --dir .dispatch/spec\n")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), named(root, workflow, 7, 13, 15))
	})
	// A quoted text that no shell runs is named too, and the line tells its writer to reword it.
	t.Run("guard tells the writer to reword a line that no shell runs", func(t *testing.T) {
		t.Parallel()
		root, workflow := workflowTree(t, "      - run: echo 'run spec-check compare by hand'\n")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), named(root, workflow, 5))
	})
	t.Run("guard does not pass when it reads no workflow", func(t *testing.T) {
		t.Parallel()
		root := ciFiles(t, "no-workflow")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{code: 1, stderr: "spec-check: " + root + ": read no workflow\n"})
		// A workflows directory that holds only an empty directory, beside an action: no workflow.
		empty := t.TempDir()
		for _, dir := range []string{filepath.Join(".github", "workflows", "nested"), filepath.Join(".github", "actions", "spec")} {
			if err := os.MkdirAll(filepath.Join(empty, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, filepath.Join(empty, ".github", "actions", "spec", "action.yml"), []byte("runs:\n  using: composite\n"))
		expect(t, runBinary(t, binary, "", "guard", "--root", empty), result{code: 1, stderr: "spec-check: " + empty + ": read no workflow\n"})
	})
	t.Run("guard names a relative root by its absolute path", func(t *testing.T) {
		t.Parallel()
		root := ciFiles(t, "clean")
		expect(t, runBinary(t, binary, filepath.Dir(root), "guard", "--root", "clean"), result{stdout: "spec-check: " + root + ": no line of 4 CI files holds the command that compares the files\n"})
	})
	t.Run("guard exits 2 on a root it cannot read", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "missing")
		expect(t, runBinary(t, binary, "", "guard", "--root", missing), result{code: 2, stderr: "spec-check: " + missing + ": does not exist\n"})
		file := filepath.Join(t.TempDir(), "file")
		writeFile(t, file, []byte("A file.\n"))
		expect(t, runBinary(t, binary, "", "guard", "--root", file), result{code: 2, stderr: "spec-check: " + file + ": is not a directory\n"})
	})
	t.Run("guard exits 2 on a CI directory it cannot read", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory of mode 000")
		}
		root := t.TempDir()
		workflows := filepath.Join(root, ".github", "workflows")
		if err := os.MkdirAll(workflows, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(workflows, "spec.yaml"), []byte("on: pull_request\n"))
		locked := filepath.Join(root, "scripts")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   2,
			stderr: "spec-check: " + locked + ": cannot be read: open " + locked + ": permission denied\n",
		})
	})
}

// The guard item of packages/envoy/AGENTS.md says that the guard names a text that no shell runs,
// and tells its writer the way past it: reword the line.
func TestTheGuideTellsTheWriterToRewordALineThatNoShellRuns(t *testing.T) {
	t.Parallel()
	guide, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, item, ok := strings.Cut(string(guide), "\n- `spec-check guard --root <dir>`")
	if !ok {
		t.Fatal("packages/envoy/AGENTS.md holds no guard item")
	}
	item, _, _ = strings.Cut(item, "\n- ")
	if !strings.Contains(item, "no shell runs") || !strings.Contains(item, "reword") {
		t.Errorf("the guard item of packages/envoy/AGENTS.md does not say that it names a text that no shell runs and that its writer rewords that line:\n%s", item)
	}
}

// workflowTree is a repository root that holds one workflow, .github/workflows/spec.yaml, whose
// one job's steps follow its head from line 5 on, and the workflow's path.
func workflowTree(t *testing.T, steps string) (root, workflow string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	workflow = filepath.Join(root, ".github", "workflows", "spec.yaml")
	writeFile(t, workflow, []byte("on: pull_request\njobs:\n  spec:\n    steps:\n"+steps))
	return root, workflow
}

// named is guard's result over root when it names each of lines of workflow.
func named(root, workflow string, lines ...int) result {
	var stderr strings.Builder
	for _, n := range lines {
		fmt.Fprintf(&stderr, "spec-check: %s:%d%s", workflow, n, guardHit)
	}
	verb := "hold"
	if len(lines) == 1 {
		verb = "holds"
	}
	return result{
		code:   1,
		stdout: fmt.Sprintf("spec-check: %s: %s of CI files %s the command that compares the files\n", root, count(len(lines), "line"), verb),
		stderr: stderr.String(),
	}
}

// ciFiles is the absolute path of testdata/ci-files/<name>, the CI directories of a repository as
// guard reads them.
func ciFiles(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "ci-files", name))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
