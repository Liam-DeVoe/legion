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
// rawStreamUpdateSubject is the prefix of a raw JetStream stream-update request's subject.
const (
	testnatsImport         = envoyModule + "/internal/testnats"
	streamUpdateFixture    = "internal/testnats/streams.go"
	rawStreamUpdateSubject = "$JS.API.STREAM.UPDATE."
)

// streamUpdateMethods are the methods that send a stream update: the legacy JetStream context's
// and the jetstream package's UpdateStream, and the jetstream package's CreateOrUpdateStream.
var streamUpdateMethods = map[string]bool{"UpdateStream": true, "CreateOrUpdateStream": true}

// directStreamUpdate is one reference to a stream update method (streamUpdateMethods), or one raw
// JetStream stream-update request, that a test makes itself: path and line, and its top-level
// function.
type directStreamUpdate struct {
	path     string
	line     int
	function string
}

func (u directStreamUpdate) key() string { return u.path + " " + u.function }

func (u directStreamUpdate) String() string {
	return fmt.Sprintf("%s:%d %s", u.path, u.line, u.function)
}

// testSources calls visit with every Go file under root (packages/envoy) that is a test file or a
// file of a test helper package (a non-test file of a package whose name begins or ends with
// "test", as testnats and cmdtest do), parsed, by its slash-separated path from root. It skips
// testdata, node_modules, vendor and hidden directories.
func testSources(root string, visit func(rel string, fileSet *token.FileSet, file *ast.File)) error {
	fileSet := token.NewFileSet()
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
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(rel), fileSet, file)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", root, err)
	}
	return nil
}

// directStreamUpdates walks the test sources under root (testSources) and returns every reference
// to a stream update method (streamUpdateMethods), the legacy JetStream context's and the
// jetstream package's alike, and every raw $JS.API.STREAM.UPDATE request. It lets two kinds of
// reference pass. One is testnats.UpdateStream itself, in streamUpdateFixture, and every call to
// it. The other is a reference in a method named UpdateStream: such a method is a fake JetStream
// context that a test hands the code under test, and it stands in for the server under that
// code's own update.
func directStreamUpdates(root string) ([]directStreamUpdate, error) {
	var found []directStreamUpdate
	err := testSources(root, func(rel string, fileSet *token.FileSet, file *ast.File) {
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
			record := func(pos token.Pos) {
				found = append(found, directStreamUpdate{path: rel, line: fileSet.Position(pos).Line, function: function})
			}
			ast.Inspect(decl, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CallExpr:
					if rawStreamUpdateRequest(node) {
						record(node.Pos())
					}
				case *ast.SelectorExpr:
					if !streamUpdateMethods[node.Sel.Name] {
						return true
					}
					if pkg, ok := node.X.(*ast.Ident); ok && fixtureNames[pkg.Name] {
						return true
					}
					record(node.Pos())
				}
				return true
			})
		}
	})
	return found, err
}

// rawSendMethods are nats.Conn's sends that take their subject as an argument.
var rawSendMethods = map[string]bool{"Request": true, "RequestWithContext": true, "Publish": true, "PublishRequest": true}

// rawStreamUpdateRequest reports whether call is a send (rawSendMethods) of a JetStream
// stream-update subject (streamUpdateSubject). Every such request bypasses testnats.UpdateStream's
// read-back and its narrowing guard. A watch of that subject, or a test of a string against it,
// sends nothing.
func rawStreamUpdateRequest(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !rawSendMethods[selector.Sel.Name] {
		return false
	}
	return slices.ContainsFunc(call.Args, streamUpdateSubject)
}

// streamUpdateSubject reports whether subject is a JetStream stream-update subject as the source
// spells it: a string literal that begins with rawStreamUpdateSubject, such a literal with more
// joined after it by +, or a fmt.Sprintf whose format is such a literal. A subject held in a
// variable is not followed.
func streamUpdateSubject(subject ast.Expr) bool {
	switch subject := subject.(type) {
	case *ast.BasicLit:
		text, err := strconv.Unquote(subject.Value)
		return subject.Kind == token.STRING && err == nil && strings.HasPrefix(text, rawStreamUpdateSubject)
	case *ast.ParenExpr:
		return streamUpdateSubject(subject.X)
	case *ast.BinaryExpr:
		return subject.Op == token.ADD && streamUpdateSubject(subject.X)
	case *ast.CallExpr:
		selector, ok := subject.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Sprintf" || len(subject.Args) == 0 {
			return false
		}
		pkg, ok := selector.X.(*ast.Ident)
		return ok && pkg.Name == "fmt" && streamUpdateSubject(subject.Args[0])
	}
	return false
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
// JetStream context, which it lets pass, and one watches the update subject and one tests a string
// against it, which send nothing; stacktest is a test helper that updates one itself, and
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
	"fmt"
	"strings"
	"time"

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

func createOrUpdate(js jetstream.JetStream, cfg jetstream.StreamConfig) { js.CreateOrUpdateStream(context.Background(), cfg) }

func methodValue(js nats.JetStreamContext, cfg nats.StreamConfig) {
	update := js.UpdateStream
	update(&cfg)
}

func rawRequest(conn *nats.Conn) { _, _ = conn.Request("$JS.API.STREAM.UPDATE.RAW", nil, time.Second) }

func rawJoined(conn *nats.Conn, stream string) { _, _ = conn.Request("$JS.API.STREAM.UPDATE."+stream, nil, time.Second) }

func rawFormatted(conn *nats.Conn, stream string) { _ = conn.Publish(fmt.Sprintf("$JS.API.STREAM.UPDATE.%s", stream), nil) }

func rawNamesTheSubjectElsewhere(conn *nats.Conn, subject string) { _ = conn.Publish(subject, []byte("$JS.API.STREAM.UPDATE.NOT_A_SUBJECT"+subject)) }

func watchesUpdates(conn *nats.Conn) { _, _ = conn.Subscribe("$JS.API.STREAM.UPDATE.>", func(*nats.Msg) {}) }

func isUpdate(subject string) bool { return strings.HasPrefix(subject, "$JS.API.STREAM.UPDATE.") }

func throughFixture(js nats.JetStreamContext, cfg nats.StreamConfig) { fixture.UpdateStream(js, cfg) }

func readOnly(js nats.JetStreamContext) { js.StreamInfo("S") }
`,
	"internal/stacktest/stack.go":   "package stacktest\n\nimport \"github.com/nats-io/nats.go\"\n\nfunc Start(js nats.JetStreamContext, cfg nats.StreamConfig) { js.UpdateStream(&cfg) }\n",
	"internal/production/update.go": "package production\n\nimport \"github.com/nats-io/nats.go\"\n\nfunc Keep(js nats.JetStreamContext, cfg nats.StreamConfig) { js.UpdateStream(&cfg) }\n",
}

// The control of the check above: in a planted tree it finds a stream update a test or a test
// helper sends itself in every shape it names, and nothing in the fixture, in a call to it, in a
// fake JetStream context's UpdateStream, in a watch of the update subject or a test of a string
// against it, or in a package that is neither a test nor a helper.
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
		"internal/demo/demo_test.go createOrUpdate",
		"internal/demo/demo_test.go methodValue",
		"internal/demo/demo_test.go narrow",
		"internal/demo/demo_test.go newAPI",
		"internal/demo/demo_test.go operator",
		"internal/demo/demo_test.go rawFormatted",
		"internal/demo/demo_test.go rawJoined",
		"internal/demo/demo_test.go rawRequest",
		"internal/demo/demo_test.go refuse",
		"internal/stacktest/stack.go Start",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the check found %q in the planted tree, want %q", got, want)
	}
}
