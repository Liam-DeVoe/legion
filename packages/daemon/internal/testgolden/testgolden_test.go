package testgolden

import (
	"os"
	"path/filepath"
	"testing"
)

// The first line that differs is named with the file's side and Go's, counted from 1, and a file
// one side ends before the other names the empty line past its end.
func TestFirstDifferenceNamesTheFirstLineThatDiffers(t *testing.T) {
	for _, tc := range []struct {
		name           string
		held, written  string
		line           int
		inFile, fromGo string
	}{
		{name: "a changed line", held: "{\n  \"a\": 1,\n  \"b\": 2\n}\n", written: "{\n  \"a\": 1,\n  \"b\": 3\n}\n", line: 3, inFile: `  "b": 2`, fromGo: `  "b": 3`},
		{name: "a line Go adds", held: "{\n}\n", written: "{\n  \"a\": 1\n}\n", line: 2, inFile: "}", fromGo: `  "a": 1`},
		{name: "the file ends first", held: "a\n", written: "a\nb\n", line: 2, inFile: "", fromGo: "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, inFile, fromGo := FirstDifference([]byte(tc.held), []byte(tc.written))
			if line != tc.line || inFile != tc.inFile || fromGo != tc.fromGo {
				t.Errorf("FirstDifference = %d %q %q, want %d %q %q", line, inFile, fromGo, tc.line, tc.inFile, tc.fromGo)
			}
		})
	}
}

// Check writes the value's indented JSON with a newline, makes the directory it needs, and then
// passes over the file it wrote.
func TestCheckWritesTheFileItThenPasses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "value.json")
	Check(t, path, map[string]int{"b": 2, "a": 1}, true, "go test -update")
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"a\": 1,\n  \"b\": 2\n}\n"; string(written) != want {
		t.Fatalf("Check wrote %q, want %q", written, want)
	}
	Check(t, path, map[string]int{"a": 1, "b": 2}, false, "go test -update")
}
