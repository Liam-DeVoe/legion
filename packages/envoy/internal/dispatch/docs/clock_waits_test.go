package docs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// wallClockWaits are the document service's waits on the wall clock, each by its file and the
// function it stands in, with why it is not on the service's clock (Deps.Clock). A wait on the wall
// clock is any call wallClockWait names: a wait of the time package's, a deadline the context
// package sets on the wall clock, a socket's own deadline, a real clock the package builds, and a
// duty loop whose ticks are the real clock's. Every other wait of the package is the service
// clock's, which a test holds (clocktest.Held) or moves.
var wallClockWaits = map[string]string{
	"awareness_relay.go:Service.coalesceAwareness":                    "a presence relay's coalescing window, which no row moves",
	"awareness_relay.go:awarenessRelay.query":                         "an awareness query's bound on its notification to Postgres (roomSyncSendTimeout), which only real time ends when the database stalls, and no row times",
	"awareness_relay.go:awarenessRelay.send":                          "a presence relay's bound on its notifications to Postgres (roomSyncSendTimeout), which only real time ends when the database stalls, and no row times",
	"block_index.go:Service.RunBlockIndexReconciliation":              "the block-index reconciliation's ticks (duty.RunLeasedAfterEachPass), which no row of this package runs",
	"compaction_sweep.go:Service.RunCompactionSweep":                  "the compaction sweep's hourly ticks (duty.RunLeased), which no row runs: each row makes the one tick it needs, at the due time it names (compactionSweepTick)",
	"conditional_edit_queue.go:Service.releaseConditionalEdit":        "a leaving edit's bound on its write of the queue's count to Postgres (conditionalEditReleaseTimeout), which only real time ends when the database stalls, and no row times",
	"decided_wording.go:Service.RestoreDecidedWordings":               "the bound on a boot restoration's rollback (decidedWordingRollbackTimeout), a round trip to Postgres on a context of its own, which no row times",
	"persistence.go:NewPgVersioned":                                   "the document store's clock, which bounds the releases of its session locks (duty.FreeSessionLock, duty.LeaseCall), round trips to Postgres: the store is built from the database alone, with no Deps to hand it the service's clock",
	"reference_excerpts.go:Service.RunReferenceExcerptReconciliation": "the excerpt reconciliation's gap between passes (duty.RunWoken handed no Clock, minGap 2 s), which its rows wait out on real time and none times: the rows whose service runs on a fake (for the retries' due times) would otherwise have to move it past every gap",
	"room_query.go:awaitStored":                                       "the room query's poll of what a peer stored, bounded by the query's own context",
	"room_query.go:roomQueries.send":                                  "a room answer's bound on its notifications to Postgres (roomSyncSendTimeout), which only real time ends when the database stalls, and no row times",
	"room_server.go:roomServer.closeWhenFree":                         "the room server's retry of a close behind a busy room, a backoff of 1 to 50 ms while the caller inside the room leaves (roomInUseFirstWait, roomInUseLongestWait), which no row times: on Deps.Clock every row whose service holds a fake would have to move it past each close a caller is inside",
	"room_sync.go:roomSyncLane.run":                                   "a room sync lane's wait for its catch-up's next retry (roomCatchUps.wakeIn), which is timed on the service's now, the func a test sets to make a retry due: the lane waits out on real time only the span that now leaves, and no row times it",
	"room_sync.go:roomSyncSender.drop":                                "the bound on closing a dropped connection to Postgres (pglisten.DefaultPingTimeout), so a half-open socket cannot hold the turn, which no row times",
	"schema_admission.go:Service.RunSchemaAdmission":                  "the schema admission's beat at the fleet's live window (processes.LiveWindow), which only the server runs: every row of this package makes the beat's pass itself (recheckSchemaAdmissions), and the twobuild rows wait the window out across real processes",
	"service.go:New":                                            "the service's own clock when Deps.Clock is nil, as it is in production",
	"service.go:Service.readCloseRowFacts":                      "a close's bound on its read of the pending-settlement row (closeRowFactsTimeout), a round trip to Postgres every other close of the room waits behind, which no row times",
	"service.go:Service.scheduleSettleAfterLocked":              "a room's settlement timer, which Shutdown stops and settles itself: rows read whether it is armed (isSettleTimerArmed) or wait for it to fire on a short real Settle, and none times it, so on Deps.Clock every row whose service holds a fake would have to move it past each settlement",
	"socket_admission.go:closeRefusedSocket":                    "a refused socket's close handshake, bounded by deadlines gorilla's socket reads on the wall clock (WriteControl, SetReadDeadline)",
	"stored_head.go:Service.recordPublishedLoss":                "the bound on judging a published write's text (publishedLossTimeout), reads of the other processes and of Postgres that only real time ends when they stall, and no row times",
	"task_progress.go:Service.runTaskProgressReconciliation":    "the task-progress reconciliation's retries after its boot pass (one second, doubling), which the row that holds an issue through the boot pass waits out on real time, asserting what a retry counts and not when it comes",
	"task_progress.go:Service.ReconcileTaskProgress":            "the task-progress reconciliation's backoff between the batches of one pass (taskProgressRetryBackoffStep), which the row that holds an issue through the boot pass waits out on real time, asserting what the pass leaves and not when",
	"testhooks_rooms.go:testHooks.awaitPersistenceHold":         "a test hook's hold, which only the test-hook route mounts",
	"websocket.go:servicePersistenceAdapter.StoreUpdateContext": "a browser append's linger for its batch's other updates (roomAppendLinger, 50 ms), which rows wait out on real time: on Deps.Clock every browser append of a service whose clock a test holds would wait for the test to move it",
}

// The calls wallClockWait names: the time package's waits, the context package's deadlines, a
// socket's deadlines (gorilla's WriteControl takes one too), and the duty loops that tick on the
// real clock, whose On forms take a clock instead.
var (
	timeWaits        = []string{"Sleep", "After", "Tick", "NewTimer", "NewTicker", "AfterFunc"}
	contextDeadlines = []string{"WithTimeout", "WithDeadline", "WithTimeoutCause", "WithDeadlineCause"}
	socketDeadlines  = []string{"SetDeadline", "SetReadDeadline", "SetWriteDeadline", "WriteControl"}
	realClockLoops   = []string{"RunLeased", "RunLeasedAfterEachPass", "RunJittered"}
)

// The document service waits on its own clock and nowhere else but where wallClockWaits says why:
// no function of the package's own source makes a wall-clock wait (wallClockWait) it does not
// list, and every one it lists still makes one. The settlement rows hold the service's clock
// (clocktest.Held) and read a drain's events by their order alone
// (TestShutdownsDrainWakesOnceWhenARoomsHeldAppendLands), so a wait the service took on the wall
// clock - a sleep after the drain's wake, a poll, a deadline on a context or a socket, a loop on
// duty's real ticker - would slip past them; here it fails by name.
func TestTheDocumentServiceWaitsOffItsClockOnlyWhereItSaysWhy(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string][]string{}
	fileSet := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			site := name + ":" + functionName(function)
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if wait, ok := wallClockWait(call); ok {
					found[site] = append(found[site], fileSet.Position(call.Pos()).String()+": "+wait)
				}
				return true
			})
		}
	}
	for _, site := range slices.Sorted(maps.Keys(found)) {
		if _, listed := wallClockWaits[site]; !listed {
			t.Errorf("%s waits on the wall clock (%s), which no row holds or moves: wait on the service's clock (s.clock), or list it in wallClockWaits with why it cannot",
				site, strings.Join(found[site], ", "))
		}
	}
	for _, site := range slices.Sorted(maps.Keys(wallClockWaits)) {
		if _, still := found[site]; !still {
			t.Errorf("wallClockWaits lists %s, which waits on the wall clock no more: take it out of the list", site)
		}
	}
}

// wallClockWait names call when it waits on the wall clock: a wait of the time package's
// (timeWaits), a deadline the context package sets (contextDeadlines), a socket's own deadline,
// whatever its receiver (socketDeadlines), a real clock the package builds (clock.Real), or a duty
// loop whose ticks are the real clock's (realClockLoops, and duty.RunWoken handed no WokenLoop
// literal that sets its Clock: a loop built elsewhere is read as one that sets none).
func wallClockWait(call *ast.CallExpr) (string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	name := selector.Sel.Name
	if slices.Contains(socketDeadlines, name) {
		return "a socket's " + name, true
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	switch {
	case pkg.Name == "time" && slices.Contains(timeWaits, name),
		pkg.Name == "context" && slices.Contains(contextDeadlines, name),
		pkg.Name == "clock" && name == "Real",
		pkg.Name == "duty" && slices.Contains(realClockLoops, name):
		return pkg.Name + "." + name, true
	case pkg.Name == "duty" && name == "RunWoken" && !setsClock(call):
		return "duty.RunWoken with no Clock", true
	}
	return "", false
}

// setsClock reports whether one of call's arguments is a composite literal that sets its Clock,
// as the duty.WokenLoop a RunWoken call is handed does when its loop runs on a clock of its own.
func setsClock(call *ast.CallExpr) bool {
	for _, argument := range call.Args {
		literal, ok := argument.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, element := range literal.Elts {
			field, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := field.Key.(*ast.Ident); ok && key.Name == "Clock" {
				return true
			}
		}
	}
	return false
}

// functionName is a declaration's name as wallClockWaits keys it: the receiver's type and the
// method's name, or the function's name.
func functionName(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return function.Name.Name
	}
	receiver := function.Recv.List[0].Type
	if star, ok := receiver.(*ast.StarExpr); ok {
		receiver = star.X
	}
	if generic, ok := receiver.(*ast.IndexExpr); ok {
		receiver = generic.X
	}
	if ident, ok := receiver.(*ast.Ident); ok {
		return ident.Name + "." + function.Name.Name
	}
	return function.Name.Name
}
