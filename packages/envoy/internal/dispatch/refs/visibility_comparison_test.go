package refs

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore/graphstoretest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The two kinds the tests below register, one through each constructor of a kind that takes
// `author`, so the person comparison and the flag are held to what they do before any kind
// dispatch://INFRA-1950 adds is registered: a kind of that child registers through one of the two
// in the commit that adds it. Neither is a kind of the store, so each is registered only for the
// test that reads it (registerForTest), and the census of the registry
// (TestEveryNodeKindAnswersAVisibilityPredicate) never meets it.
const (
	personComparedTestKind     = "refs_person_compared_test"
	credentialComparedTestKind = "refs_credential_compared_test"
)

// This test binary hands the store its two test kinds through the hand-off the registry's own
// init calls, once, as that init hands the kinds it flags: the store takes a node reserved for
// its author of an admitted kind alone.
func init() {
	graphstore.AdmitAuthorOnly(personComparedTestKind, credentialComparedTestKind)
}

// thisChildsAuthorKinds are the kinds dispatch://INFRA-1950 adds that take a node reserved for its
// author (R1): the kinds whose registry entry carries the flag, each once that child registers
// it. A lens_kind is a kind of that child too, and is always project-visible.
var thisChildsAuthorKinds = []string{
	graphstore.KindConstraint, graphstore.KindInteraction, graphstore.KindLens, graphstore.KindLensBlock, graphstore.KindRisk,
}

// registerForTest adds entries to the registry for one test, and takes them off when it ends, with
// the lists read off the registry (deriveRegistryLists) moved both times.
func registerForTest(t *testing.T, entries map[string]nodeKind) {
	t.Helper()
	for kind, entry := range entries {
		if _, held := nodeRegistry[kind]; held {
			t.Fatalf("the registry holds %s already, so this test would replace an entry of the build", kind)
		}
		nodeRegistry[kind] = entry
	}
	deriveRegistryLists()
	t.Cleanup(func() {
		for kind := range entries {
			delete(nodeRegistry, kind)
		}
		deriveRegistryLists()
	})
}

// TestTheKindsThatTakeAuthorAreThisChildsRegisteredKinds holds the registry's flag - every read of
// the kind binds its predicate, so the kind may hold a node reserved for its author - and the
// kinds dispatch://INFRA-1950 registers to one list: the flagged kinds are exactly that child's
// author kinds the registry holds, so a flag on any other kind and a registered author kind with
// no flag each fail here, named, and neither list grows alone. A lens_kind is never flagged. The
// registry's init handed the store every flagged kind (graphstore.AdmitAuthorOnly), and the store
// admits no other kind but the two this test binary hands it itself. Only the two constructors of
// a kind that takes `author` set the flag, each with the comparison its name says.
func TestTheKindsThatTakeAuthorAreThisChildsRegisteredKinds(t *testing.T) {
	flagged := authorOnlyKinds()
	var registered []string
	for _, kind := range thisChildsAuthorKinds {
		if _, held := nodeRegistry[kind]; held {
			registered = append(registered, kind)
		}
	}
	slices.Sort(registered)
	t.Logf("the registry flags %v, and holds %v of the kinds %v that take author", flagged, registered, thisChildsAuthorKinds)
	if !slices.Equal(flagged, registered) {
		t.Errorf("the registry flags %v as kinds that take a node reserved for its author, and holds %v of %v: "+
			"the two are one list, each kind flagged in the commit that registers it", flagged, registered, thisChildsAuthorKinds)
	}
	if entry, held := nodeRegistry[graphstore.KindLensKind]; held && entry.authorOnly {
		t.Errorf("the registry flags %s, which is always project-visible", graphstore.KindLensKind)
	}
	// The kinds of that child registered so far, each as R1 has it: a constraint is flagged and
	// compares the person a credential proves, and a lens kind of the catalogue is registered
	// unflagged. Both are written by migration 20261116123354, which is where they register.
	if entry, held := nodeRegistry[graphstore.KindConstraint]; !held || !entry.authorOnly || entry.visibility.compares != comparesPerson {
		t.Errorf("the registry holds %s as %+v (held %t), want it flagged and comparing the person", graphstore.KindConstraint, entry, held)
	}
	if _, held := nodeRegistry[graphstore.KindLensKind]; !held {
		t.Errorf("the registry holds no %s, which the catalogue's migration writes", graphstore.KindLensKind)
	}

	admitted := graphstore.AuthorOnlyKinds()
	want := slices.Sorted(slices.Values(append(slices.Clone(flagged), personComparedTestKind, credentialComparedTestKind)))
	t.Logf("the store admits %v", admitted)
	if !slices.Equal(admitted, want) {
		t.Errorf("the store admits %v as kinds that take a node reserved for its author, want the registry's flagged kinds "+
			"and this binary's two test kinds, %v", admitted, want)
	}

	for _, constructed := range []struct {
		name     string
		entry    nodeKind
		flagged  bool
		compares comparison
	}{
		{"resolvedKind", resolvedKind(loadStoreNodes, nodeVisibility{}), false, comparesCredential},
		{"unresolvedKind", unresolvedKind(nodeVisibility{}), false, comparesCredential},
		{"personAuthoredKind", personAuthoredKind(loadStoreNodes), true, comparesPerson},
		{"credentialAuthoredKind", credentialAuthoredKind(loadStoreNodes), true, comparesCredential},
	} {
		if constructed.entry.authorOnly != constructed.flagged || constructed.entry.visibility.compares != constructed.compares ||
			!constructed.entry.stated {
			t.Errorf("%s makes an entry flagged %t comparing %d (stated %t), want flagged %t comparing %d",
				constructed.name, constructed.entry.authorOnly, constructed.entry.visibility.compares, constructed.entry.stated,
				constructed.flagged, constructed.compares)
		}
	}
}

// TestAKindComparesThePersonOrTheCredentialItsEntryStates holds each rendered predicate to the
// comparison the kind's registry entry states (dispatch://INFRA-1950/spec#b-r-author). A kind
// that compares the person (personAuthoredKind) answers a node reserved for its author to every
// credential that proves the person its author names - alice's personal token reads what her
// cookie wrote, under any spelling of her login, and her cookie what her token wrote - and to no
// other person's; where the author names no person, a workload, it answers the node to that very
// credential. A kind that compares the whole credential (credentialAuthoredKind), and every kind
// dispatch://INFRA-1149 registers, answers the node to the credential that wrote it alone, so
// alice's cookie reads nothing of what her token recorded. A node whose author is nobody is
// answered to nobody under either comparison, and a project node to everybody.
//
// Every form the registry renders is read for each node and each reader - the loader's own read
// (Node, which binds VisibleRow), VisibleKind, VisibleKindRow, VisibleEnd and VisibleNode - and
// each answers the same.
func TestAKindComparesThePersonOrTheCredentialItsEntryStates(t *testing.T) {
	registerForTest(t, map[string]nodeKind{
		personComparedTestKind:     personAuthoredKind(loadStoreNodes),
		credentialComparedTestKind: credentialAuthoredKind(loadStoreNodes),
	})
	database := storetest.Open(t)
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	// The two test kinds are types of no migration, and the store writes a node only of a type it
	// holds, so their types are seeded first.
	if err := graphstoretest.SeedSupertags(ctx, tx, personComparedTestKind, credentialComparedTestKind); err != nil {
		t.Fatalf("seed the test kinds' types: %v", err)
	}
	name := func(login string) *string { return &login }

	aliceCookie := model.Actor{Kind: "user", ID: "alice"}
	aliceToken := model.Actor{Kind: "session", Owner: name("alice")}
	aliceSession := model.Actor{Kind: "session", ID: "session-the-broker-bound-to-alice", Owner: name("alice")}
	bobCookie := model.Actor{Kind: "user", ID: "bob"}
	bobToken := model.Actor{Kind: "session", Owner: name("bob")}
	workload := model.Actor{Service: name("system:serviceaccount:legion:worker")}
	shared := model.Actor{}
	readers := []struct {
		name string
		who  model.Actor
	}{
		{"alice's cookie", aliceCookie}, {"alice's personal token", aliceToken}, {"alice's proved session", aliceSession},
		{"bob's cookie", bobCookie}, {"bob's personal token", bobToken}, {"a workload", workload}, {"the shared bearer", shared},
	}

	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	node := func(kind, id, visibility string, madeBy model.Actor) graphstore.Node {
		return graphstore.Node{
			Kind: kind, ID: id, Supertag: kind, Visibility: visibility, State: graphstore.StateLive,
			MadeAt: at, MadeBy: madeBy, SourceEvent: graphstore.NoEvent,
		}
	}
	nodes := []struct {
		name string
		node graphstore.Node
		// readBy are the readers answered the node, by name.
		readBy []string
	}{
		{"a project node of the person kind", node(personComparedTestKind, "project", graphstore.VisibilityProject, aliceCookie),
			[]string{"alice's cookie", "alice's personal token", "alice's proved session", "bob's cookie", "bob's personal token", "a workload", "the shared bearer"}},
		{"the person kind, written by alice's cookie", node(personComparedTestKind, "by-cookie", graphstore.VisibilityAuthor, aliceCookie),
			[]string{"alice's cookie", "alice's personal token", "alice's proved session"}},
		{"the person kind, written by alice's personal token", node(personComparedTestKind, "by-token", graphstore.VisibilityAuthor, aliceToken),
			[]string{"alice's cookie", "alice's personal token", "alice's proved session"}},
		{"the person kind, written by alice's cookie under a login in mixed case", node(personComparedTestKind, "by-spelling", graphstore.VisibilityAuthor,
			model.Actor{Kind: "user", ID: "  Alice "}), []string{"alice's cookie", "alice's personal token", "alice's proved session"}},
		{"the person kind, written by a workload", node(personComparedTestKind, "by-workload", graphstore.VisibilityAuthor, workload),
			[]string{"a workload"}},
		{"the person kind, written by nobody", node(personComparedTestKind, "by-nobody", graphstore.VisibilityAuthor, shared), nil},
		{"the credential kind, written by alice's personal token", node(credentialComparedTestKind, "by-token", graphstore.VisibilityAuthor, aliceToken),
			[]string{"alice's personal token"}},
		{"the credential kind, written by alice's cookie", node(credentialComparedTestKind, "by-cookie", graphstore.VisibilityAuthor, aliceCookie),
			[]string{"alice's cookie"}},
		{"the credential kind, written by nobody", node(credentialComparedTestKind, "by-nobody", graphstore.VisibilityAuthor, shared), nil},
		{"a comment, written by alice's cookie", node(graphstore.KindComment, "11111111-1111-4111-8111-111111111111", graphstore.VisibilityAuthor, aliceCookie),
			[]string{"alice's cookie"}},
	}
	for _, written := range nodes {
		// The store takes the two test kinds at author, which this binary admits; a comment it
		// refuses (graphstore.ErrVisibilityNotAdmitted), so a comment's rows are seeded as such
		// a write would have stored them.
		if written.node.Kind == graphstore.KindComment {
			if err := graphstoretest.SeedAuthorOnly(ctx, tx, written.node); err != nil {
				t.Fatalf("seed %s: %v", written.name, err)
			}
			continue
		}
		if err := graphstore.Write(ctx, tx, []graphstore.Node{written.node}, nil); err != nil {
			t.Fatalf("write %s: %v", written.name, err)
		}
	}

	forms := []struct {
		name string
		// storeKinds is whether the form reads only the kinds the store holds rows of alone,
		// which the loader's own read does: a comment's loader reads its comments row, which
		// this fixture holds none of, so the four rendered forms are a comment's predicate here.
		storeKinds bool
		read       func(t *testing.T, who model.Actor, kind, id string) bool
	}{
		{"the loader's read (Node)", true, func(t *testing.T, who model.Actor, kind, id string) bool {
			_, found, err := Node(ctx, tx, who, kind, id, true)
			if err != nil {
				t.Fatalf("read %s %s: %v", kind, id, err)
			}
			return found
		}},
		{"VisibleKind", false, func(t *testing.T, who model.Actor, kind, id string) bool {
			return predicateHolds(t, ctx, tx, `select `+VisibleKind(kind, "$1", "$2"), id, VisibilityArg(who))
		}},
		{"VisibleKindRow", false, func(t *testing.T, who model.Actor, kind, id string) bool {
			return predicateHolds(t, ctx, tx, `select exists (select 1 from graph_nodes n where n.kind = $3 and n.id = $1 and `+
				VisibleKindRow(kind, "n", "$2")+`)`, id, VisibilityArg(who), kind)
		}},
		{"VisibleEnd", false, func(t *testing.T, who model.Actor, kind, id string) bool {
			return predicateHolds(t, ctx, tx, `select `+VisibleEnd("$3::text", "$1", true, "$2"), id, VisibilityArg(who), kind)
		}},
		{"VisibleNode", false, func(t *testing.T, who model.Actor, kind, id string) bool {
			return predicateHolds(t, ctx, tx, `select `+VisibleNode("$3::text", "$1", "$2"), id, VisibilityArg(who), kind)
		}},
	}
	for _, written := range nodes {
		for _, reader := range readers {
			want := slices.Contains(written.readBy, reader.name)
			for _, form := range forms {
				if form.storeKinds && written.node.Kind == graphstore.KindComment {
					continue
				}
				got := form.read(t, reader.who, written.node.Kind, written.node.ID)
				t.Logf("%s, read by %s through %s: found %t", written.name, reader.name, form.name, got)
				if got != want {
					t.Errorf("%s, read by %s through %s, answers found %t, want %t", written.name, reader.name, form.name, got, want)
				}
			}
		}
	}
}

// predicateHolds runs one statement that answers whether a rendered predicate holds.
func predicateHolds(t *testing.T, ctx context.Context, tx pgx.Tx, statement string, args ...any) bool {
	t.Helper()
	var holds bool
	if err := tx.QueryRow(ctx, statement, args...).Scan(&holds); err != nil {
		t.Fatalf("run %s: %v", statement, err)
	}
	return holds
}
