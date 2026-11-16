package docs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// A person's answer is held once, on the ask's node (dispatch://INFRA-1150 R3), so answering an
// ask changes no byte of a document and supersedes no approval of it. What changes the bytes is
// the answer's old second copy leaving the document: the server-owned attributes of every ask
// block (askServerAttributeNames), which the release before wrote into every version and this
// build stops writing once the whole fleet runs it (rollWindowAskAttributes). That removal rewrites
// bytes a person approved, on blocks nobody edited, on the first accepted write of every document
// holding an ask block, open or answered, since `state="open"` is written on every open ask. So
// the version that drops them carries the approval (R10), and so does a version written while two
// builds serve, whose attributes the roll window keeps in step with an answer given since.
//
// The approval carried is the document's newest review, an approval of version A; the version
// written is N. N carries it when every difference between the two falls under one of the carry
// cases (carryCases), each of which takes out of a tree what it may change - the cases compose, so
// one version that holds several of them carries where its every difference is one of theirs -
// and nothing else differs: A's stored text and N's rendering, each taken through every case, are
// byte for byte the same (carryVerdict). The comparison is against the bytes the person approved,
// which an earlier release stored: A's stored markdown must be what this build renders A's tree to
// (approvedVersion), so a renderer change between A and N is a difference no case carries. Every
// ask block of N must also agree with its node (asksAgreeWithTheirNodes: backed by a row of this document, read
// by settlement, showing no answer state other than its row's and no wording other than a decided
// row's), since a block that shows a decision nobody made, or one under words nobody decided,
// carries nothing however its bytes compare. And every block id A holds must still stand in N.
//
// The cases are R10's alone today: the server-owned ask attributes (attributeRemovalCase). The
// readings of R2, the conversion of D6 and the restore of D1 join carryCases as further cases of
// the same rule, each with the reason it records.
//
// A version whose text is the approved text carries that approval too, byte for byte and before
// any case is weighed (restoresApprovedText). The document's newest review approves version A; a
// later version, N, is stored with A's markdown byte for byte - an accidental edit undone exactly,
// or a version a route wrote over a document that changed nothing - and each ask block of N agrees
// with its node, so no block in it shows other than what was decided. Such an N holds nothing its
// reviewer has not agreed to, so Dispatch records that reviewer's approval of N in their name and
// of their review's own kind, in the transaction that writes N, rather than asking them to approve
// A's text again: the newest review of a plan.md is the plan review that approved A, so a restored
// plan version reads approved by that plan review and not by the session whose write restored it.
// Only the newest review carries: when it requests changes no approval stands, and an older
// approval was superseded by it.

// restoreReason is the reason a review carried to a version restoring the approved text records,
// naming the approval it follows. It opens as every carried reason does, which is how the Legion
// daemon tells a carry from a person's own approval (intake.CarriedApprovalReasonPrefix).
const restoreReason = "Carried forward from the approval of version %d: this version's text is that version's, unchanged."

// carriedReasonPrefix opens the reason of every review a carry case records, naming the approval
// it follows: the daemon's intake.CarriedApprovalReasonPrefix and the version.
const carriedReasonPrefix = "Carried forward from the approval of version %d: "

// askServerAttributeNames are the attributes of an ask block the release before kept in agreement
// with its ask row, which a stored version may hold: `invalid` never reaches stored bytes
// (pmdoc.BlockAttributeSchema.InMarkdown), and the other seven do wherever that release wrote
// them. It is the one place the eight are spelled, and the carry its one reader
// (attributeRemovalCase): it compares against stored versions, which are never rewritten, so it
// stays after the fleet stops writing them.
var askServerAttributeNames = [...]string{"state", "answered_by", "answered_at", "selected", "answer", "invalid", "copied_from", "copied_from_document"}

// carryCase is one kind of difference between an approved version and a later one that carries
// the approval: what its reason says the change was, and neutral, a copy of a tree with what the
// case may change taken out of it, with whether it took anything out. Two versions that differ by
// such changes alone are the same once both are taken through it.
type carryCase struct {
	change  string
	neutral func(tree *pmdoc.Node) (*pmdoc.Node, bool)
}

// attributeRemovalCase is R10's: the server-owned attributes of every ask block
// (askServerAttributeNames), which the first accepted write once the fleet is one build drops, and
// which a write while two builds serve keeps in step with an answer given since.
var attributeRemovalCase = carryCase{
	change:  "the server-owned attributes of its ask blocks, which hold no answer, were the whole change since it.",
	neutral: withoutAskServerAttributes,
}

// carryCases are the cases carryVerdict composes, in the order their reasons are named.
var carryCases = [...]carryCase{attributeRemovalCase}

// carryInput is what carryVerdict decides on: A's tree and the markdown it is stored with, and
// N's tree, each as its own read gives it, and whether every ask block of N agrees with its node
// (asksAgreeWithTheirNodes).
type carryInput struct {
	approved     *pmdoc.Node
	approvedText string
	version      *pmdoc.Node
	agrees       bool
}

// carryApproval carries the document's newest approval to version, just written from rendered,
// when version's text is the approved text, or when every difference from it is one of the carry
// cases (the rules above). It returns the events it appended - the open approval request's
// ask.answered, when one is open, then artifact.approved - and nothing when the version carries no
// approval. A version approved that holds an element this build does not know is a
// NewerContentError, which fails the version write (skipUnreadableVersion). It reads rendered.tree
// and never changes it: the tree is the live document's.
func (s *Service) carryApproval(ctx context.Context, tx pgx.Tx, artifactID string, version model.Version, rendered renderedTree) ([]model.Event, error) {
	// Every version write runs this: anything past the newest review is read only once a carry is
	// possible at all, which needs an approval still standing.
	newest, err := NewestReview(ctx, tx, artifactID)
	if err != nil || newest == nil || newest.State != "approved" {
		return nil, err
	}
	return s.carryStandingApproval(ctx, tx, artifactID, version.Number, rendered, *newest)
}

// carryStandingApproval is carryApproval once newest, the document's newest review, is known to be
// an approval (carryNewestApproval).
func (s *Service) carryStandingApproval(ctx context.Context, tx pgx.Tx, artifactID string, number int, rendered renderedTree, newest model.ArtifactReview) ([]model.Event, error) {
	return carryNewestApproval(ctx, tx, s.events, artifactID, number, rendered.markdown, rendered.tree, newest, s.serverURL)
}

// CarryUploadedApproval is carryApproval for the route that writes a document version itself
// rather than through writeVersionTx - an upload that replaces a document (the api package's
// storeArtifact) - once that version, number, is written with markdown and the open approval
// request has moved to it (MoveApprovalAsk). tree is markdown's read-back, which the caller holds
// when its write left one, and nil otherwise, when markdown is parsed here. The upload runs the
// same rules as every other version write: an upload that drops the server-owned attributes of an
// ask block, as every upload does, carries its approval where that is the whole change. It returns
// the events it appended, as carryApproval does.
func CarryUploadedApproval(ctx context.Context, tx pgx.Tx, broker *events.Broker, artifactID string, number int, markdown string, tree *pmdoc.Node, serverURL string) ([]model.Event, error) {
	newest, err := NewestReview(ctx, tx, artifactID)
	if err != nil || newest == nil || newest.State != "approved" {
		return nil, err
	}
	if tree == nil {
		if tree, err = pmdoc.ParseRendering(markdown); err != nil {
			return nil, skipUnreadableVersion(artifactID, number, err)
		}
	}
	return carryNewestApproval(ctx, tx, broker, artifactID, number, markdown, tree, *newest, serverURL)
}

// carryNewestApproval carries newest, the document's newest review and an approval of version A,
// to version number of artifactID, written as markdown from tree: when every ask block of the
// version agrees with its node (asksAgreeWithTheirNodes), and then either the version's text is
// A's stored text byte for byte (restoresApprovedText), or every difference from it is one of the
// carry cases (carryVerdict). It appends nothing otherwise. A is read only for a version holding an
// ask block: every case is a difference in ask blocks, and a version holding none either holds
// A's text or holds none of A's ask blocks, which still have to stand.
func carryNewestApproval(ctx context.Context, tx pgx.Tx, broker *events.Broker, artifactID string, number int, markdown string, tree *pmdoc.Node, newest model.ArtifactReview, serverURL string) ([]model.Event, error) {
	nodes, agrees, err := asksAgreeWithTheirNodes(ctx, tx, artifactID, tree)
	if err != nil || !agrees {
		return nil, err
	}
	restored, err := restoresApprovedText(ctx, tx, artifactID, newest.Version, markdown)
	if err != nil {
		return nil, err
	}
	if restored {
		return writeCarriedReview(ctx, tx, broker, artifactID, number, newest, fmt.Sprintf(restoreReason, newest.Version), serverURL)
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	approved, approvedText, readable, err := approvedVersion(ctx, tx, artifactID, newest.Version)
	if err != nil || !readable {
		return nil, err
	}
	change, carried, err := carryVerdict(carryInput{approved: approved, approvedText: approvedText, version: tree, agrees: agrees})
	if err != nil {
		return nil, skipUnreadableVersion(artifactID, number, err)
	}
	if !carried {
		return nil, nil
	}
	return writeCarriedReview(ctx, tx, broker, artifactID, number, newest, fmt.Sprintf(carriedReasonPrefix, newest.Version)+change, serverURL)
}

// asksAgreeWithTheirNodes is every ask block of a version, tree, by block id (askNodes), and
// whether each agrees with its node: a block the row of artifactID indexes agrees with that row,
// the projection of its node (askAgreesWithItsNode), and a block no row of this document indexes
// is a copy of an ask on another document of its owner (readCopiedAskSources, settlement's own
// classification) that agrees with its source (copyAgreesWithItsSource). A version holding no ask
// block agrees; one holding a block id twice, which settlement reads no block under, or a block
// neither indexed nor a copy - one forged on the socket, or one whose update the room has not
// credited yet - does not, and leaves its decisions unknown to every carry rule. The rows are read
// only for a version holding an ask block, and the sources only for one holding a block no row
// indexes; the carry opens no ask from what it reads, so it takes no copy lock.
func asksAgreeWithTheirNodes(ctx context.Context, tx pgx.Tx, artifactID string, tree *pmdoc.Node) (map[string]*pmdoc.Node, bool, error) {
	nodes, readable := askNodes(tree)
	if !readable || len(nodes) == 0 {
		return nil, readable, nil
	}
	rows, err := loadAskBlocks(ctx, tx, artifactID)
	if err != nil {
		return nil, false, err
	}
	var copies []askBlock
	var unindexed []string
	for id, node := range nodes {
		if ask, indexed := rows[id]; indexed {
			if !askAgreesWithItsNode(node, ask) {
				return nil, false, nil
			}
			continue
		}
		block, err := parseAskBlock(node)
		if err != nil {
			return nil, false, nil
		}
		copies, unindexed = append(copies, block), append(unindexed, id)
	}
	if len(copies) == 0 {
		return nodes, true, nil
	}
	owner, err := documentOwner(ctx, tx, artifactID)
	if err != nil {
		return nil, false, err
	}
	sources, err := readCopiedAskSources(ctx, tx, artifactID, owner, copies, unindexed)
	if err != nil {
		return nil, false, err
	}
	for _, block := range copies {
		source, copied := sources[block.id]
		if !copied || !copyAgreesWithItsSource(block.node, source) {
			return nil, false, nil
		}
	}
	return nodes, true, nil
}

// copyAgreesWithItsSource reports whether node, a copy of source's ask on another document,
// agrees with it: settlement reads it (no `invalid`), and it shows no state of the source other
// than the source's - none at all, or what the roll window keeps in step with the source, its
// provenance among it (rollWindowAskAttributes with the source's document). Its wording is the
// source's by the classification that found it.
func copyAgreesWithItsSource(node *pmdoc.Node, source copiedSource) bool {
	if _, flagged := node.Attrs["invalid"]; flagged {
		return false
	}
	inStep := rollWindowAskAttributes(true, source.ask, source.document)
	return !withoutStoredAskAttributes(attributesOf(node)) || !inStep(attributesOf(node))
}

// askAgreesWithItsNode reports whether node, an ask block of a version, agrees with ask, the row of
// its document that indexes it, which is the projection of the ask's node: settlement reads it (it
// carries no `invalid`); it shows no answer state other than the row's - none at all, as every
// block of a version written once the fleet is one build, or what the roll window keeps in step
// with the row (rollWindowAskAttributes), as a block of a version written while two builds serve -
// so a forged or stale state, answerer, time, selection or answer, or a copy's provenance on a
// block of this document's own, disagrees; and, for a row that is not open, it asks the wording
// the row was decided on (askBlock.asks, settlement's own test), which settlement writes back over
// a decided block. An open row's body is free: its question is still being written, and the row
// takes it.
func askAgreesWithItsNode(node *pmdoc.Node, ask model.Ask) bool {
	if _, flagged := node.Attrs["invalid"]; flagged {
		return false
	}
	inStep := rollWindowAskAttributes(true, ask, "")
	if withoutStoredAskAttributes(attributesOf(node)) && inStep(attributesOf(node)) {
		return false
	}
	if ask.State == asks.StateOpen {
		return true
	}
	block, err := parseAskBlock(node)
	return err == nil && block.asks(model.NewAskEditPrevious(ask))
}

// attributesOf is a node of node's type holding a copy of its attributes alone, for a write to an
// ask block's attributes to be tried on without changing node.
func attributesOf(node *pmdoc.Node) *pmdoc.Node {
	attributes := make(pmdoc.Attrs, len(node.Attrs))
	maps.Copy(attributes, node.Attrs)
	return &pmdoc.Node{Type: node.Type, Attrs: attributes}
}

// withoutAskServerAttributes is attributeRemovalCase's neutral: a copy of tree with every ask
// block's server-owned attributes (askServerAttributeNames) taken off, and whether any block held
// one. Each container on the way and each such block is a fresh node; every other node is the
// tree's own, which nothing here changes.
func withoutAskServerAttributes(tree *pmdoc.Node) (*pmdoc.Node, bool) {
	if tree.Type == "ask" {
		var stripped pmdoc.Attrs
		for _, name := range askServerAttributeNames {
			if _, held := tree.Attrs[name]; held {
				if stripped == nil {
					stripped = maps.Clone(tree.Attrs)
				}
				delete(stripped, name)
			}
		}
		children, took := withoutAskServerAttributesBelow(tree)
		if stripped == nil && !took {
			return tree, false
		}
		shown := *tree
		if stripped != nil {
			shown.Attrs = stripped
		}
		shown.Children = children
		return &shown, true
	}
	children, took := withoutAskServerAttributesBelow(tree)
	if !took {
		return tree, false
	}
	copied := *tree
	copied.Children = children
	return &copied, true
}

// withoutAskServerAttributesBelow is the children of tree each taken through
// withoutAskServerAttributes, and whether any held a server-owned ask attribute: tree's own
// children where none did.
func withoutAskServerAttributesBelow(tree *pmdoc.Node) ([]*pmdoc.Node, bool) {
	if tree.Type == "text" || len(tree.Children) == 0 {
		return tree.Children, false
	}
	var children []*pmdoc.Node
	for index, child := range tree.Children {
		next, took := withoutAskServerAttributes(child)
		if !took {
			continue
		}
		if children == nil {
			children = slices.Clone(tree.Children)
		}
		children[index] = next
	}
	if children == nil {
		return tree.Children, false
	}
	return children, true
}

// writeCarriedReview writes the carry of review to version number of artifactID with reason: the
// open approval request's answer, Approve in the approver's name, when one is open, then the
// approved review and its artifact.approved. The carry records the kind of the review it
// carries, since it is that review's approval it follows: a plan version restored to the text
// its plan review approved stays approved by that plan review, not by the session that wrote the
// version (ReviewKindPlanReview). A row read from artifact_reviews always names its kind, and a
// review that names none is a person's, as the column's default says. It returns those events in
// that order.
func writeCarriedReview(ctx context.Context, tx pgx.Tx, broker *events.Broker, artifactID string, number int, review model.ArtifactReview, reason, serverURL string) ([]model.Event, error) {
	var appended []model.Event
	var askID *string
	open, err := OpenApprovalAsk(ctx, tx, artifactID)
	if err != nil {
		return nil, err
	}
	artifact, err := reviewedArtifact(ctx, tx, artifactID)
	if err != nil {
		return nil, err
	}
	// A request that names one document - every request but one opened over a spec and its
	// issue's Verification document - is answered by this carry, as it always was. One that names
	// two is answered only once both stand approved at the versions it names, which this review
	// settles for this document; the request stays open for the other, and the carried review
	// records the request it was given against either way.
	if open != nil {
		askID = &open.ID
		closes, err := NamedDocumentsApproved(ctx, tx, open.Approval.Documents, artifactID, carriedAtNamedVersion(open, artifactID, number))
		if err != nil {
			return nil, err
		}
		if closes {
			answered, err := answerApprovalAsk(ctx, tx, broker, open, review.Actor, reason, serverURL)
			if err != nil {
				return nil, err
			}
			appended = append(appended, answered)
		}
	}
	_, event, err := WriteReview(ctx, tx, broker, artifact, number, "approved", cmp.Or(review.Kind, ReviewKindPerson), review.Actor, &reason, askID)
	if err != nil {
		return nil, err
	}
	return append(appended, event), nil
}

// restoresApprovedText reports whether markdown, the text a new version of artifactID is written
// with, is byte for byte the stored text of its version earlier, the one a person approved. A
// version that stores no markdown, a file's, holds no text and compares false. octet_length reads
// a stored text's size without decompressing it, so only a stored text of the same size is read,
// and the write of any other text reads no document.
func restoresApprovedText(ctx context.Context, tx pgx.Tx, artifactID string, earlier int, markdown string) (bool, error) {
	var stored string
	err := tx.QueryRow(ctx, `
		select markdown from artifact_versions
		where artifact_id = $1 and number = $2 and octet_length(markdown) = $3
	`, artifactID, earlier, len(markdown)).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read document version %d: %w", earlier, err)
	}
	return stored == markdown, nil
}

// carriedAtNamedVersion reports whether the version this carry approves is the version the open
// request names for the carried document. A request that does not name it at all is decided by
// its other documents alone, as it always was.
func carriedAtNamedVersion(open *model.Ask, carried string, version int) bool {
	named := open.Approval.Document(carried)
	return named == nil || named.Version == version
}

// skipUnreadableVersion is carryApproval's answer to a version this parser or renderer refuses. A
// version holding an element this build does not know (pmdoc.ErrUnknownElement) is one a newer
// build wrote while two builds serve one database: this build cannot tell whether the approval
// carries, so the refusal is a NewerContentError naming the version and the element, and the
// version write fails with it - settlement writes no version and leaves the document owed, for a
// build that reads the element (skipUnreadableSettlement). Any other schema refusal is a version
// an earlier release stored, which is not this write's to fail for: the approval is not carried
// and the refusal is logged. Any other error is the write's.
func skipUnreadableVersion(artifactID string, number int, err error) error {
	if errors.Is(err, pmdoc.ErrUnknownElement) {
		return newerContent(fmt.Errorf("version %d: %w", number, err))
	}
	if !errors.Is(err, pmdoc.ErrSchema) {
		return err
	}
	slog.Warn("dispatch: skip carrying an approval over a document version outside the Proof schema",
		"room", artifactID, "version", number, "error", err)
	return nil
}

// askNodes is every ask node tree holds, by block id, wherever it stands: in a blockquote, a list
// item or another typed block; and whether each id is held once. A repeated id is one settlement
// reads no block under (collectAskBlocksForSettlement), so the nodes are not returned, and the
// caller carries nothing; a nil tree holds none. A version's tree is read through it
// (asksAgreeWithTheirNodes).
func askNodes(tree *pmdoc.Node) (map[string]*pmdoc.Node, bool) {
	var nodes map[string]*pmdoc.Node
	repeated := false
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if node.Type != "ask" {
			return true
		}
		id, _ := node.Attrs[pmdoc.BlockIDAttr].(string)
		if _, repeated = nodes[id]; repeated {
			return false
		}
		if nodes == nil {
			nodes = map[string]*pmdoc.Node{}
		}
		nodes[id] = node
		return true
	})
	if repeated {
		return nil, false
	}
	return nodes, true
}

// carryVerdict is the rule over the two versions: N, input.version, carries the approval of A,
// input.approved, when every ask block of N agrees with its node (input.agrees); A's stored text,
// input.approvedText, is what this build renders A's tree to, so that what is compared is the
// bytes the person approved, and a renderer change between A and N is a difference no case
// carries; both trees, taken through every carry case (carryCases), render byte for byte the
// same; at least one case took something out of one of them, since a version that equals A
// without any is the restore's to carry (restoresApprovedText), or a change nobody approved; and
// every block id A holds still stands in N (blockIDsStand). It answers what the carried reason
// says the change was: the change of each case that took something out, in carryCases' order.
// Its error is a refusal of this renderer.
func carryVerdict(input carryInput) (string, bool, error) {
	if !input.agrees {
		return "", false, nil
	}
	approvedText, err := pmdoc.Render(input.approved)
	if err != nil || approvedText != input.approvedText {
		return "", false, err
	}
	approved, version := input.approved, input.version
	var changes []string
	for _, carried := range carryCases {
		var tookApproved, tookVersion bool
		approved, tookApproved = carried.neutral(approved)
		version, tookVersion = carried.neutral(version)
		if tookApproved || tookVersion {
			changes = append(changes, carried.change)
		}
	}
	if len(changes) == 0 {
		return "", false, nil
	}
	approvedText, err = pmdoc.Render(approved)
	if err != nil {
		return "", false, err
	}
	versionText, err := pmdoc.Render(version)
	if err != nil || versionText != approvedText {
		return "", false, err
	}
	if !blockIDsStand(input.approved, input.version) {
		return "", false, nil
	}
	return strings.Join(changes, " "), true, nil
}

// blockIDsStand reports whether every typed block id approved holds - the ids a stored version
// writes, its other blocks' being minted afresh by every read - is a typed block's id in version
// too. A stored version's read-back repairs an id it holds twice by minting the second block a
// fresh one (pmdoc.ParseRendering), which no later version holds, so such a version carries
// nothing.
func blockIDsStand(approved, version *pmdoc.Node) bool {
	standing := typedBlockIDs(version)
	stands := true
	pmdoc.Walk(approved, func(node *pmdoc.Node) bool {
		if _, typed := pmdoc.BlockTypeAttributes(node.Type); typed {
			id, _ := node.Attrs[pmdoc.BlockIDAttr].(string)
			if _, held := standing[id]; !held {
				stands = false
			}
		}
		return stands
	})
	return stands
}

// typedBlockIDs is the id of every typed block of tree.
func typedBlockIDs(tree *pmdoc.Node) map[string]struct{} {
	ids := map[string]struct{}{}
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if _, typed := pmdoc.BlockTypeAttributes(node.Type); typed {
			id, _ := node.Attrs[pmdoc.BlockIDAttr].(string)
			ids[id] = struct{}{}
		}
		return true
	})
	return ids
}

// approvedVersion is the tree of the stored version number of artifactID and the markdown it is
// stored with, and whether that version could be read: a version outside the Proof schema is
// logged and not readable, a number below 1, before the document's first version, is no version
// and not readable either, and a version holding an element this build does not know is an error
// (skipUnreadableVersion).
func approvedVersion(ctx context.Context, tx pgx.Tx, artifactID string, number int) (*pmdoc.Node, string, bool, error) {
	if number < 1 {
		return nil, "", false, nil
	}
	var markdown string
	if err := tx.QueryRow(ctx, `select markdown from artifact_versions where artifact_id = $1 and number = $2`, artifactID, number).Scan(&markdown); err != nil {
		return nil, "", false, fmt.Errorf("read document version %d: %w", number, err)
	}
	tree, err := pmdoc.ParseRendering(markdown)
	if err != nil {
		return nil, "", false, skipUnreadableVersion(artifactID, number, err)
	}
	return tree, markdown, true, nil
}

// answerApprovalAsk answers the open approval request ask with Approve in actor's name, giving
// reason as the answer's text, as a human's answer through the answer route is written, and
// appends its ask.answered event on the ask's owner. The answer goes through the one writer of an
// ask's answer state (asks.Settle), on the ask's node and then its row, and what it states on the
// node is appended under that ask.answered once it exists. The write names the open state it
// answers from: a request that is no longer open is an error, not a second answer. The answer's
// time is the transaction's now(), as the answer route's is: each process's host keeps its own
// clock, and this one is the created_at of the ask.answered event appended here and of the review
// the carry writes beside it, and the time the answer's part is written at (IndexAskPartsCounted
// below).
func answerApprovalAsk(ctx context.Context, tx pgx.Tx, broker *events.Broker, ask *model.Ask, actor model.Actor, reason, serverURL string) (model.Event, error) {
	var answeredAt time.Time
	if err := tx.QueryRow(ctx, `select now()`).Scan(&answeredAt); err != nil {
		return model.Event{}, fmt.Errorf("read the approval answer's time: %w", err)
	}
	answer := model.AskAnswer{User: actor.ID, Selected: []string{ApprovalOptionApprove}, Text: &reason, At: answeredAt.UTC()}
	var held graphstore.Uncaused
	change := asks.Answered(answer)
	change.From, change.Who, change.At, change.Held = asks.StateOpen, actor, answer.At, &held
	settled, err := asks.Settle(ctx, tx, ask.ID, change)
	if err != nil {
		return model.Event{}, fmt.Errorf("answer approval ask: %w", err)
	}
	ask.State, ask.Answer, ask.Resolution = settled.State, settled.Answer, settled.Resolution
	// The answer is a part of the request, so its text reaches the graph with it, and the
	// ask.answered below names what it moved.
	changes, err := refs.IndexAskPartsCounted(ctx, tx, refs.NewReader(ctx, tx, "ask", ask.ID, serverURL),
		*ask, answer.At, model.ReferenceChanges{})
	if err != nil {
		return model.Event{}, fmt.Errorf("index the parts of approval ask %s: %w", ask.ID, err)
	}
	payload, err := events.AskEventPayload(ctx, tx, *ask, changes)
	if err != nil {
		return model.Event{}, err
	}
	event, err := broker.Append(ctx, tx, model.Event{
		IssueKey: ask.IssueKey, ArtifactID: ask.ArtifactID,
		Type: "ask.answered", Actor: actor,
		Payload: model.AskAnsweredEventPayload{AskEventPayload: payload},
	})
	if err != nil {
		return model.Event{}, fmt.Errorf("append approval ask answer: %w", err)
	}
	// The answer's part edges are new rows of this transaction, so they take the event's id and
	// a ?since= read lists them.
	if err := refs.Stamp(ctx, tx, "ask", ask.ID, event.ID); err != nil {
		return model.Event{}, fmt.Errorf("stamp approval ask answer references: %w", err)
	}
	if err := graphstore.Caused(ctx, tx, held, event.ID); err != nil {
		return model.Event{}, fmt.Errorf("state the answer of approval ask %s: %w", ask.ID, err)
	}
	return event, nil
}
