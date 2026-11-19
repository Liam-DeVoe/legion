package graphstore

import (
	"bytes"
	"errors"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// moduleRoot is the packages/envoy module, read from this package's own directory.
const moduleRoot = "../../.."

// graphstoreImportPath is the path a file outside this package names the store by.
const graphstoreImportPath = "github.com/sjawhar/envoy/internal/dispatch/graphstore"

// graphstoreDirectory is this package's own directory under moduleRoot, where its writers are
// named without a package.
const graphstoreDirectory = "internal/dispatch/graphstore"

// graphstoretestDirectory is the one package besides the store's own whose statements write its
// tables: graphstoretest, which seeds for other packages' tests the rows the store's writers
// refuse to write, and which no deployment builds. Any other package under the store's directory
// is held to the rule like every package outside it.
const graphstoretestDirectory = graphstoreDirectory + "/graphstoretest"

// storeWriteFunctions are the calls a write of graph_nodes and graph_tuples from outside this
// package goes through, the same ones docs' register of the tree's passes scans for
// (storeWriterCall), and the session writers beside them: Write, WriteUncaused and Caused in a
// caller's transaction, and QueueWrite, QueueNodes and QueueCaused, the same writes queued on a
// write phase's batch; WriteArms and WriteArmsUncaused spliced into a caller's statement;
// Authored, Reached and SessionRecorded, which run those arms for the writers that record an
// author, a reached session and a session an ask names, and QueueAuthored, QueueReached and
// QueueSessionRecorded, which queue them; EraseNode, the erase's delete of a node and its tuples;
// RedactValues, the erase's rewrite of the quote of an erased block in the text values of the
// blocks holding it; FillScopes, the backfill's fill of the project of a node stored with none;
// Retarget, the move of one pointer tuple of a node from one far end to another; and
// QueueFrameTuple, Write's append of one frame value of a run queued on a caller's batch. Each is
// a function of the package, never a method, so a call of one is named by the package and found
// here wherever it is made. A write that holds a statement of its own instead is found by
// rawStoreWrite, and none outside this package may hold one.
var storeWriteFunctions = []string{"Write", "WriteUncaused", "WriteArms", "WriteArmsUncaused", "Caused",
	"QueueWrite", "QueueNodes", "QueueCaused",
	"Authored", "Reached", "SessionRecorded", "QueueAuthored", "QueueReached", "QueueSessionRecorded",
	"EraseNode", "RedactValues", "FillScopes", "Retarget", "QueueFrameTuple"}

// rawStoreWrite matches a statement that writes graph_nodes or graph_tuples itself rather than
// through one of storeWriteFunctions: an insert, an update or a delete, in any case, over any
// whitespace, and under the schema's name as well as bare.
var rawStoreWrite = regexp.MustCompile(`(?is)\b(?:insert\s+into|update|delete\s+from)\s+(?:public\s*\.\s*)?graph_(?:nodes|tuples)\b`)

// storeWriteCallSites is every function outside the tests that calls one of storeWriteFunctions,
// named as docs' register names a pass: the file, from internal/dispatch where it lives under it,
// and the function, without its receiver. Beside each site is the test that drives its lock steps
// against the order written on api/inbox_read.go's markInboxReadThreads, as "<package dir>#<Test>":
// the package's directory, named as a site's file is (siteKey), and the test's name. Such a test
// runs the site's real code, holds each step it takes in another transaction, and reads where the
// write waits and which table locks it holds by then. TestEveryStoreWriteTakesOneLockOrder proves
// the order on this package's own writers. A site that is not listed has had nobody prove its
// steps, and a site whose driver is missing or names no test of that package is one c-lock-order's
// run drives no test of, so each fails here until it is put right.
var storeWriteCallSites = map[string]string{
	// The follow of an ask, which records the following session's node in the caller's
	// transaction or write phase, and each site that moves that node onto the event it appends
	// after the follow, and appends the node's base attributes under it, through QueueCaused:
	// the follower route (appendCausingEvent), an ask's create, an approval request renewed,
	// and a settlement's asks, each stated right after its ask.opened (appendEvents). asks'
	// TestAFollowTakesTheStoreLockOrder proves both steps on the follow alone, and each caller's
	// test proves them inside its caller. The comment path and a delivery's reply give the follow
	// their own event instead, which it states in its own statement.
	"asks/followers.go#Follow":         "asks#TestAFollowTakesTheStoreLockOrder",
	"asks/followers.go#queueFollow":    "asks#TestAFollowTakesTheStoreLockOrder",
	"asks/followers.go#Queue":          "asks#TestAFollowTakesTheStoreLockOrder",
	"api/server.go#appendCausingEvent": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/asks.go#createAskFor":         "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/reviews.go#renewApprovalAsk":  "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"docs/settlement.go#appendEvents":  "docs#TestEveryDocsStoreWriterTakesTheStoreLockOrder",
	// The block index's cause: each site that appends what a version's index pass stated under
	// the event that recorded the version, through QueueCaused (refs'
	// TestCausedTakesTheStoreLockOrder proves the step on its own): an upload, a version write's
	// event, an issue's spec, the block-index tick, an issue room's pass under each of its
	// documents' events, and the seed's documents. A settlement's version states it right after
	// its event (appendEvents, above).
	"api/artifacts.go#commitArtifactVersionEvent":   "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/artifacts.go#storeArtifact":                "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/artifacts.go#queueNewArtifact":             "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/issue_create.go#createIssue":               "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"cmd/dispatch-seed/write_docs.go#writeDocument": "cmd/dispatch-seed#TestTheSeedsDocumentWriteTakesTheStoreLockOrder",
	"docs/block_index.go#indexOwedDocument":         "docs#TestEveryDocsStoreWriterTakesTheStoreLockOrder",
	"docs/block_index.go#causeRoomFacts":            "docs#TestEveryDocsStoreWriterTakesTheStoreLockOrder",
	// The comment path: the comment's node before the comments row that names it, then its
	// tuples and its cause under the event appended after both, and who wrote it.
	"api/comment_create.go#caused":           "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/comment_create.go#createCommentFor": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/comment_create.go#writeNode":        "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	// A delivery's reply and the settle of a delivery attempt, which records the session the
	// attempt reached, and the one record both replies share of what their answer makes of the
	// attempt they answer: an attempt the answer flips to sent is one the listener took, and
	// its session is owed a node (queueAnsweredDelivery, which the reply's driver runs last).
	"api/comment_delivery.go#completeCommentDelivery": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/comment_delivery.go#queueAnsweredDelivery":   "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/comment_delivery.go#replyComment":            "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/message_delivery.go#completeMessageDelivery": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	// The nodes and tuples of the reasoning kinds a request writes - a lens, its findings, a
	// risk and a constraint - with the author's node where the credential proves one: the issue's
	// owner row, then one Write of every node in ascending (kind, id) and every tuple at the
	// sentinel, appending no event. Its order is the order TestEveryStoreWriteTakesOneLockOrder
	// proves of Write.
	"api/reasoning_write.go#writeReasoningNodes": "graphstore#TestEveryStoreWriteTakesOneLockOrder",
	// One pointer tuple a request appends, retracts or retargets out of a node of an issue
	// (has_kind, triggered_by, constrained_by, prevented_by): the issue's owner row, then one
	// Write of the tuple, or Retarget, which takes the node's row and writes through Write. It
	// appends no event. Its order is the order TestEveryStoreWriteTakesOneLockOrder proves of
	// Write, after the owner row writeReasoningNodes takes first too.
	"api/reasoning_write.go#writeReasoningPointer": "graphstore#TestEveryStoreWriteTakesOneLockOrder",
	// One node's state a request writes or settles (api/node_state.go): the issue's owner row,
	// then one Write of the node as the store holds it but for its state, a plain write that
	// moves the row and appends the state tuple. It appends no event. Its order is the order
	// TestEveryStoreWriteTakesOneLockOrder proves of Write, after the owner row writeReasoningNodes
	// takes first too.
	"api/reasoning_write.go#writeNodeState": "graphstore#TestEveryStoreWriteTakesOneLockOrder",
	// An issue's own field write made for a run (api/issue_patch.go), which names the issue changed:
	// the issue's owner row (lockPatchRows), the issue's row, then, on a context that carries a
	// run alone, the issue's node through WriteUncaused, the issue's own event, and what the node
	// states with its changed_by_run under that event through Caused. api's
	// TestEveryApiStoreWriterTakesTheStoreLockOrder holds it to those steps.
	"api/issue_patch.go#patchIssue": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	// An issue's relations (dispatch://INFRA-1253 R9, R10), which its create and its PATCH state:
	// after the issue's owner row, its row and its issue_links rows, the node of the item its
	// filed_from names where the store held none, through Write, with a comment's row named after
	// it (stateFiledFromNode), then the issue's own node through WriteUncaused (createIssue's and
	// patchIssue's), the issue's event, and last the relations that event causes, its child_of,
	// needs, duplicate_of and filed_from and the part_of it no longer hangs from, in one statement
	// of the store's arms (queueIssueRelations). api's TestEveryApiStoreWriterTakesTheStoreLockOrder
	// holds a PATCH of its needs, and a PATCH of its filed_from at a comment with no node, to those
	// steps.
	"api/issue_relations.go#stateFiledFromNode":  "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/issue_relations.go#queueIssueRelations": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	// An edit of an ask's or a comment's words made for a run (api/asks.go, api/comment_actions.go),
	// which names the ask or the comment changed: the owner row of the ask or the comment, its row
	// `for no key update` or `for update` and then its update, then, on a context that carries a
	// run alone, its node through WriteUncaused, its own event, and what the node states with its
	// changed_by_run under that event through Caused. api's
	// TestEveryApiStoreWriterTakesTheStoreLockOrder holds each to those steps.
	"api/asks.go#editAsk":                "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/comment_actions.go#editComment": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	// A comment's anchor block (T17b of dispatch://INFRA-1253's plan), the anchored_in that
	// comments.anchor's block_id projects, kept by each write that moves the anchor's block or its
	// orphan flag beside the comment's create (createCommentFor's caused, above): an accept or a
	// reject that finds a suggestion's mark gone orphans the anchor and retracts its anchored_in,
	// after the comment's owner row and its row `for update` (lockedComment), its update, then the
	// retraction through Write at the sentinel (api's TestEveryApiStoreWriterTakesTheStoreLockOrder);
	// the refresh of a document's anchors retracts or states it for each comment whose anchor it
	// orphans or finds again, after the owner row its settlement or version write takes first, the
	// comments update and the comment.anchor_refreshed events it appends, which cause the tuples
	// (docs' TestTheAnchorRefreshTakesTheOwnerRowTheRowTheEventThenTheTuple); and the anchor block
	// backfill states it beside the block id it pins, the comment's row, then the tuple through
	// Write at the sentinel. It takes no owner row, which the register's order admits: once it holds
	// the row it waits on nothing, so no owner-row holder that waits on the row can be waited on in
	// turn (docs' TestTheAnchorBlockBackfillTakesTheRowThenTheTuple holds the issue's row through the
	// whole run, and the run never queues behind it).
	"api/comment_actions.go#orphanCommentAnchor":      "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"docs/anchor_blocks.go#pinCommentAnchorBlock":     "docs#TestTheAnchorBlockBackfillTakesTheRowThenTheTuple",
	"docs/anchor_refresh.go#stateRefreshedAnchoredIn": "docs#TestTheAnchorRefreshTakesTheOwnerRowTheRowTheEventThenTheTuple",
	// A run of the command bar (api/going.go), which takes no owner row: its record writes the
	// interaction node, its author's node and their tuples in one Write before the going_requests
	// row, and each frame, in a transaction of its own, takes the run's interaction node row
	// `for no key update`, then the run's row `for key share`, then appends the frame value through
	// Write before the going_events row. api's TestEveryApiStoreWriterTakesTheStoreLockOrder holds
	// both to their steps. A frame of a run a pod of the release before recorded with no node
	// writes that node first through the backfill's writer for one run, which takes the run's
	// going_requests row `for no key update` and then writes in one Write; its order is the order
	// TestEveryStoreWriteTakesOneLockOrder proves of Write.
	"api/going.go#appendGoingEvent":        "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/going.go#recordGoingRequest":      "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"graphcensus/interactions.go#writeRun": "graphstore#TestEveryStoreWriteTakesOneLockOrder",
	// The backfill's passes over what a process of the release before writes of a run during a
	// roll (graphcensus' TestTheBackfillMendsWhatAnOlderPodWroteOfARun): a frame it appended with
	// no value, stated through Write after the run's interaction node row `for no key update`, the
	// frame path's own first lock, and a run whose row it deleted, whose node is written gone
	// through Write after that same row. Neither takes an owner row, as no run stands under one;
	// graphcensus' TestEveryBackfillBatchTakesTheStoreLockOrder drives both through those steps.
	"graphcensus/backfill.go#writeGoneRun":   "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	"graphcensus/backfill.go#writeRunFrames": "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	// The node of every issue, document, ask and message a request creates, in the project or
	// between the parties it belongs to: written before its row, and caused by the event the
	// create appends after it. createIssue, storeArtifact with queueNewArtifact, and createAskFor
	// are listed above for their block index and follow, and their drivers hold the node step too;
	// these are the others. Their order - the owner lock their path already takes, the node before
	// the row, the event before the tuples - is the order of the write they share (QueueNodes, then
	// QueueCaused, or WriteUncaused, then Caused, where a route appends its event at once), and
	// api's TestEveryObjectNodeWriterTakesTheStoreLockOrder drives each of them through it.
	// The bytes of an image's or a file's later version (api's statedVersionBytes, which plans them
	// and writes nothing) are stated on the node its row names with no node written, after the
	// owner lock and that row read FOR SHARE, through storeArtifact's QueueCaused after the event
	// it appends, which causes them (api's TestALaterVersionStatesItsBytesInTheStoreLockOrder proves
	// its steps, and that a later version waits for a backfill naming its node). removedDocumentNodes is a removal's nodes of the
	// document and of its versions stated gone, after the issue's row and before any row of the
	// document is deleted, and removeArtifact appends what it stated under the artifact.removed
	// event, through Caused (api's TestARemovalStatesItsDocumentGoneInTheStoreLockOrder).
	// createIssueTree is an issue's tree artifact (dispatch://INFRA-1253 R5): its node and its
	// part_of to the issue through WriteUncaused before the artifacts row that names the node and
	// the issue_trees row, in its caller's transaction, which holds the issue's row and causes what
	// the write stated under the event it appends - the order of the write it shares with
	// createIssue's spec (WriteUncaused, then the row), which TestEveryStoreWriteTakesOneLockOrder
	// proves of WriteUncaused. createDecisionsQuery is the decisions query node of an issue born
	// converted (T12b of dispatch://INFRA-1253's plan): in createIssue's transaction, after the
	// issue's row, the tree's issue_trees row and the spec's document_rooms row, its node and its
	// part_of through WriteUncaused before the artifacts row naming the node, then its empty
	// version 1 and that version's node, and createIssue causes what both stated under
	// issue.created through Caused. api's TestEveryObjectNodeWriterTakesTheStoreLockOrder runs
	// createIssue born converted and holds it to those steps.
	"api/artifact_removal.go#removedDocumentNodes": "api#TestARemovalStatesItsDocumentGoneInTheStoreLockOrder",
	"api/artifacts.go#createIssueTree":             "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"api/broadcasts.go#writeBroadcast":             "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"api/issue_create.go#createDecisionsQuery":     "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"api/message_delivery.go#insertSessionReply":   "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"api/messages.go#createStoredMessage":          "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"api/reviews.go#openApprovalAsk":               "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"docs/ask_blocks.go#createAskBlock":            "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	// The removal of an untouched document states its node gone, before its row goes, caused by
	// the artifact.removed event it appends after the row (Write of the node the document's
	// create would have written, for a document an earlier build made with none, then
	// WriteUncaused, then Caused).
	"api/artifact_removal.go#removeArtifact": "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	// The node of every version a write inserts, in the row's transaction (dispatch://INFRA-1253):
	// the one writer every insert of a version calls (docs.WriteVersionNode) - a version write
	// (writeVersionTx), an upload (api's writeDocumentVersion and storeArtifact) and an issue's spec
	// (api's createIssue) - after the owner row its caller holds and the block nodes of the
	// version's index, with its facts held for the event the caller appends and causes them with,
	// through Caused at the sites listed above. Its address is drawn from the version's own number,
	// so no other transaction holds it; api's TestEveryObjectNodeWriterTakesTheStoreLockOrder and
	// TestALaterVersionStatesItsBytesInTheStoreLockOrder hold an upload's and an issue create's
	// steps around it, and api's TestEveryVersionRowIsTheProjectionOfAVersionNode holds its node to
	// its row's transaction at each site.
	"docs/mutation.go#WriteVersionNode": "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	// A document's retirement, in the same order under the document's
	// owner row: its artifact node stated gone, then the artifacts row that retires it, then
	// document.retired and the node's facts under it. A removal of an untouched document states
	// its node gone through its own writer (removedDocumentNodes, listed above), with each
	// changed_by_run of the node retracted, under the owner lock and the room, then deletes its
	// rows, appends artifact.removed and the node's facts under it (api's
	// TestEveryApiStoreWriterTakesTheStoreLockOrder holds it to those steps).
	"docs/retire.go#RetireDocument":   "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
	"docs/retire.go#writeRetiredNode": "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
	// A person's Keep of an agent's decision: under the issue's owner
	// row, the person's node, then the decision_keeps row that projects the Keep, then
	// decision.kept, and the kept_by tuple and the kept_text_sum value under it, through Caused.
	"api/decisions.go#keepDecisionNode": "api#TestEveryAskTransitionAndKeepTakesTheStoreLockOrder",
	// The decisions step's restore and conversion of an issue,
	// under the issue's owner row and its asks' rows: each document's version and its event, the
	// index pass's facts under that event through Caused, then the moves, the restored asks'
	// relations and the carried Keeps, with the person node a Keep names, under the spec's event.
	"docs/decision_restore.go#writeVersion": "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
	"docs/decision_restore.go#writeFacts":   "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
	// A person's own write that takes an answered ask's block out of its document: the person's
	// node at the sentinel, after the version's block index and before the event that records the
	// version, and the removed_by tuple appended with the index's facts under that event. A
	// rendered version's removals ride in its block index's own write (refs.PlanBlocks' beside);
	// an unrendered one's are queued here.
	"docs/removed_by.go#queueRemovals": "docs#TestAPersonsRemovalOfAnAnsweredAskTakesTheStoreLockOrder",
	// The seed's comments, with their author's person node and each comment's authored_by
	// tuple, which its one batch write states as a seeded database's own writer, the session
	// nodes of the followers its ask batch copies, and its issues', asks' and messages' nodes,
	// each written in the batch that copies their rows, in its project, before any row of it.
	"cmd/dispatch-seed/write_core.go#writeIssues":         "cmd/dispatch-seed#TestTheSeedsObjectNodeWritesTakeTheStoreLockOrder",
	"cmd/dispatch-seed/write_items.go#writeComments":      "cmd/dispatch-seed#TestTheSeedsCommentWriteTakesTheStoreLockOrder",
	"cmd/dispatch-seed/write_items.go#writeFollowerNodes": "cmd/dispatch-seed#TestTheSeedsFollowerWriteTakesTheStoreLockOrder",
	"cmd/dispatch-seed/write_items.go#writeItemNodes":     "cmd/dispatch-seed#TestTheSeedsObjectNodeWritesTakeTheStoreLockOrder",
	// The backfill command's five batches: the blocks, the block references and the comments
	// written before the store existed, the role and page nodes of the roles the census holds to
	// one (addressedRoles), and the session nodes of the sessions it holds to one
	// (recordedSessions). The comments are written through the backfill's comment write
	// (graphcensus.CommentWrite.Queue), which the block-index tick queues too, for the comments
	// the events after its build's cursor name whose rows name no node, after its cursor row and
	// its marking statement's document_block_versions rows (docs' test holds it to that).
	"graphcensus/backfill.go#writeBlockBatch":    "graphcensus#TestTheBlockBatchTakesEveryOwnerRowBeforeAnyFenceRow",
	"graphcensus/backfill.go#writeBlockRefBatch": "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	"graphcensus/backfill.go#Queue":              "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	"graphcensus/backfill.go#writeRoleBatch":     "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	"graphcensus/backfill.go#writeSessionBatch":  "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	// The backfill's passes over the project of every node: the nodes of the issues, documents,
	// asks and messages that their marker does not mark, in WriteUncaused and Caused over each run
	// of the batch's nodes of one cause (a row's own create event, or the sentinel), after the
	// owner locks a document's removal takes (lockObjectOwners) and under them, and the project of
	// the comment and block nodes written before a node named one (graphstore.FillScopes), the
	// block batch under the revision fence (lockBlockDocuments). An issue's batch then states,
	// under those locks, the child_of claim of the parent its row names through WriteArms, which
	// appends at the sentinel, outside every repeat key, and so waits on no row, then names each
	// node on its row, which graphcensus' TestTheObjectBatchTakesTheStoreLockOrder proves too.
	"graphcensus/projects_backfill.go#writeObjectBatch": "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	"graphcensus/projects_backfill.go#writeScopeBatch":  "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	// The fill itself appends what it states through Write; writeScopeBatch is its one caller, and
	// its driver holds the fill's steps.
	"graphstore/scope.go#FillScopes": "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	// The block-index tick's sweep of the documents a build before this one removed, which states
	// each orphaned node gone as this build's removal does: the issue's row, then the node's row,
	// then the state tuple, under the removal's own event.
	"graphcensus/orphans.go#StateOrphanedGone": "graphcensus#TestTheOrphanSweepTakesTheStoreLockOrder",
	// The backfill's pass over the versions an older build stored with no node
	// (dispatch://INFRA-1253): the owner rows of the batch's documents first, then the version
	// rows read again under them, then the version nodes and their facts, in one graphstore.Write
	// per batch (graphcensus' TestTheVersionBatchTakesTheStoreLockOrder).
	"graphcensus/census_versions.go#writeVersionBatch": "graphcensus#TestTheVersionBatchTakesTheStoreLockOrder",
	// The same nodes written by the block-index pass that drains a mark, for the versions of the
	// documents it covers that hold none (docs' indexRenderedVersion: the reconciliation's drain on
	// either mark, and a request's version write that drains this build's mark first, leaving out
	// its own version and writing none where more than missingVersionNodesBound are missing): under
	// the owner lock its caller took first, which every writer of a version node takes, the version
	// rows read in the pass's read phase (PlanMissingVersionNodes), then, queued after the nodes of
	// the pass, the version nodes and their facts in one graphstore.QueueWrite at the sentinel, the
	// same read and the same write as the batch above.
	"graphcensus/census_versions.go#Queue": "graphcensus#TestTheVersionBatchTakesTheStoreLockOrder",
	// The backfill's pass over the waits a process of the release before wrote with no node
	// (dispatch://INFRA-1253): the issue rows the batch's links stand under first, then the links
	// read again under them, then their needs tuples through Write, then the rows naming the issue's
	// node (graphcensus' TestTheIssueLinkBatchTakesTheStoreLockOrder).
	"graphcensus/census_issues.go#writeIssueLinkBatch": "graphcensus#TestTheIssueLinkBatchTakesTheStoreLockOrder",
	// The backfill's pass over the comments' anchor blocks (T17b of dispatch://INFRA-1253's plan):
	// the owner rows the batch's comments stand under first, then the comments' rows and their
	// nodes' anchored_in read again under them, then the tuples that level the two through Write
	// at the sentinel (graphcensus' TestTheAnchorBatchTakesTheStoreLockOrder).
	"graphcensus/census_anchors.go#writeAnchorBatch": "graphcensus#TestTheAnchorBatchTakesTheStoreLockOrder",
	// The backfill's pass over the references a document makes: the owner rows the batch's
	// documents stand under first, then the refs rows and their documents' nodes' mentions read
	// again under them, then the mentions tuples each row owes through Write at the sentinel
	// (graphcensus' TestTheReferenceBatchTakesTheStoreLockOrder).
	"graphcensus/census_refs.go#writeReferenceBatch": "graphcensus#TestTheReferenceBatchTakesTheStoreLockOrder",
	// A recorded result (T11 of this branch's plan, R12): the record route's transaction holds the
	// rows of the recording issue and of the check's issue (lockIssueRows), appends
	// result.recorded, writes the check_results row, and then the result's node, the recorder's
	// nodes and every tuple in one Write caused by that event (writeRecordedResult); a retraction
	// takes the result's check_results row `for update`, appends result.retracted under the check's
	// issue row, writes the check_result_retractions row, and then appends the result's retracted
	// value through Write under that event (stateResultRetracted). api's
	// TestEveryApiStoreWriterTakesTheStoreLockOrder holds both to those steps. The backfill's passes
	// over the results and the retractions an earlier release wrote take the rows of the batch's
	// check issues first, then read the rows again under them, then write the nodes and tuples, or
	// the retracted values, in one Write at the sentinel (graphcensus'
	// TestTheResultBatchTakesTheStoreLockOrder and TestTheResultRetractionBatchTakesTheStoreLockOrder).
	"api/results.go#writeRecordedResult":                       "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/results_retract.go#stateResultRetracted":              "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"graphcensus/census_results.go#writeResultBatch":           "graphcensus#TestTheResultBatchTakesTheStoreLockOrder",
	"graphcensus/census_results.go#writeResultRetractionBatch": "graphcensus#TestTheResultRetractionBatchTakesTheStoreLockOrder",
	// The backfill's passes over the components a process of the release before wrote with no
	// node (dispatch://INFRA-1253 R9): the architecture source rows of the batch's projects first,
	// in ascending (project, id), then the rows read again under them, then the component nodes and
	// their part_of, or the depends_on of a component whose node stands, in one Write at the
	// sentinel, then the rows naming the node; and over the issues' components, the issue rows
	// first, then the rows read again under them, then their affects through Write, then the rows
	// naming the issue's node (graphcensus' TestTheComponentBatchesTakeTheStoreLockOrder).
	"graphcensus/census_components.go#writeComponentBatch":            "graphcensus#TestTheComponentBatchesTakeTheStoreLockOrder",
	"graphcensus/census_components.go#writeComponentDependsBatch":     "graphcensus#TestTheComponentBatchesTakeTheStoreLockOrder",
	"graphcensus/census_components.go#writeIssueComponentMemberBatch": "graphcensus#TestTheComponentBatchesTakeTheStoreLockOrder",
	// A component's node and its relations as an import and a source's removal state them
	// (dispatch://INFRA-1253 R9): the import's one Write of every component node of the source,
	// each component it drops stated gone, and the part_of and depends_on that moved, at the
	// sentinel, after the row locks of every source of the project and before the components and
	// component_depends rows it replaces (architecture's TestTheImportTakesTheStoreLockOrder); and
	// a source's removal's Write of each of its components gone with every relation it held
	// retracted, after the source's row and before the delete whose cascade takes the rows (api's
	// TestEveryApiStoreWriterTakesTheStoreLockOrder).
	"architecture/importer.go#RetireComponents": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"architecture/importer.go#stateComponents":  "architecture#TestTheImportTakesTheStoreLockOrder",
	// The erase, the one writer that deletes from the store, through EraseNode: the tuples on both
	// sides of the node, then the node's row. It takes the owner locks it writes under before that
	// row, and its projection rows after it.
	"grapherase/erase.go#apply": "grapherase#TestTheEraseTakesTheStoreLockOrder",
	// The erase's end of the versions it deletes: each one's node stated gone, in the erase's
	// transaction, under the owner rows the erase takes first (dispatch://INFRA-1253; grapherase's
	// TestTheEraseOfABlockAVersionHeldEndsThatVersionsNode).
	"grapherase/erase.go#endErasedVersions": "grapherase#TestTheEraseOfABlockAVersionHeldEndsThatVersionsNode",
	// The erase's rewrite of the text values of the blocks holding the erased block, through
	// RedactValues, in the erase's transaction and under the owner rows it takes first, beside its
	// rewrite of those blocks' document_block_texts rows (dispatch://INFRA-1253; grapherase's
	// TestTheEraseTakesTheStoreLockOrder).
	"grapherase/redaction.go#redactBlocks": "grapherase#TestTheEraseTakesTheStoreLockOrder",
	// This package's own writers that run another of them. Authored and SessionRecorded are
	// arms of one statement, and Reached is SessionRecorded for an attempt that was sent; each
	// runs its queued form (QueueAuthored, QueueSessionRecorded, QueueReached) as a batch of its
	// own. Write, WriteUncaused and Caused run QueueWrite, QueueNodes and QueueCaused the same
	// way, and TestEveryStoreWriteTakesOneLockOrder proves the order on them.
	"graphstore/people.go#Authored":             "graphstore#TestAuthoredAndReachedTakeTheStoreLockOrder",
	"graphstore/people.go#QueueAuthored":        "graphstore#TestAuthoredAndReachedTakeTheStoreLockOrder",
	"graphstore/people.go#QueueReached":         "graphstore#TestAuthoredAndReachedTakeTheStoreLockOrder",
	"graphstore/people.go#QueueSessionRecorded": "graphstore#TestAuthoredAndReachedTakeTheStoreLockOrder",
	"graphstore/people.go#Reached":              "graphstore#TestAuthoredAndReachedTakeTheStoreLockOrder",
	"graphstore/people.go#SessionRecorded":      "graphstore#TestAuthoredAndReachedTakeTheStoreLockOrder",
	"graphstore/write.go#Caused":                "graphstore#TestEveryStoreWriteTakesOneLockOrder",
	"graphstore/write.go#Write":                 "graphstore#TestEveryStoreWriteTakesOneLockOrder",
	"graphstore/write.go#WriteUncaused":         "graphstore#TestEveryStoreWriteTakesOneLockOrder",
	// The one writer of an ask's answer state: asks.Settled.Queue states the answer on the ask's
	// node, appended under the event it is given or held for the caller's, and then writes the
	// row; the catch-up of a follow another build recorded states it on the node. Each site that
	// appends what a transition held under the event it appends after it, through QueueCaused:
	// the answer, resolve and decline routes' transition, an issue's close and reopen, the
	// approval carry's answer, and a settlement's retractions and restorations (appendEvents,
	// above). The settlement states the copies among a document's ask blocks as claims once its
	// version is indexed, and beside them the readability of each ask whose block it read, as
	// that ask node's `invalid` value (G1), and the backfill of the boot step's first phase states
	// the asks and the followers written before this release, each batch under its owners' locks
	// first. The decisions step's catch-up drives Settle, under the ask's owner lock and then its
	// row.
	"api/ask_transitions.go#closeAskTx":                "api#TestEveryAskTransitionAndKeepTakesTheStoreLockOrder",
	"api/issue_ask_lifecycle.go#recordWithdrawnAsksTx": "api#TestEveryAskTransitionAndKeepTakesTheStoreLockOrder",
	"api/issue_ask_lifecycle.go#recordRestoredAsksTx":  "api#TestEveryAskTransitionAndKeepTakesTheStoreLockOrder",
	"asks/followers.go#CatchUpFollower":                "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
	"asks/settle.go#Queue":                             "docs#TestTheCatchUpTakesTheAsksOwnerRowBeforeItsRow",
	"docs/approval_carry.go#answerApprovalAsk":         "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
	"docs/ask_blocks.go#claimCopies":                   "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
	"docs/ask_blocks.go#stateInvalidReasons":           "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
	"graphcensus/backfill.go#writeAskBatch":            "graphcensus#TestTheAskBatchesTakeEveryOwnerRowBeforeTheirRows",
	"graphcensus/backfill.go#writeAskFollowerBatch":    "graphcensus#TestTheAskBatchesTakeEveryOwnerRowBeforeTheirRows",
	// Retarget moves one pointer through Write, after the row of the node it moves it out of.
	"graphstore/reasoning.go#Retarget": "graphstore#TestEveryStoreWriteTakesOneLockOrder",
	// AddLensKind adds one kind to the lens catalogue through Write: the kind's constraint and
	// lens_kind nodes in ascending (kind, id) and the constrained_by tuple between them, at the
	// sentinel, under a transaction lock on the id and no owner row, since a catalogue node
	// stands under no issue. Its order is the order TestEveryStoreWriteTakesOneLockOrder proves of
	// Write; `envoy-dispatch lens-kind-add` is its one caller.
	"graphstore/lens_catalogue.go#AddLensKind": "graphstore#TestEveryStoreWriteTakesOneLockOrder",
	// The publisher's record of a batch, which splices the store's arms into its statement, and
	// its record of a fallback pass, which splices in what each published copy delivered.
	"outbox/mentions.go#recordFallbackCopies": "outbox#TestAFallbackCopyThePassPublishedIsADelivery",
	"outbox/publisher.go#recordBatch":         "outbox#TestRecordBatchTakesAPrefixOfTheStoreLockOrder",
	// The block index: its nodes inside the revision fence, queued on the write phase of the
	// writer that runs it (BlockIndexing.Queue), with the typed-node pass's own nodes and their
	// authors' (refs.PlanBlocks' typed facts), and the person and session nodes of the authors of
	// each block whose node records them in the same node write, whose tuples and cause wait for
	// the event that recorded the version (the sites of QueueCaused above), driven inside the tick
	// and the settlement that run it. Above the deferral bound the typed-node pass runs on its own
	// (refs.DecisionIndexing.Queue), under the document's owner lock its caller holds, its tuples
	// and cause waiting for the same event.
	"refs/blocks.go#Queue":         "docs#TestEveryDocsStoreWriterTakesTheStoreLockOrder",
	"refs/decision_nodes.go#Queue": "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
	// rebuild-refs' reconcile of one source: a document's owner row first (lockDocumentOwner),
	// then its refs rows, then the mentions tuples of the rows the reconcile inserted and deleted,
	// through Caused at the sentinel, since no event records the rebuild (refs'
	// TestARebuildOfADocumentsReferencesTakesTheStoreLockOrder).
	"refs/rebuild.go#replaceInTx": "refs#TestARebuildOfADocumentsReferencesTakesTheStoreLockOrder",
}

// TestEveryStoreWriteCallSiteIsListed reads the module's source, every Go file but the tests,
// and finds each function that names one of storeWriteFunctions, called or passed on as a value,
// and each function outside this package whose string literals hold a statement writing
// graph_nodes or graph_tuples (rawStoreWrite). It fails on a site storeWriteCallSites does not
// list, naming the site and the call, so a new writer of the store cannot appear without its lock
// steps being proven, and on a listed site the source no longer holds, so the list never names a
// writer that is gone. It fails on every statement outside this package that writes the store,
// naming the site and the statement: this package is the one place that writes those tables, so
// such a statement moves behind a writer here. It parses the source rather than matching its
// text, so a call written over two lines, a comment that names a writer or spells a statement,
// and a method of another type that shares a writer's name are each read for what they are.
//
// It then reads the driver storeWriteCallSites names beside each site, and fails on a site that
// names none, and on one whose driver names a test no _test.go file of its package declares, read
// from those files as `go test` builds them with no build tag (requireALockStepDriver): either is
// a listed site whose lock steps no test of c-lock-order's run drives.
func TestEveryStoreWriteCallSiteIsListed(t *testing.T) {
	found, raw := map[string][]string{}, map[string][]string{}
	parsed := 0
	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != moduleRoot && skippedDirectory(path, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// A file that names the store names it by its import path or is the store's own
		// package, and both spell the word; a file that writes the store's tables itself spells
		// one of their names, in whatever case its statement is written in. The rest of the
		// module is never parsed.
		lowered := bytes.ToLower(source)
		if !bytes.Contains(source, []byte("graphstore")) &&
			!bytes.Contains(lowered, []byte("graph_nodes")) && !bytes.Contains(lowered, []byte("graph_tuples")) {
			return nil
		}
		relative, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		files := token.NewFileSet()
		file, err := parser.ParseFile(files, relative, source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		parsed++
		for site, calls := range storeWriteSitesIn(relative, files, file) {
			found[site] = append(found[site], calls...)
		}
		for site, statements := range rawStoreWritesIn(relative, files, file) {
			raw[site] = append(raw[site], statements...)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read the module's source under %s: %v", moduleRoot, err)
	}
	if parsed == 0 || len(found) == 0 {
		t.Fatalf("read %d files naming graphstore or its tables and found %d store writers: the walk read nothing worth checking", parsed, len(found))
	}
	t.Logf("read %d files naming graphstore or its tables under %s and found %d store writer sites", parsed, moduleRoot, len(found))

	sites := make([]string, 0, len(found))
	for site := range found {
		sites = append(sites, site)
	}
	slices.Sort(sites)
	for _, site := range sites {
		t.Logf("store writer %s (%s), its lock steps driven by %s", site, strings.Join(found[site], ", "), storeWriteCallSites[site])
		if _, listed := storeWriteCallSites[site]; !listed {
			t.Errorf("%s writes the store (%s) and is not in storeWriteCallSites: a new writer of graph_nodes and graph_tuples takes the lock order written on api/inbox_read.go's markInboxReadThreads, so a test of its own package drives its steps and then it is listed here beside that test",
				site, strings.Join(found[site], ", "))
		}
	}
	tests := map[string]map[string]bool{}
	for _, site := range slices.Sorted(maps.Keys(storeWriteCallSites)) {
		if _, ok := found[site]; !ok {
			t.Errorf("storeWriteCallSites lists %s, and no file of the module holds a call of a store writer or a statement writing the store there any more: take it off the list, since the proofs of the writers rest on it naming the ones there are", site)
		}
		requireALockStepDriver(t, site, storeWriteCallSites[site], tests)
	}
	for _, site := range slices.Sorted(maps.Keys(raw)) {
		t.Errorf("%s writes the store with a statement of its own (%s): graphstore is the one place that writes graph_nodes and graph_tuples, so the statement moves behind one of its writers (storeWriteFunctions) and the site calls that writer",
			site, strings.Join(raw[site], ", "))
	}
}

// lockStepDriver is a driver as storeWriteCallSites names one: a package's directory, a '#', and
// the name of a test.
var lockStepDriver = regexp.MustCompile(`^([a-z0-9][a-z0-9_./-]*)#(Test\w*)$`)

// requireALockStepDriver fails unless driver, the test storeWriteCallSites names beside site, is a
// test the package it names declares in a _test.go file `go test` builds with no build tag, which
// is how c-lock-order's run builds it. A site with no driver, or with one that names a test no such
// file declares, is a site whose lock steps the run drives no test of. tests holds each package's
// tests by the directory a driver names, read once.
func requireALockStepDriver(t *testing.T, site, driver string, tests map[string]map[string]bool) {
	t.Helper()
	match := lockStepDriver.FindStringSubmatch(driver)
	if match == nil {
		t.Errorf("storeWriteCallSites names %q beside %s, which is no test that drives its lock steps: name one as \"<package dir>#<TestName>\", the package's directory named as a site's file is (from internal/dispatch where it lives under it), and a test of that package that runs %s and holds each of its steps in another transaction to read where it waits",
			driver, site, site)
		return
	}
	directory, name := match[1], match[2]
	declared, read := tests[directory]
	if !read {
		declared = testsDeclaredIn(t, directory)
		tests[directory] = declared
	}
	if !declared[name] {
		// The directory as the module names it, which is how a reader finds the package.
		shown, err := filepath.Rel(moduleRoot, driverDirectory(directory))
		if err != nil {
			shown = driverDirectory(directory)
		}
		t.Errorf("storeWriteCallSites names %s as the test that drives the lock steps of %s, and no _test.go file of %s that `go test` builds declares func %s(*testing.T): c-lock-order's run drives no test of %s until one does",
			driver, site, filepath.ToSlash(shown), name, site)
	}
}

// testsDeclaredIn is every test the package in directory declares, by name, read from the
// _test.go files `go test` builds for it with no build tag (build.Default), so a test that only a
// tagged build compiles is none. A directory that is not there declares none.
func testsDeclaredIn(t *testing.T, directory string) map[string]bool {
	t.Helper()
	path := driverDirectory(directory)
	declared := map[string]bool{}
	entries, err := os.ReadDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return declared
	}
	if err != nil {
		t.Fatalf("read the test files of %s: %v", path, err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		built, err := build.Default.MatchFile(path, entry.Name())
		if err != nil {
			t.Fatalf("read the build constraints of %s: %v", filepath.Join(path, entry.Name()), err)
		}
		if !built {
			continue
		}
		file, err := parser.ParseFile(files, filepath.Join(path, entry.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Join(path, entry.Name()), err)
		}
		for _, declaration := range file.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && isTestFunction(function) {
				declared[function.Name.Name] = true
			}
		}
	}
	return declared
}

// isTestFunction is whether function is one `go test` runs as a test: a function, not a method,
// whose one parameter is a *testing.T and which answers nothing.
func isTestFunction(function *ast.FuncDecl) bool {
	if function.Recv != nil || function.Type.Results != nil {
		return false
	}
	parameters := function.Type.Params.List
	if len(parameters) != 1 || len(parameters[0].Names) > 1 {
		return false
	}
	pointer, ok := parameters[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	named, ok := pointer.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	qualifier, ok := named.X.(*ast.Ident)
	return ok && qualifier.Name == "testing" && named.Sel.Name == "T"
}

// driverDirectory is the directory a driver names, read as siteKey writes a site's file: under
// internal/dispatch where such a directory is, and from the module otherwise.
func driverDirectory(directory string) string {
	under := filepath.Join(moduleRoot, "internal", "dispatch", filepath.FromSlash(directory))
	if info, err := os.Stat(under); err == nil && info.IsDir() {
		return under
	}
	return filepath.Join(moduleRoot, filepath.FromSlash(directory))
}

// skippedDirectory is a directory the walk does not enter: one Go itself ignores (testdata, a
// name starting with a dot or an underscore), a vendored or installed tree, and another module.
func skippedDirectory(path, name string) bool {
	if name == "testdata" || name == "vendor" || name == "node_modules" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

// storeWriteSitesIn is every site one file names a store writer at, each with the calls it makes
// as "graphstore.<writer> at <file>:<line>". In the store's own package the writers are named
// bare; anywhere else through the name the file imports graphstore under.
func storeWriteSitesIn(relative string, files *token.FileSet, file *ast.File) map[string][]string {
	bare := filepath.ToSlash(filepath.Dir(relative)) == graphstoreDirectory && file.Name.Name == "graphstore"
	qualifier := ""
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != graphstoreImportPath {
			continue
		}
		switch {
		case spec.Name == nil:
			qualifier = "graphstore"
		case spec.Name.Name == ".":
			bare = true
		case spec.Name.Name != "_":
			qualifier = spec.Name.Name
		}
	}
	if !bare && qualifier == "" {
		return nil
	}

	sites := map[string][]string{}
	for _, declaration := range file.Decls {
		site := siteKey(relative, "package-level declaration")
		var body ast.Node = declaration
		if decl, ok := declaration.(*ast.FuncDecl); ok {
			site = siteKey(relative, decl.Name.Name)
			if decl.Body == nil {
				continue
			}
			body = decl.Body
		}
		record := func(name *ast.Ident) {
			line := files.Position(name.Pos()).Line
			sites[site] = append(sites[site], "graphstore."+name.Name+" at "+relative+":"+strconv.Itoa(line))
		}
		var visit func(node ast.Node) bool
		visit = func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.SelectorExpr:
				if x, ok := node.X.(*ast.Ident); ok && qualifier != "" && x.Name == qualifier &&
					slices.Contains(storeWriteFunctions, node.Sel.Name) {
					record(node.Sel)
					return false
				}
				// The name after a dot is a field or a method of something else, never the
				// store's own function, so only the expression before it is read on.
				ast.Inspect(node.X, visit)
				return false
			case *ast.Ident:
				// Bare, a writer is named only in the store's own package or in a file that
				// dot-imports it.
				if bare && slices.Contains(storeWriteFunctions, node.Name) {
					record(node)
				}
			}
			return true
		}
		ast.Inspect(body, visit)
	}
	return sites
}

// rawStoreWritesIn is every site one file writes the store at with a statement of its own, each
// with the statements it holds as "<statement> at <file>:<line>", read from the file's string
// literals alone, so a statement a comment spells is none. The store's own package writes its
// tables that way by design, through the writers storeWriteFunctions names, and graphstoretest
// seeds what those writers refuse (graphstoretestDirectory), so the files of those two packages
// alone are not read here.
func rawStoreWritesIn(relative string, files *token.FileSet, file *ast.File) map[string][]string {
	if directory := filepath.ToSlash(filepath.Dir(relative)); directory == graphstoreDirectory || directory == graphstoretestDirectory {
		return nil
	}
	sites := map[string][]string{}
	for _, declaration := range file.Decls {
		site := siteKey(relative, "package-level declaration")
		if decl, ok := declaration.(*ast.FuncDecl); ok {
			site = siteKey(relative, decl.Name.Name)
		}
		ast.Inspect(declaration, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			for _, match := range rawStoreWrite.FindAllStringIndex(text, -1) {
				// A raw string holds its lines as the source does, so a statement inside one is
				// on the literal's line plus the line breaks before it.
				line := files.Position(literal.Pos()).Line
				if strings.HasPrefix(literal.Value, "`") {
					line += strings.Count(text[:match[0]], "\n")
				}
				statement := strings.ToLower(strings.Join(strings.Fields(text[match[0]:match[1]]), " "))
				sites[site] = append(sites[site], statement+" at "+relative+":"+strconv.Itoa(line))
			}
			return true
		})
	}
	return sites
}

// siteKey names a site as docs' register of the tree's passes names one (passKey): the file,
// from internal/dispatch where it lives under it and from the module otherwise, and the function.
func siteKey(relative, function string) string {
	return strings.TrimPrefix(relative, "internal/dispatch/") + "#" + function
}
