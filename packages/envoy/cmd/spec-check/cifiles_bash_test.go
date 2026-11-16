package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The forms of the claim, from which the test builds its scripts: the command's name, the white
// space between it and the subcommand, the subcommand (each quote form around it or inside it, a
// $ quote, an escaped letter, and a backslash and a line break inside it), the text before the
// command (a comment and a here-document whose bodies hold a quote and a backslash, a quote that an
// earlier line opens), and a nested shell around the script.
var (
	bashNames    = []string{"spec-check", "'spec-check'", `spec"-"check`, "./bin/spec-check", "spec-\\\ncheck"}
	bashBetween  = []string{" ", " \\\n", " \\\n  ", "\\\n  "}
	bashSubs     = []string{"compare", "'compare'", `"compare"`, "co'mp'are", "''compare", "$'compare'", `$"compare"`, `\compare`, "com\\\npare", "\"com\\\npare\"", "tree", "'tree'", "tr\\\nee"}
	bashPrefixes = []string{"", "env -u GITHUB_ACTIONS ", "# \" \\\n", ": <<'END\\'\n\" \\\nEND\\\n", ": \"it's\"; ", "echo 'start\nend' && "}
	bashNestedBy = []string{"", "# \" \\\n"}
	bashSingle   = func(s string) string { return "sh -c '" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	bashDouble   = func(s string) string {
		return `bash -c "` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(s) + `"`
	}
	bashHere      = func(s string) string { return "bash <<'EOF'\n" + s + "\nEOF" }
	bashWrappings = []func(string) string{bashSingle, bashDouble, bashHere,
		func(s string) string { return bashSingle(bashSingle(s)) }, func(s string) string { return bashSingle(bashDouble(s)) },
		func(s string) string { return bashDouble(bashSingle(s)) }, func(s string) string { return bashDouble(bashDouble(s)) }}
)

// bashScripts is each command of bashNames, bashBetween and bashSubs after each of bashPrefixes,
// and the same commands after each of bashNestedBy inside each of bashWrappings. It leaves out a
// command where a backslash and a line break right after the command's name, with blanks after
// them, meet another backslash and line break in the name or in the subcommand: one reading of the
// rule keeps the blanks and the other removes them, and neither reads both
// (TestCIFilesThatCompareLeavesAFormOutsideTheClaim).
func bashScripts() []string {
	var commands []string
	for _, name := range bashNames {
		for _, between := range bashBetween {
			for _, sub := range bashSubs {
				if between == "\\\n  " && strings.Contains(name+sub, "\\\n") {
					continue
				}
				commands = append(commands, name+between+sub+" --dir a")
			}
		}
	}
	var scripts []string
	for _, prefix := range bashPrefixes {
		for _, command := range commands {
			scripts = append(scripts, prefix+command)
		}
	}
	for _, wrap := range bashWrappings {
		for _, prefix := range bashNestedBy {
			for _, command := range commands {
				scripts = append(scripts, wrap(prefix+command))
			}
		}
	}
	return scripts
}

// The test asks Bash itself. Each script of bashScripts runs in bash three ways: as a script, as a
// workflow's run: | block and as a folded run: > value, the value that YAML reads from the file.
// spec-check is a shell function that records its first argument, and a spec-check on PATH and in
// ./bin does the same for a nested sh and for a path. Whenever Bash runs spec-check with compare
// as its first argument, directly or in a nested shell, the guard names the file; a miss fails the
// test. The guard can name more, and the test logs how many files it names where no shell runs
// the comparison.
func TestCIFilesThatCompareNamesEachComparisonThatBashRuns(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is not on PATH, so no script can run in it: %v", err)
	}
	began := time.Now()
	type file struct{ kind, raw, script string }
	var files []file
	for _, script := range bashScripts() {
		files = append(files, file{"script", script, script})
		for _, style := range []string{"|", ">"} {
			raw := workflowSteps + "      - run: " + style + "\n          " + strings.ReplaceAll(script, "\n", "\n          ") + "\n"
			var workflow struct {
				Jobs map[string]struct{ Steps []struct{ Run string } }
			}
			if err := yaml.Unmarshal([]byte(raw), &workflow); err != nil {
				t.Fatalf("YAML cannot read %q: %v", raw, err)
			}
			files = append(files, file{"run: " + style, raw, workflow.Jobs["spec"].Steps[0].Run})
		}
	}
	scripts := make([]string, len(files))
	for n, f := range files {
		scripts[n] = f.script
	}
	calls := firstArguments(t, bash, scripts)
	ran, missed, over := 0, 0, 0
	for n, f := range files {
		named := textHits([]byte(f.raw)) != nil
		switch compares := slices.Contains(calls[n], "compare"); {
		case compares && !named:
			if missed++; missed <= 20 {
				t.Errorf("%s %q: bash runs the comparison (first arguments %q), and the guard names no line", f.kind, f.raw, calls[n])
			}
		case compares:
			ran++
		case named:
			over++
		}
	}
	if ran == 0 || ran == len(files) {
		t.Fatalf("UNKNOWN: bash ran the comparison in %d of %d files, so the files cannot tell a guard that names each file from one that names none", ran+missed, len(files))
	}
	t.Logf("%d files in bash, in %v. Bash ran the comparison in %d; the guard missed %d, and named %d files where no shell ran the comparison",
		len(files), time.Since(began).Round(time.Millisecond), ran+missed, missed, over)
}

// firstArguments runs each of scripts in bash and answers, for each, the first argument of each
// call of spec-check that it makes. A bash that defines spec-check as a function, and exports it to
// a nested bash, reads each script with ., as a CI step's bash reads its run: value, and a
// spec-check on PATH and in ./bin records the calls of a nested sh and of a path. The scripts run
// in batches, one bash for each.
func firstArguments(t *testing.T, bash string, scripts []string) [][]string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "spec-check"), []byte("#!/bin/sh\nprintf '%s\\0' \"$1\" >&3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "driver.sh")
	writeFile(t, driver, []byte("spec-check() { printf '%s\\0' \"$1\" >&3; }\nexport -f spec-check\nfor f; do . \"$f\" 3>\"$f.calls\" </dev/null >/dev/null 2>&1; done\nexit 0\n"))
	batches := make([][]string, min(runtime.NumCPU(), 16))
	paths := make([]string, len(scripts))
	for n, s := range scripts {
		paths[n] = filepath.Join(dir, fmt.Sprint(n))
		writeFile(t, paths[n], []byte(s))
		batches[n%len(batches)] = append(batches[n%len(batches)], paths[n])
	}
	failures := make([]error, len(batches))
	var wg sync.WaitGroup
	for b, batch := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run := exec.Command(bash, append([]string{driver}, batch...)...)
			run.Dir = dir
			run.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "LC_ALL=C"}
			if out, err := run.CombinedOutput(); err != nil {
				failures[b] = fmt.Errorf("%v: %s", err, out)
			}
		}()
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := make([][]string, len(scripts))
	for n, path := range paths {
		got, err := os.ReadFile(path + ".calls")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 0 {
			calls[n] = strings.Split(strings.TrimSuffix(string(got), "\x00"), "\x00")
		}
	}
	return calls
}
