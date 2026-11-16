package specgraph

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// CIFilesThatCompare names each line of the CI files that runs the comparison of the files with
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
			if got := CIFilesThatCompare(files); !reflect.DeepEqual(got, row.want) {
				t.Errorf("CIFilesThatCompare(%s) = %q, want %q", row.fixture, got, row.want)
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
			if got := CIFilesThatCompare(files); !reflect.DeepEqual(got, row.want) {
				t.Errorf("CIFilesThatCompare = %q, want %q", got, row.want)
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
// comment that names the command is still named, a line is named once, and a YAML file that does
// not parse is read as text (GitHub runs no step of it).
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
			if got := CIFilesThatCompare(files); !reflect.DeepEqual(got, row.want) {
				t.Errorf("CIFilesThatCompare = %q, want %q", got, row.want)
			}
		})
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
