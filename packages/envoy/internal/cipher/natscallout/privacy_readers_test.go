package natscallout

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/cipher/api"
)

// privacyDecisions is the one decision a person makes for every kind of service: whether an entry
// of it, with the whole grant, reads every private project and its Slack channel ids at the
// broker's GET /v1/project-privacy. A listener reads it for its privacy decisions, and the privacy
// measurement times that read on the listener's path (plan C's measurement Job). Every other kind
// does not.
var privacyDecisions = map[Kind]bool{
	KindDispatch:       false,
	KindListener:       true,
	KindDaemon:         false,
	KindMeasure:        false,
	KindPrivacyMeasure: true,
}

// declaredKinds is every Kind constant this package's own source declares, read from its non-test
// files, so a kind added there is a kind this test sees.
func declaredKinds(t *testing.T) map[string]Kind {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)
	files := token.NewFileSet()
	kinds := map[string]Kind{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, path, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				if typ, ok := value.Type.(*ast.Ident); !ok || typ.Name != "Kind" {
					continue
				}
				for i, ident := range value.Names {
					literal, ok := value.Values[i].(*ast.BasicLit)
					require.True(t, ok, "the Kind constant %s is no string literal", ident.Name)
					text, err := strconv.Unquote(literal.Value)
					require.NoError(t, err)
					kinds[ident.Name] = Kind(text)
				}
			}
		}
	}
	return kinds
}

// TestEveryKindHasAPrivacyDecision: every Kind constant the package declares has a row in
// privacyDecisions, and Kind.ReadsProjectPrivacy answers each as its row says. A kind added
// without a row fails here until someone decides whether it reads every private project; the
// default is no.
func TestEveryKindHasAPrivacyDecision(t *testing.T) {
	declared := declaredKinds(t)
	require.NotEmpty(t, declared, "this test read no Kind constant from the package's source")
	for name, kind := range declared {
		want, decided := privacyDecisions[kind]
		if !decided {
			t.Errorf("the kind %s (%q) has no row in privacyDecisions: decide whether it reads every private project at GET /v1/project-privacy (the default is no), and add its row", name, kind)
			continue
		}
		require.Equal(t, want, kind.ReadsProjectPrivacy(), "the kind %q reads project privacy: %v, want %v", kind, kind.ReadsProjectPrivacy(), want)
	}
	for kind := range privacyDecisions {
		require.True(t, slices.Contains(slices.Collect(maps.Values(declared)), kind), "privacyDecisions has a row for %q, which the package does not declare", kind)
	}
}

// TestOnlyWholeGrantReadersAreAnsweredTheRoute: PrivacyReaders keeps, from an entry of every kind
// and a listener held to its exchange projects, exactly the whole-grant listener and the privacy
// measurement, named as the auth service names its users; an account listed nowhere, a session
// and a scoped listener are none.
func TestOnlyWholeGrantReadersAreAnsweredTheRoute(t *testing.T) {
	services, err := ParseServices("listener/example-hub/*=" + listenerAccount +
		" listener/example-scoped/-=system:serviceaccount:example-ns:example-scoped-listener" +
		" dispatch/example-dispatch/*=system:serviceaccount:dispatch:dispatch" +
		" daemon/-/*=" + daemonAccount +
		" measure/-/*=google:" + measureAccount +
		" privacy-measure/project-privacy-measure/*=" + privacyMeasureAccount)
	require.NoError(t, err)
	require.Equal(t, api.PrivacyReaders{
		K8s: map[string]string{
			listenerAccount:       "listener/example-hub",
			privacyMeasureAccount: "privacy-measure/project-privacy-measure",
		},
		Google: map[string]string{},
	}, PrivacyReaders(services))
}
