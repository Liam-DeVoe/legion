package docs

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reearth/ygo/crdt"

	"github.com/sjawhar/envoy/internal/dispatch/docs/docstest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// laterAddress is the address after `/` in each shape below whose email links the renderer cannot
// write so that they read back: the email rule leaves it unlinked, and the base links it whole.
const laterAddress = "smith@example.com"

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
			service, artifactID := newTestService(t)
			service.settle = time.Hour // the test runs each settlement itself
			seedServiceText(t, service, artifactID, "First.\n")
			settleCurrentGeneration(t, service, artifactID)
			browser := connectBrowser(t, newTestServer(t, service).URL, artifactID, "bob")

			typed, err := pmdoc.Parse(row.markdown)
			if err != nil {
				t.Fatalf("Parse(%q): %v", row.markdown, err)
			}
			if _, href := runHolding(typed, laterAddress); row.repaired && href != "" {
				t.Fatalf("the rule reads %q as %s, linking %s, where it links nothing", row.markdown, linkSummary(typed), laterAddress)
			}
			typeAsBrowser(t, service, artifactID, browser, typed)
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

// A version taken from a live document before its settlement - a snapshot after an edit through the
// service, or a named version, right after a person typed an address the renderer cannot write as
// the email rule reads it - is stored as the settlement's repair would leave the tree: its markdown
// is the rendering of the tree with the base's link on `smith@example.com`
// (pmdoc.FallBackEmailLinks), it reads back as that tree, its document token is that tree's, and
// its block index holds each block's text in that markdown, so the row's markdown, token and index
// name one tree. The room keeps the rule's tree until its settlement, which then gives it the
// base's link.
func TestAVersionBeforeTheSettlementStoresTheBaseEmailLinksItsTreeCannotBeWrittenAs(t *testing.T) {
	const shape = "b-_www.@example.com./bob-smith@example.com\n"
	writer := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
	for _, row := range []struct {
		name    string
		version func(ctx context.Context, service *Service, artifactID string) (VersionResult, error)
	}{
		{name: "a snapshot after an edit", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
			if _, err := service.ApplyOps(ctx, artifactID, []model.EditOp{{Op: "insert", After: "end", Markdown: "A later paragraph.\n"}}, writer, nil); err != nil {
				return VersionResult{}, err
			}
			return service.SnapshotVersion(ctx, artifactID, writer)
		}},
		{name: "a named version", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
			return service.NamedVersion(ctx, artifactID, "Named before the settlement", writer)
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			service.settle = time.Hour // the test runs each settlement itself
			seedServiceText(t, service, artifactID, "First.\n")
			settleCurrentGeneration(t, service, artifactID)
			browser := connectBrowser(t, newTestServer(t, service).URL, artifactID, "bob")
			typed, err := pmdoc.Parse(shape)
			if err != nil {
				t.Fatalf("Parse(%q): %v", shape, err)
			}
			typeAsBrowser(t, service, artifactID, browser, typed)

			var version VersionResult
			if err := joinedWrite(service, func(ctx context.Context) error {
				var err error
				version, err = row.version(ctx, service, artifactID)
				return err
			}); err != nil {
				t.Fatalf("write the version: %v", err)
			}
			if !version.Wrote {
				t.Fatal("the write stored no version")
			}
			room := liveTree(t, service, artifactID)
			if _, href := runHolding(room, laterAddress); href != "" {
				t.Fatalf("the room holds %s before its settlement, linking %s, where the browser typed it unlinked", linkSummary(room), laterAddress)
			}
			want, changed, _, err := pmdoc.FallBackEmailLinks(room)
			if err != nil || !changed {
				t.Fatalf("FallBackEmailLinks(the room's tree) = changed %t, %v; the test needs a tree whose email links do not read back", changed, err)
			}
			rendering, offsets, err := pmdoc.RenderWithBlockOffsets(want)
			if err != nil {
				t.Fatalf("render the room's tree with the base's links: %v", err)
			}
			stored := latestVersionMarkdown(t, service, artifactID)
			if stored != rendering {
				t.Errorf("the version stores %q, where the room's tree with the base's links renders to %q", stored, rendering)
			}
			requireReadsBackAs(t, "the version", stored, want)
			var token string
			if err := service.store.Pool.QueryRow(context.Background(), `
				select document_token from artifact_versions where artifact_id = $1 order by number desc limit 1
			`, artifactID).Scan(&token); err != nil {
				t.Fatalf("read the version's token: %v", err)
			}
			if wantToken, err := nodeToken(want); err != nil || token != wantToken {
				t.Errorf("the version's token is %q, where its tree's is %q (%v)", token, wantToken, err)
			}
			indexed := map[string]string{}
			rows, err := service.store.Pool.Query(context.Background(), `select block_id, text_sha256 from document_blocks where artifact_id = $1`, artifactID)
			if err != nil {
				t.Fatalf("read the block index: %v", err)
			}
			for rows.Next() {
				var id, sum string
				if err := rows.Scan(&id, &sum); err != nil {
					t.Fatalf("read a block of the index: %v", err)
				}
				indexed[id] = sum
			}
			rows.Close()
			for _, block := range offsets {
				if sum, held := indexed[block.ID]; !held || sum != refs.BlockTextSum(rendering[block.From:block.To]) {
					t.Errorf("the block index holds block %s as %q (held %t), where the version's markdown holds %q", block.ID, sum, held, rendering[block.From:block.To])
				}
			}

			settleCurrentGeneration(t, service, artifactID)
			if err := service.roomFailure(artifactID); err != nil {
				t.Fatalf("the settlement failed the room: %v", err)
			}
			requireSettledAtLatestVersion(t, service, artifactID)
			settled := liveTree(t, service, artifactID)
			requireBaseLink(t, "the settled room", settled)
			requireReadsBackAs(t, "the latest version", latestVersionMarkdown(t, service, artifactID), settled)
		})
	}
}

// typeAsBrowser makes browser's copy of the document hold typed and sends the room the update, as
// a person's typing or paste reaches the room: the browser editor's engine gives the room its own
// tree, with no markdown behind it. It returns once the room's update observer has run for that
// update, as editAsBrowser does.
func typeAsBrowser(t *testing.T, service *Service, artifactID string, browser *docstest.Peer, typed *pmdoc.Node) {
	t.Helper()
	// These take the copy's lock themselves, so they are read before the transaction that holds it.
	fragment := browser.Doc.GetXmlFragment(fragmentName)
	client := browser.Doc.ClientID()
	sentFrom := browser.Doc.StateVector().Clock(client)
	room := service.srv.GetDoc(artifactID)
	if room == nil {
		t.Fatal("the room is not resident while its browser is connected")
	}
	recorded := make(chan struct{})
	var once sync.Once
	unsubscribe := room.OnUpdate(func([]byte, any) {
		if room.StateVector().Clock(client) > sentFrom {
			once.Do(func() { close(recorded) })
		}
	})
	defer unsubscribe()
	var changeErr error
	if _, err := browser.Send(func(txn *crdt.Transaction) {
		changeErr = pmdoc.Update(txn, fragment, typed)
	}); err != nil || changeErr != nil {
		t.Fatalf("send the browser's edit: %v %v", err, changeErr)
	}
	select {
	case <-recorded:
	case <-time.After(roomWait(t)):
		t.Fatal("the room did not record the browser's edit within roomWait")
	}
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
