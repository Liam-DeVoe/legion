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

// wallClockWaits are the document service's waits on the wall clock - a call of time.Sleep,
// time.After, time.Tick, time.NewTimer, time.NewTicker or time.AfterFunc - each by its file and the
// function it stands in, with why it is not on the service's clock (Deps.Clock). Every other wait
// of the package is the service clock's, which a test holds (clocktest.Held) or moves.
var wallClockWaits = map[string]string{
	"awareness_relay.go:Service.coalesceAwareness":              "a presence relay's coalescing window, which no row moves",
	"room_query.go:awaitStored":                                 "the room query's poll of what a peer stored, bounded by the query's own context",
	"room_server.go:roomServer.closeWhenFree":                   "the room server's retry of a close behind a busy room",
	"room_sync.go:roomSyncLane.run":                             "a room sync lane's retry wait after a failed pass",
	"schema_admission.go:Service.RunSchemaAdmission":            "the schema admission's beat at the fleet's live window",
	"service.go:Service.scheduleSettleAfterLocked":              "a room's settlement timer, which Shutdown stops and settles itself",
	"task_progress.go:Service.runTaskProgressReconciliation":    "the task-progress reconciliation's retry wait",
	"task_progress.go:Service.ReconcileTaskProgress":            "the task-progress reconciliation's backoff between batches",
	"testhooks_rooms.go:testHooks.awaitPersistenceHold":         "a test hook's hold, which only the test-hook route mounts",
	"websocket.go:servicePersistenceAdapter.StoreUpdateContext": "a browser append's linger for its batch's other updates",
}

// wallClockCalls are the time package's waits wallClockWaits judges.
var wallClockCalls = []string{"Sleep", "After", "Tick", "NewTimer", "NewTicker", "AfterFunc"}

// The document service waits on its own clock and nowhere else but where wallClockWaits says why:
// no function of the package's own source calls a wall-clock wait it does not list, and every one
// it lists still makes one. The settlement rows hold the service's clock (clocktest.Held) and read
// a drain's events by their order alone (TestShutdownsDrainWakesOnceWhenARoomsHeldAppendLands), so
// a wait the service took on the wall clock - a sleep after the drain's wake, a poll - would slip
// past them; here it fails by name.
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
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "time" && slices.Contains(wallClockCalls, selector.Sel.Name) {
					found[site] = append(found[site], fileSet.Position(call.Pos()).String()+": time."+selector.Sel.Name)
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
