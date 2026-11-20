package docs

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reearth/ygo/crdt"

	"github.com/sjawhar/envoy/internal/dispatch/docs/docstest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// laterAddress is the address after `/` in each shape below whose email links the renderer cannot
// write so that they read back: the email rule leaves it unlinked, and the base links it whole.
const laterAddress = "smith@example.com"

// unwritableAddresses are two texts whose email rule reading has no spelling that reads back: the
// renderer writes the earlier address as an explicit link, and after an explicit link the rule
// links laterAddress, which it leaves unlinked in the text typed.
var unwritableAddresses = []string{"b-_www.@example.com./bob-smith@example.com", "`c`&period;.@x_y.com/bob-smith@example.com"}

// A person who types or pastes an address the renderer cannot write as the email rule reads it
// gives the room the rule's tree, which no markdown reads back as: the renderer writes the earlier
// address as an explicit link, and after an explicit link the rule links `smith@example.com`,
// which it leaves unlinked in the text typed. The settlement gives that address the base's link in
// the room, in one repair it stores and sends the room's browsers, and versions the repaired tree,
// so the version reads back as the room's tree; a later version write, an edit through the service
// and its version, renders from the repaired room and reads back too. A document whose email links
// read back - its text holding no `@`, or holding an address the rule reads as written - settles as
// before: no repair, and a version that takes the rendering of the settlement's read.
func TestSettlementGivesALiveDocumentTheBaseEmailLinksItsOwnCannotBeWrittenAs(t *testing.T) {
	for _, row := range []struct {
		name, markdown string
		repaired       bool
	}{
		{name: "an email link that starts b-_www.", markdown: "b-_www.@example.com./bob-smith@example.com\n", repaired: true},
		{name: "an email link after a code span and a character reference", markdown: "`c`&period;.@x_y.com/bob-smith@example.com\n", repaired: true},
		{name: "no @", markdown: "Plain words, and no address.\n"},
		{name: "an address the rule reads as written", markdown: "Write to bob-smith@example.com today.\n"},
	} {
		t.Run(row.name, func(t *testing.T) {
			service, artifactID, browser, typed := roomTypedByBrowser(t, row.markdown)
			if _, href := runHolding(typed, laterAddress); row.repaired && href != "" {
				t.Fatalf("the rule reads %q as %s, linking %s, where it links nothing", row.markdown, linkSummary(typed), laterAddress)
			}
			if held := liveTree(t, service, artifactID); !held.EqualWithBlockIDs(typed) {
				t.Fatalf("the room holds %s, not the tree the browser typed, %s", linkSummary(held), linkSummary(typed))
			}

			before := latestVersionNumber(t, service, artifactID)
			repairs := countRepairs(t, service, artifactID)
			// Whether the version takes the rendering the settlement made of its read, as one that
			// writes nothing into the room does, rather than rendering a read after its repairs.
			var reused atomic.Bool
			service.beforeSettleVersionRender = func(room string, _ *pmdoc.Node, took bool) {
				if room == artifactID {
					reused.Store(took)
				}
			}
			settleCurrentGeneration(t, service, artifactID)
			service.beforeSettleVersionRender = nil
			wrote := repairs()
			if err := service.roomFailure(artifactID); err != nil {
				t.Fatalf("the settlement failed the room: %v", err)
			}
			requireSettledAtLatestVersion(t, service, artifactID)
			if latest := latestVersionNumber(t, service, artifactID); latest != before+1 {
				t.Fatalf("latest version = %d, want %d, the settlement's", latest, before+1)
			}
			settled := liveTree(t, service, artifactID)
			requireReadsBackAs(t, "the settlement's version", latestVersionMarkdown(t, service, artifactID), settled)
			if row.repaired {
				requireBaseLink(t, "the settled room", settled)
			} else if !settled.EqualWithBlockIDs(typed) {
				t.Errorf("the settlement left the room holding %s, not the tree the browser typed, %s", linkSummary(settled), linkSummary(typed))
			}
			want := int32(0)
			if row.repaired {
				want = 1
			}
			if wrote != want {
				t.Errorf("the settlement wrote %d repairs into the room, want %d", wrote, want)
			}
			if reused.Load() == row.repaired {
				t.Errorf("the settlement's version took the rendering of its read = %t, want %t", reused.Load(), !row.repaired)
			}
			if stored := persistedProofTree(t, service.store, artifactID); !stored.EqualWithBlockIDs(settled) {
				t.Errorf("the store holds %s, not the settled room's %s", linkSummary(stored), linkSummary(settled))
			}
			waitFor(t, roomWait(t), "the browser to hold the settled room's tree", func() bool {
				held := browserTree(browser)
				return held != nil && held.EqualWithBlockIDs(settled)
			})

			writer := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
			var later VersionResult
			if err := joinedWrite(service, func(ctx context.Context) error {
				if _, err := service.ApplyOps(ctx, artifactID, []model.EditOp{{Op: "insert", After: "end", Markdown: "A later paragraph.\n"}}, writer, nil); err != nil {
					return err
				}
				var err error
				later, err = service.SnapshotVersion(ctx, artifactID, writer)
				return err
			}); err != nil {
				t.Fatalf("insert a paragraph and version it: %v", err)
			}
			if !later.Wrote {
				t.Fatal("the later write wrote no version")
			}
			edited := liveTree(t, service, artifactID)
			requireReadsBackAs(t, "the later write's version", latestVersionMarkdown(t, service, artifactID), edited)
			if row.repaired {
				requireBaseLink(t, "the room after the later write", edited)
			}
		})
	}
}

// A browser's edit applied after the settlement's read, and before its observer arms a newer
// settlement, can take the address out from under the base's links the settlement planned: the
// live write then finds nothing to give them, and the settlement versions the tree it read. That
// version is stored as the repair would leave its tree, so its markdown reads back as the tree its
// token names; the room keeps the browser's edit, which the next settlement versions.
func TestASettlementWhoseLiveFallbackWritesNothingVersionsItsReadWithTheBaseEmailLinks(t *testing.T) {
	shape := unwritableAddresses[0] + "\n"
	service, artifactID, _, _ := roomTypedByBrowser(t, shape)
	read := liveTree(t, service, artifactID)
	want, changed, err := pmdoc.FallBackEmailLinks(read)
	if err != nil || !changed {
		t.Fatalf("FallBackEmailLinks(the room's tree) = changed %t, %v; the test needs a tree whose email links do not read back", changed, err)
	}
	rendering, err := pmdoc.Render(want)
	if err != nil {
		t.Fatalf("render the room's tree with the base's links: %v", err)
	}
	versionsBefore := latestVersionNumber(t, service, artifactID)

	var release func()
	service.afterSettleReconcile = func(room string) {
		if room != artifactID || release != nil {
			return
		}
		// The browser takes `@example.com` off the later address, so the room's tree reads back
		// and the live write of the base's links has nothing to give. Its observer is held before
		// it arms a settlement, so this one stays the room's and writes its version.
		release = holdPeerEdit(t, service, artifactID, &service.afterCreditUpdate, replaceRun(laterAddress, "smith"))
	}
	settleCurrentGeneration(t, service, artifactID)
	service.afterSettleReconcile = nil
	if release == nil {
		t.Fatal("the settlement never reached the window between its plan and its repairs")
	}
	if versions := latestVersionNumber(t, service, artifactID); versions != versionsBefore+1 {
		t.Fatalf("latest version = %d, want %d: the settlement wrote no version of the typed address", versions, versionsBefore+1)
	}
	stored := latestVersionMarkdown(t, service, artifactID)
	if stored != rendering {
		t.Errorf("the settlement's version stores %q, where its read with the base's links renders to %q", stored, rendering)
	}
	requireReadsBackAs(t, "the settlement's version", stored, want)
	token := latestVersionToken(t, service, artifactID)
	if wantToken, err := nodeToken(want); err != nil || token != wantToken {
		t.Errorf("the version's token is %q, where its tree's is %q (%v)", token, wantToken, err)
	}

	release()
	settleCurrentGeneration(t, service, artifactID)
	if err := service.roomFailure(artifactID); err != nil {
		t.Fatalf("the settlement failed the room: %v", err)
	}
	requireSettledAtLatestVersion(t, service, artifactID)
	requireReadsBackAs(t, "the latest version", latestVersionMarkdown(t, service, artifactID), liveTree(t, service, artifactID))
}

// An edit through the service that writes one of the shapes whose rule's reading has no spelling
// that reads back is stored, as an upload of the same text is, with the base's link on
// `smith@example.com` (R4 of INFRA-4224): an insert, a replacement of blocks and an accepted
// suggestion, which main stores so, and a delete or a rejected suggestion that takes out the text
// between the first address and `/bob-smith@example.com`, which joins the two into the shape. The
// text before that delete reads back with `smith@example.com` unlinked, so the base's link is the
// edit's. The room holds that tree, its rendering reads back as it, and the settlement's version
// reads back as it.
func TestAnEditOfAnAddressTheRuleCannotWriteStoresTheBaseEmailLinks(t *testing.T) {
	writer := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
	for _, shape := range unwritableAddresses {
		// The shape with " and " before `/bob-`, which the rule reads with `smith@example.com`
		// unlinked, and which reads back so.
		parted := strings.Replace(shape, "/bob-", " and /bob-", 1)
		for _, row := range []struct {
			name, seed string
			edit       func(t *testing.T, service *Service, artifactID string) error
		}{
			{name: "an insert", seed: "First.\n", edit: func(_ *testing.T, service *Service, artifactID string) error {
				_, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "insert", After: "end", Markdown: shape + "\n"}}, writer, nil)
				return err
			}},
			{name: "a replacement of blocks", seed: "First.\n", edit: func(_ *testing.T, service *Service, artifactID string) error {
				_, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: "First.", With: shape + "\n\nMore."}}, writer, nil)
				return err
			}},
			{name: "an accepted suggestion", seed: "First.\n", edit: func(_ *testing.T, service *Service, artifactID string) error {
				spec := MarkSpec{Kind: MarkSuggestion, ID: "s1", By: writer}
				if _, err := joinedMarkQuote(service, artifactID, spec, "First.", nil); err != nil {
					return err
				}
				return joinedAcceptSuggestion(service, artifactID, "s1", shape, model.Actor{Kind: "user", ID: "alice"})
			}},
			{name: "a delete of the text before the later address", seed: "First.\n\n" + parted + "\n", edit: func(_ *testing.T, service *Service, artifactID string) error {
				_, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "delete", Find: " and "}}, writer, nil)
				return err
			}},
			{name: "a rejected suggestion of the text before the later address", seed: "First.\n\n" + parted + "\n", edit: func(t *testing.T, service *Service, artifactID string) error {
				browserMarkWithAttrs(t, service, artifactID, "proofSuggestion", " and ", pmdoc.Attrs{"id": "ins", "by": "user:bob", "kind": "insert"})
				return joinedRejectSuggestion(service, artifactID, "ins", model.Actor{Kind: "user", ID: "alice"})
			}},
		} {
			t.Run(row.name+" of "+shape, func(t *testing.T) {
				service, artifactID := newTestService(t)
				service.settle = time.Hour // the test runs each settlement itself
				seedServiceText(t, service, artifactID, row.seed)
				settleCurrentGeneration(t, service, artifactID)
				seeded := liveTree(t, service, artifactID)
				if _, href := runHolding(seeded, laterAddress); href != "" {
					t.Fatalf("the seeded room links %s already: %s", laterAddress, linkSummary(seeded))
				}
				if err := row.edit(t, service, artifactID); err != nil {
					t.Fatalf("the edit was refused: %v", err)
				}
				room := liveTree(t, service, artifactID)
				requireBaseLink(t, "the edited room", room)
				rendering, err := pmdoc.Render(room)
				if err != nil {
					t.Fatalf("render the edited room: %v", err)
				}
				requireReadsBackAs(t, "the edited room's rendering", rendering, room)
				settleCurrentGeneration(t, service, artifactID)
				if err := service.roomFailure(artifactID); err != nil {
					t.Fatalf("the settlement failed the room: %v", err)
				}
				requireSettledAtLatestVersion(t, service, artifactID)
				requireReadsBackAs(t, "the latest version", latestVersionMarkdown(t, service, artifactID), liveTree(t, service, artifactID))
			})
		}
	}
}

// A replace whose `with` is one paragraph writing one of the shapes whose rule's reading has no
// spelling that reads back, in a paragraph or over a whole one, answers the token of the tree the
// room stores, the one with the base's link on `smith@example.com`: the settlement after it
// repairs nothing, so the document's token is still the one the edit answered, and a second edit
// passing that token as its document precondition is applied (verification v16 of INFRA-4224). At
// 677a5306 the replace answered the token of the rule's tree, the settlement gave the room the
// base's link, and the second edit was refused as stale.
func TestAnInlineReplaceOfAnAddressTheRuleCannotWriteAnswersTheTokenOfTheStoredTree(t *testing.T) {
	writer := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
	for _, shape := range unwritableAddresses {
		for _, row := range []struct{ name, seed string }{
			{name: "inside a paragraph", seed: "First.\n\nSee PLACEHOLDER here.\n"},
			{name: "as a whole paragraph", seed: "First.\n\nPLACEHOLDER\n"},
		} {
			t.Run(row.name+" of "+shape, func(t *testing.T) {
				service, artifactID := newTestService(t)
				service.settle = time.Hour // the test runs each settlement itself
				seedServiceText(t, service, artifactID, row.seed)
				settleCurrentGeneration(t, service, artifactID)
				outcome, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: "PLACEHOLDER", With: shape}}, writer, nil)
				if err != nil {
					t.Fatalf("the replace was refused: %v", err)
				}
				if outcome.Token == "" {
					t.Fatal("the replace answered no token")
				}
				settleCurrentGeneration(t, service, artifactID)
				if err := service.roomFailure(artifactID); err != nil {
					t.Fatalf("the settlement failed the room: %v", err)
				}
				if settled := documentToken(t, service, artifactID); settled != outcome.Token {
					t.Errorf("the replace answered the token %s, and the document's token after the settlement is %s", outcome.Token, settled)
				}
				second := []model.EditOp{{Op: "replace", Find: "First.", With: "First, again."}}
				if _, err := joinedApplyOps(service, artifactID, second, writer, &model.EditPrecondition{Document: outcome.Token}); err != nil {
					t.Errorf("a second edit under the token the replace answered was refused: %v", err)
				}
			})
		}
	}
}

// roomTypedByBrowser is a document seeded "First.\n" and settled on a new service, which runs each
// settlement itself, over which browser bob typed markdown's tree (typeAsBrowser): the service, the
// document, the browser and the tree it typed.
func roomTypedByBrowser(t *testing.T, markdown string) (service *Service, artifactID string, browser *docstest.Peer, typed *pmdoc.Node) {
	t.Helper()
	service, artifactID = newTestService(t)
	service.settle = time.Hour // the test runs each settlement itself
	seedServiceText(t, service, artifactID, "First.\n")
	settleCurrentGeneration(t, service, artifactID)
	browser = connectBrowser(t, newTestServer(t, service).URL, artifactID, "bob")
	typed, err := pmdoc.Parse(markdown)
	if err != nil {
		t.Fatalf("Parse(%q): %v", markdown, err)
	}
	typeAsBrowser(t, service, artifactID, browser, typed)
	return service, artifactID, browser, typed
}

// typeAsBrowser makes browser's copy of the document hold typed and sends the room the update, as
// a person's typing or paste reaches the room: the browser editor's engine gives the room its own
// tree, with no markdown behind it. It returns once the room's update observer has run for that
// update (sendAsBrowser).
func typeAsBrowser(t *testing.T, service *Service, artifactID string, browser *docstest.Peer, typed *pmdoc.Node) {
	t.Helper()
	sendAsBrowser(t, service, artifactID, browser, func(txn *crdt.Transaction, fragment *crdt.YXmlFragment) error {
		return pmdoc.Update(txn, fragment, typed)
	})
}

// browserTree is browser's copy of the document, read under its lock while the room's updates
// apply, or nil while the copy holds no tree it can read.
func browserTree(browser *docstest.Peer) *pmdoc.Node {
	fragment := browser.Doc.GetXmlFragment(fragmentName)
	var tree *pmdoc.Node
	browser.Doc.Transact(func(txn *crdt.Transaction) {
		tree, _ = pmdoc.ReadInTransaction(txn, fragment)
	}, nil)
	return tree
}

// countRepairs counts the repairs the service writes into the room from now on - each update a
// transaction tagged as a repair commits (applySuppressed, identityClosureOrigin) - until the
// function it returns is called, which stops the count and answers it.
func countRepairs(t *testing.T, service *Service, artifactID string) func() int32 {
	t.Helper()
	room := service.srv.GetDoc(artifactID)
	if room == nil {
		t.Fatal("the room is not resident")
	}
	var repairs atomic.Int32
	unsubscribe := room.OnUpdate(func(_ []byte, origin any) {
		if _, repair := origin.(*identityClosureOrigin); repair {
			repairs.Add(1)
		}
	})
	return func() int32 {
		unsubscribe()
		return repairs.Load()
	}
}

// requireReadsBackAs fails unless stored, what names as a version's markdown, reads back
// (pmdoc.ParseRendering) as tree.
func requireReadsBackAs(t *testing.T, what, stored string, tree *pmdoc.Node) {
	t.Helper()
	back, err := pmdoc.ParseRendering(stored)
	if err != nil {
		t.Errorf("%s %q reads back as no document: %v", what, stored, err)
		return
	}
	if !back.Equal(tree) {
		t.Errorf("%s %q reads back as %s, not as the room's tree %s", what, stored, linkSummary(back), linkSummary(tree))
	}
}

// requireBaseLink fails unless tree, what names, holds the base's reading of laterAddress: the
// address alone in a text node under a link to it.
func requireBaseLink(t *testing.T, what string, tree *pmdoc.Node) {
	t.Helper()
	if node, href := runHolding(tree, laterAddress); node == nil || node.Text != laterAddress || href != "mailto:"+laterAddress {
		t.Errorf("%s holds %s, where the base links %s whole", what, linkSummary(tree), laterAddress)
	}
}

// runHolding is the first text node of tree whose text holds text, nil where none does, and the
// href of that node's link, "" where it holds none.
func runHolding(tree *pmdoc.Node, text string) (node *pmdoc.Node, href string) {
	pmdoc.Walk(tree, func(visited *pmdoc.Node) bool {
		if visited.Type == "text" && strings.Contains(visited.Text, text) {
			node = visited
		}
		return node == nil
	})
	if node != nil {
		href = linkHref(node)
	}
	return node, href
}

// linkHref is the href of node's link, "" where it holds none.
func linkHref(node *pmdoc.Node) string {
	for _, mark := range node.Marks {
		if mark.Type == "link" {
			href, _ := mark.Attrs["href"].(string)
			return href
		}
	}
	return ""
}

// linkSummary is tree's text nodes in order, each with the href of its link where it holds one,
// for a failure to show where two trees' links differ.
func linkSummary(tree *pmdoc.Node) string {
	if tree == nil {
		return "no tree"
	}
	var parts []string
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if node.Type == "text" {
			part := strconv.Quote(node.Text)
			if href := linkHref(node); href != "" {
				part += " -> " + href
			}
			parts = append(parts, part)
		}
		return true
	})
	return "[" + strings.Join(parts, ", ") + "]"
}
