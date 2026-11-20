// packages/envoy/cmd/agent-secrets/secret_gcp_runrecorded_test.go
//
// The order of runRecorded's statements, which the signal rows can hold only at a point each.
package main

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// secretGCPSource is secret_gcp.go as this test binary was built from it. It is embedded rather
// than read from the disk, so a copy of the file that `go test -overlay` puts in its place, as a
// mutation of it does, is the one this test reads, as the compiler does.
//
//go:embed secret_gcp.go
var secretGCPSource string

// TestGcloudRunRecordedWatchesAndHoldsTheLockAcrossTheWholeStart: runRecorded's first two
// statements arm its signal watch and defer its stop, and nothing else in it stops or arms one,
// so the watch runs from before gcloud starts until after what is left of gcloud's group is
// killed; its next two take runningGcloud and start gcloud, and gcloud's pid is recorded before
// that lock is given up, so a signal during the start waits for the record and kills that gcloud.
//
// TestGcloudSignInEndedByASignalKillsGcloudsChild sends a signal at the step right after the start
// and at the last kill, and each row fails where the watch is not armed at that point. This holds
// what a point cannot: a watch armed after cmd.Start returns but before that step, or the lock
// taken there, leaves a window only as wide as the statements between, which no signal sent from
// a test lands in on purpose, and which a loaded machine widens.
func TestGcloudRunRecordedWatchesAndHoldsTheLockAcrossTheWholeStart(t *testing.T) {
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "secret_gcp.go", secretGCPSource, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body []ast.Stmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "runRecorded" && fn.Body != nil {
			body = fn.Body.List
		}
	}
	if len(body) < 4 {
		t.Fatalf("secret_gcp.go holds no runRecorded of at least four statements (%d found): this test reads nothing", len(body))
	}
	if !armsTheWatch(body[0]) || !defersTheStop(body[1]) {
		t.Errorf("runRecorded's first two statements are not `stop := watchSignalsWhileGcloudRuns()` and `defer stop()`: a watch armed later leaves gcloud's start unwatched, and one stopped before the function returns leaves the last kill of gcloud's group unwatched")
	}
	for _, statement := range body[2:] {
		ast.Inspect(statement, func(node ast.Node) bool {
			if ident, ok := node.(*ast.Ident); ok && (ident.Name == "stop" || ident.Name == "watchSignalsWhileGcloudRuns") {
				t.Errorf("runRecorded names %s after its first two statements (%s): the watch is armed once and stopped only by the deferred call", ident.Name, files.Position(ident.Pos()))
			}
			return true
		})
	}
	if !callsOn(body[2], "runningGcloud", "Lock") || !startsTheCommand(body[3]) {
		t.Errorf("runRecorded's third and fourth statements are not `runningGcloud.Lock()` and `if err := cmd.Start(); …`: a signal that comes while gcloud starts finds runningGcloud free, kills no gcloud, and ends the command with gcloud's group running")
	}
	recorded, unlocked := -1, -1
	for i, statement := range body[4:] {
		if recorded < 0 && recordsThePid(statement) {
			recorded = i
		}
		if unlocked < 0 && callsOn(statement, "runningGcloud", "Unlock") {
			unlocked = i
		}
	}
	if recorded < 0 || unlocked < 0 || recorded > unlocked {
		t.Errorf("runRecorded does not record gcloud's pid (`runningGcloud.pids[pid] = true`, statement %d after the start) before it gives up runningGcloud (statement %d): a signal in between kills no gcloud", recorded, unlocked)
	}
}

// armsTheWatch: `stop := watchSignalsWhileGcloudRuns()`.
func armsTheWatch(statement ast.Stmt) bool {
	assign, ok := statement.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 || !isIdent(assign.Lhs[0], "stop") {
		return false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	return ok && isIdent(call.Fun, "watchSignalsWhileGcloudRuns") && len(call.Args) == 0
}

// defersTheStop: `defer stop()`.
func defersTheStop(statement ast.Stmt) bool {
	deferred, ok := statement.(*ast.DeferStmt)
	return ok && isIdent(deferred.Call.Fun, "stop") && len(deferred.Call.Args) == 0
}

// callsOn: `<receiver>.<method>()` as a statement of its own.
func callsOn(statement ast.Stmt, receiver, method string) bool {
	expr, ok := statement.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == method && isIdent(selector.X, receiver)
}

// startsTheCommand: an if statement whose init calls cmd.Start().
func startsTheCommand(statement ast.Stmt) bool {
	branch, ok := statement.(*ast.IfStmt)
	if !ok {
		return false
	}
	assign, ok := branch.Init.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) != 1 {
		return false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Start" && isIdent(selector.X, "cmd")
}

// recordsThePid: `runningGcloud.pids[…] = true`.
func recordsThePid(statement ast.Stmt) bool {
	assign, ok := statement.(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 || !isIdent(assign.Rhs[0], "true") {
		return false
	}
	index, ok := assign.Lhs[0].(*ast.IndexExpr)
	if !ok {
		return false
	}
	selector, ok := index.X.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "pids" && isIdent(selector.X, "runningGcloud")
}

func isIdent(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}
