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
)

// bashParts are the parts of a script that come before the command. Each starts where a command can
// start and ends where one can start, so that the parts join in any order: a word, each quote form,
// a backslash and a line break between words, inside a word and inside each quote form, each quote
// form over two and three lines, a comment with and without a quote and a trailing backslash, and a
// here-document with a bare, a quoted and a <<- delimiter, whose body holds a quote, a backslash and
// the text of a comparison.
var bashParts = []struct{ name, text string }{
	{"a word", ": x; "},
	{"'…' with a double quote", ": 'say \"x\"'; "},
	{`"…" with a single quote`, ": \"it's\"; "},
	{"$'…' with both quotes", ": $'it\\'s \"x\"'; "},
	{"a pair between words", ": a \\\nb; "},
	{"a pair inside a word", ": a\\\nb; "},
	{"a pair inside '…', over two lines", ": 'a\\\n\"b'; "},
	{`a pair inside "…", over three lines`, ": \"a\\\n'b\nc\"; "},
	{"a pair inside $'…', over two lines", ": $'a\\\n\"b'; "},
	{"'…' over three lines", ": 'a\"\nb\nc'; "},
	{`"…" over two lines`, ": \"a'\nb\"; "},
	{"$'…' over three lines", ": $'a\"\nb\nc'; "},
	{"a comment", "# a\n"},
	{"a comment with a quote", "# it's\n"},
	{"a comment with a trailing backslash", "# a \\\n"},
	{"a comment with a quote and a trailing backslash", "# \" \\\n"},
	{"a here-document with a bare delimiter", ": <<END\nspec-check compare --dir \"it's\" \\\nx\nEND\n"},
	{"a here-document with a quoted delimiter", ": <<'END\\'\nspec-check compare --dir \" \\\nEND\\\n"},
	{"a here-document with a <<- delimiter", ": <<-\"END\"\n\tspec-check compare --dir 'it\\'s\n\tEND\n"},
}

// bashForms are the ways that a script writes the command: {w} is the subcommand, {h} and {t} are
// its two halves, and {e} is the subcommand with an ANSI-C escape in it. The last form after each
// sequence of parts is the one with a pair inside the word.
var bashForms = []struct{ name, text string }{
	{"plain", "spec-check {w} --dir a\n"},
	{"a pair between the words", "spec-check \\\n{w} --dir a\n"},
	{"a pair between $ and '…'", "spec-check $\\\n'{e}' --dir a\n"},
	{`a pair between $ and "…"`, "spec-check $\\\n\"{w}\" --dir a\n"},
	{`a pair inside "…"`, "spec-check \"{h}\\\n{t}\" --dir a\n"},
	{"a pair inside '…'", "spec-check '{h}\\\n{t}' --dir a\n"},
	{"a pair inside $'…'", "spec-check $'{h}\\\n{t}' --dir a\n"},
	{"a backslash before a space", "spec-check \\ {w} --dir a\n"},
	{"a backslash before a tab", "spec-check \\\t{w} --dir a\n"},
	{"a backslash before a quote", "spec-check \\\"{w}\\\" --dir a\n"},
	{"a backslash before #", ": \\#'\n'; spec-check {w} --dir a\n"},
	{"a backslash at the end of the text", "spec-check {w} --dir a \\"},
	{"inside '…' over three lines", ": '\nspec-check {w} --dir a\n'\n"},
	{`inside "…" over three lines`, ": \"\nspec-check {w} --dir a\n\"\n"},
	{"inside $'…' over three lines", ": $'\nspec-check {w} --dir a\n'\n"},
	{"a comment after it", "spec-check {w} --dir a # it's \\\n"},
	{"a here-document after it", "spec-check {w} --dir a <<'END'\nit's\nEND\n"},
	{"a redirection between the words", "spec-check 2>/dev/null {w} --dir a\n"},
	{"a here-document between the words", "spec-check <<END {w} --dir a\nx\nEND\n"},
	{"a pair inside the word", "spec-check {h}\\\n{t} --dir a\n"},
}

// nestedShells give a script to a nested shell as one quoted word: sh -c '…' and bash -c "…", and
// each of them inside each of them.
var nestedShells = func() []func(string) string {
	single := func(s string) string { return "sh -c '" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	double := func(s string) string {
		return `bash -c "` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(s) + `"`
	}
	return []func(string) string{single, double,
		func(s string) string { return single(single(s)) }, func(s string) string { return single(double(s)) },
		func(s string) string { return double(single(s)) }, func(s string) string { return double(double(s)) }}
}()

// bashScript is a script of the test, and the line, from 0, of its command's spec-check.
type bashScript struct {
	name, text string
	line       int
}

// bashScripts is each form of the command after no part and after each part, then the last form
// after each sequence of two parts and of three parts, each for compare and for tree. The scripts
// before wrapped are the ones that the test also gives to each nested shell.
func bashScripts() (scripts []bashScript, wrapped int) {
	add := func(prefix []int, form int, word string) {
		head, tail, escaped := "com", "pare", `com\x70are`
		if word == "tree" {
			head, tail, escaped = "tr", "ee", `t\x72ee`
		}
		var name, text strings.Builder
		for _, p := range prefix {
			name.WriteString(bashParts[p].name + ", ")
			text.WriteString(bashParts[p].text)
		}
		line := strings.Count(text.String(), "\n")
		command := strings.NewReplacer("{w}", word, "{h}", head, "{t}", tail, "{e}", escaped).Replace(bashForms[form].text)
		line += strings.Count(command[:strings.Index(command, "spec-check")], "\n")
		text.WriteString(command)
		name.WriteString(bashForms[form].name + ", " + word)
		scripts = append(scripts, bashScript{name.String(), text.String(), line})
	}
	last := len(bashForms) - 1
	for _, word := range []string{"compare", "tree"} {
		for form := range bashForms {
			add(nil, form, word)
			for p := range bashParts {
				add([]int{p}, form, word)
			}
		}
		for p := range bashParts {
			for q := range bashParts {
				add([]int{p, q}, last, word)
			}
		}
	}
	wrapped = len(scripts)
	for _, word := range []string{"compare", "tree"} {
		for p := range bashParts {
			for q := range bashParts {
				for r := range bashParts {
					add([]int{p, q, r}, last, word)
				}
			}
		}
	}
	return scripts, wrapped
}

// The test asks Bash itself. Each script runs in bash, with spec-check a shell function that records
// its first argument, and a spec-check on PATH that does the same for a nested shell.
//
//   - The words of the commands as readShell reads them name a comparison exactly where bash runs
//     one: for each script, the line of its spec-check when bash ran it with compare, else none.
//   - The guard, both layers of shellHits, names the line of each comparison that bash runs,
//     directly or in a nested shell: over each script, and over the forms and the sequences of two
//     given to each nested shell, two levels deep. It can name more; the count of those lines,
//     each a quoted text that no shell runs, is in the test's log.
func TestCIFilesThatCompareReadsAScriptAsBashRunsIt(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is not on PATH, so no script can run in it: %v", err)
	}
	began := time.Now()
	scripts, wrapped := bashScripts()
	texts := make([]string, 0, len(scripts)+wrapped*len(nestedShells))
	for _, s := range scripts {
		texts = append(texts, s.text)
	}
	for _, nest := range nestedShells {
		for _, s := range scripts[:wrapped] {
			texts = append(texts, nest(s.text))
		}
	}
	calls := firstArguments(t, bash, texts)
	compares := func(n int) bool { return slices.Contains(calls[n], "compare") }

	ran, wrong := 0, 0
	for n, s := range scripts {
		var want []int
		if compares(n) {
			want = []int{s.line}
			ran++
		}
		if got := lineNumbers(s.text, readShell(s.text).commands()); !slices.Equal(got, want) {
			if wrong++; wrong <= 20 {
				t.Errorf("%s: the words of %q run the comparison at the lines %v (from 0), and bash runs it at %v (first arguments %q)", s.name, s.text, got, want, calls[n])
			}
		}
	}
	if ran == 0 || ran == len(scripts) {
		t.Fatalf("UNKNOWN: bash ran the comparison in %d of %d scripts, so no script can tell a reader that names too many lines from one that names too few", ran, len(scripts))
	}

	missed, extra, extraScripts := 0, 0, 0
	for n, text := range texts {
		k := n
		if n >= len(scripts) {
			k = (n - len(scripts)) % wrapped
		}
		s := scripts[k]
		got := shellHits(text)
		named := len(got)
		if compares(n) {
			if !slices.Contains(got, s.line) {
				if missed++; missed <= 20 {
					t.Errorf("%s: the guard names the lines %v (from 0) of %q, and bash runs the comparison at %d", s.name, got, text, s.line)
				}
				continue
			}
			named--
		}
		if named > 0 {
			extra += named
			extraScripts++
		}
	}
	t.Logf("%d scripts in bash, %d of them in a nested shell, in %v. Bash ran the comparison directly in %d of the %d direct scripts. The words named a line that bash does not run in %d scripts; the guard missed a comparison in %d, and named %d lines in %d scripts where no shell ran that comparison",
		len(texts), len(texts)-len(scripts), time.Since(began).Round(time.Millisecond), ran, len(scripts), wrong, missed, extra, extraScripts)
}

// firstArguments runs each of scripts in bash and answers, for each, the first argument of each
// call of spec-check that it makes. A bash that defines spec-check as a function, and exports it to
// a nested bash, reads each script with ., as a CI step's bash reads its run: value, and a
// spec-check on PATH records the calls of a nested sh. The scripts run in batches, one bash for
// each.
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
