//go:build twobuild

// Two builds of Dispatch serving one Postgres and one NATS at once, which is what a rolling release
// runs: the build that serves now (the previous build) and the build that rolls in beside it (this
// tree). Each row starts one envoy-dispatch of each build, on one database storetest cloned at this
// tree's schema (the previous build boots on it, which is the SQL half of the rule in
// cmd/dispatch/README.md) and one NATS server, with one signing key, and drives the surface the
// other build's clients use: the CLI (twobuild_cli_test.go), the stored documents and the document
// socket (twobuild_documents_test.go), the messages and subscription settings on the bus
// (twobuild_messages_test.go), and what the page admission does while the previous build's process
// is live and once it has left the registry (twobuild_transition_test.go). This file is the
// harness all four start their builds with.
//
// The previous build is a directory packages/envoy/scripts/build-dispatch-build.sh writes, named by
// DISPATCH_PREVIOUS_BUILD. The suite skips only when that variable is unset; a directory it names
// that lacks a part fails the suite, naming the parts it lacks. The current side is this tree: its
// server from cmdtest.Build, its CLI run as `bun packages/envoy-client/bin/dispatch.ts`, its corpus
// read from the working tree.
//
// Its own build tag and its own CI job, dispatch-two-builds in .github/workflows/ci.yaml, for the
// reason the multiprocess suite has its own: a row waits for durable hand-overs and settings
// polls, tens of seconds each. That job builds the previous release with build-dispatch-build.sh,
// caches the build by its commit, and runs this suite against it.

package main

import (
	"cmp"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
)

// ───── the previous build ───────────────────────────────────────────────────

// previousBuildVariable names the directory build-dispatch-build.sh wrote for the previous build.
const previousBuildVariable = "DISPATCH_PREVIOUS_BUILD"

// previousBuildParts is every part of a previous build's directory a row reads, as
// build-dispatch-build.sh lays it out.
var previousBuildParts = []struct {
	path string
	dir  bool
}{
	{"COMMIT", false},
	{"envoy-dispatch", false},
	{"dispatch.js", false},
	{"web/index.html", false},
	{"pmdoc/testdata/corpus", true},
	{"pmdoc/schema/blocks.json", false},
	{"text/testdata/dispatch-text-references.json", false},
	{"text/testdata/dispatch-href-references.json", false},
}

// previousBuild is the directory of the previous build and the commit it was built from.
type previousBuild struct {
	dir    string
	commit string
}

// openPreviousBuild reads DISPATCH_PREVIOUS_BUILD. It skips the test when the variable is unset,
// and fails it when the directory the variable names lacks any part a row reads.
func openPreviousBuild(t *testing.T) previousBuild {
	t.Helper()
	dir, set := os.LookupEnv(previousBuildVariable)
	if !set {
		t.Skipf("%s is unset. Build the previous build with packages/envoy/scripts/build-dispatch-build.sh --previous-release --out <dir>, then set %s=<dir>.",
			previousBuildVariable, previousBuildVariable)
	}
	if dir == "" {
		t.Fatalf("%s is set but empty. Set it to the directory build-dispatch-build.sh wrote.", previousBuildVariable)
	}
	var missing []string
	for _, part := range previousBuildParts {
		info, err := os.Stat(filepath.Join(dir, part.path))
		switch {
		case err != nil:
			missing = append(missing, part.path)
		case part.dir && !info.IsDir(), !part.dir && info.IsDir():
			missing = append(missing, part.path+" (wrong kind of file)")
		case part.dir:
			if entries, err := os.ReadDir(filepath.Join(dir, part.path)); err != nil || len(entries) == 0 {
				missing = append(missing, part.path+" (empty)")
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s=%s is not a complete previous build. It has no %s. Build it again with packages/envoy/scripts/build-dispatch-build.sh.",
			previousBuildVariable, dir, strings.Join(missing, ", "))
	}
	commit, err := os.ReadFile(filepath.Join(dir, "COMMIT"))
	if err != nil {
		t.Fatalf("read the previous build's COMMIT: %v", err)
	}
	return previousBuild{dir: dir, commit: strings.TrimSpace(string(commit))}
}

func (b previousBuild) path(parts ...string) string {
	return filepath.Join(append([]string{b.dir}, parts...)...)
}

// ───── the two builds ───────────────────────────────────────────────────────

// twoBuilds is one process of each build on one database and one NATS server: a is the previous
// build's, b is this tree's.
type twoBuilds struct {
	*dispatchPair
	build previousBuild
	// binary is this tree's envoy-dispatch, built once so a restart runs the same binary.
	binary string
}

// newTwoBuilds opens the previous build, the database and the NATS server, and starts nothing.
func newTwoBuilds(t *testing.T) *twoBuilds {
	t.Helper()
	build := openPreviousBuild(t)
	return &twoBuilds{dispatchPair: newPair(t), build: build, binary: cmdtest.Build(t, "envoy-dispatch")}
}

// newTwoBuildsRaisingTheSchema is newTwoBuilds with this tree's envoy-dispatch built from a block
// manifest one version above this tree's, as a release that raises the document schema is
// (buildRaisingTheSchema), and returns the page that build presents on a document socket.
func newTwoBuildsRaisingTheSchema(t *testing.T) (*twoBuilds, pageParameters) {
	t.Helper()
	build := openPreviousBuild(t)
	binary, manifest := buildRaisingTheSchema(t)
	// That release's page sends schema_creatable, as this tree's does
	// (packages/dispatch/web/src/features/doc/connection.ts).
	page := readManifest(t, manifest).page(true)
	return &twoBuilds{dispatchPair: newPair(t), build: build, binary: binary}, page
}

// raisedBlock is the typed block the raised manifest adds (raiseManifest).
const raisedBlock = "raised"

// buildRaisingTheSchema builds this tree's envoy-dispatch with raiseManifest's manifest embedded in
// place of its own (pmdoc's blockSchemaJSON), through go build -overlay, so its process reads,
// creates and registers the raised numbers. It returns the binary and that manifest's path.
//
// go build reads the last -overlay it is given, so the overlay passed here also carries every
// replacement of the one ENVOY_TEST_GO_OVERLAY names (cmdtest.BuildCommand), and raises the
// manifest that one puts in place of this tree's where it puts one.
func buildRaisingTheSchema(t *testing.T) (binary, manifest string) {
	t.Helper()
	embedded, err := filepath.Abs(filepath.Join("..", "..", "internal", "dispatch", "pmdoc", "schema", "blocks.json"))
	if err != nil {
		t.Fatalf("locate this tree's block manifest: %v", err)
	}
	overlay := struct{ Replace map[string]string }{Replace: map[string]string{}}
	if probe := os.Getenv("ENVOY_TEST_GO_OVERLAY"); probe != "" {
		raw, err := os.ReadFile(probe)
		if err != nil {
			t.Fatalf("read ENVOY_TEST_GO_OVERLAY: %v", err)
		}
		if err := json.Unmarshal(raw, &overlay); err != nil {
			t.Fatalf("decode ENVOY_TEST_GO_OVERLAY %s: %v", probe, err)
		}
	}
	dir := t.TempDir()
	manifest = filepath.Join(dir, "blocks.json")
	writeJSON(t, manifest, raiseManifest(t, cmp.Or(overlay.Replace[embedded], embedded)))
	overlay.Replace[embedded] = manifest
	overlayPath := filepath.Join(dir, "overlay.json")
	writeJSON(t, overlayPath, overlay)
	return cmdtest.BuildCommand(t, cmdtest.Command{Name: "envoy-dispatch", Flags: []string{"-overlay", overlayPath}}), manifest
}

// raiseManifest is the block manifest at path as the release that introduces an element
// creatable at once declares it (pmdoc.UseStagedBlockForTest, creatable): its version raised by
// one, and one more typed block, raisedBlock, read and created from the raised version. So a build
// of it reads and creates up to the raised version, and its page creates what a build of path
// does not read.
func raiseManifest(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the block manifest to raise: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode the block manifest %s: %v", path, err)
	}
	version, versioned := manifest["version"].(float64)
	types, typed := manifest["types"].([]any)
	if !versioned || !typed {
		t.Fatalf("the block manifest %s declares no version or no types array", path)
	}
	raised := int(version) + 1
	manifest["version"] = raised
	manifest["types"] = append(types, map[string]any{
		"name": raisedBlock, "content": "block+", "render": "host", "attributes": map[string]any{},
		"since": raised, "creatable_since": raised,
	})
	return manifest
}

// writeJSON writes value to path as JSON.
func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// options is what both builds' processes start with: the pair's NATS, the one signing key, and env.
func (b *twoBuilds) options(env map[string]string) dispatchOptions {
	return dispatchOptions{NATSURL: b.nats, SigningKey: multiprocessSigningKey, Env: env}
}

// startPrevious starts a process of the previous build, its own web app served, and returns once it
// answers /healthz. The previous build may predate DISPATCH_LISTEN_FD, so it binds its port itself.
func (b *twoBuilds) startPrevious(t *testing.T, env map[string]string) *dispatchProcess {
	t.Helper()
	options := b.options(env)
	options.Binary, options.WebDist = b.build.path("envoy-dispatch"), b.build.path("web")
	options.BindsItsPort = true
	b.a = startDispatchProcessWith(t, b.databaseURL(t), options)
	b.a.waitHealthy(t)
	return b.a
}

// startCurrent starts a process of this tree and returns once it answers /healthz.
func (b *twoBuilds) startCurrent(t *testing.T, env map[string]string) *dispatchProcess {
	t.Helper()
	options := b.options(env)
	options.Binary = b.binary
	b.b = startDispatchProcessWith(t, b.databaseURL(t), options)
	b.b.waitHealthy(t)
	return b.b
}

func (b *twoBuilds) previous() *dispatchProcess { return b.a }
func (b *twoBuilds) current() *dispatchProcess  { return b.b }

// buildName is what a failure calls a process: which build it runs.
func (b *twoBuilds) buildName(p *dispatchProcess) string {
	if p == b.a {
		return b.previousName()
	}
	return b.currentName()
}

// previousName and currentName are what a failure calls each build.
func (b *twoBuilds) previousName() string {
	return "previous build (" + shortCommit(b.build.commit) + ")"
}

func (b *twoBuilds) currentName() string { return "current build" }

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// stop stops p as a deploy does, SIGTERM and its graceful shutdown, and waits for it to exit.
func stop(t *testing.T, p *dispatchProcess) {
	t.Helper()
	p.Terminate(t)
	p.WaitExit(t, "SIGTERM")
}

// getJSON reads path from p as the test's login and decodes it into into.
func getJSON(t *testing.T, p *dispatchProcess, path string, into any) []byte {
	t.Helper()
	answer := p.call(t, http.MethodGet, path, "", http.StatusOK)
	if into != nil {
		if err := json.Unmarshal(answer, into); err != nil {
			t.Fatalf("decode GET %s: %v: %s", path, err, answer)
		}
	}
	return answer
}

// jsonBody is value as a request body.
func jsonBody(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode a request body: %v", err)
	}
	return string(body)
}
