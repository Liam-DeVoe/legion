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

// storeWriteFunctions are the calls a write of graph_nodes and graph_tuples from outside this
// package goes through, the same ten docs' register of the tree's passes scans for
// (storeWriterCall): Write, WriteUncaused and Caused in a caller's transaction, WriteArms and
// WriteArmsUncaused spliced into a caller's statement, Authored, Reached and SessionRecorded,
// which run those arms for the writers that record an author, a reached session and a session
// an ask names, EraseNode, the erase's delete of a node and its tuples, and FillScopes, the
// backfill's fill of the project of a node stored with none. Each is a function of the package,
// never a method, so a call of one is named by the package and found here wherever it is made.
// A write that holds a statement of its own instead is found by rawStoreWrite, and none outside
// this package may hold one.
var storeWriteFunctions = []string{"Write", "WriteUncaused", "WriteArms", "WriteArmsUncaused", "Caused",
	"Authored", "Reached", "SessionRecorded", "EraseNode", "FillScopes"}

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
	// transaction, and each site that moves that node onto the event it appends after the
	// follow, and appends the node's base attributes under it, through Caused: the follower
	// route, an ask's create, an approval request renewed, and a settlement's asks (asks'
	// TestAFollowTakesTheStoreLockOrder proves both steps on Follow alone, and each caller's test
	// proves them inside its caller). The comment path and a delivery's reply give the follow
	// their own event instead, which it states in its own statement.
	"asks/followers.go#Follow":               "asks#TestAFollowTakesTheStoreLockOrder",
	"api/ask_followers.go#changeAskFollower": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/asks.go#createAskFor":               "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/reviews.go#renewApprovalAsk":        "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"docs/ask_blocks.go#causeFollows":        "docs#TestEveryDocsStoreWriterTakesTheStoreLockOrder",
	// The shaped ties of each ask block a settlement opened, when it writes no version: appended
	// under the ask.opened that records the ask, through Caused, after the follow's node above.
	"docs/ask_blocks.go#causeOpenedAskTies": "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
	// The block index's cause: each site that appends what a version's index pass stated under
	// the event that recorded the version, through Caused (refs' TestCausedTakesTheStoreLockOrder
	// proves the step on its own): an upload, a version write's event, an issue's spec, the
	// block-index tick, a settlement's version and the seed's documents.
	"api/artifacts.go#commitArtifactVersionEvent":   "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/artifacts.go#storeArtifact":                "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/issue_create.go#createIssue":               "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"cmd/dispatch-seed/write_docs.go#writeDocument": "cmd/dispatch-seed#TestTheSeedsDocumentWriteTakesTheStoreLockOrder",
	"docs/block_index.go#indexOwedDocument":         "docs#TestEveryDocsStoreWriterTakesTheStoreLockOrder",
	"docs/settlement.go#commitVersion":              "docs#TestEveryDocsStoreWriterTakesTheStoreLockOrder",
	// The comment path: the comment's node before the comments row that names it, then its
	// tuples and its cause under the event appended after both, and who wrote it.
	"api/comment_create.go#caused":           "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/comment_create.go#createCommentFor": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/comment_create.go#writeNode":        "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	// A delivery's reply and the settle of a delivery attempt, which records the session the
	// attempt reached, and the one record both replies share of what their answer makes of the
	// attempt they answer: an attempt the answer flips to sent is one the listener took, and
	// its session is owed a node (recordAnsweredDelivery, which the reply's driver runs last).
	"api/comment_delivery.go#completeCommentDelivery": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/comment_delivery.go#recordAnsweredDelivery":  "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/comment_delivery.go#replyComment":            "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	"api/message_delivery.go#completeMessageDelivery": "api#TestEveryApiStoreWriterTakesTheStoreLockOrder",
	// The node of every issue, document, ask and message a request creates, in the project or
	// between the parties it belongs to: written before its row, and caused by the event the
	// create appends after it. createIssue, storeArtifact and createAskFor are listed above for
	// their block index and follow, and their drivers hold the node step too; these are the
	// others. Their order - the owner row their path already takes, the node before the row, the
	// event before the tuples - is the order of the write they share (WriteUncaused, then Caused),
	// and api's TestEveryObjectNodeWriterTakesTheStoreLockOrder drives each of them through it.
	"api/broadcasts.go#writeBroadcast":           "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"api/message_delivery.go#insertSessionReply": "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"api/messages.go#createStoredMessage":        "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"api/messages.go#writeMessageNode":           "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"api/reviews.go#openApprovalAsk":             "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	"docs/ask_blocks.go#createAskBlock":          "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	// The removal of an untouched document states its node gone, before its row goes, caused by
	// the artifact.removed event it appends after the row (Write of the node the document's
	// create would have written, for a document an earlier build made with none, then
	// WriteUncaused, then Caused).
	"api/artifact_removal.go#removeArtifact": "api#TestEveryObjectNodeWriterTakesTheStoreLockOrder",
	// A document's retirement (dispatch://INFRA-1150 R11), in the same order under the document's
	// owner row: its artifact node stated gone, then the artifacts row that retires it, then
	// document.retired and the node's facts under it.
	"docs/retire.go#RetireDocument":   "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
	"docs/retire.go#writeRetiredNode": "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
	// A person's Keep of an agent's decision (dispatch://INFRA-1150 R2): under the issue's owner
	// row, the person's node, then the decision_keeps row that projects the Keep, then
	// decision.kept, and the kept_by tuple and the kept_text_sum value under it, through Caused.
	"api/decisions.go#keepDecisionNode": "api#TestEveryAskTransitionAndKeepTakesTheStoreLockOrder",
	// The decisions step's restore and conversion of an issue (dispatch://INFRA-1150 R6 to R9),
	// under the issue's owner row and its asks' rows: each document's version and its event, the
	// index pass's facts under that event through Caused, then the moves, the restored asks'
	// relations and the carried Keeps, with the person node a Keep names, under the spec's event.
	"docs/decision_restore.go#writeVersion": "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
	"docs/decision_restore.go#writeFacts":   "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
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
	// (recordedSessions).
	"graphcensus/backfill.go#writeBlockBatch":    "graphcensus#TestTheBlockBatchTakesEveryOwnerRowBeforeAnyFenceRow",
	"graphcensus/backfill.go#writeBlockRefBatch": "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	"graphcensus/backfill.go#writeCommentBatch":  "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	"graphcensus/backfill.go#writeRoleBatch":     "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	"graphcensus/backfill.go#writeSessionBatch":  "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	// The backfill's passes over the project of every node: the nodes of the issues, documents,
	// asks and messages that their marker does not mark, in WriteUncaused and Caused over each run
	// of the batch's nodes of one cause (a row's own create event, or the sentinel), after the
	// owner rows a document's removal takes (lockObjectOwners) and under them, and the project of
	// the comment and block nodes written before a node named one (graphstore.FillScopes), the
	// block batch under the revision fence (lockBlockDocuments).
	"graphcensus/projects_backfill.go#writeObjectBatch": "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	"graphcensus/projects_backfill.go#writeScopeBatch":  "graphcensus#TestEveryBackfillBatchTakesTheStoreLockOrder",
	// The erase, the one writer that deletes from the store, through EraseNode: the tuples on both
	// sides of the node, then the node's row. It takes the owner rows it writes under before that
	// row, and its projection rows after it.
	"grapherase/erase.go#apply": "grapherase#TestTheEraseTakesTheStoreLockOrder",
	// This package's own writers that run another of them: Authored and SessionRecorded are
	// arms of one statement, and Reached is SessionRecorded for an attempt that was sent.
	"graphstore/people.go#Authored":        "graphstore#TestAuthoredAndReachedTakeTheStoreLockOrder",
	"graphstore/people.go#Reached":         "graphstore#TestAuthoredAndReachedTakeTheStoreLockOrder",
	"graphstore/people.go#SessionRecorded": "graphstore#TestAuthoredAndReachedTakeTheStoreLockOrder",
	// The one writer of an ask's answer state (dispatch://INFRA-1150): asks.Settle states the answer
	// on the ask's node, appended at once under the event it is given or held for the caller's, and
	// then writes the row; the catch-up of a follow another build recorded states it on the node.
	// Each site that appends what a transition held under the event it appends after it, through
	// Caused: the answer, resolve and decline routes' transition, an issue's close and reopen, the
	// approval carry's answer, and a settlement's retractions and restorations. The settlement
	// states the copies among a document's ask blocks as claims once its version is indexed, and
	// beside them the readability of each ask whose block it read, as that ask node's `invalid`
	// value (G1), and the backfill of the boot step's first phase states the asks and the
	// followers written before this release, each batch with its rows locked first. The decisions
	// step's catch-up drives Settle, under the ask's owner row and then its row.
	"api/ask_transitions.go#closeAskTx":                "api#TestEveryAskTransitionAndKeepTakesTheStoreLockOrder",
	"api/issue_ask_lifecycle.go#recordWithdrawnAsksTx": "api#TestEveryAskTransitionAndKeepTakesTheStoreLockOrder",
	"api/issue_ask_lifecycle.go#restoreIssueAsksTx":    "api#TestEveryAskTransitionAndKeepTakesTheStoreLockOrder",
	"asks/followers.go#CatchUpFollower":                "docs#TestTheDecisionsStepsWritersTakeTheStoreLockOrder",
	"asks/settle.go#Settle":                            "docs#TestTheCatchUpTakesTheAsksOwnerRowBeforeItsRow",
	"docs/approval_carry.go#answerApprovalAsk":         "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
	"docs/ask_blocks.go#causeSettledAnswers":           "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
	"docs/ask_blocks.go#claimCopies":                   "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
	"docs/ask_blocks.go#stateInvalidReasons":           "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
	"graphcensus/backfill.go#writeAskBatch":            "graphcensus#TestTheAskBatchesTakeEveryOwnerRowBeforeTheirRows",
	"graphcensus/backfill.go#writeAskFollowerBatch":    "graphcensus#TestTheAskBatchesTakeEveryOwnerRowBeforeTheirRows",
	// The publisher's record of a batch, which splices the store's arms into its statement, and
	// its record of a fallback pass, which splices in what each published copy delivered.
	"outbox/mentions.go#recordFallbackCopies": "outbox#TestAFallbackCopyThePassPublishedIsADelivery",
	"outbox/publisher.go#recordBatch":         "outbox#TestRecordBatchTakesAPrefixOfTheStoreLockOrder",
	// The block index: its nodes inside the revision fence, whose tuples and cause wait for the
	// event that recorded the version (the sites of Caused above), driven inside the tick and the
	// settlement that run it.
	"refs/blocks.go#IndexBlocks": "docs#TestEveryDocsStoreWriterTakesTheStoreLockOrder",
	// The typed-node pass (dispatch://INFRA-1150): the decision and reading nodes of a version and
	// their authors' nodes, in the transaction that accepts it, under the document's owner row its
	// caller holds, after the block index's own nodes where that runs and on its own above the
	// deferral bound, its tuples and cause waiting for the same event (the sites of Caused above).
	"refs/decision_nodes.go#IndexDecisionNodes": "docs#TestEverySettlementWriterTakesTheStoreLockOrder",
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
// tables that way by design, through the writers storeWriteFunctions names, so its files are
// not read here.
func rawStoreWritesIn(relative string, files *token.FileSet, file *ast.File) map[string][]string {
	if strings.HasPrefix(relative, graphstoreDirectory+"/") {
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
