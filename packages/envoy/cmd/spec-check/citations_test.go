package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
)

// The check of every citations row, whose block opens at line 5 of spec.md, and its statement.
const (
	citedCheck   = "the-answer-is-forty-two"
	citedSpecTop = ":::requirement{#the-answer-is-known kind=\"behavior\"}\nThe service answers each question with the known answer.\n:::\n\n"
	citedSpecEnd = "Claim: The service answers forty-two.\nProcedure: The program asks the service and reads the answer.\nIt must find forty-two.\nFailing example: A service that answers zero.\n:::\n"
)

// citedSpec is spec.md with the program check whose test part is attrs: runner, selector and job,
// or "" for none.
func citedSpec(attrs string) string {
	if attrs != "" {
		attrs = " " + attrs
	}
	return citedSpecTop + ":::check{#" + citedCheck + " serves=\"the-answer-is-known\" subject=\"code\" procedure=\"program\"" + attrs + "}\n" + citedSpecEnd
}

// testPart is the test part of the check: go, selector and job.
func testPart(selector, job string) string {
	return "runner=\"go\" selector=\"" + selector + "\" job=\"" + job + "\""
}

// goReport is a go test -json report of TestA of package a with outcome, or a run with no end for "".
func goReport(outcome string) string {
	report := `{"Action":"run","Package":"example.com/citations/a","Test":"TestA"}` + "\n"
	if outcome != "" {
		report += `{"Action":"` + outcome + `","Package":"example.com/citations/a","Test":"TestA","Elapsed":0.01}` + "\n"
	}
	return report
}

// needsOf is toJSON(needs) of a run where changes succeeded and go-checks ended with result.
func needsOf(result string) string {
	return `{"changes":{"result":"success","outputs":{}},"go-checks":{"result":"` + result + `","outputs":{}}}`
}

// citationRun is the input of one run of citations: a copy of testdata/citations/repo with spec.md
// in its .dispatch/spec, and the files of the flags.
type citationRun struct {
	spec       string
	changed    string            // changed.txt; ignored with allChanged
	allChanged bool              // --all-changed in place of --changed
	reports    map[string]string // each directory of reports/ by name, with its report.jsonl
	needs      string
	ties       string // ties.json, or "" for no --ties
	extra      []string
}

// newCitationRun is the base: the check's test is TestA of package a, run by go-checks; nothing
// changed; go-checks succeeded and its report holds TestA as a pass; the tie is a/* and
// fixtures/a.json.
func newCitationRun() citationRun {
	return citationRun{
		spec:    citedSpec(testPart("example.com/citations/a TestA", "go-checks")),
		reports: map[string]string{"spec-report--go-checks": goReport("pass")},
		needs:   needsOf("success"),
		ties:    `{"` + citedCheck + `":["a/*","fixtures/a.json"]}`,
	}
}

// citationFiles is where one run's files are.
type citationFiles struct {
	root, repo, dir, changed, reports, needs, ties, json string
}

// write writes the run's files in a directory of the test's own and answers where they are.
func (r citationRun) write(t *testing.T) citationFiles {
	t.Helper()
	root := t.TempDir()
	f := citationFiles{root: root, repo: filepath.Join(root, "repo"), changed: filepath.Join(root, "changed.txt"),
		reports: filepath.Join(root, "reports"), needs: filepath.Join(root, "needs.json"), ties: filepath.Join(root, "ties.json"),
		json: filepath.Join(root, "out", "faults.json")}
	if err := os.CopyFS(f.repo, os.DirFS(fixture(t, "citations", "repo"))); err != nil {
		t.Fatal(err)
	}
	f.dir = filepath.Join(f.repo, ".dispatch", "spec")
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if r.spec != "" {
		writeFile(t, filepath.Join(f.dir, "spec.md"), []byte(r.spec))
	}
	writeFile(t, f.changed, []byte(r.changed))
	if r.reports != nil {
		if err := os.Mkdir(f.reports, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, report := range r.reports {
		if err := os.Mkdir(filepath.Join(f.reports, name), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(f.reports, name, "report.jsonl"), []byte(report))
	}
	writeFile(t, f.needs, []byte(r.needs))
	writeFile(t, f.ties, []byte(r.ties))
	return f
}

// args is the command line of the run.
func (r citationRun) args(f citationFiles, workflow string) []string {
	args := []string{"citations", "--dir", f.dir, "--repo", f.repo, "--workflow", workflow}
	if r.allChanged {
		args = append(args, "--all-changed")
	} else {
		args = append(args, "--changed", f.changed)
	}
	args = append(args, "--reports", f.reports, "--needs", f.needs, "--json", f.json)
	if r.ties != "" {
		args = append(args, "--ties", f.ties)
	}
	return append(args, r.extra...)
}

// TestCitationsCommand runs the built binary over each fixture of specgraph's TestCitations
// (v-citations, each run alone), through files on disk: a copy of testdata/citations/repo outside
// the root go.work, so its go commands need no GOWORK setting, a changed.txt, a reports/ tree, a
// needs.json and a ties.json through --ties. Each row asserts the exit status, both streams and the
// --json file byte for byte.
func TestCitationsCommand(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	workflow := fixture(t, "citations", "workflow.yaml")
	const notRun = "test-not-run: "
	for _, row := range []struct {
		name   string
		change func(r *citationRun)
		rule   string // "" when no fault is wanted
		detail string
	}{
		{name: "v-citations 1: the check names a test that someone renamed", change: func(r *citationRun) {
			r.spec = citedSpec(testPart("example.com/citations/a TestRenamed", "go-checks"))
		}, rule: "test-not-in-code", detail: "test-not-in-code: example.com/citations/a TestRenamed is not in the code at this commit"},
		{name: "v-citations 2: code of the tie changed and the job skipped the test", change: func(r *citationRun) {
			r.changed = "a/a.go\n"
			r.reports["spec-report--go-checks"] = goReport("skip")
		}, rule: "test-not-run", detail: notRun + "a/a.go changed, and go-checks did not run example.com/citations/a TestA (its report shows it skipped)"},
		{name: "v-citations 3: a program check names no test", change: func(r *citationRun) {
			r.spec = citedSpec("")
		}, rule: "no-test", detail: "no-test: the check names no test"},
		{name: "v-citations 5: no code of the tie changed and the job skipped the test", change: func(r *citationRun) {
			r.changed = "docs/x.md\n"
			r.reports["spec-report--go-checks"] = goReport("skip")
		}},
		{name: "v-citations 6: the selector picks two tests", change: func(r *citationRun) {
			r.spec = citedSpec(testPart("example.com/citations/b TestB", "go-checks"))
		}, rule: "two-tests", detail: "two-tests: example.com/citations/b TestB picks 2 tests"},
		{name: "code of the tie changed and the job did not run", change: func(r *citationRun) {
			r.changed = "a/a.go\n"
			r.needs = needsOf("skipped")
			r.reports = map[string]string{}
		}, rule: "test-not-run", detail: notRun + "a/a.go changed, and go-checks did not run example.com/citations/a TestA (the job did not run)"},
		{name: "code of the tie changed and the job ran and uploaded no report", change: func(r *citationRun) {
			r.changed = "a/a.go\n"
			r.reports = map[string]string{}
		}, rule: "test-not-run", detail: notRun + "a/a.go changed, and go-checks did not run example.com/citations/a TestA (the job left no report)"},
		{name: "a missing reports directory is no report", change: func(r *citationRun) {
			r.changed = "a/a.go\n"
			r.reports = nil
		}, rule: "test-not-run", detail: notRun + "a/a.go changed, and go-checks did not run example.com/citations/a TestA (the job left no report)"},
		{name: "B5: a read that the repository records for the test changed", change: func(r *citationRun) {
			r.changed = "fixtures/a.json\n"
			r.reports["spec-report--go-checks"] = goReport("skip")
		}, rule: "test-not-run", detail: notRun + "fixtures/a.json changed, and go-checks did not run example.com/citations/a TestA (its report shows it skipped)"},
		{name: "a file moves out of the tie", change: func(r *citationRun) {
			r.changed = "a/data.json\ndocs/data.json\n"
			r.reports["spec-report--go-checks"] = goReport("skip")
		}, rule: "test-not-run", detail: notRun + "a/data.json changed, and go-checks did not run example.com/citations/a TestA (its report shows it skipped)"},
		{name: "the check names a job that the citations job does not wait for", change: func(r *citationRun) {
			r.spec = citedSpec(testPart("example.com/citations/a TestA", "envoy-go"))
		}, rule: "job-not-awaited", detail: `job-not-awaited: the citations job does not wait for "envoy-go", so it cannot read its report`},
		{name: "the check names a job that the workflow does not have", change: func(r *citationRun) {
			r.spec = citedSpec(testPart("example.com/citations/a TestA", "gone-job"))
		}, rule: "job-not-in-workflow", detail: `job-not-in-workflow: no job "gone-job"`},
		{name: "the tie unknown, nothing changed, the test did not run", change: func(r *citationRun) {
			r.ties = "{}"
			r.reports["spec-report--go-checks"] = ""
		}, rule: "test-not-run", detail: notRun + "its tie is unknown (no tie was derived for the check), and go-checks did not run example.com/citations/a TestA (its report does not hold it)"},
		{name: "no base commit, the test ran and passed", change: func(r *citationRun) {
			r.allChanged = true
		}},
		{name: "no base commit, the test skipped", change: func(r *citationRun) {
			r.allChanged = true
			r.reports["spec-report--go-checks"] = goReport("skip")
		}, rule: "test-not-run", detail: notRun + "no base commit, and go-checks did not run example.com/citations/a TestA (its report shows it skipped)"},
		{name: "code of the tie changed and the test could not run", change: func(r *citationRun) {
			r.changed = "a/a.go\n"
			r.reports["spec-report--go-checks"] = goReport("")
		}, rule: "test-not-run", detail: notRun + "a/a.go changed, and go-checks did not run example.com/citations/a TestA (it could not run)"},
		{name: "code of the tie changed and one leg of the job skipped the test", change: func(r *citationRun) {
			r.changed = "a/a.go\n"
			r.reports = map[string]string{"spec-report--go-checks--amd64": goReport("pass"), "spec-report--go-checks--arm64": goReport("skip")}
		}, rule: "test-not-run", detail: notRun + "a/a.go changed, and go-checks (leg arm64) did not run example.com/citations/a TestA (its report shows it skipped)"},
		{name: "code of the tie changed and the test ran", change: func(r *citationRun) {
			r.changed = "a/a.go\n"
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newCitationRun()
			row.change(&r)
			f := r.write(t)
			got := runBinary(t, binary, "", r.args(f, workflow)...)
			want := result{stdout: "spec-check: " + f.dir + ": 1 program check; each is cited\n"}
			wantJSON := "[]\n"
			if row.rule != "" {
				want = result{code: 1,
					stdout: "spec-check: " + f.dir + ": 1 program check; 1 refused\n",
					stderr: "spec-check: " + filepath.Join(f.dir, "spec.md") + ":5: " + citedCheck + ": " + row.detail + "\n"}
				wantJSON = `[{"check":"` + citedCheck + `","rule":"` + row.rule + `","detail":"` + strings.ReplaceAll(row.detail, `"`, `\"`) + `"}]` + "\n"
			}
			expect(t, got, want)
			if content, err := os.ReadFile(f.json); err != nil || string(content) != wantJSON {
				t.Errorf("--json file %q, %v; want %q", content, err, wantJSON)
			}
		})
	}

	t.Run("no program check: each of none is cited", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		r.spec = citedSpecTop + ":::check{#the-answer-is-in-the-records serves=\"the-answer-is-known\" subject=\"production\" procedure=\"query\"}\n" +
			"Claim: The records hold the answer.\nProcedure: The query reads the records.\nIt returns each record with no answer.\nFailing example: A copy of the records with one record with no answer.\n:::\n"
		r.changed = "a/a.go\n"
		r.reports = nil
		f := r.write(t)
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{stdout: "spec-check: " + f.dir + ": 0 program checks; each is cited\n"})
	})

	// Without --ties the tie of a go check is what the repository's go-test-reads.sh says its test
	// reads. The copy holds no such script, so every tie is unknown, and fixture 5, which passes with
	// a known tie, names the check: the instrument says that it could not read the tie.
	t.Run("without --ties, a repository with no go-test-reads.sh has no known tie", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		r.changed = "docs/x.md\n"
		r.reports["spec-report--go-checks"] = goReport("skip")
		r.ties = ""
		f := r.write(t)
		script := filepath.Join(f.repo, ".github", "scripts", "go-test-reads.sh")
		detail := notRun + "its tie is unknown (specgraph: " + script + ": fork/exec " + script + ": no such file or directory), and go-checks did not run example.com/citations/a TestA (its report shows it skipped)"
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{code: 1,
			stdout: "spec-check: " + f.dir + ": 1 program check; 1 refused\n",
			stderr: "spec-check: " + filepath.Join(f.dir, "spec.md") + ":5: " + citedCheck + ": " + detail + "\n"})
	})

	t.Run("a test that the code cannot be searched for is refused, naming go's answer", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		r.spec = citedSpec(testPart("example.com/citations/gone TestA", "go-checks"))
		f := r.write(t)
		got := runBinary(t, binary, "", r.args(f, workflow)...)
		prefix := "spec-check: " + filepath.Join(f.dir, "spec.md") + ":5: " + citedCheck +
			": test-lookup-failed: example.com/citations/gone TestA could not be looked up in the code at this commit (specgraph: go list example.com/citations/gone in " + f.repo + ": "
		if got.code != 1 || got.stdout != "spec-check: "+f.dir+": 1 program check; 1 refused\n" || !strings.HasPrefix(got.stderr, prefix) ||
			strings.Count(got.stderr, "\n") != 1 || !strings.Contains(got.stderr, "example.com/citations/gone") {
			t.Errorf("exit %d, stdout %q, stderr %q; want exit 1, the count line, and one line starting %q", got.code, got.stdout, got.stderr, prefix)
		}
		if content, err := os.ReadFile(f.json); err != nil || !strings.HasPrefix(string(content), `[{"check":"`+citedCheck+`","rule":"test-lookup-failed","detail":"test-lookup-failed: `) {
			t.Errorf("--json file %q, %v; want the test-lookup-failed fault", content, err)
		}
	})

	t.Run("a report of a job that the citations job does not wait for is passed over with a warning", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		r.reports["spec-report--envoy-go"] = goReport("skip")
		f := r.write(t)
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{
			stdout: "spec-check: " + f.dir + ": 1 program check; each is cited\n",
			stderr: "spec-check: warning: " + filepath.Join(f.reports, "spec-report--envoy-go") + ": names no job that spec-citations waits for; passed over\n"})
	})

	t.Run("a directory with no spec file", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		r.spec = ""
		f := r.write(t)
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{code: 2, stderr: "spec-check: " + f.dir + ": holds no spec file\n"})
		r.extra = []string{"--allow-empty"}
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{stdout: "spec-check: " + f.dir + ": holds no spec file; no check to cite\n"})
		if content, err := os.ReadFile(f.json); err != nil || string(content) != "[]\n" {
			t.Errorf("--json file %q, %v; want []", content, err)
		}
		// Git keeps no empty directory, so a repository with no spec file has no directory.
		removeAll(t, f.dir)
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{stdout: "spec-check: " + f.dir + ": holds no spec file; no check to cite\n"})
		r.extra = nil
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{code: 2, stderr: "spec-check: " + f.dir + ": does not exist\n"})
	})

	t.Run("a spec file that does not parse exits 2 with its problem", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		r.spec = "A line outside a block.\n"
		f := r.write(t)
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{code: 2,
			stderr: "spec-check: " + filepath.Join(f.dir, "spec.md") + ":1: -: text-outside-block: a spec file holds only blocks, headings and blank lines\n"})
	})

	// An input that the command cannot read exits 2 with one line naming it, nothing on stdout, and
	// no --json file.
	for _, row := range []struct {
		name   string
		change func(t *testing.T, f citationFiles)
		line   func(f citationFiles) string
	}{
		{"needs.json that is no JSON", func(t *testing.T, f citationFiles) { writeFile(t, f.needs, []byte("x")) },
			func(f citationFiles) string {
				return f.needs + ": cannot be read: invalid character 'x' looking for beginning of value"
			}},
		{"needs.json with no result of an awaited job", func(t *testing.T, f citationFiles) {
			writeFile(t, f.needs, []byte(`{"changes":{"result":"success","outputs":{}}}`))
		}, func(f citationFiles) string {
			return f.needs + `: holds no result of job "go-checks", which spec-citations waits for`
		}},
		{"needs.json with a job that spec-citations does not wait for", func(t *testing.T, f citationFiles) {
			writeFile(t, f.needs, []byte(`{"changes":{"result":"success"},"go-checks":{"result":"success"},"envoy-go":{"result":"success"}}`))
		}, func(f citationFiles) string {
			return f.needs + `: holds job "envoy-go", which spec-citations does not wait for`
		}},
		{"needs.json with a job of no result", func(t *testing.T, f citationFiles) {
			writeFile(t, f.needs, []byte(`{"changes":{"result":"success"},"go-checks":{"outputs":{}}}`))
		}, func(f citationFiles) string {
			return f.needs + `: job "go-checks" has no result`
		}},
		{"a report that is no go test report", func(t *testing.T, f citationFiles) {
			writeFile(t, filepath.Join(f.reports, "spec-report--go-checks", "report.jsonl"), []byte("ok  \texample.com/citations/a\n"))
		}, func(f citationFiles) string {
			return filepath.Join(f.reports, "spec-report--go-checks", "report.jsonl") +
				": cannot be read: specgraph: the go test report holds no event at byte 0: invalid character 'o' looking for beginning of value"
		}},
		{"a report directory with no report.jsonl", func(t *testing.T, f citationFiles) {
			removeAll(t, filepath.Join(f.reports, "spec-report--go-checks", "report.jsonl"))
		}, func(f citationFiles) string {
			path := filepath.Join(f.reports, "spec-report--go-checks", "report.jsonl")
			return path + ": cannot be read: open " + path + ": no such file or directory"
		}},
		{"an entry of reports that is no report artifact", func(t *testing.T, f citationFiles) {
			writeFile(t, filepath.Join(f.reports, "notes.txt"), []byte("A note.\n"))
		}, func(f citationFiles) string {
			return filepath.Join(f.reports, "notes.txt") + ": is no directory named spec-report--<job> or spec-report--<job>--<leg>"
		}},
		{"a ties file with an entry in another form", func(t *testing.T, f citationFiles) {
			writeFile(t, f.ties, []byte(`{"`+citedCheck+`":["p/*.go"]}`))
		}, func(f citationFiles) string {
			return f.ties + `: check "` + citedCheck + `": specgraph: tie entry "p/*.go" is not a repository path, <dir>/* or <dir>/**`
		}},
		{"a changed list with an empty line", func(t *testing.T, f citationFiles) {
			writeFile(t, f.changed, []byte("a/a.go\n\ndocs/x.md\n"))
		}, func(f citationFiles) string {
			return f.changed + ":2: an empty line, where a path was due"
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newCitationRun()
			f := r.write(t)
			row.change(t, f)
			expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{code: 2, stderr: "spec-check: " + row.line(f) + "\n"})
			if _, err := os.Stat(f.json); err == nil {
				t.Errorf("an input it could not read, and a --json file at %s", f.json)
			}
		})
	}
	t.Run("a workflow with no spec-citations job", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		f := r.write(t)
		other := filepath.Join(f.root, "other.yaml")
		writeFile(t, other, []byte("jobs:\n  go-checks:\n    runs-on: ubuntu-24.04\n"))
		expect(t, runBinary(t, binary, "", r.args(f, other)...), result{code: 2,
			stderr: "spec-check: " + other + `: specgraph: the workflow has no job "spec-citations"` + "\n"})
	})

	t.Run("a bad argument exits 2 with the line that names it and the usage", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		f := r.write(t)
		base := []string{"citations", "--dir", f.dir, "--repo", f.repo, "--workflow", workflow, "--reports", f.reports, "--needs", f.needs}
		for _, row := range []struct {
			args []string
			line string
		}{
			{[]string{"citations"}, "citations: --dir is required"},
			{append(slices.Clone(base), "extra"), `citations: takes no argument but its flags, got "extra"`},
			{base, "citations: give --changed <file> or --all-changed"},
			{append(slices.Clone(base), "--changed", f.changed, "--all-changed"), "citations: give --changed <file> or --all-changed, not both"},
			{[]string{"changed"}, "changed: --repo is required"},
			{[]string{"changed", "--repo", f.repo}, "changed: --base is required"},
		} {
			expect(t, runBinary(t, binary, "", row.args...), result{code: 2, stderr: "spec-check: " + row.line + "\n" + usage + "\n"})
		}
	})
}

// TestChanged runs changed in a scratch git repository: commit A holds a/data.json and a/a.go, B is
// A with `git mv a/data.json docs/data.json`, and C adds to B a file whose name holds a line break.
// Each git command, the command's own included, reads no configuration of this machine. C is made
// with git's plumbing (update-index, write-tree, commit-tree), since a git wrapper that checks a
// commit's text can refuse such a name, and the test is about what changed prints.
func TestChanged(t *testing.T) {
	binary := cmdtest.Build(t, "spec-check")
	for name, value := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "ACME", "GIT_AUTHOR_EMAIL": "ci@example.com",
		"GIT_COMMITTER_NAME": "ACME", "GIT_COMMITTER_EMAIL": "ci@example.com",
	} {
		t.Setenv(name, value)
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %q: %v\n%s%s", args, err, out, stderr.String())
		}
		return string(out)
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "a", "data.json"), []byte("{\"answer\": 42}\n"))
	writeFile(t, filepath.Join(repo, "a", "a.go"), []byte("package a\n"))
	git("add", ".")
	git("commit", "-q", "-m", "A")
	a := strings.TrimSpace(git("rev-parse", "HEAD"))
	if err := os.Mkdir(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("mv", "a/data.json", "docs/data.json")
	git("commit", "-q", "-m", "B")
	b := strings.TrimSpace(git("rev-parse", "HEAD"))

	// The control: git's own diff reads the move as a rename and names the new path alone, so a
	// command that left rename detection on would miss a/data.json.
	if got := git("diff", "--name-only", a, b); got != "docs/data.json\n" {
		t.Fatalf("git diff --name-only A B = %q; the control wants docs/data.json alone", got)
	}
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", a, "--head", b), result{
		stdout: "a/data.json\ndocs/data.json\n",
		stderr: "spec-check: " + repo + ": 2 paths changed from " + a + " to " + b + "\n"})
	// --head defaults to HEAD, which is B.
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", a), result{
		stdout: "a/data.json\ndocs/data.json\n",
		stderr: "spec-check: " + repo + ": 2 paths changed from " + a + " to HEAD\n"})
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", b), result{
		stderr: "spec-check: " + repo + ": 0 paths changed from " + b + " to HEAD\n"})

	unknown := strings.Repeat("0", 39) + "1"
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", unknown), result{code: 2,
		stderr: "spec-check: " + repo + ": git cannot read commit " + unknown + "\n"})
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", a, "--head", "--output=x"), result{code: 2,
		stderr: "spec-check: " + repo + ": git cannot read commit --output=x\n"})
	if _, err := os.Stat(filepath.Join(repo, "x")); err == nil {
		t.Error("git read --output=x as an option and wrote a file")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	expect(t, runBinary(t, binary, "", "changed", "--repo", missing, "--base", a), result{code: 2, stderr: "spec-check: " + missing + ": does not exist\n"})

	writeFile(t, filepath.Join(repo, "a", "line\nbreak.go"), []byte("package a\n"))
	blob := strings.TrimSpace(git("hash-object", "-w", "a/line\nbreak.go"))
	git("update-index", "--add", "--cacheinfo", "100644,"+blob+",a/line\nbreak.go")
	c := strings.TrimSpace(git("commit-tree", strings.TrimSpace(git("write-tree")), "-p", b, "-m", "C"))
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", b, "--head", c), result{code: 2,
		stderr: "spec-check: " + repo + `: the path "a/line\nbreak.go" holds a line break, which a list of one path a line cannot hold` + "\n"})
}
