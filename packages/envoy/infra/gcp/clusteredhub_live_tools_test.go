//go:build natslive

package gcp_test

import (
	"bufio"
	"debug/buildinfo"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// liveToolsFile pins the tools the live tests run, one `go install` argument (a package path and
// its version) per line, at the versions the hub runs; the file says which pin each follows.
// .github/scripts/go-fetch.sh installs them before the tests, and the tests only look them up.
const liveToolsFile = "testdata/live-tools.txt"

var liveToolPins = sync.OnceValues(func() (map[string]string, error) { return readLiveTools(liveToolsFile) })

// liveTool is the path of the tool liveToolsFile pins under the name binary, in `go env GOBIN`,
// where a `go install` of its line puts it. It installs nothing and so downloads nothing: a fetch
// from the module proxy or the checksum database while a test runs would fail the test whenever
// that service fails, on a change that touched nothing it tests. A tool that is absent, or that
// is another build than its line, fails the test with the command that installs it.
func liveTool(t *testing.T, binary string) string {
	t.Helper()
	pins, err := liveToolPins()
	require.NoError(t, err)
	pin, ok := pins[binary]
	require.True(t, ok, "%s pins no tool named %s", liveToolsFile, binary)
	gobin, err := goBin(t.TempDir())
	require.NoError(t, err)
	path, err := findLiveTool(gobin, binary, pin)
	require.NoError(t, err)
	t.Logf("%s: %s, the build %s pins", binary, path, pin)
	return path
}

// findLiveTool is the path of binary in gobin when its build information names pin's package and
// version, and otherwise why not, with the command that installs pin.
func findLiveTool(gobin, binary, pin string) (string, error) {
	path := filepath.Join(gobin, binary)
	install := "Install it before the tests, as CI's envoy-infra-go does:\n  go install " + pin +
		"\nor install every tool the live tests run, from the repository root:\n" +
		"  .github/scripts/go-fetch.sh tools packages/envoy/infra/gcp/" + liveToolsFile
	info, err := buildinfo.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%s is not installed in %s (go env GOBIN), and the live tests "+
			"download nothing while they run.\n%s", binary, gobin, install)
	case err != nil:
		return "", fmt.Errorf("read the build information of %s: %w\n%s", path, err, install)
	}
	if got := info.Path + "@" + info.Main.Version; got != pin {
		return "", fmt.Errorf("%s is %s, not %s, the build %s pins.\n%s", path, got, pin, liveToolsFile, install)
	}
	return path, nil
}

// goBin is where `go install` puts a binary: GOBIN, or the bin directory of GOPATH's first entry
// when GOBIN is unset. It asks the go command, which also reads `go env -w` settings, in dir, a
// directory outside any module, so no go.mod can make it fetch a toolchain.
func goBin(dir string) (string, error) {
	cmd := exec.Command("go", "env", "GOBIN", "GOPATH")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOBIN GOPATH: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		return "", fmt.Errorf("go env GOBIN GOPATH printed %q, not two lines", out)
	}
	if lines[0] != "" {
		return lines[0], nil
	}
	gopath := filepath.SplitList(lines[1])
	if len(gopath) == 0 || gopath[0] == "" {
		return "", fmt.Errorf("go env names neither GOBIN nor GOPATH")
	}
	return filepath.Join(gopath[0], "bin"), nil
}

// readLiveTools reads a list of pins, liveToolsFile's form, into each pin by the name of the
// binary it installs. A line that is no pin, and two pins of one binary, are refused.
func readLiveTools(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pins := map[string]string{}
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line, _, _ := strings.Cut(scanner.Text(), "#")
		pin := strings.TrimSpace(line)
		if pin == "" {
			continue
		}
		binary, err := liveToolBinary(pin)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if earlier, ok := pins[binary]; ok {
			return nil, fmt.Errorf("%s:%d: two lines install tool %s: %s and %s", path, n, binary, earlier, pin)
		}
		pins[binary] = pin
	}
	return pins, scanner.Err()
}

var (
	pinPattern     = regexp.MustCompile(`^([^\s@]+)@(v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.+-]+)?)$`)
	versionElement = regexp.MustCompile(`^v[1-9][0-9]*$`)
)

// liveToolBinary is the name `go install` gives pin's binary: the last element of its package
// path, or the one before it when the last is a major version (cmd/go's DefaultExecName). A pin
// is a package path and a semantic version; a query such as @latest is refused, since the tests
// check the build against the version their line names.
func liveToolBinary(pin string) (string, error) {
	m := pinPattern.FindStringSubmatch(pin)
	if m == nil {
		return "", fmt.Errorf("%q is not a package path and a semantic version, path@vX.Y.Z", pin)
	}
	elems := strings.Split(m[1], "/")
	name := elems[len(elems)-1]
	if len(elems) > 1 && versionElement.MatchString(name) {
		name = elems[len(elems)-2]
	}
	return name, nil
}

// The tool lookup the live tests run their tools through: a pinned tool is taken from GOBIN only
// when its binary is the build its line pins, and a tool that is absent or another build is
// refused with the command that installs it. Nothing installs or downloads: the absent row's
// GOBIN stays empty. The rows fail when the lookup accepts a binary by its name alone (the
// another-build row), when it installs what is missing (the absent row's GOBIN would hold it),
// or when its message stops naming the install command.
func TestALiveToolIsTakenFromGOBINOnlyAtItsPin(t *testing.T) {
	self, err := os.Executable()
	require.NoError(t, err)
	info, err := buildinfo.ReadFile(self)
	require.NoError(t, err)
	// The test binary stands in for a tool: its build information names its main package and
	// that module's version, as a `go install` of a pinned tool's does.
	pin := info.Path + "@" + info.Main.Version
	name := filepath.Base(info.Path)

	t.Run("the pinned build", func(t *testing.T) {
		gobin := t.TempDir()
		copyFile(t, self, filepath.Join(gobin, name))
		path, err := findLiveTool(gobin, name, pin)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(gobin, name), path)
	})
	t.Run("another build", func(t *testing.T) {
		gobin := t.TempDir()
		copyFile(t, self, filepath.Join(gobin, name))
		other := info.Path + "@v0.0.1"
		_, err := findLiveTool(gobin, name, other)
		require.ErrorContains(t, err, pin+", not "+other)
		require.ErrorContains(t, err, "go install "+other)
	})
	t.Run("absent", func(t *testing.T) {
		gobin := t.TempDir()
		absent := "example.com/tools/cmd/absent-tool@v1.2.3"
		_, err := findLiveTool(gobin, "absent-tool", absent)
		require.ErrorContains(t, err, "absent-tool is not installed in "+gobin)
		require.ErrorContains(t, err, "go install "+absent)
		require.ErrorContains(t, err, ".github/scripts/go-fetch.sh tools packages/envoy/infra/gcp/"+liveToolsFile)
		entries, err := os.ReadDir(gobin)
		require.NoError(t, err)
		require.Empty(t, entries, "the lookup installs nothing")
	})
}

// A tool's binary is the last element of its package path, or the one before a major-version
// suffix, as `go install` names it; the pinned list holds the four tools the live tests run.
func TestTheLiveToolsListPinsEachToolTheTestsRun(t *testing.T) {
	for pin, want := range map[string]string{
		"helm.sh/helm/v3/cmd/helm@v3.20.2":                                "helm",
		"github.com/nats-io/nats-server/v2@v2.15.1":                       "nats-server",
		"github.com/nats-io/nack/cmd/nats-server-config-reloader@v0.24.0": "nats-server-config-reloader",
		"github.com/nats-io/prometheus-nats-exporter@v0.20.2":             "prometheus-nats-exporter",
	} {
		got, err := liveToolBinary(pin)
		require.NoError(t, err)
		require.Equal(t, want, got, pin)
	}
	for _, bad := range []string{"helm.sh/helm/v3/cmd/helm", "helm.sh/helm/v3/cmd/helm@latest", "@v1.0.0", "a b@v1"} {
		_, err := liveToolBinary(bad)
		require.Error(t, err, bad)
	}

	pins, err := readLiveTools(liveToolsFile)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"helm":                        "helm.sh/helm/v3/cmd/helm@v3.20.2",
		"nats-server":                 "github.com/nats-io/nats-server/v2@v2.15.1",
		"nats-server-config-reloader": "github.com/nats-io/nack/cmd/nats-server-config-reloader@v0.24.0",
		"prometheus-nats-exporter":    "github.com/nats-io/prometheus-nats-exporter@v0.20.2",
	}, pins)

	dir := t.TempDir()
	twice := filepath.Join(dir, "twice.txt")
	require.NoError(t, os.WriteFile(twice, []byte("example.com/a/tool@v1.0.0\nexample.com/b/tool@v2.0.0\n"), 0o600))
	_, err = readLiveTools(twice)
	require.ErrorContains(t, err, "two lines install tool")
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(to, b, 0o700))
}
