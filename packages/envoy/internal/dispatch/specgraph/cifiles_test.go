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
