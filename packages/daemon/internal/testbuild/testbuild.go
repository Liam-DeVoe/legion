// Package testbuild builds a command of this checkout's envoy module once per test process,
// however many of a package's tests run it. A `go build` of cmd/dispatch is seconds on a warm
// build cache and most of a minute on a cold one, and the tests that run a real Dispatch or a real
// listener paid it once each.
//
// It is an ordinary package rather than a _test.go file so every test package can import it; Go
// cannot share test-only code across packages. The binaries go into one directory of the process,
// which Main removes: a test's own temporary directory is gone before the next test runs.
package testbuild

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// RootEnv names the checkout whose packages/envoy is built. A driver that runs one checkout's
// tests against another's sources sets it (internal/daemon's handoff records live proof); unset,
// the checkout the test runs in is built.
const RootEnv = "LEGION_E2E_REPO_ROOT"

// shared is this test process's build directory and the build of each command. Main sets main
// before the package's tests run; EnvoyBinary refuses to run without it, since only Main removes
// the directory.
var shared struct {
	main bool

	mu     sync.Mutex
	dir    string
	builds map[string]*build
}

// build is one command's binary, built on first use.
type build struct {
	once sync.Once
	path string
	err  error
}

// EnvoyBinary is the path of the envoy module's command — "./cmd/dispatch", "./cmd/listener" —
// built once per test process and gone when the process ends.
func EnvoyBinary(t testing.TB, command string) string {
	t.Helper()
	return EnvoyBinaryTagged(t, command, "")
}

// EnvoyBinaryTagged is EnvoyBinary built with the Go build tags tags, comma-separated, as `go
// build -tags` takes them: a test that needs a seam the command builds only under a tag of its own
// (the listener's envoytestclock, its kept_clock_envoytestclock.go) gets a binary of its own, built
// once per process for that set of tags, beside the untagged one every other test runs.
func EnvoyBinaryTagged(t testing.TB, command, tags string) string {
	t.Helper()
	if !shared.main {
		t.Fatal("testbuild.EnvoyBinary needs the package's TestMain to be: func TestMain(m *testing.M) { os.Exit(testbuild.Main(m.Run)) }")
	}
	built := memo(command, tags)
	built.once.Do(func() { built.path, built.err = buildEnvoy(command, tags) })
	if built.err != nil {
		t.Fatal(built.err)
	}
	return built.path
}

// Main runs a package's tests and removes the directory their binaries were built into, answering
// run's code — 1 instead of 0 when the removal failed, so a directory left behind is a red run.
//
// It takes run rather than *testing.M so a package that has a TestMain of its own composes the
// two:
//
//	func TestMain(m *testing.M) { os.Exit(testbuild.Main(func() int { return testnats.Main(m) })) }
func Main(run func() int) int {
	shared.main = true
	code := run()
	shared.mu.Lock()
	dir := shared.dir
	shared.mu.Unlock()
	if dir == "" {
		return code
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "testbuild: remove the build directory %s: %v\n", dir, err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// memo is command's build under tags, the same one for every caller of this process.
func memo(command, tags string) *build {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.builds == nil {
		shared.builds = map[string]*build{}
	}
	key := command + "\x00" + tags
	if shared.builds[key] == nil {
		shared.builds[key] = &build{}
	}
	return shared.builds[key]
}

// buildEnvoy builds command under tags into the process's build directory and logs one line naming
// it and how long it took: a run that logs two of one command and one set of tags has lost the
// memoisation this package exists for. A tagged build is named for its tags too, so it never
// overwrites the untagged binary.
func buildEnvoy(command, tags string) (string, error) {
	module, err := envoyModule()
	if err != nil {
		return "", err
	}
	dir, err := buildDir()
	if err != nil {
		return "", err
	}
	name, args, built := path.Base(command), []string{"build", "-C", module}, command
	if tags != "" {
		name += "-" + strings.ReplaceAll(tags, ",", "-")
		args = append(args, "-tags", tags)
		built += " -tags " + tags
	}
	binary := filepath.Join(dir, name)
	started := time.Now()
	build := exec.Command("go", append(args, "-o", binary, command)...)
	if output, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build %s in %s: %v\n%s", built, module, err, output)
	}
	fmt.Fprintf(os.Stderr, "testbuild: built %s in %s\n", built, time.Since(started).Round(time.Millisecond))
	return binary, nil
}

// buildDir is the one directory this process builds into, made on first use.
func buildDir() (string, error) {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.dir != "" {
		return shared.dir, nil
	}
	dir, err := os.MkdirTemp("", "legion-testbuild")
	if err != nil {
		return "", fmt.Errorf("make the build directory: %w", err)
	}
	shared.dir = dir
	return dir, nil
}

// envoyModule is the packages/envoy of RootEnv's checkout, or of the one holding the test's
// working directory.
func envoyModule() (string, error) {
	root := os.Getenv(RootEnv)
	if root == "" {
		dir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("read the working directory: %w", err)
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "packages", "envoy", "go.mod")); err == nil {
				root = dir
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return "", fmt.Errorf("no packages/envoy/go.mod above the working directory; set %s to the checkout to build", RootEnv)
			}
			dir = parent
		}
	}
	module, err := filepath.Abs(filepath.Join(root, "packages", "envoy"))
	if err != nil {
		return "", fmt.Errorf("resolve the envoy module: %w", err)
	}
	return module, nil
}
