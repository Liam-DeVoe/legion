package textcheck

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// envoyRoot is the module's directory, packages/envoy, from this package's own.
const envoyRoot = "../.."

// testnatsPackage is the in-process NATS rig, whose natslive half (image.go and image_test.go,
// which an untagged build does not see at all) starts the hub's image.
const testnatsPackage = "github.com/sjawhar/envoy/internal/testnats"

// containerRigs are the packages of this module that may reach testcontainers: the NATS rig, and
// the two whose containers are not NATS (internal/testpg runs pgvector, internal/tests3
// SeaweedFS). Every other package is a finding, whichever testcontainers package it imports:
// modules/nats is the obvious way to start a NATS container, and
// testcontainers.Run(ctx, testnats.Image) is the one that needs no such import.
var containerRigs = []string{
	testnatsPackage,
	"github.com/sjawhar/envoy/internal/testpg",
	"github.com/sjawhar/envoy/internal/tests3",
}

// containerModule is the import prefix every testcontainers package shares.
const containerModule = "github.com/testcontainers/testcontainers-go"

// dockerDriver matches a file of this module driving a container runtime by hand: exec's run with
// docker as the command, however the call spells its context argument, and testcontainers' own
// Docker client. The context argument runs to the end of the line rather than to the first ')',
// since the module already passes context.Background() inline and a test's own is t.Context().
// It is written from pieces so that this file, which has to name them, is not itself a finding.
var dockerDriver = regexp.MustCompile(
	`exec\.Command(Context)?\([^\n]*` + `"docker"` + `|NewDockerClient` + `WithOpts`)

// No package of this module outside the rigs above imports a container runtime directly or drives
// docker by hand: Envoy's agents have no container runtime, and a test that needs one cannot run
// where they do. This pins those two ways to get one — a direct import of testcontainers, read
// from go list's Imports, TestImports and XTestImports, and a hand-driven docker — against the
// packages allowed to have them. It does not prove that no other package needs a runtime: a
// package that imports an allowlisted rig passes and still starts that rig's container, as
// cmd/listener's tests do through testpg.URL whenever DISPATCH_TEST_DATABASE_URL is unset. Direct
// imports are the right scope today because that is the one such case with no opt-out (tests3's
// one consumer, internal/dispatch/files, skips under DISPATCH_TEST_NO_CONTAINERS=1), and a
// separate issue removes Postgres through testpg; widening this guard to walk go list -deps is
// that issue's work, not this one's. internal/smoke's containers are behind the smoke build tag
// and the rig's own behind natslive, so an untagged go list never sees their imports. The walk
// does not go by tag: it reads every .go file, those included, and passes them because none
// drives docker by hand.
func TestNoPackageOutsideTheRigsImportsOrDrivesAContainerRuntime(t *testing.T) {
	t.Run("imports", func(t *testing.T) {
		for _, finding := range containerImporters(t) {
			t.Errorf("package %s imports %s, which belongs to a container runtime; a test that needs a NATS server starts it with testnats.Start, StartConfig or URL", finding.pkg, finding.imported)
		}
	})
	t.Run("docker", func(t *testing.T) {
		for _, file := range dockerDriverFiles(t) {
			t.Errorf("%s drives a container runtime itself; a test that has to stop a server's traffic uses testnats.Server.Freeze, and one that has to stop the server, Server.Stop", file)
		}
	})
}

// The file walk's matcher recognises a hand-driven docker in every way a test of this module spells
// the call, the context argument included: the module already passes context.Background() inline,
// and t.Context() is the test's own. Each line is assembled from pieces so this file, which the walk
// also reads, is not a finding itself.
func TestTheDockerDriverMatchCatchesEveryContextArgument(t *testing.T) {
	const run, docker = "exec." + "Command", `"dock` + `er"`
	for _, line := range []string{
		run + "(" + docker + `, "pause", id)`,
		run + "Context(ctx, " + docker + `, "pause", id)`,
		run + "Context(context.Background(), " + docker + `, "pause", id)`,
		run + "Context(t.Context(), " + docker + `, "unpause", id)`,
		"client.NewDocker" + "ClientWithOpts(client.FromEnv)",
	} {
		if !dockerDriver.MatchString(line) {
			t.Errorf("the docker-driver match misses %s", line)
		}
	}
	if line := run + `("go", "list", "./...")`; dockerDriver.MatchString(line) {
		t.Errorf("the docker-driver match flags %s, which runs no container", line)
	}
}

// containerImport is a package of this module importing a testcontainers package, and the import
// it was found by: a package reaching only testcontainers-go/wait has imported neither a runtime
// client nor modules/nats, and the finding has to say which it did import.
type containerImport struct{ pkg, imported string }

// containerImporters lists every package of this module, outside the rigs allowed one, that
// imports a testcontainers package, from its own sources or its tests', with the first such
// import of each.
func containerImporters(t *testing.T) []containerImport {
	t.Helper()
	const format = `{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`
	list := exec.Command("go", "list", "-f", format, "./...")
	list.Dir = envoyRoot
	out, err := list.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("go list ./...: %v: %s", err, exit.Stderr)
		}
		t.Fatalf("go list ./...: %v", err)
	}
	var importers []containerImport
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || slices.Contains(containerRigs, fields[0]) {
			continue
		}
		for _, imported := range fields[1:] {
			if imported == containerModule || strings.HasPrefix(imported, containerModule+"/") {
				importers = append(importers, containerImport{pkg: fields[0], imported: imported})
				break
			}
		}
	}
	return importers
}

// dockerDriverFiles lists every Go file under packages/envoy that drives a container runtime
// itself, by its path from there. The nested infra module is left out: its pulumilive lane runs a
// real stack of containers on a host that has a runtime, which is the one place in this tree that
// legitimately drives Docker.
func dockerDriverFiles(t *testing.T) []string {
	t.Helper()
	var drivers []string
	err := filepath.WalkDir(envoyRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(envoyRoot, "infra") || entry.Name() == "node_modules" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if dockerDriver.Match(text) {
			relative, err := filepath.Rel(envoyRoot, path)
			if err != nil {
				return err
			}
			drivers = append(drivers, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", envoyRoot, err)
	}
	return drivers
}
