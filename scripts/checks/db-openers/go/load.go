//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// openerPackages are the packages whose functions can open or parse a Postgres connection string,
// and pgsetting, the formatter: each is read from its export data, so its functions are known by
// their real signatures. Every other package outside the module is an empty one.
var openerPackages = regexp.MustCompile(`^(database/sql|github\.com/jackc/pgx(/v[0-9]+)?(/pgconn|/pgxpool|/stdlib)?|github\.com/jackc/pgconn|github\.com/lib/pq|github\.com/sjawhar/envoy/pgsetting)$`)

// module is a module's packages as read from source, type-checked.
type module struct {
	root, dir, path string
	fset            *token.FileSet
	info            *types.Info
	files           []*ast.File
	// sources holds each file's bytes, for the text of a call.
	sources map[string][]byte
	// packages holds each package read, module and opener packages alike, by import path.
	packages map[string]*types.Package
	// skipped counts the files the build's constraints leave out for this GOOS and GOARCH.
	skipped    int
	typeErrors int
}

var moduleLine = regexp.MustCompile(`(?m)^module\s+"?([^"\s]+)"?`)

// load reads every package of the module in moduleDir (a directory of root) from source, with the
// planted shapes as one more file of the package in plantDir when it names one.
func load(root, moduleDir, plantDir string) *module {
	dir := filepath.Join(root, moduleDir)
	goMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		fail(err)
	}
	match := moduleLine.FindSubmatch(goMod)
	if match == nil {
		fail(fmt.Errorf("%s/go.mod names no module", moduleDir))
	}
	m := &module{
		root: root, dir: dir, path: string(match[1]), fset: token.NewFileSet(),
		sources: map[string][]byte{}, packages: map[string]*types.Package{},
		info: &types.Info{
			Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
			Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
		},
	}
	byPath := map[string][]*ast.File{}
	imports := map[string]map[string]bool{}
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != dir && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			if p != dir {
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if ok, err := build.Default.MatchFile(filepath.Dir(p), d.Name()); err != nil || !ok {
			m.skipped++
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(m.fset, p, src, parser.ParseComments)
		if err != nil {
			return err
		}
		m.sources[p] = src
		importPath := m.importPath(filepath.Dir(p))
		byPath[importPath] = append(byPath[importPath], f)
		return nil
	})
	if walkErr != nil {
		fail(walkErr)
	}
	if plantDir != "" {
		importPath := m.importPath(filepath.Join(dir, plantDir))
		pkgFiles := byPath[importPath]
		if len(pkgFiles) == 0 {
			fail(fmt.Errorf("no package of %s is in %s, the planted shapes' package", moduleDir, plantDir))
		}
		source := strings.Replace(planted, "package main", "package "+pkgFiles[0].Name.Name, 1)
		plantedPath := filepath.Join(root, plantedName)
		f, err := parser.ParseFile(m.fset, plantedPath, source, parser.ParseComments)
		if err != nil {
			fail(err)
		}
		m.sources[plantedPath] = []byte(source)
		byPath[importPath] = append(pkgFiles, f)
	}
	needed := map[string]bool{}
	for importPath, files := range byPath {
		imports[importPath] = map[string]bool{}
		for _, f := range files {
			for _, spec := range f.Imports {
				p := strings.Trim(spec.Path.Value, "\"`")
				imports[importPath][p] = true
				if _, inModule := byPath[p]; !inModule && openerPackages.MatchString(p) {
					needed[p] = true
				}
			}
		}
	}
	gc := m.exportImporter(needed)
	var order []string
	seen := map[string]bool{}
	var visit func(string)
	visit = func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		var deps []string
		for dep := range imports[p] {
			if _, inModule := byPath[dep]; inModule {
				deps = append(deps, dep)
			}
		}
		sort.Strings(deps)
		for _, dep := range deps {
			visit(dep)
		}
		order = append(order, p)
	}
	var all []string
	for p := range byPath {
		all = append(all, p)
	}
	sort.Strings(all)
	for _, p := range all {
		visit(p)
	}
	for _, p := range order {
		conf := types.Config{
			Importer: importerFunc(func(importPath string) (*types.Package, error) {
				if pkg := m.packages[importPath]; pkg != nil {
					return pkg, nil
				}
				if needed[importPath] {
					pkg, err := gc.Import(importPath)
					if err != nil {
						return nil, err
					}
					m.packages[importPath] = pkg
					return pkg, nil
				}
				// An empty package: what the code names in it does not resolve, which can make no
				// call an opener.
				pkg := types.NewPackage(importPath, packageName(importPath))
				pkg.MarkComplete()
				m.packages[importPath] = pkg
				return pkg, nil
			}),
			FakeImportC: true,
			Error:       func(error) { m.typeErrors++ },
		}
		pkg, _ := conf.Check(p, m.fset, byPath[p], m.info)
		m.packages[p] = pkg
		m.files = append(m.files, byPath[p]...)
	}
	return m
}

// importPath is the import path of the module's package in dir.
func (m *module) importPath(dir string) string {
	rel, err := filepath.Rel(m.dir, dir)
	if err != nil || rel == "." {
		return m.path
	}
	return m.path + "/" + filepath.ToSlash(rel)
}

// exportImporter is an importer of the packages needed from the export data `go list -export`
// writes for them, run in the module's directory with the environment this program was given.
func (m *module) exportImporter(needed map[string]bool) types.Importer {
	exports := map[string]string{}
	if len(needed) > 0 {
		var paths []string
		for p := range needed {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		list := exec.Command("go", append([]string{"list", "-export", "-json=ImportPath,Export"}, paths...)...)
		list.Dir = m.dir
		list.Stderr = os.Stderr
		out, err := list.Output()
		if err != nil {
			fail(fmt.Errorf("go list -export in %s: %w", m.dir, err))
		}
		decoder := json.NewDecoder(bytes.NewReader(out))
		for decoder.More() {
			var p struct{ ImportPath, Export string }
			if err := decoder.Decode(&p); err != nil {
				fail(err)
			}
			exports[p.ImportPath] = p.Export
		}
	}
	return importer.ForCompiler(m.fset, "gc", func(importPath string) (io.ReadCloser, error) {
		file, ok := exports[importPath]
		if !ok || file == "" {
			return nil, fmt.Errorf("no export data for %s", importPath)
		}
		return os.Open(file)
	})
}

// packageName is the name a package at importPath most likely declares: its last element, without
// a major version, a "go-" prefix or a ".vN" suffix.
func packageName(importPath string) string {
	name := path.Base(importPath)
	if len(name) > 1 && name[0] == 'v' && strings.Trim(name[1:], "0123456789") == "" {
		name = path.Base(path.Dir(importPath))
	}
	name = strings.TrimPrefix(name, "go-")
	if i := strings.Index(name, ".v"); i > 0 {
		name = name[:i]
	}
	return strings.NewReplacer("-", "_", ".", "_").Replace(name)
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
