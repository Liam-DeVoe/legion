package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ciFilesThatCompare names each line of the CI files that runs the comparison of the files with
// the approved spec (R2: "No CI job compares the files"). The clean fixture runs spec-check tree and
// print, and says in a comment that no step compares the files: no line is named. The planted
// fixture runs the comparison once from a job step, once from a composite action and once from a
// script: each of the three lines is named.
func TestCIFilesThatCompare(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		fixture string
		want    []string
	}{
		{"clean", nil},
		{"no-workflow", nil},
		{"planted", []string{
			".github/actions/compare/action.yml:9",
			".github/workflows/compare.yaml:13",
			"scripts/compare.sh:4",
		}},
	} {
		t.Run(row.fixture, func(t *testing.T) {
			t.Parallel()
			files := readCIFiles(t, filepath.Join("testdata", "ci-files", row.fixture))
			if got := ciFilesThatCompare(files); !reflect.DeepEqual(got, row.want) {
				t.Errorf("ciFilesThatCompare(%s) = %q, want %q", row.fixture, got, row.want)
			}
		})
	}
}

// Each form a CI file can write the command in: a command that a backslash continues on the next
// line, as a shell reads it, the word compare only as a whole word, a CRLF file, one hit for a line
// that runs it twice, and the hits in path and line order, the line as a number.
func TestCIFilesThatCompareReadsEachLineAsTheShellDoes(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"a backslash continues the command on the next line",
			map[string]string{"scripts/a.sh": "set -e\nspec-check \\\n  compare --dir .dispatch/spec --approved a.json\n"},
			[]string{"scripts/a.sh:2"}},
		{"the hit is the line that names spec-check",
			map[string]string{"scripts/a.sh": "env DIR=.dispatch/spec \\\n  spec-check compare --dir \"$DIR\" --approved a.json\n"},
			[]string{"scripts/a.sh:2"}},
		{"a backslash with no space before the next line joins two words, as in a shell",
			map[string]string{"scripts/a.sh": "spec-check\\\ncompare --dir .dispatch/spec\n"},
			nil},
		{"compare is a whole word",
			map[string]string{"scripts/a.sh": "spec-check comparer --dir .dispatch/spec\nspec-check tree --dir compare\nspec-check to compare the files\n"},
			nil},
		{"a CRLF file",
			map[string]string{".github/workflows/a.yaml": "on: pull_request\r\n  run: spec-check compare --dir .dispatch/spec\r\n"},
			[]string{".github/workflows/a.yaml:2"}},
		{"one hit for a line that runs it twice",
			map[string]string{"scripts/a.sh": "spec-check compare --dir a && spec-check compare --dir b\n"},
			[]string{"scripts/a.sh:1"}},
		{"the hits in path and line order",
			map[string]string{
				"scripts/b.sh": "\n" + "spec-check compare --dir a\n" + "\n\n\n\n\n\n\n" + "spec-check compare --dir b\n",
				"scripts/a.sh": "spec-check compare --dir a\n",
			},
			[]string{"scripts/a.sh:1", "scripts/b.sh:2", "scripts/b.sh:10"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			files := map[string][]byte{}
			for path, content := range row.files {
				files[path] = []byte(content)
			}
			if got := ciFilesThatCompare(files); !reflect.DeepEqual(got, row.want) {
				t.Errorf("ciFilesThatCompare = %q, want %q", got, row.want)
			}
		})
	}
}

// workflowSteps is the head of a workflow whose one job's steps follow it, from line 4 on.
const workflowSteps = "jobs:\n  spec:\n    steps:\n"

// A word as the shell reads it: a quoted or an escaped subcommand is the word compare, and a line
// that ends in two backslashes ends there. A run: value of a workflow or of an action as YAML reads
// it: a plain, quoted or folded scalar over two lines is one line, named where the value starts; a
// literal block keeps its lines, each named at its own line; an alias reads its anchor's value. A
// comment that names the command is still named, and a line is named once. A YAML file that does
// not parse is read as text (GitHub runs no step of it), and the documents before the first one
// that YAML cannot read keep their run: values.
func TestCIFilesThatCompareReadsARunValueAsYAMLAndTheShellReadIt(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"a subcommand in double quotes",
			map[string]string{".github/workflows/a.yaml": workflowSteps + "      - run: spec-check \"compare\" --dir .dispatch/spec\n"},
			[]string{".github/workflows/a.yaml:4"}},
		{"a subcommand in single quotes",
			map[string]string{"scripts/a.sh": "spec-check 'compare' --dir .dispatch/spec\n"},
			[]string{"scripts/a.sh:1"}},
		{"quotes inside the word and an escaped letter",
			map[string]string{"scripts/a.sh": "spec-check com\"\"pare --dir a\nspec-check \\compare --dir b\n"},
			[]string{"scripts/a.sh:1", "scripts/a.sh:2"}},
		{"a line that ends in two backslashes ends there",
			map[string]string{"scripts/a.sh": "spec-check\\\\\n  compare --dir .dispatch/spec\n"},
			nil},
		{"a plain scalar over two lines",
			map[string]string{".github/workflows/a.yaml": workflowSteps + "      - run: spec-check\n          compare --dir .dispatch/spec\n"},
			[]string{".github/workflows/a.yaml:4"}},
		{"a double-quoted scalar over two lines",
			map[string]string{".github/workflows/a.yaml": workflowSteps + "      - run: \"spec-check\n          compare --dir .dispatch/spec\"\n"},
			[]string{".github/workflows/a.yaml:4"}},
		{"a folded scalar over two lines",
			map[string]string{".github/actions/a/action.yml": "runs:\n  using: composite\n  steps:\n    - shell: bash\n      run: >-\n        spec-check\n        compare --dir .dispatch/spec\n"},
			[]string{".github/actions/a/action.yml:5"}},
		{"a literal block keeps its lines",
			map[string]string{".github/workflows/a.yaml": workflowSteps + "      - run: |\n          echo start\n          spec-check \\\n            compare --dir .dispatch/spec\n"},
			[]string{".github/workflows/a.yaml:6"}},
		{"an alias reads its anchor's value",
			map[string]string{".github/workflows/a.yaml": "x-command: &compare >-\n  spec-check\n  compare --dir .dispatch/spec\n" + workflowSteps + "      - run: *compare\n"},
			[]string{".github/workflows/a.yaml:1"}},
		{"a comment that names the command is named",
			map[string]string{".github/workflows/a.yaml": workflowSteps + "      # spec-check compare is the work of Dispatch, so no step here runs it.\n      - run: spec-check tree --dir .dispatch/spec\n"},
			[]string{".github/workflows/a.yaml:4"}},
		{"a line that the text and the YAML both name is named once",
			map[string]string{".github/workflows/a.yaml": workflowSteps + "      - run: spec-check compare --dir .dispatch/spec\n"},
			[]string{".github/workflows/a.yaml:4"}},
		{"a YAML file that does not parse is read as text",
			map[string]string{".github/workflows/a.yaml": "on: [pull_request\n  run: spec-check compare --dir .dispatch/spec\n"},
			[]string{".github/workflows/a.yaml:2"}},
		{"a run value over two lines in a document before one that YAML cannot read",
			map[string]string{".github/workflows/a.yaml": workflowSteps + "      - run: spec-check\n          compare --dir .dispatch/spec\n---\non: [pull_request\n"},
			[]string{".github/workflows/a.yaml:4"}},
		{"a value under another key is not a step",
			map[string]string{".github/workflows/a.yaml": workflowSteps + "      - name: >-\n          spec-check\n          compare\n        run: spec-check tree --dir .dispatch/spec\n"},
			nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			files := map[string][]byte{}
			for path, content := range row.files {
				files[path] = []byte(content)
			}
			if got := ciFilesThatCompare(files); !reflect.DeepEqual(got, row.want) {
				t.Errorf("ciFilesThatCompare = %q, want %q", got, row.want)
			}
		})
	}
}

// Bash's literal quoting: $'…' is the text that its ANSI-C escapes decode to, and $"…" is the
// quoted text, so spec-check $'compare' and spec-check $'com\x70are' run the comparison, as the
// plain form does, behind env -u too. A literal that decodes to another word, or to compare with
// more letters, runs no comparison.
func TestCIFilesThatCompareReadsBashLiteralQuoting(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name string
		line string
		hit  bool
	}{
		{"the plain form", "spec-check compare --dir .dispatch/spec --approved a.json", true},
		{"an ANSI-C quoted subcommand", "spec-check $'compare' --dir .dispatch/spec --approved a.json", true},
		{"an ANSI-C quoted subcommand with a hex escape, behind env -u",
			`env -u GITHUB_ACTIONS /usr/local/bin/spec-check $'com\x70are' --dir .dispatch/spec --approved a.json`, true},
		{"an ANSI-C quoted command and subcommand with octal escapes", `$'spec\055check' $'\143ompare' --dir a`, true},
		{"a locale-quoted subcommand", `spec-check $"compare" --dir .dispatch/spec`, true},
		{"an ANSI-C quoted other subcommand", "spec-check $'tree' --dir .dispatch/spec", false},
		{"an ANSI-C quoted word with a tab in it", `spec-check $'com\tpare' --dir a`, false},
		{"an ANSI-C quoted word longer than compare", `spec-check $'compare\x64' --dir a`, false},
		{"an escaped dollar before a quote", `spec-check \$'compare' --dir a`, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			var want []string
			if row.hit {
				want = []string{".github/workflows/a.yaml:4", "scripts/a.sh:1"}
			}
			files := map[string][]byte{
				"scripts/a.sh":             []byte(row.line + "\n"),
				".github/workflows/a.yaml": []byte(workflowSteps + "      - run: " + row.line + "\n"),
			}
			if got := ciFilesThatCompare(files); !reflect.DeepEqual(got, want) {
				t.Errorf("ciFilesThatCompare(%q) = %q, want %q", row.line, got, want)
			}
		})
	}
}

// Bash removes a backslash and the line break after it before it reads the words, with nothing
// in their place, except inside '…' and $'…', where it keeps them. So a line continuation inside
// a word, between $ and its quote, or between two words gives the words that the joined line
// gives. bash is what bash 5.2.21 prints for printf '%s|' and the row's text, run from a file;
// the reader names the row's first line when bash reads spec-check, then compare.
func TestCIFilesThatCompareJoinsALineAsBashDoes(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ name, text, bash string }{
		{"between $ and an ANSI-C quote", "spec-check $\\\n'com\\x70are' --dir a", "spec-check|compare|--dir|a|"},
		{"between $ and a locale quote", "spec-check $\\\n\"compare\" --dir a", "spec-check|compare|--dir|a|"},
		{"inside a word", "spec-check com\\\npare --dir a", "spec-check|compare|--dir|a|"},
		{"two times inside a word", "spec-check c\\\no\\\nmpare --dir a", "spec-check|compare|--dir|a|"},
		{"between two words", "spec-check \\\ncompare --dir a", "spec-check|compare|--dir|a|"},
		{"inside double quotes", "spec-check \"com\\\npare\" --dir a", "spec-check|compare|--dir|a|"},
		{"after a single quote inside double quotes", "env X=\"it's\" spec-check com\\\npare --dir a", "env|X=it's|spec-check|compare|--dir|a|"},
		{"inside single quotes, where it stays", "spec-check 'com\\\npare' --dir a", "spec-check|com\\\npare|--dir|a|"},
		{"inside an ANSI-C quote, where it stays", "spec-check $'com\\\npare' --dir a", "spec-check|com\\\npare|--dir|a|"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			var want []string
			if strings.Contains("|"+row.bash, "|spec-check|compare|") {
				want = []string{".github/workflows/a.yaml:5", "scripts/a.sh:1"}
			}
			block := "          " + strings.ReplaceAll(row.text, "\n", "\n          ")
			files := map[string][]byte{
				"scripts/a.sh":             []byte(row.text + "\n"),
				".github/workflows/a.yaml": []byte(workflowSteps + "      - run: |\n" + block + "\n"),
			}
			if got := ciFilesThatCompare(files); !reflect.DeepEqual(got, want) {
				t.Errorf("ciFilesThatCompare(%q) = %q, want %q", row.text, got, want)
			}
		})
	}
}

// shellWords reads $'…' as Bash decodes it: each escape that Bash's ANSI-C quoting names, an
// unknown escape as its backslash and its letter, and a NUL as the end of the quoted text. Each
// want but the last is what bash 5.2.21 prints for printf %s with the same text; the last is a
// quote that the line does not close, which this reader reads to the end of the line.
func TestShellWordsDecodesAnANSICQuoteAsBashDoes(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ in, want string }{
		{`$'a\ab\bc'`, "a\ab\bc"},
		{`$'\e\E'`, "\x1b\x1b"},
		{`$'\f\n\r\t\v'`, "\f\n\r\t\v"},
		{`$'\\\'\"\?'`, `\'"?`},
		{`$'\101\7\1011\777'`, "A\x07A1\xff"},
		{`$'ab\0cd'`, "ab"},
		{`$'\x41\x4\x4G\xq'`, "A\x04\x04G\\xq"},
		{`$'\u00e9\u20AC\U0001F600\uq'`, "é€😀\\uq"},
		{`$'\cA\c?\ca'`, "\x01\x7f\x01"},
		{`$'a\c\\x'`, "a\x1cx"},
		{`$'\c'`, `\c`},
		{`$'\q\''`, `\q'`},
		{`a$'b'c`, "abc"},
		{`$"compare"`, "compare"},
		{`$'com\x70are'`, "compare"},
		{`$'never closed`, "never closed"},
	} {
		if got, _ := shellWords(row.in); got != row.want {
			t.Errorf("shellWords(%q) = %q, want %q", row.in, got, row.want)
		}
	}
}

// readCIFiles reads every regular file under dir, keyed by its path from dir with "/", as spec-check
// guard reads a repository.
func readCIFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := fs.WalkDir(os.DirFS(dir), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		content, err := os.ReadFile(filepath.Join(dir, path))
		files[path] = content
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
