package textcheck

import (
	"go/ast"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// streamCreateMethods are the JetStream context methods that create a stream or a bucket: the
// legacy context's AddStream and CreateKeyValue, and the jetstream package's CreateStream and
// CreateKeyValue.
var streamCreateMethods = map[string]bool{"AddStream": true, "CreateKeyValue": true, "CreateStream": true}

// clusterContext reports whether expr is a JetStream context a test opened on a testnats cluster
// with a fixed request wait: a call of Cluster.JetStream(t), whose one argument is the test, or of
// Cluster.MemberJetStream; a JetStream context of a connection to one member (memberConnection),
// whatever its options; a variable known holds, one assigned such a context in the same function;
// or the result at its index of a call to a function of the same package that returns such a
// context (clusterContextHelpers, as internal/bus's startClusterJetStream and memberJetStream).
// nats.Conn's own JetStream takes options, never the test, so a context on a lone server's
// connection is none.
func clusterContext(expr ast.Expr, known clusterNames) bool {
	switch expr := expr.(type) {
	case *ast.Ident:
		return known.contexts[expr.Name]
	case *ast.ParenExpr:
		return clusterContext(expr.X, known)
	case *ast.CallExpr:
		selector, ok := expr.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		switch selector.Sel.Name {
		case "MemberJetStream":
			return true
		case "JetStream":
			if conn, ok := selector.X.(*ast.Ident); ok && known.conns[conn.Name] {
				return true
			}
			if len(expr.Args) != 1 {
				return false
			}
			_, isIdent := expr.Args[0].(*ast.Ident)
			return isIdent
		}
	}
	return false
}

// clusterNames are the variables of one function that hold a cluster context (clusterContext),
// and those that hold a connection to one member of a cluster (memberConnection).
type clusterNames struct {
	contexts, conns map[string]bool
}

// memberConnection reports whether expr is a connection a test opened to one member of a cluster:
// a Connect whose URL is that member's, <cluster>.Servers[i].URL().
func memberConnection(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) < 2 {
		return false
	}
	if selector, ok := call.Fun.(*ast.SelectorExpr); !ok || selector.Sel.Name != "Connect" {
		return false
	}
	url, ok := call.Args[1].(*ast.CallExpr)
	if !ok {
		return false
	}
	method, ok := url.Fun.(*ast.SelectorExpr)
	if !ok || method.Sel.Name != "URL" {
		return false
	}
	member, ok := method.X.(*ast.IndexExpr)
	if !ok {
		return false
	}
	servers, ok := member.X.(*ast.SelectorExpr)
	return ok && servers.Sel.Name == "Servers"
}

// clusterContextsIn walks body in source order and returns the names it assigns a cluster context
// (clusterContext) or a member's connection (memberConnection): each name on the left of an
// assignment whose right side, at its place or as the first of a call's results, is one, and each
// name at the index helpers gives of a call to that helper. It calls found with each call and each
// return statement, and the names known there.
func clusterContextsIn(body ast.Node, helpers map[string]int, found func(node ast.Node, known clusterNames)) clusterNames {
	known := clusterNames{contexts: map[string]bool{}, conns: map[string]bool{}}
	ast.Inspect(body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			for i, right := range node.Rhs {
				name, ok := node.Lhs[i].(*ast.Ident)
				switch {
				case !ok:
				case memberConnection(right):
					known.conns[name.Name] = true
				case clusterContext(right, known):
					known.contexts[name.Name] = true
				}
			}
			if len(node.Rhs) == 1 {
				if index, ok := isHelperCall(node.Rhs[0], helpers); ok && index < len(node.Lhs) {
					if name, ok := node.Lhs[index].(*ast.Ident); ok {
						known.contexts[name.Name] = true
					}
				}
			}
		case *ast.CallExpr, *ast.ReturnStmt:
			found(node, known)
		}
		return true
	})
	return known
}

// isHelperCall reports whether expr is a call of a helper in helpers (clusterContextsIn).
func isHelperCall(expr ast.Expr, helpers map[string]int) (int, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return 0, false
	}
	name, ok := call.Fun.(*ast.Ident)
	if !ok {
		return 0, false
	}
	index, ok := helpers[name.Name]
	return index, ok
}

// clusterContextHelpers is each function of files, the files of one package, that returns a
// cluster context (clusterContext), by its name, with the index of that result. A helper that
// returns another helper's context counts as well.
func clusterContextHelpers(files []*ast.File) map[string]int {
	helpers := map[string]int{}
	for changed := true; changed; {
		changed = false
		for _, file := range files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body == nil {
					continue
				}
				if _, known := helpers[fn.Name.Name]; known {
					continue
				}
				clusterContextsIn(fn.Body, helpers, func(node ast.Node, known clusterNames) {
					ret, ok := node.(*ast.ReturnStmt)
					if !ok {
						return
					}
					for index, result := range ret.Results {
						_, viaHelper := isHelperCall(result, helpers)
						if _, done := helpers[fn.Name.Name]; !done && (clusterContext(result, known) || viaHelper && len(ret.Results) == 1) {
							helpers[fn.Name.Name] = index
							changed = true
						}
					}
				})
			}
		}
	}
	return helpers
}

// directClusterCreates walks the test sources under root (testSources) and returns every create
// of a stream or a bucket (streamCreateMethods) a test sends itself through a cluster context
// (clusterContext). Such a context bounds each request by JetStream's fixed wait, ten seconds,
// and nats-server answers a create of more than one copy only from the stream's first leader,
// once its members have elected one, which on a loaded machine can come later: the create then
// fails with `context deadline exceeded` although the server made the stream. A test creates a
// stream on a cluster with testnats's Cluster.CreateStream and a bucket with
// Cluster.CreateKeyValue, which send it once through a context of their own and fail the test
// with the server's refusal. A context handed in as a parameter is not followed, nor one opened on
// a connection held anywhere but a variable of the same function.
func directClusterCreates(root string) ([]directStreamUpdate, error) {
	type parsed struct {
		rel     string
		fileSet *token.FileSet
		file    *ast.File
	}
	packages := map[string][]parsed{}
	if err := testSources(root, func(rel string, fileSet *token.FileSet, file *ast.File) {
		dir := path.Dir(rel)
		packages[dir] = append(packages[dir], parsed{rel: rel, fileSet: fileSet, file: file})
	}); err != nil {
		return nil, err
	}
	var found []directStreamUpdate
	for _, files := range packages {
		asts := make([]*ast.File, 0, len(files))
		for _, file := range files {
			asts = append(asts, file.file)
		}
		helpers := clusterContextHelpers(asts)
		for _, file := range files {
			for _, decl := range file.file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				clusterContextsIn(fn.Body, helpers, func(node ast.Node, known clusterNames) {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return
					}
					selector, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || !streamCreateMethods[selector.Sel.Name] {
						return
					}
					if clusterContext(selector.X, known) {
						found = append(found, directStreamUpdate{path: file.rel, line: file.fileSet.Position(call.Pos()).Line, function: fn.Name.Name})
					}
				})
			}
		}
	}
	slices.SortFunc(found, func(a, b directStreamUpdate) int { return strings.Compare(a.String(), b.String()) })
	return found, nil
}

// A test creates a stream or a bucket on a cluster only through testnats's Cluster.CreateStream
// or Cluster.CreateKeyValue (directClusterCreates).
func TestNoTestCreatesAStreamOnAClusterExceptThroughTheFixture(t *testing.T) {
	found, err := directClusterCreates(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		var lines []string
		for _, create := range found {
			lines = append(lines, create.String())
		}
		t.Errorf("%d creates a test sends itself through a cluster's JetStream context, whose fixed wait a first election can outlast; create each with testnats's Cluster.CreateStream or Cluster.CreateKeyValue:\n%s",
			len(found), strings.Join(lines, "\n"))
	}
}

// plantedClusterCreates is a tree under the envoy module's path for the control below: demo's
// tests create a stream or a bucket themselves through each cluster context the check names, and
// through the fixture, a lone server's context and a fake context's AddStream, which it lets pass;
// production is a package that is neither a test nor a helper.
var plantedClusterCreates = map[string]string{
	"internal/demo/helpers_test.go": `package demo

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/testnats"
)

func startClusterJetStream(t *testing.T) (*testnats.Cluster, nats.JetStreamContext) {
	cluster := testnats.StartCluster(t, 3)
	return cluster, cluster.JetStream(t)
}

func clusterJetStream(t *testing.T) nats.JetStreamContext {
	_, js := startClusterJetStream(t)
	return js
}

func memberJetStream(t *testing.T, cluster *testnats.Cluster, i int) (*nats.Conn, nats.JetStreamContext) {
	conn := testnats.Connect(t, cluster.Servers[i].URL())
	js, _ := conn.JetStream(nats.MaxWait(0))
	return conn, js
}
`,
	"internal/demo/demo_test.go": `package demo

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/testnats"
)

func chained(t *testing.T, cluster *testnats.Cluster, cfg nats.StreamConfig) { cluster.JetStream(t).AddStream(&cfg) }

func held(t *testing.T, cluster *testnats.Cluster, cfg nats.StreamConfig) {
	js := cluster.JetStream(t)
	js.AddStream(&cfg)
}

func member(t *testing.T, cluster *testnats.Cluster, kv nats.KeyValueConfig) {
	js := cluster.MemberJetStream(t, 0)
	js.CreateKeyValue(&kv)
}

func throughHelper(t *testing.T, cfg nats.StreamConfig) {
	cluster, js := startClusterJetStream(t)
	js.AddStream(&cfg)
	cluster.CreateStream(t, cfg)
}

func throughHelperOfAHelper(t *testing.T, cfg nats.StreamConfig) {
	js := clusterJetStream(t)
	js.AddStream(&cfg)
}

func throughMemberHelper(t *testing.T, cluster *testnats.Cluster, kv nats.KeyValueConfig) {
	conn, js := memberJetStream(t, cluster, 0)
	js.CreateKeyValue(&kv)
	cluster.CreateKeyValue(t, conn, &kv)
}

func onAMemberConnection(t *testing.T, cluster *testnats.Cluster, cfg nats.StreamConfig) {
	conn := testnats.Connect(t, cluster.Servers[1].URL())
	js, _ := conn.JetStream()
	js.AddStream(&cfg)
}

func throughFixture(t *testing.T, cluster *testnats.Cluster, conn *nats.Conn, cfg nats.StreamConfig, kv nats.KeyValueConfig) {
	cluster.CreateStream(t, cfg)
	cluster.CreateKeyValue(t, conn, &kv)
}

func loneServer(t *testing.T, conn *nats.Conn, cfg nats.StreamConfig) {
	js, _ := conn.JetStream()
	js.AddStream(&cfg)
	waited, _ := conn.JetStream(nats.MaxWait(0))
	waited.AddStream(&cfg)
	lone := testnats.Connect(t, testnats.URL(t))
	loneJS, _ := lone.JetStream()
	loneJS.AddStream(&cfg)
}

type fake struct{ nats.JetStreamContext }

func (f fake) AddStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	return f.JetStreamContext.AddStream(cfg, opts...)
}

func readOnly(t *testing.T, cluster *testnats.Cluster) { cluster.JetStream(t).StreamInfo("S") }
`,
	"internal/production/create.go": "package production\n\nimport \"testing\"\n\ntype cluster interface{ JetStream(testing.TB) interface{ AddStream(any) } }\n\nfunc Create(t *testing.T, c cluster) { c.JetStream(t).AddStream(nil) }\n",
}

// The control of the check above: in a planted tree it finds a create a test sends itself through
// every cluster context it names, and nothing through the fixture, a lone server's context or a
// fake context, or in a package that is neither a test nor a helper.
func TestTheClusterCreateCheckFindsEveryShapeItNamesAndNothingElse(t *testing.T) {
	root := t.TempDir()
	for name, contents := range plantedClusterCreates {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found, err := directClusterCreates(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, create := range found {
		got = append(got, create.key())
	}
	slices.Sort(got)
	want := []string{
		"internal/demo/demo_test.go chained",
		"internal/demo/demo_test.go held",
		"internal/demo/demo_test.go member",
		"internal/demo/demo_test.go onAMemberConnection",
		"internal/demo/demo_test.go throughHelper",
		"internal/demo/demo_test.go throughHelperOfAHelper",
		"internal/demo/demo_test.go throughMemberHelper",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the check found %q in the planted tree, want %q", got, want)
	}
}
