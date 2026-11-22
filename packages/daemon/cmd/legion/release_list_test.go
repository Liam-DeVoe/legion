package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The envoy packages release.yaml's LEGION_VERSION_ARGS lists are every envoy package the legion
// binary links, as `go list -deps ./cmd/legion` names them (.github/ci-notes/release.md): the
// release finds no commit in the listed paths when one it misses alone changes (release-push.sh
// version), so no legion carries that change, and one it names that the binary no longer links
// releases a legion for nothing. The list is written by hand, so this row names each package found
// on one side only.
func TestTheReleaseListNamesEveryEnvoyPackageLegionLinks(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	listed := legionReleaseEnvoyPackages(t, string(workflow))
	linked := linkedEnvoyPackages(t)
	for _, pkg := range linked {
		if !slices.Contains(listed, pkg) {
			t.Errorf("legion links %s, which LEGION_VERSION_ARGS in release.yaml does not list: a change to it alone would release no legion", pkg)
		}
	}
	for _, pkg := range listed {
		if !slices.Contains(linked, pkg) {
			t.Errorf("LEGION_VERSION_ARGS in release.yaml lists %s, which legion does not link", pkg)
		}
	}
}

// legionReleaseEnvoyPackages is the envoy package directories LEGION_VERSION_ARGS lists after its
// "--", as packages/envoy/<path>, read from release.yaml's text: the folded value's lines, each
// indented under the key, the trailing "/" of each directory dropped. The module's go.mod and
// go.sum, which it lists too, are no package.
func legionReleaseEnvoyPackages(t *testing.T, workflow string) []string {
	t.Helper()
	const key = "  LEGION_VERSION_ARGS: >-"
	_, value, found := strings.Cut(workflow, "\n"+key+"\n")
	if !found {
		t.Fatalf("release.yaml has no line %q", key)
	}
	var words []string
	for line := range strings.Lines(value) {
		if !strings.HasPrefix(line, "    ") {
			break
		}
		words = append(words, strings.Fields(line)...)
	}
	dash := slices.Index(words, "--")
	if dash < 0 {
		t.Fatalf("LEGION_VERSION_ARGS in release.yaml has no \"--\" before its paths: %q", words)
	}
	var packages []string
	for _, path := range words[dash+1:] {
		if strings.HasPrefix(path, "packages/envoy/") && strings.HasSuffix(path, "/") {
			packages = append(packages, strings.TrimSuffix(path, "/"))
		}
	}
	if len(packages) == 0 {
		t.Fatalf("LEGION_VERSION_ARGS in release.yaml lists no envoy package directory, so this row would compare nothing: %q", words)
	}
	return packages
}

// linkedEnvoyPackages is every package of the envoy module `go list -deps` finds this package (the
// legion binary) linking, as packages/envoy/<path>, run with the go command of the toolchain that
// built this test, or the one on PATH.
func linkedEnvoyPackages(t *testing.T) []string {
	t.Helper()
	gocmd := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(gocmd); err != nil {
		if gocmd, err = exec.LookPath("go"); err != nil {
			t.Fatalf("no go command to list legion's packages with: %v", err)
		}
	}
	out, err := exec.Command(gocmd, "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps ./cmd/legion: %v", err)
	}
	var packages []string
	for _, imported := range strings.Fields(string(out)) {
		if rest, ok := strings.CutPrefix(imported, "github.com/sjawhar/envoy/"); ok {
			packages = append(packages, "packages/envoy/"+rest)
		}
	}
	if len(packages) == 0 {
		t.Fatal("go list -deps ./cmd/legion names no envoy package, so this row would compare nothing")
	}
	return packages
}
