package textcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// testnatsImport is the package of the one function a test updates a stream through,
// testnats.UpdateStream, and streamUpdateFixture is that function, by its file.
const (
	testnatsImport      = envoyModule + "/internal/testnats"
	streamUpdateFixture = "internal/testnats/streams.go"
)

// directStreamUpdate is one call of a method named UpdateStream that a test makes itself: path and
// line of the call, and the top-level function that holds it.
type directStreamUpdate struct {
	path     string
	line     int
	function string
}

func (u directStreamUpdate) key() string { return u.path + " " + u.function }

func (u directStreamUpdate) String() string {
	return fmt.Sprintf("%s:%d %s", u.path, u.line, u.function)
}

// directStreamUpdates walks the Go files under root (packages/envoy) and returns every call of a
// method named UpdateStream, the legacy JetStream context's and the jetstream package's alike, in a
// test file or in a test helper (a non-test file of a package whose name begins or ends with
// "test", as testnats and cmdtest do). It lets two kinds of call pass. One is testnats.UpdateStream
// itself, in streamUpdateFixture, and every call to it. The other is a call in a method named
// UpdateStream: such a method is a fake JetStream context that a test hands the code under test,
// and it stands in for the server under that code's own update.
func directStreamUpdates(root string) ([]directStreamUpdate, error) {
	fileSet := token.NewFileSet()
	var found []directStreamUpdate
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == "testdata" || entry.Name() == "node_modules" || entry.Name() == "vendor" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		dir := filepath.Base(filepath.Dir(path))
		if !strings.HasSuffix(path, "_test.go") && !strings.HasPrefix(dir, "test") && !strings.HasSuffix(dir, "test") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		fixtureNames := map[string]bool{}
		for _, spec := range file.Imports {
			imported, _ := strconv.Unquote(spec.Path.Value)
			if imported != testnatsImport {
				continue
			}
			name := "testnats"
			if spec.Name != nil {
				name = spec.Name.Name
			}
			fixtureNames[name] = true
		}
		for _, decl := range file.Decls {
			function := ""
			if fn, ok := decl.(*ast.FuncDecl); ok {
				function = fn.Name.Name
				if fn.Name.Name == "UpdateStream" && (fn.Recv != nil || rel == streamUpdateFixture) {
					continue
				}
			}
			ast.Inspect(decl, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "UpdateStream" {
					return true
				}
				if pkg, ok := selector.X.(*ast.Ident); ok && fixtureNames[pkg.Name] {
					return true
				}
				found = append(found, directStreamUpdate{path: rel, line: fileSet.Position(call.Pos()).Line, function: function})
				return true
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	return found, nil
}

// A test updates a stream only through testnats.UpdateStream. A test that sends the update itself
// and waits for its answer fails when the answer never comes: nats-server answers an update only
// from the member that leads the stream's group when it applies it, so an update applied while
// the group elects a leader gets no answer. testnats.UpdateStream reads the stream until the
// update shows, under the test's own deadline, and never sends the update again.
func TestNoTestUpdatesAStreamExceptThroughTestnatsUpdateStream(t *testing.T) {
	found, err := directStreamUpdates(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		var lines []string
		for _, update := range found {
			lines = append(lines, update.String())
		}
		t.Errorf("%d stream updates a test sends itself and waits on for an answer that may never come; send each through testnats.UpdateStream:\n%s",
			len(found), strings.Join(lines, "\n"))
	}
}

// plantedStreamUpdates is a tree under the envoy module's path for the control below: demo's tests
// update a stream themselves in each shape the check names, and through the fixture and a fake
// JetStream context, which it lets pass; stacktest is a test helper that updates one itself, and
// production a package that is neither a test nor a helper.
var plantedStreamUpdates = map[string]string{
	"internal/testnats/streams.go": `package testnats

import natsgo "github.com/nats-io/nats.go"

func UpdateStream(js natsgo.JetStreamContext, config natsgo.StreamConfig) {
	js.UpdateStream(&config)
}

func fixtureCaller(js natsgo.JetStreamContext, config natsgo.StreamConfig) { UpdateStream(js, config) }
`,
	"internal/demo/demo_test.go": `package demo

import (
	"context"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	fixture "github.com/sjawhar/envoy/internal/testnats"
)

type rig struct{ js nats.JetStreamContext }

type fake struct{ nats.JetStreamContext }

func (f fake) UpdateStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	return f.JetStreamContext.UpdateStream(cfg, opts...)
}

func operator(js nats.JetStreamContext, cfg nats.StreamConfig) { js.UpdateStream(&cfg) }

func (r rig) refuse(cfg nats.StreamConfig) { r.js.UpdateStream(&cfg) }

func narrow(js nats.JetStreamContext, cfg *nats.StreamConfig) {
	update := func() { js.UpdateStream(cfg) }
	update()
}

func newAPI(js jetstream.JetStream, cfg jetstream.StreamConfig) { js.UpdateStream(context.Background(), cfg) }

func throughFixture(js nats.JetStreamContext, cfg nats.StreamConfig) { fixture.UpdateStream(js, cfg) }

func readOnly(js nats.JetStreamContext) { js.StreamInfo("S") }
`,
	"internal/stacktest/stack.go":   "package stacktest\n\nimport \"github.com/nats-io/nats.go\"\n\nfunc Start(js nats.JetStreamContext, cfg nats.StreamConfig) { js.UpdateStream(&cfg) }\n",
	"internal/production/update.go": "package production\n\nimport \"github.com/nats-io/nats.go\"\n\nfunc Keep(js nats.JetStreamContext, cfg nats.StreamConfig) { js.UpdateStream(&cfg) }\n",
}

// The control of the check above: in a planted tree it finds a stream update a test or a test
// helper sends itself in every shape it names, and nothing in the fixture, in a call to it, in a
// fake JetStream context's UpdateStream, or in a package that is neither a test nor a helper.
func TestTheStreamUpdateCheckFindsEveryShapeItNamesAndNothingElse(t *testing.T) {
	root := t.TempDir()
	for name, contents := range plantedStreamUpdates {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found, err := directStreamUpdates(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, update := range found {
		got = append(got, update.key())
	}
	slices.Sort(got)
	want := []string{
		"internal/demo/demo_test.go narrow",
		"internal/demo/demo_test.go newAPI",
		"internal/demo/demo_test.go operator",
		"internal/demo/demo_test.go refuse",
		"internal/stacktest/stack.go Start",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the check found %q in the planted tree, want %q", got, want)
	}
}
