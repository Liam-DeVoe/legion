package main

import (
	"encoding/json"
	"errors"
	"io/fs"
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

// goReport is a go test -json report of TestA of package a with outcome, pass, fail or skip, or a
// run with no end for "", as go test of Go 1.27 writes it: the run and its end each with go test's
// own line, a frame, before the end event (testdata/gotest/pass.jsonl of specgraph is one such
// report), and last the package's own end event, without which no run of the package counts. A
// report of a Go before 1.27 has no frame, and ReadGoTestJSON refuses it. "cached" is a pass that
// go test replayed from its cache, with the package's summary line that says so
// (testdata/gotest/cached/cached.jsonl of specgraph).
func goReport(outcome string) string {
	const pkg = `"Package":"example.com/citations/a","Test":"TestA"`
	report := `{"Action":"run",` + pkg + `}` + "\n" +
		`{"Action":"output",` + pkg + `,"Output":"=== RUN   TestA\n","OutputType":"frame"}` + "\n"
	end := outcome
	if outcome == "cached" {
		end = "pass"
	}
	if outcome != "" {
		report += `{"Action":"output",` + pkg + `,"Output":"--- ` + strings.ToUpper(end) + `: TestA (0.01s)\n","OutputType":"frame"}` + "\n" +
			`{"Action":"` + end + `",` + pkg + `,"Elapsed":0.01}` + "\n"
	}
	if outcome == "cached" {
		report += `{"Action":"output","Package":"example.com/citations/a","Output":"ok  \texample.com/citations/a\t(cached)\n"}` + "\n"
	}
	if end == "fail" || end == "" {
		return report + packageEnd("fail")
	}
	return report + packageEnd("pass")
}

// packageEnd is the end event of package a with action, the last event that go test writes of a
// package.
func packageEnd(action string) string {
	return `{"Action":"` + action + `","Package":"example.com/citations/a","Elapsed":0.01}` + "\n"
}

// needsOf is toJSON(needs) of a run where changes succeeded and go-checks ended with result.
func needsOf(result string) string {
	return `{"changes":{"result":"success","outputs":{}},"go-checks":{"result":"` + result + `","outputs":{}}}`
}

// citationRun is the input of one run of citations: a git repository whose base commit holds a
// copy of testdata/citations/repo with spec.md in its .dispatch/spec, and whose head commit, HEAD,
// has the same tree, and the files of the flags.
type citationRun struct {
	spec       string
	changed    string            // the paths of changed.txt, after its base line; ignored with allChanged
	allChanged bool              // --all-changed in place of --changed
	reports    map[string]string // each directory of reports/ by name, with its report.jsonl
	needs      string
	ties       string // the ties of the head, {"<check>":[…]}, or "" for no --ties
	baseTies   string // the ties of the base, as ties; "" for the head's, "-" for none of the base
	extra      []string
}

// newCitationRun is the base: the check's test is TestA of package a, run by go-checks; nothing
// changed; go-checks succeeded and its report holds TestA as a pass; the tie is a/* and
// fixtures/a.json, at the head and at the base.
func newCitationRun() citationRun {
	return citationRun{
		spec:    citedSpec(testPart("example.com/citations/a TestA", "go-checks")),
		reports: map[string]string{"spec-report--go-checks": goReport("pass")},
		needs:   needsOf("success"),
		ties:    `{"` + citedCheck + `":["a/*","fixtures/a.json"]}`,
	}
}

// citationFiles is where one run's files are, and the base and head commits of its repository.
type citationFiles struct {
	root, repo, dir, changed, reports, needs, ties, json string
	base, head                                           string
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
	gitIn(t, f.repo, "init", "-q")
	f.base = commitAll(t, f.repo, "", "base")
	f.head = commitAll(t, f.repo, f.base, "head")
	writeFile(t, f.changed, []byte("base "+f.base+"\n"+r.changed))
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
	if r.ties != "" {
		ties := `{"` + f.head + `":` + r.ties
		switch r.baseTies {
		case "":
			ties += `,"` + f.base + `":` + r.ties
		case "-":
		default:
			ties += `,"` + f.base + `":` + r.baseTies
		}
		writeFile(t, f.ties, []byte(ties+"}"))
	}
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

// gitIn runs git in dir, reading no configuration of this machine, and answers its output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=ACME", "GIT_AUTHOR_EMAIL=ci@example.com", "GIT_COMMITTER_NAME=ACME", "GIT_COMMITTER_EMAIL=ci@example.com")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %q in %s: %v\n%s%s", args, dir, err, out, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// commitAll commits every file of dir's working tree, on parent ("" for none), with git's plumbing
// (write-tree, commit-tree), makes it HEAD and answers it: a git wrapper that checks a commit's
// text then has no say over a fixture's commit.
func commitAll(t *testing.T, dir, parent, message string) string {
	t.Helper()
	gitIn(t, dir, "add", "-A")
	args := []string{"commit-tree", gitIn(t, dir, "write-tree"), "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	commit := gitIn(t, dir, args...)
	gitIn(t, dir, "update-ref", "HEAD", commit)
	return commit
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
		}, rule: "test-not-run", detail: notRun + "its tie is unknown (--ties holds no tie of the check at commit {head}), and go-checks did not run example.com/citations/a TestA (its report does not hold it)"},
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
		// A pass that go test replayed from its cache did not run at this commit.
		{name: "code of the tie changed and go test replayed the test from its cache", change: func(r *citationRun) {
			r.changed = "a/a.go\n"
			r.reports["spec-report--go-checks"] = goReport("cached")
		}, rule: "test-not-run", detail: notRun + "a/a.go changed, and go-checks did not run example.com/citations/a TestA (its report holds a result that go test replayed from its cache, which did not run at this commit)"},
		{name: "no code of the tie changed and go test replayed the test from its cache", change: func(r *citationRun) {
			r.changed = "docs/x.md\n"
			r.reports["spec-report--go-checks"] = goReport("cached")
		}},
		// A run counts only when the report holds the end of its package, after which go test writes
		// nothing of it: a report cut before that end can lack the line that says the run was cached.
		// Its control is the whole report, the row "code of the tie changed and the test ran".
		{name: "code of the tie changed and the report ends before its package's end", change: func(r *citationRun) {
			r.changed = "a/a.go\n"
			r.reports["spec-report--go-checks"] = strings.TrimSuffix(goReport("pass"), packageEnd("pass"))
		}, rule: "test-not-run", detail: notRun + "a/a.go changed, and go-checks did not run example.com/citations/a TestA (it could not run)"},
		// A changed path counts when the tie at either end holds it.
		{name: "the change deleted the test's testdata directory, which only the tie at the base holds", change: func(r *citationRun) {
			r.baseTies = `{"` + citedCheck + `":["a/*","a/testdata/**","fixtures/a.json"]}`
			r.changed = "a/testdata/answer.json\n"
			r.reports["spec-report--go-checks"] = goReport("skip")
		}, rule: "test-not-run", detail: notRun + "a/testdata/answer.json changed (the tie at the base commit holds it), and go-checks did not run example.com/citations/a TestA (its report shows it skipped)"},
		{name: "--ties holds no ties of the base commit", change: func(r *citationRun) {
			r.baseTies = "-"
			r.changed = "docs/x.md\n"
			r.reports["spec-report--go-checks"] = goReport("skip")
		}, rule: "test-not-run", detail: notRun + "its tie at the base commit is unknown (--ties holds no ties of commit {base}), and go-checks did not run example.com/citations/a TestA (its report shows it skipped)"},
		// A result carries from the base only while the tie is known at both ends, also when no path
		// changed. The control: both ties known, nothing changed, and the skipped test is cited.
		{name: "--ties holds no ties of the base commit, and nothing changed", change: func(r *citationRun) {
			r.baseTies = "-"
			r.reports["spec-report--go-checks"] = goReport("skip")
		}, rule: "test-not-run", detail: notRun + "its tie at the base commit is unknown (--ties holds no ties of commit {base}), and go-checks did not run example.com/citations/a TestA (its report shows it skipped)"},
		{name: "control: --ties holds both ties, and nothing changed", change: func(r *citationRun) {
			r.reports["spec-report--go-checks"] = goReport("skip")
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
				detail := strings.NewReplacer("{head}", f.head, "{base}", f.base).Replace(row.detail)
				want = result{code: 1,
					stdout: "spec-check: " + f.dir + ": 1 program check; 1 refused\n",
					stderr: "spec-check: " + filepath.Join(f.dir, "spec.md") + ":5: " + citedCheck + ": " + detail + "\n"}
				wantJSON = `[{"check":"` + citedCheck + `","rule":"` + row.rule + `","detail":"` + strings.ReplaceAll(detail, `"`, `\"`) + `"}]` + "\n"
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
		// The words after the script's path are os/exec's and the system's, so the row reads ours.
		prefix := "spec-check: " + filepath.Join(f.dir, "spec.md") + ":5: " + citedCheck + ": " + notRun + "its tie is unknown (specgraph: " + script + ": "
		suffix := "), and go-checks did not run example.com/citations/a TestA (its report shows it skipped)\n"
		got := runBinary(t, binary, "", r.args(f, workflow)...)
		if got.code != 1 || got.stdout != "spec-check: "+f.dir+": 1 program check; 1 refused\n" ||
			!strings.HasPrefix(got.stderr, prefix) || !strings.HasSuffix(got.stderr, suffix) || strings.Count(got.stderr, "\n") != 1 {
			t.Errorf("exit %d, stdout %q, stderr %q; want exit 1, the count line, and one line %q…%q", got.code, got.stdout, got.stderr, prefix, suffix)
		}
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

	// The --json file is written by printJSON, the writer of the JSON of checks and state: the "[" of
	// each "##[" in a value is \u005b, which a JSON reader reads back as "[".
	t.Run("the --json file writes the [ of a ##[ as checks writes its JSON", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		r.spec = citedSpec(testPart("example.com/citations/a TestA", "##[error]forged"))
		f := r.write(t)
		got := runBinary(t, binary, "", r.args(f, workflow)...)
		if got.code != 1 || got.stdout != "spec-check: "+f.dir+": 1 program check; 1 refused\n" || strings.Contains(got.stderr, "##[") {
			t.Errorf("exit %d, stdout %q, stderr %q; want exit 1, the count line, and no \"##[\"", got.code, got.stdout, got.stderr)
		}
		content, err := os.ReadFile(f.json)
		want := `[{"check":"` + citedCheck + `","rule":"job-not-in-workflow","detail":"job-not-in-workflow: no job \"##\u005berror]forged\""}]` + "\n"
		if err != nil || string(content) != want {
			t.Fatalf("--json file %q, %v; want %q", content, err, want)
		}
		var faults []struct{ Check, Rule, Detail string }
		if err := json.Unmarshal(content, &faults); err != nil || len(faults) != 1 || faults[0].Detail != `job-not-in-workflow: no job "##[error]forged"` {
			t.Errorf("the --json file reads as %+v, %v; want the job as written", faults, err)
		}
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
	// no --json file. The line starts with the words of the command (line); where a library's words
	// follow them (more), the row reads the command's alone, since a library may word its errors
	// otherwise in another version or process.
	for _, row := range []struct {
		name   string
		change func(t *testing.T, f citationFiles)
		line   func(f citationFiles) string
		more   bool
	}{
		{"needs.json that is no JSON", func(t *testing.T, f citationFiles) { writeFile(t, f.needs, []byte("x")) },
			func(f citationFiles) string { return f.needs + ": cannot be read: " }, true},
		{"needs.json with no result of an awaited job", func(t *testing.T, f citationFiles) {
			writeFile(t, f.needs, []byte(`{"changes":{"result":"success","outputs":{}}}`))
		}, func(f citationFiles) string {
			return f.needs + `: holds no result of job "go-checks", which spec-citations waits for`
		}, false},
		{"needs.json with a job that spec-citations does not wait for", func(t *testing.T, f citationFiles) {
			writeFile(t, f.needs, []byte(`{"changes":{"result":"success"},"go-checks":{"result":"success"},"envoy-go":{"result":"success"}}`))
		}, func(f citationFiles) string {
			return f.needs + `: holds job "envoy-go", which spec-citations does not wait for`
		}, false},
		{"needs.json with a job of no result", func(t *testing.T, f citationFiles) {
			writeFile(t, f.needs, []byte(`{"changes":{"result":"success"},"go-checks":{"outputs":{}}}`))
		}, func(f citationFiles) string {
			return f.needs + `: job "go-checks" has no result`
		}, false},
		{"a report that is no go test report", func(t *testing.T, f citationFiles) {
			writeFile(t, filepath.Join(f.reports, "spec-report--go-checks", "report.jsonl"), []byte("ok  \texample.com/citations/a\n"))
		}, func(f citationFiles) string {
			return filepath.Join(f.reports, "spec-report--go-checks", "report.jsonl") +
				": cannot be read: specgraph: the go test report holds no event at byte 0: "
		}, true},
		{"a report directory with no report.jsonl", func(t *testing.T, f citationFiles) {
			removeAll(t, filepath.Join(f.reports, "spec-report--go-checks", "report.jsonl"))
		}, func(f citationFiles) string {
			return filepath.Join(f.reports, "spec-report--go-checks", "report.jsonl") + ": cannot be read: "
		}, true},
		{"an entry of reports that is no report artifact", func(t *testing.T, f citationFiles) {
			writeFile(t, filepath.Join(f.reports, "notes.txt"), []byte("A note.\n"))
		}, func(f citationFiles) string {
			return filepath.Join(f.reports, "notes.txt") + ": is no directory named spec-report--<job> or spec-report--<job>--<leg>"
		}, false},
		{"a ties file with an entry in another form", func(t *testing.T, f citationFiles) {
			writeFile(t, f.ties, []byte(`{"`+f.head+`":{"`+citedCheck+`":["p/*.go"]}}`))
		}, func(f citationFiles) string {
			return f.ties + `: commit "` + f.head + `": check "` + citedCheck + `": specgraph: tie entry "p/*.go" is not a repository path, <dir>/* or <dir>/**`
		}, false},
		{"a ties file in the form by check, with no commit", func(t *testing.T, f citationFiles) {
			writeFile(t, f.ties, []byte(`{"`+citedCheck+`":["a/*"]}`))
		}, func(f citationFiles) string { return f.ties + ": " }, true},
		{"a ties file that names a commit by a short name", func(t *testing.T, f citationFiles) {
			writeFile(t, f.ties, []byte(`{"`+f.head[:12]+`":{"`+citedCheck+`":["a/*"]}}`))
		}, func(f citationFiles) string {
			return f.ties + `: commit "` + f.head[:12] + `": not a full object name of 40 or 64 lower-case hex digits`
		}, false},
		// Of the commits of one file, the first in sorted order is judged first, so a refusal names
		// the same commit on each run, though a map holds the ten in no order.
		{"a ties file of ten commits by short names names the first in sorted order", func(t *testing.T, f citationFiles) {
			var entries []string
			for _, commit := range strings.Fields("c9 c8 c7 c6 c5 c4 c3 c2 c1 c0") {
				entries = append(entries, `"`+commit+`":{"`+citedCheck+`":["a/*"]}`)
			}
			writeFile(t, f.ties, []byte("{"+strings.Join(entries, ",")+"}"))
		}, func(f citationFiles) string {
			return f.ties + `: commit "c0": not a full object name of 40 or 64 lower-case hex digits`
		}, false},
		{"a changed list with an empty line", func(t *testing.T, f citationFiles) {
			writeFile(t, f.changed, []byte("base "+f.base+"\na/a.go\n\ndocs/x.md\n"))
		}, func(f citationFiles) string {
			return f.changed + ":3: an empty line, where a path was due"
		}, false},
		{"a changed list with no base line", func(t *testing.T, f citationFiles) {
			writeFile(t, f.changed, []byte("a/a.go\n"))
		}, func(f citationFiles) string {
			return f.changed + `:1: no base line "base <commit>", which spec-check changed writes first`
		}, false},
		{"a changed list whose base is a short name", func(t *testing.T, f citationFiles) {
			writeFile(t, f.changed, []byte("base "+f.base[:12]+"\na/a.go\n"))
		}, func(f citationFiles) string {
			return f.changed + `:1: no base line "base <commit>", which spec-check changed writes first`
		}, false},
		{"an empty changed list", func(t *testing.T, f citationFiles) {
			writeFile(t, f.changed, nil)
		}, func(f citationFiles) string {
			return f.changed + `:1: no base line "base <commit>", which spec-check changed writes first`
		}, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newCitationRun()
			f := r.write(t)
			row.change(t, f)
			got := runBinary(t, binary, "", r.args(f, workflow)...)
			if line := "spec-check: " + row.line(f); row.more {
				if got.code != 2 || got.stdout != "" || !strings.HasPrefix(got.stderr, line) || strings.Count(got.stderr, "\n") != 1 {
					t.Errorf("exit %d, stdout %q, stderr %q; want exit 2 and one line that starts %q", got.code, got.stdout, got.stderr, line)
				}
			} else {
				expect(t, got, result{code: 2, stderr: line + "\n"})
			}
			if _, err := os.Stat(f.json); err == nil {
				t.Errorf("an input it could not read, and a --json file at %s", f.json)
			}
		})
	}
	t.Run("a commit that two ties files hold", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		f := r.write(t)
		second := filepath.Join(f.root, "second.json")
		writeFile(t, second, []byte(`{"`+f.head+`":{"`+citedCheck+`":["a/*"]}}`))
		r.extra = []string{"--ties", second}
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{code: 2,
			stderr: "spec-check: " + second + ": commit " + f.head + ": " + f.ties + " holds its ties too\n"})
	})
	t.Run("the ties of the head and of the base in two files", func(t *testing.T) {
		t.Parallel()
		r := newCitationRun()
		r.changed = "a/testdata/answer.json\n"
		r.reports["spec-report--go-checks"] = goReport("skip")
		r.baseTies = "-"
		f := r.write(t)
		second := filepath.Join(f.root, "base.json")
		writeFile(t, second, []byte(`{"`+f.base+`":{"`+citedCheck+`":["a/*","a/testdata/**"]}}`))
		r.extra = []string{"--ties", second}
		expect(t, runBinary(t, binary, "", r.args(f, workflow)...), result{code: 1,
			stdout: "spec-check: " + f.dir + ": 1 program check; 1 refused\n",
			stderr: "spec-check: " + filepath.Join(f.dir, "spec.md") + ":5: " + citedCheck + ": " + notRun +
				"a/testdata/answer.json changed (the tie at the base commit holds it), and go-checks did not run example.com/citations/a TestA (its report shows it skipped)\n"})
	})
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

// stubReads is a .github/scripts/go-test-reads.sh that answers reads for every package, as the
// repository's own script answers at one commit.
func stubReads(reads string) []byte {
	return []byte("#!/bin/sh\necho '{\"reads\":" + reads + ",\"problems\":[]}'\n")
}

// gitCase is a git repository that holds a copy of testdata/citations/repo, the spec file of the
// check on TestA and a go-test-reads.sh, and the files of a citations run over it but its list.
type gitCase struct{ repo, reports, needs, json, tmp string }

func newGitCase(t *testing.T, reads string) gitCase {
	t.Helper()
	root := t.TempDir()
	g := gitCase{repo: filepath.Join(root, "repo"), reports: filepath.Join(root, "reports"), needs: filepath.Join(root, "needs.json"),
		json: filepath.Join(root, "out", "faults.json"), tmp: filepath.Join(root, "tmp")}
	if err := os.CopyFS(g.repo, os.DirFS(fixture(t, "citations", "repo"))); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(g.repo, ".dispatch", "spec"), filepath.Join(g.repo, ".github", "scripts"),
		filepath.Join(g.reports, "spec-report--go-checks"), g.tmp} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(g.repo, ".dispatch", "spec", "spec.md"), []byte(citedSpec(testPart("example.com/citations/a TestA", "go-checks"))))
	g.script(t, reads)
	writeFile(t, filepath.Join(g.reports, "spec-report--go-checks", "report.jsonl"), []byte(goReport("skip")))
	writeFile(t, g.needs, []byte(needsOf("success")))
	gitIn(t, g.repo, "init", "-q")
	return g
}

// script writes the repository's go-test-reads.sh, which answers reads.
func (g gitCase) script(t *testing.T, reads string) {
	t.Helper()
	path := filepath.Join(g.repo, ".github", "scripts", "go-test-reads.sh")
	writeFile(t, path, stubReads(reads))
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// cite runs changed from base to HEAD, then citations over its list with no --ties, as the job runs
// them, with TMPDIR at g.tmp; it fails the test unless changed exits 0 and the checkout of the base
// is gone after the run.
func (g gitCase) cite(t *testing.T, binary, workflow, base string) result {
	t.Helper()
	list := runBinary(t, binary, "", "changed", "--repo", g.repo, "--base", base)
	if list.code != 0 {
		t.Fatalf("changed: exit %d, stderr %q", list.code, list.stderr)
	}
	changed := filepath.Join(filepath.Dir(g.reports), "changed.txt")
	writeFile(t, changed, []byte(list.stdout))
	got := runBinaryIn(t, binary, "", []string{"TMPDIR=" + g.tmp}, "citations", "--dir", filepath.Join(g.repo, ".dispatch", "spec"), "--repo", g.repo,
		"--workflow", workflow, "--changed", changed, "--reports", g.reports, "--needs", g.needs, "--json", g.json)
	if left, err := os.ReadDir(g.tmp); err != nil || len(left) != 0 {
		t.Errorf("the run left %v in its TMPDIR (%v); want the checkout of the base removed", left, err)
	}
	return got
}

// R8: without --ties, citations derives the tie at the base commit from a checkout of that commit
// with the base's own go-test-reads.sh, so a file that the change deleted counts when the base's
// tie holds it. The head deletes a/testdata and its script answers a/* alone; at the base the
// script answers a/* and a/testdata/**. The control is the same change where the base's script
// answers a/* alone: the deleted file is then no read of the test at either end, and it is cited.
func TestCitationsReadsTheTieAtTheBaseCommit(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	workflow := fixture(t, "citations", "workflow.yaml")
	for _, row := range []struct {
		name, baseReads string
		want            func(dir string) result
	}{
		{"the base's tie holds the deleted testdata", `["a/*","a/testdata/**"]`, func(dir string) result {
			return result{code: 1, stdout: "spec-check: " + dir + ": 1 program check; 1 refused\n",
				stderr: "spec-check: " + filepath.Join(dir, "spec.md") + ":5: " + citedCheck + ": test-not-run: a/testdata/answer.json changed " +
					"(the tie at the base commit holds it), and go-checks did not run example.com/citations/a TestA (its report shows it skipped)\n"}
		}},
		{"control: the base's tie does not hold it", `["a/*"]`, func(dir string) result {
			return result{stdout: "spec-check: " + dir + ": 1 program check; each is cited\n"}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			g := newGitCase(t, row.baseReads)
			if err := os.MkdirAll(filepath.Join(g.repo, "a", "testdata"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(g.repo, "a", "testdata", "answer.json"), []byte("{\"answer\": 42}\n"))
			base := commitAll(t, g.repo, "", "base")
			removeAll(t, filepath.Join(g.repo, "a", "testdata"))
			g.script(t, `["a/*"]`)
			commitAll(t, g.repo, base, "delete the testdata")
			expect(t, g.cite(t, binary, workflow, base), row.want(filepath.Join(g.repo, ".dispatch", "spec")))
		})
	}
}

// R8: a carried result stands at the commit that the list starts from, so the base of changed is
// the merge base with the default branch, never the branch's push before. Push 1 adds t.Skip to
// TestA, push 2 changes docs alone. From the merge base the list holds a/a_test.go, and the skipped
// test is refused at push 2; from push 1, the control, the list holds the docs alone, and the check
// reads as cited although its test has not run since a/a_test.go changed.
func TestCitationsFromTheMergeBase(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	workflow := fixture(t, "citations", "workflow.yaml")
	g := newGitCase(t, `["a/*"]`)
	if err := os.MkdirAll(filepath.Join(g.repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(g.repo, "docs", "readme.md"), []byte("The answer.\n"))
	main := commitAll(t, g.repo, "", "main")
	gitIn(t, g.repo, "update-ref", "refs/heads/main", main)
	test := filepath.Join(g.repo, "a", "a_test.go")
	content, err := os.ReadFile(test)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, test, []byte(strings.Replace(string(content), "func TestA(t *testing.T) {\n", "func TestA(t *testing.T) {\n\tt.Skip(\"flaky\")\n", 1)))
	push1 := commitAll(t, g.repo, main, "skip TestA")
	writeFile(t, filepath.Join(g.repo, "docs", "readme.md"), []byte("The answer, again.\n"))
	push2 := commitAll(t, g.repo, push1, "docs")
	if mergeBase := gitIn(t, g.repo, "merge-base", "main", push2); mergeBase != main {
		t.Fatalf("merge base %s, want main %s", mergeBase, main)
	}
	dir := filepath.Join(g.repo, ".dispatch", "spec")
	expect(t, g.cite(t, binary, workflow, main), result{code: 1, stdout: "spec-check: " + dir + ": 1 program check; 1 refused\n",
		stderr: "spec-check: " + filepath.Join(dir, "spec.md") + ":5: " + citedCheck + ": test-not-run: a/a_test.go changed, and go-checks did not run example.com/citations/a TestA (its report shows it skipped)\n"})
	expect(t, g.cite(t, binary, workflow, push1), result{stdout: "spec-check: " + dir + ": 1 program check; each is cited\n"})
}

// Without --ties, citations derives a tie only for a check whose test did not run: a run where
// each test ran starts no go-test-reads.sh and makes no checkout of the base. The script of the
// fixture writes a line to a file of the test each time it runs, at the head and at the base. The
// control is the same run with the test skipped, where only docs/readme.md changed: the tie at the
// head covers no changed path, so citations asks the script at the head and then at the base. A
// list from the head itself has the head as its base, whose tie is the one at the head: the script
// runs once, and no checkout is made.
func TestCitationsDerivesATieOnlyForATestThatDidNotRun(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	workflow := fixture(t, "citations", "workflow.yaml")
	for _, row := range []struct {
		name, outcome string
		fromHead      bool // the list's base is the head itself
		runs          int
	}{{"pass", "pass", false, 0}, {"skip", "skip", false, 2}, {"skip, from the head itself", "skip", true, 1}} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			g := newGitCase(t, `["a/*"]`)
			runs := filepath.Join(t.TempDir(), "runs")
			writeFile(t, filepath.Join(g.repo, ".github", "scripts", "go-test-reads.sh"),
				[]byte("#!/bin/sh\necho run >> '"+runs+"'\necho '{\"reads\":[\"a/*\"],\"problems\":[]}'\n"))
			writeFile(t, filepath.Join(g.reports, "spec-report--go-checks", "report.jsonl"), []byte(goReport(row.outcome)))
			if err := os.MkdirAll(filepath.Join(g.repo, "docs"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(g.repo, "docs", "readme.md"), []byte("The answer.\n"))
			base := commitAll(t, g.repo, "", "base")
			writeFile(t, filepath.Join(g.repo, "docs", "readme.md"), []byte("The answer, again.\n"))
			if head := commitAll(t, g.repo, base, "docs"); row.fromHead {
				base = head
			}
			dir := filepath.Join(g.repo, ".dispatch", "spec")
			expect(t, g.cite(t, binary, workflow, base), result{stdout: "spec-check: " + dir + ": 1 program check; each is cited\n"})
			content, err := os.ReadFile(runs)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			if got := strings.Count(string(content), "run\n"); got != row.runs {
				t.Errorf("go-test-reads.sh ran %d times, want %d", got, row.runs)
			}
		})
	}
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
		return gitIn(t, repo, args...)
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "a", "data.json"), []byte("{\"answer\": 42}\n"))
	writeFile(t, filepath.Join(repo, "a", "a.go"), []byte("package a\n"))
	git("add", ".")
	git("commit", "-q", "-m", "A")
	a := git("rev-parse", "HEAD")
	if err := os.Mkdir(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("mv", "a/data.json", "docs/data.json")
	git("commit", "-q", "-m", "B")
	b := git("rev-parse", "HEAD")

	// The control: git's own diff reads the move as a rename and names the new path alone, so a
	// command that left rename detection on would miss a/data.json.
	if got := git("diff", "--name-only", a, b); got != "docs/data.json" {
		t.Fatalf("git diff --name-only A B = %q; the control wants docs/data.json alone", got)
	}
	// The first line names the base by its full object name, whatever name --base gave it.
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", a, "--head", b), result{
		stdout: "base " + a + "\na/data.json\ndocs/data.json\n",
		stderr: "spec-check: " + repo + ": 2 paths changed from " + a + " to " + b + "\n"})
	// --head defaults to HEAD, which is B.
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", a), result{
		stdout: "base " + a + "\na/data.json\ndocs/data.json\n",
		stderr: "spec-check: " + repo + ": 2 paths changed from " + a + " to HEAD\n"})
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", "HEAD~1"), result{
		stdout: "base " + a + "\na/data.json\ndocs/data.json\n",
		stderr: "spec-check: " + repo + ": 2 paths changed from HEAD~1 to HEAD\n"})
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", b), result{
		stdout: "base " + b + "\n",
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
	blob := git("hash-object", "-w", "a/line\nbreak.go")
	git("update-index", "--add", "--cacheinfo", "100644,"+blob+",a/line\nbreak.go")
	c := git("commit-tree", git("write-tree"), "-p", b, "-m", "C")
	expect(t, runBinary(t, binary, "", "changed", "--repo", repo, "--base", b, "--head", c), result{code: 2,
		stderr: "spec-check: " + repo + `: the path "a/line\nbreak.go" holds a line break, which a list of one path a line cannot hold` + "\n"})
}
