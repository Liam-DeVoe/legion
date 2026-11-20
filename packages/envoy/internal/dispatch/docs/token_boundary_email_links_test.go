package docs

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// Every row here begins where a person typed or pasted an address the renderer cannot write as the
// email rule reads it (unwritableAddresses), so the room holds the rule's tree, whose rendering does
// not read back, until its settlement gives the address the base's link. Before Dispatch answers any
// token for the document in that window, the room takes that repair (INFRA-4224, round 8): a read
// outside every transaction runs the settlement first (readBeforeAnswer), and a write, or
// an answer taken inside a write's transaction, writes the repair into its own fork
// (applyLiveWritten, writeOwedEmailLinks). Each token a caller can hold then names a tree the
// document stores as itself, and an edit passing it as its document precondition is applied.

// tokenBoundaryWriter is the agent whose edits and versions the rows make.
var tokenBoundaryWriter = model.Actor{Kind: "session", ID: "session-0123456789abcdef"}

// A read of the document's text and token - GET /text, and GET /text?blocks=true with each block's
// token - made before the settlement runs that settlement first: it writes a version, the room then
// holds the base's link, the token the read answers is the document's token once the settlement is
// done and each block's token is that block's then, and an edit under the token is applied. Before
// the boundary the read answered the rule's tree, which the settlement then repaired, so the token
// it answered named no tree the document stores and the edit under it was refused as stale.
func TestAReadBeforeTheSettlementOfUnwritableEmailLinksSettlesThemFirst(t *testing.T) {
	for _, shape := range unwritableAddresses {
		for _, row := range []struct {
			name string
			read func(service *Service, artifactID string) (token string, blocks map[string]string, err error)
		}{
			{name: "text", read: func(service *Service, artifactID string) (string, map[string]string, error) {
				_, token, err := service.TextWithToken(context.Background(), artifactID)
				return token, nil, err
			}},
			{name: "text with its blocks", read: func(service *Service, artifactID string) (string, map[string]string, error) {
				_, token, blocks, err := service.TextWithTokenAndBlocks(context.Background(), artifactID)
				return token, blockTokens(blocks), err
			}},
		} {
			t.Run(row.name+" of "+shape, func(t *testing.T) {
				service, artifactID, _, _ := roomTypedByBrowser(t, "First.\n\n"+shape+"\n")
				before := latestVersionNumber(t, service, artifactID)
				token, blocks, err := row.read(service, artifactID)
				if err != nil {
					t.Fatalf("read the document's token: %v", err)
				}
				if latest := latestVersionNumber(t, service, artifactID); latest != before+1 {
					t.Errorf("latest version after the read = %d, want %d: the read ran no settlement", latest, before+1)
				}
				requireBaseLink(t, "the room after the read", liveTree(t, service, artifactID))
				settleCurrentGeneration(t, service, artifactID)
				if err := service.roomFailure(artifactID); err != nil {
					t.Fatalf("the settlement failed the room: %v", err)
				}
				if settled := documentToken(t, service, artifactID); settled != token {
					t.Errorf("the read answered the token %s, and the document's token after the settlement is %s", token, settled)
				}
				if blocks != nil {
					_, _, settled, err := service.TextWithTokenAndBlocks(context.Background(), artifactID)
					if err != nil {
						t.Fatalf("read the settled document's blocks: %v", err)
					}
					if settledTokens := blockTokens(settled); !maps.Equal(blocks, settledTokens) {
						t.Errorf("the read answered the block tokens %v, and the settled document's are %v", blocks, settledTokens)
					}
				}
				later := []model.EditOp{{Op: "insert", After: "end", Markdown: "A later paragraph.\n"}}
				if _, err := joinedApplyOps(service, artifactID, later, tokenBoundaryWriter, &model.EditPrecondition{Document: token}); err != nil {
					t.Errorf("an edit under the token the read answered was refused: %v", err)
				}
			})
		}
	}
}

// An agent's edit of other text of the document, through the edit route alone (ApplyOps, no
// version), answers the token of the tree its write stores: the write gives the room the base's link
// before its batch (applyLiveWritten's repair before mutate), so the settlement after it repairs
// nothing, the document's token is still the one the edit answered, and a second edit under it is
// applied. Before the boundary the edit answered the rule's tree with its own change in it.
func TestAnEditBeforeTheSettlementAnswersTheTokenOfTheTreeWithTheBaseEmailLinks(t *testing.T) {
	for _, shape := range unwritableAddresses {
		t.Run(shape, func(t *testing.T) {
			service, artifactID, _, _ := roomTypedByBrowser(t, "First.\n\n"+shape+"\n")
			outcome, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: "First.", With: "First, again."}}, tokenBoundaryWriter, nil)
			if err != nil {
				t.Fatalf("the edit was refused: %v", err)
			}
			requireBaseLink(t, "the room after the edit", liveTree(t, service, artifactID))
			requireTokenSurvivesSettlement(t, service, artifactID, "the edit", outcome.Token)
		})
	}
}

// An edit with no operation, which answers the document's current token as the next edit's
// precondition, gives the room the base's link first (answerTree): the token it answers is
// usable by an edit, and the settlement after it leaves it the document's token. Before the boundary
// it answered the rule's tree, which the settlement then repaired.
func TestAnEditWithNoOperationBeforeTheSettlementAnswersTheTokenOfTheTreeWithTheBaseEmailLinks(t *testing.T) {
	for _, shape := range unwritableAddresses {
		t.Run(shape, func(t *testing.T) {
			service, artifactID, _, _ := roomTypedByBrowser(t, "First.\n\n"+shape+"\n")
			token := documentToken(t, service, artifactID)
			requireBaseLink(t, "the room after the edit with no operation", liveTree(t, service, artifactID))
			requireTokenSurvivesSettlement(t, service, artifactID, "the edit with no operation", token)
		})
	}
}

// An edit refused for a stale document precondition, with operations or none, names the document's
// current token, which is the token of the tree with the base's link (the refusal is checked after
// the repair before mutate): an edit under it is applied, and the token that edit answers is the
// document's once the settlement is done. Before the boundary the refusal named the rule's tree, the
// edit under it answered a token of that tree, and the settlement then repaired it.
func TestAPreconditionRefusalBeforeTheSettlementNamesTheTokenOfTheTreeWithTheBaseEmailLinks(t *testing.T) {
	stale := tokenOfMarkdown(t, "Another document altogether.\n")
	for _, shape := range unwritableAddresses {
		for _, row := range []struct {
			name string
			ops  []model.EditOp
		}{
			{name: "an edit", ops: []model.EditOp{{Op: "replace", Find: "First.", With: "First, again."}}},
			{name: "an edit with no operation"},
		} {
			t.Run(row.name+" of "+shape, func(t *testing.T) {
				service, artifactID, _, _ := roomTypedByBrowser(t, "First.\n\n"+shape+"\n")
				_, err := joinedApplyOps(service, artifactID, row.ops, tokenBoundaryWriter, &model.EditPrecondition{Document: stale})
				var refused *ErrPreconditionFailed
				if !errors.As(err, &refused) || refused.CurrentDocument == "" {
					t.Fatalf("the edit under a stale token = %v, want a refusal naming the current token", err)
				}
				later := []model.EditOp{{Op: "insert", After: "end", Markdown: "A later paragraph.\n"}}
				outcome, err := joinedApplyOps(service, artifactID, later, tokenBoundaryWriter, &model.EditPrecondition{Document: refused.CurrentDocument})
				if err != nil {
					t.Fatalf("an edit under the token the refusal named was refused: %v", err)
				}
				requireBaseLink(t, "the room after the edit", liveTree(t, service, artifactID))
				requireTokenSurvivesSettlement(t, service, artifactID, "the edit under the refusal's token", outcome.Token)
			})
		}
	}
}

// An agent reads the document's text and token (GET /text) on the process that holds the room, and
// uploads its change of that text (ReplaceText) naming the token, as every upload names the token
// of the version it replaces. The upload checks that token inside its mutation against the tree
// the write starts from, the one the repair before mutate leaves (applyLiveWritten), so the read
// must answer that tree's token: it runs the settlement first (readBeforeAnswer), and the upload
// under its token is applied. With the read answering the room's tree as the rule left it, the
// upload's repair before mutate moved the tree the token is checked against, and the upload was
// refused as stale with no write between the read and the upload.
func TestAnUploadUnderTheTokenAReadAnswersBeforeTheSettlementIsApplied(t *testing.T) {
	for _, shape := range unwritableAddresses {
		t.Run(shape, func(t *testing.T) {
			service, artifactID, _, _ := roomTypedByBrowser(t, "First.\n\n"+shape+"\n")
			if service.srv.GetDoc(artifactID) == nil {
				t.Fatal("this process holds no room of the document, so nothing here is tested")
			}
			markdown, token, err := service.TextWithToken(context.Background(), artifactID)
			if err != nil {
				t.Fatalf("read the document's text and token: %v", err)
			}
			upload := strings.Replace(markdown, "First.", "First, uploaded.", 1)
			if upload == markdown {
				t.Fatalf("the read answered %q, which holds no \"First.\" for the upload to change", markdown)
			}
			canonical, err := joinedReplaceTextNaming(service, artifactID, upload, tokenBoundaryWriter, &model.EditPrecondition{Document: token})
			if err != nil {
				t.Fatalf("an upload under the token the read answered was refused: %v", err)
			}
			waitForDocumentText(t, service, artifactID, canonical)
		})
	}
}

// A version taken in a write's transaction before the settlement - a snapshot after an edit through
// the edit route, a snapshot as the anchor route takes one after a comment's quote (MarkQuote) or
// after a comment's mark_id (JoinWrite), a snapshot and a named version after a comment's margin
// record (ProjectMark), and a named version - gives the room the base's link in that transaction
// first (the write's own repair, or captureRepairedTextAndAuthors), so the room holds the base's
// link once the transaction commits and the version holds the room's tree: its markdown is that
// tree's rendering and reads back as it, its token is that tree's, and its block index is that
// markdown's. A second snapshot finds the version holding the room and writes none, a read answers
// the version's token, the settlement after it leaves the room and that token as they are, and an
// edit under the token is applied. Before the boundary the version stored the base's links
// (versionedEmailLinks) while the room kept the rule's tree until its settlement, so the version
// and the room named two trees.
func TestAVersionBeforeTheSettlementGivesTheRoomTheBaseEmailLinks(t *testing.T) {
	record := MarkRecord{Kind: "comment", By: ActorRef(tokenBoundaryWriter), CreatedAt: "2026-11-19T00:00:00Z", Text: "About this."}
	for _, shape := range unwritableAddresses {
		for _, row := range []struct {
			name    string
			version func(ctx context.Context, service *Service, artifactID string) (VersionResult, error)
		}{
			{name: "a snapshot after an edit", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
				if _, err := service.ApplyOps(ctx, artifactID, []model.EditOp{{Op: "replace", Find: "First.", With: "First, again."}}, tokenBoundaryWriter, nil); err != nil {
					return VersionResult{}, err
				}
				return service.SnapshotVersion(ctx, artifactID, tokenBoundaryWriter)
			}},
			{name: "a snapshot after a comment's quote", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
				ctx = WithCommentMarks(ctx)
				if _, err := service.MarkQuote(ctx, artifactID, MarkSpec{Kind: MarkComment, ID: "c1", By: tokenBoundaryWriter}, "First.", nil); err != nil {
					return VersionResult{}, err
				}
				return service.SnapshotVersion(ctx, artifactID, tokenBoundaryWriter)
			}},
			{name: "a snapshot after a comment's mark", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
				ctx = WithCommentMarks(ctx)
				if err := service.JoinWrite(ctx, artifactID); err != nil {
					return VersionResult{}, err
				}
				return service.SnapshotVersion(ctx, artifactID, tokenBoundaryWriter)
			}},
			{name: "a snapshot after a margin record", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
				ctx = WithCommentMarks(ctx)
				if err := service.ProjectMark(ctx, artifactID, "c1", record, tokenBoundaryWriter); err != nil {
					return VersionResult{}, err
				}
				return service.SnapshotVersion(ctx, artifactID, tokenBoundaryWriter)
			}},
			{name: "a named version", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
				return service.NamedVersion(ctx, artifactID, "Named before the settlement", tokenBoundaryWriter)
			}},
			{name: "a named version after a margin record", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
				if err := service.ProjectMark(ctx, artifactID, "c1", record, tokenBoundaryWriter); err != nil {
					return VersionResult{}, err
				}
				return service.NamedVersion(ctx, artifactID, "Named before the settlement", tokenBoundaryWriter)
			}},
		} {
			t.Run(row.name+" of "+shape, func(t *testing.T) {
				service, artifactID, _, _ := roomTypedByBrowser(t, "First.\n\n"+shape+"\n")
				version := writeVersion(t, service, func(ctx context.Context) (VersionResult, error) {
					return row.version(ctx, service, artifactID)
				})
				room := liveTree(t, service, artifactID)
				requireBaseLink(t, "the room after the version", room)
				rendering, offsets, err := pmdoc.RenderWithBlockOffsets(room)
				if err != nil {
					t.Fatalf("render the room's tree: %v", err)
				}
				stored := latestVersionMarkdown(t, service, artifactID)
				if stored != rendering {
					t.Errorf("the version stores %q, where the room's tree renders to %q", stored, rendering)
				}
				// A comment's mark is an anchor, which no rendering writes, so the version reads back
				// as the room's tree without it.
				requireReadsBackAs(t, "the version", stored, pmdoc.StripAnchorMarks(room))
				versionToken := latestVersionToken(t, service, artifactID)
				if roomToken, err := nodeToken(room); err != nil || versionToken != roomToken {
					t.Errorf("the version's token is %q, where the room's tree's is %q (%v)", versionToken, roomToken, err)
				}
				requireIndexedBlocks(t, service, artifactID, rendering, offsets)
				// A snapshot of the same room holds the version just stored, so it writes none.
				var again VersionResult
				if err := joinedWrite(service, func(ctx context.Context) error {
					var err error
					again, err = service.SnapshotVersion(ctx, artifactID, tokenBoundaryWriter)
					return err
				}); err != nil {
					t.Fatalf("snapshot the room again: %v", err)
				}
				if again.Wrote || again.Version.Number != version.Version.Number {
					t.Errorf("a second snapshot wrote %t, version %d, where the latest version %d holds the room", again.Wrote, again.Version.Number, version.Version.Number)
				}
				_, read, err := service.TextWithToken(context.Background(), artifactID)
				if err != nil {
					t.Fatalf("read the document's token: %v", err)
				}
				if read != versionToken {
					t.Errorf("a read after the version answers the token %s, where the version's is %s", read, versionToken)
				}
				settleCurrentGeneration(t, service, artifactID)
				if err := service.roomFailure(artifactID); err != nil {
					t.Fatalf("the settlement failed the room: %v", err)
				}
				requireSettledAtLatestVersion(t, service, artifactID)
				settled := liveTree(t, service, artifactID)
				requireBaseLink(t, "the settled room", settled)
				requireReadsBackAs(t, "the latest version", latestVersionMarkdown(t, service, artifactID), pmdoc.StripAnchorMarks(settled))
				if token := documentToken(t, service, artifactID); token != read {
					t.Errorf("a read before the settlement answered the token %s, and the document's token after it is %s", read, token)
				}
				later := []model.EditOp{{Op: "insert", After: "end", Markdown: "A later paragraph.\n"}}
				if _, err := joinedApplyOps(service, artifactID, later, tokenBoundaryWriter, &model.EditPrecondition{Document: read}); err != nil {
					t.Errorf("an edit under the token the read answered was refused: %v", err)
				}
			})
		}
	}
}

// A read whose every settlement meets a new address the renderer cannot write - a person types one
// more each time a settlement has read the tree it versions (afterSettleVersionRead), after the
// repair that settlement wrote - runs three settlements in all, then answers the room as it stands
// and logs one line naming the room and the attempts. Before the boundary the read ran none.
func TestAReadSettlesUnwritableEmailLinksThreeTimesAtMostBeforeItAnswers(t *testing.T) {
	shape := unwritableAddresses[0]
	service, artifactID, browser, _ := roomTypedByBrowser(t, "First.\n\n"+shape+"\n")
	logs := captureLogs(t)
	var settlements atomic.Int32
	service.afterSettleVersionRead = func(room string) {
		if room != artifactID {
			return
		}
		typed := settlements.Add(1)
		tree, err := pmdoc.Parse("First.\n\n" + strings.Repeat(shape+"\n\n", int(typed)+1))
		if err != nil {
			t.Errorf("parse the next typing: %v", err)
			return
		}
		typeAsBrowser(t, service, artifactID, browser, tree)
	}
	began := time.Now()
	_, _, err := service.TextWithToken(context.Background(), artifactID)
	service.afterSettleVersionRead = nil
	if err != nil {
		t.Fatalf("the read: %v", err)
	}
	if ran := settlements.Load(); ran < 1 || ran > 3 {
		t.Errorf("the read ran %d settlements that wrote the base's links, want between 1 and 3", ran)
	}
	lines := logs.Lines("email links still do not read back after its settlements")
	if strings.Count(lines, "\n") != 1 || !strings.Contains(lines, "room="+artifactID) || !strings.Contains(lines, "attempts=3") {
		t.Errorf("the read logged %q after %s, want one line naming room=%s and attempts=3", lines, time.Since(began).Round(time.Millisecond), artifactID)
	}
}

// Two envoy-dispatch processes serve one database: a person on task B types or pastes an address
// the renderer cannot write as the email rule reads it, task B's room appends it, and its
// settlement has not run (pairTypedOnTaskB). Task A holds no room of the document and its record
// owes nothing, while the store holds the rule's tree and owes the settlement (settlementPending).
// Every token task A answers then - a read of the text, with its blocks or not, an agent's edit of
// other text, an edit with no operation, a snapshot and a named version - names the tree the
// document stores once both processes' settlements have run, and an edit on task A under it is
// applied: task A's gate reads the store's row where its record says nothing (settlementOwed), so
// its read runs the settlement first and its writes give their fork the base's link. A version
// task A writes reads back as the tree it stores, and a read answers its token. With the record
// alone in the gate, task A answered the rule's tree, which the settlement then moved.
func TestATokenAnotherProcessAnswersBeforeTheSettlementOfUnwritableEmailLinksIsTheStoredTree(t *testing.T) {
	record := MarkRecord{Kind: "comment", By: ActorRef(tokenBoundaryWriter), CreatedAt: "2026-11-19T00:00:00Z", Text: "About this."}
	for _, shape := range unwritableAddresses {
		for _, row := range []struct {
			name   string
			answer func(t *testing.T, service *Service, artifactID string) string
		}{
			{name: "a read of the text", answer: func(t *testing.T, service *Service, artifactID string) string {
				_, token, err := service.TextWithToken(context.Background(), artifactID)
				if err != nil {
					t.Fatalf("read the document's token on task A: %v", err)
				}
				return token
			}},
			{name: "a read of the text with its blocks", answer: func(t *testing.T, service *Service, artifactID string) string {
				_, token, _, err := service.TextWithTokenAndBlocks(context.Background(), artifactID)
				if err != nil {
					t.Fatalf("read the document's token and blocks on task A: %v", err)
				}
				return token
			}},
			{name: "an edit of other text", answer: func(t *testing.T, service *Service, artifactID string) string {
				outcome, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: "First.", With: "First, again."}}, tokenBoundaryWriter, nil)
				if err != nil {
					t.Fatalf("the edit on task A was refused: %v", err)
				}
				return outcome.Token
			}},
			{name: "an edit with no operation", answer: func(t *testing.T, service *Service, artifactID string) string {
				return documentToken(t, service, artifactID)
			}},
			{name: "a snapshot", answer: func(t *testing.T, service *Service, artifactID string) string {
				return versionOnTaskA(t, service, artifactID, func(ctx context.Context) (VersionResult, error) {
					return service.SnapshotVersion(ctx, artifactID, tokenBoundaryWriter)
				})
			}},
			{name: "a snapshot after a margin record", answer: func(t *testing.T, service *Service, artifactID string) string {
				return versionOnTaskA(t, service, artifactID, func(ctx context.Context) (VersionResult, error) {
					if err := service.ProjectMark(WithCommentMarks(ctx), artifactID, "c1", record, tokenBoundaryWriter); err != nil {
						return VersionResult{}, err
					}
					return service.SnapshotVersion(WithCommentMarks(ctx), artifactID, tokenBoundaryWriter)
				})
			}},
			{name: "a named version", answer: func(t *testing.T, service *Service, artifactID string) string {
				return versionOnTaskA(t, service, artifactID, func(ctx context.Context) (VersionResult, error) {
					return service.NamedVersion(ctx, artifactID, "Named on task A before the settlement", tokenBoundaryWriter)
				})
			}},
		} {
			t.Run(row.name+" of "+shape, func(t *testing.T) {
				pair := pairTypedOnTaskB(t, "First.\n\n"+shape+"\n")
				token := row.answer(t, pair.taskA, pair.artifactID)
				for _, task := range []*Service{pair.taskA, pair.taskB} {
					settleCurrentGeneration(t, task, pair.artifactID)
					if err := task.roomFailure(pair.artifactID); err != nil {
						t.Fatalf("a settlement failed its room: %v", err)
					}
				}
				stored := persistedProofTree(t, pair.database, pair.artifactID)
				requireBaseLink(t, "the stored document after both settlements", stored)
				if settled, err := nodeToken(stored); err != nil || settled != token {
					t.Errorf("task A answered the token %s, and the document's token after both settlements is %s (%v)", token, settled, err)
				}
				later := []model.EditOp{{Op: "insert", After: "end", Markdown: "A later paragraph.\n"}}
				if _, err := joinedApplyOps(pair.taskA, pair.artifactID, later, tokenBoundaryWriter, &model.EditPrecondition{Document: token}); err != nil {
					t.Errorf("an edit on task A under the token it answered was refused: %v", err)
				}
			})
		}
	}
}

// pairTypedOnTaskB is a document served by two processes over one database (newSeededProcessPair),
// seeded "First.\n" and settled on task A, whose room is then closed (Evict), over which a browser
// on task B typed markdown's tree (typeAsBrowser), which task B's room appended: the store holds
// that tree and owes its settlement, which neither process runs (each settles an hour on). It
// fails unless task A then holds no room of the document and its record owes nothing, while the
// store's row owes the settlement.
func pairTypedOnTaskB(t *testing.T, markdown string) processPair {
	t.Helper()
	ctx := context.Background()
	pair := newSeededProcessPair(t, "First.\n")
	settleCurrentGeneration(t, pair.taskA, pair.artifactID)
	if err := pair.taskA.Evict(ctx, pair.artifactID); err != nil {
		t.Fatalf("close task A's room: %v", err)
	}
	browser := connectBrowser(t, pair.serverB.URL, pair.artifactID, "bob")
	typed, err := pmdoc.Parse(markdown)
	if err != nil {
		t.Fatalf("Parse(%q): %v", markdown, err)
	}
	typeAsBrowser(t, pair.taskB, pair.artifactID, browser, typed)
	waitForLandedAppends(t, pair.taskB, pair.artifactID)
	if pair.taskA.srv.GetDoc(pair.artifactID) != nil {
		t.Fatal("task A holds a room of the document, so nothing here is tested")
	}
	if pair.taskA.owesSettlement(pair.artifactID) {
		t.Fatal("task A's record owes the document a settlement, so nothing here is tested")
	}
	if owed, err := settlementPending(ctx, pair.database.Pool, pair.artifactID); err != nil || !owed {
		t.Fatalf("the store owes the document a settlement = %t (%v), want true after task B's append", owed, err)
	}
	if _, href := runHolding(persistedProofTree(t, pair.database, pair.artifactID), laterAddress); href != "" {
		t.Fatalf("the store links %s already, where the browser typed it unlinked", laterAddress)
	}
	return pair
}

// A version taken on task A while task A's room stands behind the durable head - task A loaded its
// room, then a person on task B typed or pasted an address the renderer cannot write as the email
// rule reads it, and task B's room appended it (leaveTaskABehind) - is taken from the stored
// document, which holds the rule's tree (captureLiveTextAndAuthors's fromStore): task A's own room
// holds no `@`, so nothing opened a write to repair it first. The version holds the stored tree
// with the base's email links where its own do not read back, so its markdown reads back as the
// tree its token names; once both processes' settlements have run, the stored tree's token is the
// version's, a read on task A answers it, and an edit on task A under it is applied. Versioned
// from the stored tree as it is, the version's markdown read back as another tree.
func TestAVersionOfARoomBehindTheHeadHoldsTheStoredTreeWithTheBaseEmailLinks(t *testing.T) {
	bob := model.Actor{Kind: "user", ID: "bob"}
	for _, shape := range unwritableAddresses {
		for _, row := range []struct {
			name    string
			version func(ctx context.Context, service *Service, artifactID string) (VersionResult, error)
		}{
			{name: "a snapshot", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
				return service.SnapshotVersion(ctx, artifactID, tokenBoundaryWriter)
			}},
			{name: "a named version", version: func(ctx context.Context, service *Service, artifactID string) (VersionResult, error) {
				return service.NamedVersion(ctx, artifactID, "Named on task A behind the head", tokenBoundaryWriter)
			}},
		} {
			t.Run(row.name+" of "+shape, func(t *testing.T) {
				pair := newProcessPair(t)
				pair.leaveTaskABehind(t, 1, bob, shape+"\n")
				stored := persistedProofTree(t, pair.database, pair.artifactID)
				if _, href := runHolding(stored, laterAddress); href != "" {
					t.Fatalf("the store links %s already, where the browser typed it unlinked", laterAddress)
				}
				want, changed, err := pmdoc.FallBackEmailLinks(stored)
				if err != nil || !changed {
					t.Fatalf("FallBackEmailLinks(the stored tree) = changed %t, %v; the test needs a tree whose email links do not read back", changed, err)
				}
				writeVersion(t, pair.taskA, func(ctx context.Context) (VersionResult, error) {
					return row.version(ctx, pair.taskA, pair.artifactID)
				})
				requireReadsBackAs(t, "task A's version", latestVersionMarkdown(t, pair.taskA, pair.artifactID), want)
				versionToken := latestVersionToken(t, pair.taskA, pair.artifactID)
				if wantToken, err := nodeToken(want); err != nil || versionToken != wantToken {
					t.Errorf("task A's version's token is %q, where the stored tree with the base's links has %q (%v)", versionToken, wantToken, err)
				}
				for _, task := range []*Service{pair.taskA, pair.taskB} {
					settleCurrentGeneration(t, task, pair.artifactID)
					if err := task.roomFailure(pair.artifactID); err != nil {
						t.Fatalf("a settlement failed its room: %v", err)
					}
				}
				if settled, err := nodeToken(persistedProofTree(t, pair.database, pair.artifactID)); err != nil || settled != versionToken {
					t.Errorf("the document's token after both settlements is %s, where task A's version's is %s (%v)", settled, versionToken, err)
				}
				_, read, err := pair.taskA.TextWithToken(context.Background(), pair.artifactID)
				if err != nil {
					t.Fatalf("read the document's token on task A: %v", err)
				}
				if read != versionToken {
					t.Errorf("a read on task A answers the token %s, where task A's version's is %s", read, versionToken)
				}
				later := []model.EditOp{{Op: "insert", After: "end", Markdown: "A later paragraph.\n"}}
				if _, err := joinedApplyOps(pair.taskA, pair.artifactID, later, tokenBoundaryWriter, &model.EditPrecondition{Document: versionToken}); err != nil {
					t.Errorf("an edit on task A under the version's token was refused: %v", err)
				}
			})
		}
	}
}

// writeVersion writes the version write makes on service in a joined transaction of its own
// (joinedWrite) and fails unless that transaction committed and stored a version, which it
// returns.
func writeVersion(t *testing.T, service *Service, write func(ctx context.Context) (VersionResult, error)) VersionResult {
	t.Helper()
	var version VersionResult
	if err := joinedWrite(service, func(ctx context.Context) error {
		var err error
		version, err = write(ctx)
		return err
	}); err != nil {
		t.Fatalf("write the version: %v", err)
	}
	if !version.Wrote {
		t.Fatal("the transaction stored no version")
	}
	return version
}

// versionOnTaskA writes the version write makes on service (writeVersion) and fails unless its
// markdown reads back as the tree the store then holds, its token is that tree's, and a read on
// service then answers that token; it returns that token.
func versionOnTaskA(t *testing.T, service *Service, artifactID string, write func(ctx context.Context) (VersionResult, error)) string {
	t.Helper()
	writeVersion(t, service, write)
	stored := persistedProofTree(t, service.store, artifactID)
	requireReadsBackAs(t, "task A's version", latestVersionMarkdown(t, service, artifactID), pmdoc.StripAnchorMarks(stored))
	versionToken := latestVersionToken(t, service, artifactID)
	if storedToken, err := nodeToken(stored); err != nil || versionToken != storedToken {
		t.Errorf("task A's version's token is %q, where the stored tree's is %q (%v)", versionToken, storedToken, err)
	}
	_, read, err := service.TextWithToken(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("read the document's token on task A: %v", err)
	}
	if read != versionToken {
		t.Errorf("a read on task A after its version answers the token %s, where the version's is %s", read, versionToken)
	}
	return read
}

// requireTokenSurvivesSettlement settles artifactID's document and fails unless its token is then
// token, the one what answered, and unless a later edit under that token is applied.
func requireTokenSurvivesSettlement(t *testing.T, service *Service, artifactID, what, token string) {
	t.Helper()
	settleCurrentGeneration(t, service, artifactID)
	if err := service.roomFailure(artifactID); err != nil {
		t.Fatalf("the settlement failed the room: %v", err)
	}
	if settled := documentToken(t, service, artifactID); settled != token {
		t.Errorf("%s answered the token %s, and the document's token after the settlement is %s", what, token, settled)
	}
	later := []model.EditOp{{Op: "insert", After: "end", Markdown: "Another later paragraph.\n"}}
	if _, err := joinedApplyOps(service, artifactID, later, tokenBoundaryWriter, &model.EditPrecondition{Document: token}); err != nil {
		t.Errorf("an edit under the token %s answered was refused: %v", what, err)
	}
}

// latestVersionToken is the document token artifactID's latest version stored.
func latestVersionToken(t *testing.T, service *Service, artifactID string) string {
	t.Helper()
	var token string
	if err := service.store.Pool.QueryRow(context.Background(), `
		select document_token from artifact_versions where artifact_id = $1 order by number desc limit 1
	`, artifactID).Scan(&token); err != nil {
		t.Fatalf("read the latest version's token: %v", err)
	}
	return token
}

// tokenOfMarkdown is the document token of markdown's tree, a token no row's document holds.
func tokenOfMarkdown(t *testing.T, markdown string) string {
	t.Helper()
	tree, err := pmdoc.Parse(markdown)
	if err != nil {
		t.Fatalf("Parse(%q): %v", markdown, err)
	}
	token, err := nodeToken(tree)
	if err != nil {
		t.Fatalf("token of %q: %v", markdown, err)
	}
	return token
}

// blockTokens is each block's token by its id.
func blockTokens(blocks []model.ArtifactBlock) map[string]string {
	tokens := make(map[string]string, len(blocks))
	for _, block := range blocks {
		tokens[block.ID] = block.Token
	}
	return tokens
}
