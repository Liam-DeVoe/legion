package workflow

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// tryInTx runs write in a transaction of its own, with the commit scope applyInTx gives, and
// answers its error: a write that fails is rolled back, and one that does not is committed.
func tryInTx(t *testing.T, pool *pgxpool.Pool, write func(ctx context.Context, tx pgx.Tx) error) error {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(t.Context())
	ctx, committed := intake.WithCommitHooks(t.Context())
	if err := write(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	committed()
	return nil
}

// forgeTaskPlace puts task a of key's plan at generation at place, running, through the store
// alone: no write of the workflow puts it there while its issue stands elsewhere.
func forgeTaskPlace(t *testing.T, pool *pgxpool.Pool, key string, generation uint64, place record.TaskPlace) {
	t.Helper()
	seedRecord(t, pool, func(tx pgx.Tx) error {
		return record.NewStore().MoveTask(t.Context(), tx, key, generation, "a", place, record.TaskRunning)
	})
}

// The workflow writes an issue in one place, putIssue, which checks the phase it writes against
// where the issue's task stands: a scan of the package's own source finds each call of a store's
// PutIssue, and every one is in putIssue. The scan counts the files it parsed and the calls it
// found, so a scan that read nothing cannot pass for one that found no other writer.
func TestTheWorkflowWritesAnIssueOnlyThroughTheCheckOfItsTasksPlace(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list the package's files: %v", err)
	}
	set := token.NewFileSet()
	parsed, calls := 0, 0
	var outside []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "PutIssue" {
					calls++
					if fn.Name.Name != "putIssue" {
						outside = append(outside, set.Position(call.Pos()).String()+" in "+fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if parsed == 0 || calls == 0 {
		t.Fatalf("the scan parsed %d files and found %d calls of PutIssue, want the package's files and putIssue's own call", parsed, calls)
	}
	if len(outside) > 0 {
		t.Fatalf("the package writes an issue outside putIssue, which checks its phase against its task's place:\n%s", strings.Join(outside, "\n"))
	}
}

// Each writer of an issue's phase that names the phase itself, rather than moving the task
// (moveTask), is refused where that phase disagrees with where the task stands, and the refusal
// names both: the phase the write stands in (a hold by the phase it is from) and the task's state,
// place and the phase that place stands in. Each fixture puts the task elsewhere through the store
// alone, since no write of the workflow can leave the two apart. The writers that move the task
// before they write (transition and its hold before the retro, closeMerged, leave and
// beginLinger) or that refuse a task elsewhere themselves (startHeldRetro) write the phase the
// place gives, so no fixture makes them disagree.
func TestAWriteOfAPhaseThatDisagreesWithItsTasksPlaceIsRefused(t *testing.T) {
	reviewing := record.TaskPlace{Kind: record.PlaceStep, Position: 3}
	implementing := record.TaskPlace{Kind: record.PlaceStep, Position: 1}
	for _, tc := range []struct {
		name       string
		at         phase.Phase
		heldFrom   phase.Phase
		generation uint64
		place      record.TaskPlace
		write      func(ctx context.Context, tx pgx.Tx, engine *Engine, issue record.Issue) error
		refusal    string
	}{
		{
			name: "the hold of a failed claim (claimFailed)", at: phase.Implementing, generation: 1, place: reviewing,
			write: func(ctx context.Context, tx pgx.Tx, engine *Engine, issue record.Issue) error {
				_, err := engine.claimFailed(ctx, tx, intake.ClaimFailed{Issue: issue.Key, Role: claim.RoleImplementer})
				return err
			},
			refusal: "put LEGION-208 at generation 1: it stands in held from implementing, and its task a is running at {Kind:step Position:3}, which stands in reviewing",
		},
		{
			name: "the retry of a hold (retryPhase)", at: phase.Held, heldFrom: phase.Implementing, generation: 1, place: reviewing,
			write: func(ctx context.Context, tx pgx.Tx, engine *Engine, issue record.Issue) error {
				_, err := engine.retryPhase(ctx, tx, issue, phase.Implementing, "the architect retried the phase", false)
				return err
			},
			refusal: "put LEGION-208 at generation 1: it stands in implementing, and its task a is running at {Kind:step Position:3}, which stands in reviewing",
		},
		{
			name: "the hold of a stop (stopTask)", at: phase.Implementing, generation: 1, place: reviewing,
			write: func(ctx context.Context, tx pgx.Tx, engine *Engine, issue record.Issue) error {
				w, err := engine.walk(ctx, tx, issue)
				if err != nil {
					return err
				}
				return engine.stopTask(ctx, tx, issue, w, stopsBy, stopsBy, stepStopped)
			},
			refusal: "put LEGION-208 at generation 1: it stands in held from implementing, and its task a is stopped at {Kind:step Position:3}, which stands in reviewing",
		},
		{
			name: "a root's wait on a person (enterAwaiting)", at: phase.Admitted, generation: 1, place: implementing,
			write: func(ctx context.Context, tx pgx.Tx, engine *Engine, issue record.Issue) error {
				return engine.enterAwaiting(ctx, tx, issue, record.Awaiting{Ask: "ask-1"}, "the architect asked a person")
			},
			refusal: "put LEGION-208 at generation 1: it stands in awaiting_person, and its task a is running at {Kind:step Position:1}, which stands in implementing",
		},
		{
			name: "an adopted member parked at its next run (moveIntoTree)", at: phase.Admitted, generation: 2, place: implementing,
			write: func(ctx context.Context, tx pgx.Tx, engine *Engine, issue record.Issue) error {
				issue.Status = "backlog"
				into := record.Issue{Key: "LEGION-300", Tree: "LEGION-300", Project: "LEGION"}
				return engine.moveIntoTree(ctx, tx, issue, into, intake.AdoptedChild{})
			},
			refusal: "put LEGION-208 at generation 2: it stands in done, and its task a is running at {Kind:step Position:1}, which stands in implementing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			root := stepRoot(t, pool, tc.at)
			if tc.heldFrom != "" {
				holdFrom(t, pool, root.Key, tc.heldFrom)
			}
			seedStepsPlan(t, pool, root.Key, tc.generation, "implementer", "tester", "reviewer")
			forgeTaskPlace(t, pool, root.Key, tc.generation, tc.place)
			before := issueOf(t, pool, engine, root.Key)

			err := tryInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
				return tc.write(ctx, tx, engine, before)
			})

			if err == nil || !strings.Contains(err.Error(), tc.refusal) {
				t.Fatalf("the write = %v, want it refused: %s", err, tc.refusal)
			}
			if after := issueOf(t, pool, engine, root.Key); after.Phase != before.Phase || after.Generation != before.Generation {
				t.Fatalf("%s stands in %s at generation %d after the refused write, want %s at %d as before", root.Key, after.Phase, after.Generation, before.Phase, before.Generation)
			}
		})
	}
}

// A root that leaves the workflow while the operator runs a tree under its key ends its task as
// every other leave does (leaveState): left where the status is not done, at the place it stood,
// so the phase done its record takes is the phase its task stands in.
func TestARootLeavingUnderTheOperatorsTreeEndsItsTask(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208",
		Project: "LEGION", Title: "root", Phase: phase.Admitted, Generation: 1, Status: "todo", Rank: "U", LastDispatchSeq: 1}
	seedIssue(t, pool, root)
	seedOperatorTree(t, pool, root.Tree)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")

	if err := tryInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		issue, err := engine.store.Issue(ctx, tx, root.Key)
		if err != nil {
			return err
		}
		return engine.leave(ctx, tx, *issue, "backlog")
	}); err != nil {
		t.Fatalf("the leave under the operator's tree: %v", err)
	}

	if issue := issueOf(t, pool, engine, root.Key); issue.Phase != phase.Done {
		t.Fatalf("%s stands in %s after its leave, want done", root.Key, issue.Phase)
	}
	if task := taskOf(t, pool, engine, root.Key); task.State != record.TaskLeft || task.Place != (record.TaskPlace{}) {
		t.Fatalf("task a is %s at %+v after its issue left, want left at no act", task.State, task.Place)
	}
}
