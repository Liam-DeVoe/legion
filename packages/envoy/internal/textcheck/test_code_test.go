package textcheck

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// goSource is one Go file testCode's walk found: its path, its package's directory and name,
// whether it is a test file, and the import paths it names.
type goSource struct {
	path, dir, pkg string
	test           bool
	imports        []string
}

// testCodeFile is one Go file of test code: its path, and its path relative to the repository
// with forward slashes, the form an offence names it by.
type testCodeFile struct {
	path, rel string
}

// testCode walks the Go modules under roots and answers every Go file of test code, sorted by its
// path: every _test.go file, every file of a program that programs names (a directory relative to
// repository) as one only tests run, and every file of a package only test code imports, the
// fixtures among them, as a test file's imports are. It also answers each of programs that holds no
// Go file, and how many Go files the walk read. node_modules, vendor, testdata and hidden
// directories are passed over, as go passes over the last two.
func testCode(t *testing.T, repository string, roots, programs []string) (files []testCodeFile, missingPrograms []string, read int) {
	t.Helper()
	repository, err := filepath.Abs(repository)
	if err != nil {
		t.Fatal(err)
	}
	modules := map[string]string{} // a module's directory -> its path
	var sources []goSource
	fileSet := token.NewFileSet()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if name := entry.Name(); path != root && (name == "node_modules" || name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			switch {
			case entry.Name() == "go.mod":
				text, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				for line := range strings.Lines(string(text)) {
					if module, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
						modules[filepath.Dir(path)] = strings.Trim(strings.TrimSpace(module), `"`)
					}
				}
			case strings.HasSuffix(path, ".go"):
				file, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
				if err != nil {
					return err
				}
				source := goSource{path: path, dir: filepath.Dir(path), pkg: file.Name.Name, test: strings.HasSuffix(path, "_test.go")}
				for _, spec := range file.Imports {
					imported, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						return err
					}
					source.imports = append(source.imports, imported)
				}
				sources = append(sources, source)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("read the Go files under %s: %v", root, err)
		}
	}

	relative := func(path string) string {
		t.Helper()
		absolute, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(repository, absolute)
		if err != nil {
			t.Fatal(err)
		}
		return filepath.ToSlash(rel)
	}

	// A file of a program only tests run counts as a test file.
	listed := map[string]bool{}
	for _, program := range programs {
		listed[program] = false
	}
	for i, source := range sources {
		if _, ok := listed[relative(source.dir)]; ok {
			listed[relative(source.dir)] = true
			sources[i].test = true
		}
	}

	// Each directory's import path, from the module whose directory is the nearest above it.
	importPath := map[string]string{}
	for _, source := range sources {
		if _, done := importPath[source.dir]; done {
			continue
		}
		for dir := source.dir; ; dir = filepath.Dir(dir) {
			if module, ok := modules[dir]; ok {
				rel, err := filepath.Rel(dir, source.dir)
				if err != nil {
					t.Fatal(err)
				}
				importPath[source.dir] = strings.TrimSuffix(module+"/"+filepath.ToSlash(rel), "/.")
				break
			}
			if parent := filepath.Dir(dir); parent == dir {
				importPath[source.dir] = ""
				break
			}
		}
	}
	// A package only test code imports: it has importers, and each is a test file or a file of
	// another such package. Every imported package starts as one, and each loses it once a file
	// of a package that is not one imports it, until no package loses it.
	directory := map[string]string{}
	for dir, path := range importPath {
		directory[path] = dir
	}
	importers := map[string][]goSource{}
	for _, source := range sources {
		for _, imported := range source.imports {
			if dir, ok := directory[imported]; ok && dir != source.dir {
				importers[dir] = append(importers[dir], source)
			}
		}
	}
	testOnly := map[string]bool{}
	for dir := range importers {
		testOnly[dir] = true
	}
	for changed := true; changed; {
		changed = false
		for dir := range testOnly {
			for _, importer := range importers[dir] {
				if !importer.test && !testOnly[importer.dir] {
					delete(testOnly, dir)
					changed = true
					break
				}
			}
		}
	}

	for _, source := range sources {
		if source.test || testOnly[source.dir] {
			files = append(files, testCodeFile{path: source.path, rel: relative(source.path)})
		}
	}
	for program, found := range listed {
		if !found {
			missingPrograms = append(missingPrograms, program)
		}
	}
	return files, missingPrograms, len(sources)
}
