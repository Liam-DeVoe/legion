package workflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// An approval is a person's recorded decision, and one function decides whether it is still that
// person's word (dispatch://INFRA-3136/spec R2): SupersedingApproval, in superseding_approval.go.
// classify's approval rule (classify.DesignApprovalStands) must be exported, since classify is
// another package, so the compiler lets any sender call it and decide for itself, as the approval
// news did (the daemon's itemStands). Only a reading of the source can hold that no sender does:
// this test lists every call of that rule in the daemon's own packages and fails on any call
// outside the one predicate's file. Delete it when the rule can be unexported.
func TestOnlyTheOnePredicateAsksClassifysApprovalRule(t *testing.T) {
	const allowed = "workflow/superseding_approval.go"
	fset := token.NewFileSet()
	var outside []string
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "DesignApprovalStands" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "classify" && !strings.HasSuffix(filepath.ToSlash(path), allowed) {
				outside = append(outside, fset.Position(call.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("read the daemon's sources: %v", err)
	}
	if len(outside) > 0 {
		t.Fatalf("classify.DesignApprovalStands is called outside %s, so a sender decides an approval for itself: %s",
			allowed, strings.Join(outside, "; "))
	}
}
