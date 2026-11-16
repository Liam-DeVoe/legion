package main

import (
	"os"
	"path/filepath"
	"testing"
)

// testGuard runs guard over the CI-file fixtures of testdata/ci-files and over trees it makes: it
// names each line of a CI file that runs the comparison of the files with the approved spec (R2:
// "No CI job compares the files"), and an instrument that read no workflow does not pass.
func testGuard(t *testing.T, binary string) {
	t.Run("guard passes CI files that do not run the comparison", func(t *testing.T) {
		t.Parallel()
		root := ciFiles(t, "clean")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{stdout: "spec-check: " + root + ": no line of 4 CI files runs the comparison\n"})
	})
	t.Run("guard names each line that runs the comparison, from a workflow, an action and a script", func(t *testing.T) {
		t.Parallel()
		root := ciFiles(t, "planted")
		line := ": this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n"
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 3 lines of CI files run the comparison\n",
			stderr: "spec-check: " + filepath.Join(root, ".github", "actions", "compare", "action.yml") + ":9" + line +
				"spec-check: " + filepath.Join(root, ".github", "workflows", "compare.yaml") + ":13" + line +
				"spec-check: " + filepath.Join(root, "scripts", "compare.sh") + ":4" + line,
		})
	})
	t.Run("guard names one line in the singular", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, ".github", "workflows", "spec.yaml"), []byte("on: pull_request\njobs:\n  spec:\n    steps:\n      - run: |\n          spec-check \\\n            compare --dir .dispatch/spec --approved a.json\n"))
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 1 line of CI files runs the comparison\n",
			stderr: "spec-check: " + filepath.Join(root, ".github", "workflows", "spec.yaml") + ":6: this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n",
		})
	})
	t.Run("guard reads a run value as YAML and a word as the shell reads them", func(t *testing.T) {
		t.Parallel()
		line := ": this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n"
		for _, row := range []struct{ name, step string }{
			{"a quoted subcommand", "      - run: spec-check \"compare\" --dir .dispatch/spec --approved a.json\n"},
			{"a plain scalar over two lines", "      - run: spec-check\n          compare --dir .dispatch/spec --approved a.json\n"},
			{"a folded scalar over two lines", "      - run: >-\n          spec-check\n          compare --dir .dispatch/spec --approved a.json\n"},
		} {
			t.Run(row.name, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
					t.Fatal(err)
				}
				workflow := filepath.Join(root, ".github", "workflows", "spec.yaml")
				writeFile(t, workflow, []byte("on: pull_request\njobs:\n  spec:\n    steps:\n"+row.step))
				expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
					code:   1,
					stdout: "spec-check: " + root + ": 1 line of CI files runs the comparison\n",
					stderr: "spec-check: " + workflow + ":5" + line,
				})
			})
		}
	})
	// sudo -n and docker run start the command in a CI step past the refusal of compare. The guard
	// names each such line in a workflow, which is the part of R2 that covers them.
	t.Run("guard names a sudo or a container step that runs the comparison", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		workflow := filepath.Join(root, ".github", "workflows", "spec.yaml")
		writeFile(t, workflow, []byte("on: pull_request\njobs:\n  spec:\n    steps:\n"+
			"      - run: sudo -n spec-check compare --dir .dispatch/spec --approved a.json\n"+
			"      - run: docker run --rm -v \"$PWD:/w\" -w /w -v \"$RUNNER_TEMP/spec-check:/usr/local/bin/spec-check:ro\" debian:bookworm-slim spec-check compare --dir .dispatch/spec --approved a.json\n"))
		line := ": this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n"
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 2 lines of CI files run the comparison\n",
			stderr: "spec-check: " + workflow + ":5" + line + "spec-check: " + workflow + ":6" + line,
		})
	})
	// Bash's literal quoting gives the command the word compare: $'compare' and $'com\x70are' run
	// the comparison as the plain form does, and env -u takes the step past the refusal of compare.
	// A literal that decodes to another subcommand runs no comparison.
	t.Run("guard names a subcommand in Bash's literal quoting", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		workflow := filepath.Join(root, ".github", "workflows", "spec.yaml")
		writeFile(t, workflow, []byte("on: pull_request\njobs:\n  spec:\n    steps:\n"+
			"      - run: spec-check compare --dir .dispatch/spec --approved a.json\n"+
			"      - run: env -u GITHUB_ACTIONS spec-check $'compare' --dir .dispatch/spec --approved a.json\n"+
			"      - run: env -u GITHUB_ACTIONS spec-check $'com\\x70are' --dir .dispatch/spec --approved a.json\n"+
			"      - run: spec-check $'tree' --dir .dispatch/spec\n"))
		line := ": this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n"
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 3 lines of CI files run the comparison\n",
			stderr: "spec-check: " + workflow + ":5" + line + "spec-check: " + workflow + ":6" + line + "spec-check: " + workflow + ":7" + line,
		})
	})
	// Bash removes a backslash and the line break after it before it reads the words, so a step
	// that splits $ from its quote that way runs spec-check compare, and env -u takes it past the
	// refusal of compare. Inside single quotes the two stay, and that word is no compare.
	t.Run("guard names a subcommand that a line continuation splits", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		workflow := filepath.Join(root, ".github", "workflows", "spec.yaml")
		writeFile(t, workflow, []byte("on: pull_request\njobs:\n  spec:\n    steps:\n"+
			"      - run: |\n"+
			"          env -u GITHUB_ACTIONS spec-check $\\\n"+
			"          'com\\x70are' --dir .dispatch/spec --approved a.json\n"+
			"      - run: |\n"+
			"          env -u GITHUB_ACTIONS spec-check $\\\n"+
			"          \"compare\" --dir .dispatch/spec --approved a.json\n"+
			"      - run: |\n"+
			"          spec-check 'com\\\n"+
			"          pare' --dir .dispatch/spec\n"))
		line := ": this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n"
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 2 lines of CI files run the comparison\n",
			stderr: "spec-check: " + workflow + ":6" + line + "spec-check: " + workflow + ":9" + line,
		})
	})
	// A quote in a comment or in a here-document opens no quote for Bash, so the audit's two inputs
	// run the split comparison, and env -u takes them past the refusal of compare. The plain
	// comparison is named, and the same comment before spec-check tree is not.
	t.Run("guard names a comparison after a quote in a comment or a here-document", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		workflow := filepath.Join(root, ".github", "workflows", "spec.yaml")
		writeFile(t, workflow, []byte("on: pull_request\njobs:\n  spec:\n    steps:\n"+
			"      - run: |\n"+
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
			"          : \"it's\"; env -u GITHUB_ACTIONS spec-check tree --dir .dispatch/spec\n"))
		line := ": this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n"
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 3 lines of CI files run the comparison\n",
			stderr: "spec-check: " + workflow + ":7" + line + "spec-check: " + workflow + ":13" + line + "spec-check: " + workflow + ":15" + line,
		})
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
		expect(t, runBinary(t, binary, filepath.Dir(root), "guard", "--root", "clean"), result{stdout: "spec-check: " + root + ": no line of 4 CI files runs the comparison\n"})
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
