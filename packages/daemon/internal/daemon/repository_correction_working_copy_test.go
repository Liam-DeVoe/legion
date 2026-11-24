package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/runtime/tmux"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

const (
	// workingCopyIssue is the issue these cases correct, its implementer the worker whose task is
	// held across the correction, and workingCopyOwnersLine the line of the owner's text of its
	// task's block that reads like the daemon's working copy line.
	workingCopyIssue      = "LEGION-208"
	workingCopyOwnersLine = " Working copy: branch legion/LEGION-208, directory /owners/own/LEGION-208."
)

// workingCopyRig is one issue's implementer under the parts main wires together for a worker's
// task: the record over a database of its own with the issue's plan, the workflow engine that
// composes the task under the directory the runtime gives the record's repository
// (engineWorkspaceDir), the outbox runner that runs the start it writes, and a claim supervisor
// whose Deps.WorkingCopy is the daemon's own answer (workflowRuntime.workingCopy), as daemon.go
// wires it.
type workingCopyRig struct {
	t        *testing.T
	ctx      context.Context
	pool     *store.Pool
	records  record.Store
	engine   *workflow.Engine
	sup      *supervisor
	runner   *outbox
	conn     *fake.Conn
	token    claim.Token
	runtime  string
	stateDir string
}

func newWorkingCopyRig(t *testing.T, runtimeName string) *workingCopyRig {
	t.Helper()
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	issue := record.Issue{Key: workingCopyIssue, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Project: "LEGION", Tree: workingCopyIssue, Title: "Workflow", Phase: phase.Implementing, Generation: 1, Status: "in_progress"}
	if err := store.Transact(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		version := record.PlanVersion{Issue: issue.Key, Generation: 1, Version: 2, ArtifactID: "plan-artifact", Source: record.PlanSourceOwner,
			GivenBy: mustClaimToken(t, issue.Key, claim.RoleArchitect), GivenAt: time.Now().UTC().Add(-time.Hour)}
		task := record.PlanTask{Issue: issue.Key, Generation: 1, Task: "a", PlanVersion: 2, BlockText: "Build the widget.\n" + workingCopyOwnersLine,
			State: record.TaskRunning, Place: record.TaskPlace{Kind: record.PlaceStep, Position: 1}}
		steps := []record.PlanStep{
			{Issue: issue.Key, Generation: 1, Task: "a", Position: 1, Role: "implementer", Type: "implementation"},
			{Issue: issue.Key, Generation: 1, Task: "a", Position: 2, Role: "reviewer", Type: "review"},
		}
		if err := records.PutIssue(ctx, tx, issue); err != nil {
			return err
		}
		return records.PutPlan(ctx, tx, version, []record.PlanTask{task}, steps)
	}); err != nil {
		t.Fatalf("seed %s with its plan: %v", issue.Key, err)
	}
	stateDir := t.TempDir()
	engine := workflow.New(records, workflow.Config{Project: "LEGION", Repositories: testRepositories, Steps: config.DesignGateRootIssues.Steps(),
		WorkspaceDir: engineWorkspaceDir(config.Config{Runtime: config.Runtime{Name: runtimeName}, StateDir: stateDir})}, quietLogger())
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	sup.deps.WorkingCopy = (&workflowRuntime{pool: pool, records: records, engine: engine}).workingCopy
	token := mustClaimToken(t, issue.Key, claim.RoleImplementer)
	conn := fake.NewConn()
	sup.deps.Conns.(*fake.Conns).Register(token, conn)
	return &workingCopyRig{t: t, ctx: ctx, pool: pool, records: records, engine: engine, sup: sup, conn: conn, token: token,
		runtime: runtimeName, stateDir: stateDir,
		runner: &outbox{log: quietLogger(), pool: pool, dispatchProject: "LEGION", records: records, supervisor: sup,
			tokens: outboxTokens{}, project: "legion", stateDir: t.TempDir(),
			provision: func(context.Context, workspace.Request) (workspace.Workspace, error) {
				return workspace.Workspace{Dir: t.TempDir(), Bookmark: claim.IssueBranch(workingCopyIssue)}, nil
			},
		},
	}
}

// line is the working copy line of a task of the issue on repo, under the directory the rig's
// runtime places that repository's workspace of the issue in: the pod's under Kubernetes, the
// pane's under tmux.
func (r *workingCopyRig) line(repo string) string {
	r.t.Helper()
	place := func(repo ghrepo.Repository, issue string) (string, error) {
		return tmux.WorkspaceDir(r.stateDir, repo, issue)
	}
	if r.runtime == "kubernetes" {
		place = sandbox.WorkspaceDir
	}
	dir, err := place(ghrepo.MustParse(repo), workingCopyIssue)
	if err != nil {
		r.t.Fatal(err)
	}
	return " Working copy: branch legion/" + workingCopyIssue + ", directory " + dir + "."
}

// start is the implementer's start the engine writes for the promotion's resume of its step
// (workflow.Engine.StartPromotedWorker), composed from the record as it stands now: the task the
// phases row keeps (record.PhaseRow.EntryTask) and the outbox row carries.
func (r *workingCopyRig) start() entryStartRow {
	r.t.Helper()
	issue := readIssue(r.t, r.pool, workingCopyIssue)
	if err := startPromoted(r.ctx, r.pool, r.engine, *issue, claim.RoleImplementer); err != nil {
		r.t.Fatalf("write the implementer's start: %v", err)
	}
	rows := roleStartRows(r.t, r.pool, workingCopyIssue, claim.RoleImplementer)
	if len(rows) == 0 {
		r.t.Fatal("the promotion wrote no start of the implementer")
	}
	start := rows[len(rows)-1]
	if task := start.request.Task; !strings.Contains(task, r.line("acme/widgets")) || !strings.Contains(task, workingCopyOwnersLine) {
		r.t.Fatalf("the implementer's start carries %q, want it composed naming %q and the owner's line %q: the case's own premise",
			task, r.line("acme/widgets"), workingCopyOwnersLine)
	}
	return start
}

// run runs the start row as the supervise lane runs it, and waits for every send it started.
func (r *workingCopyRig) run(start entryStartRow) {
	r.t.Helper()
	if err := r.runner.execute(r.ctx, start.row); err != nil {
		r.t.Fatalf("run the implementer's start row %d: %v", start.row.ID, err)
	}
	r.wait()
}

func (r *workingCopyRig) machine() *supervise.Machine {
	r.t.Helper()
	machine, found := r.sup.Machine(r.token)
	if !found {
		r.t.Fatalf("%s has no implementer claim", workingCopyIssue)
	}
	return machine
}

func (r *workingCopyRig) wait() {
	if machine, found := r.sup.Machine(r.token); found {
		machine.Wait()
	}
}

// corrects moves the issue's record onto zeta/tools, as the workflow's own pass does when Dispatch
// names another repository for the issue.
func (r *workingCopyRig) corrects() {
	r.t.Helper()
	correctRecord(r.t, r.pool, r.records, workingCopyIssue, "zeta/tools", "release/v9", time.Now())
}

// wantSentAfterTheCorrection fails unless sent, a prompt of the implementer's task, and the task the
// claim holds name the workspace of zeta/tools in their working copy line, and never the
// workspace of acme/widgets, with the owner's line inside the owner's text as the owner wrote it.
func (r *workingCopyRig) wantSentAfterTheCorrection(sent string) {
	r.t.Helper()
	left, moved := r.line("acme/widgets"), r.line("zeta/tools")
	held := r.machine().Claim().Pending
	if held == nil {
		r.t.Fatal("the implementer holds no task after its send")
	}
	for _, read := range []struct{ what, text string }{
		{"the prompt sent after the correction", sent},
		{"the task the claim holds after it", held.Task},
	} {
		if strings.Contains(read.text, left) || !strings.Contains(read.text, moved) {
			r.t.Fatalf("%s names the working copy %q, the workspace of acme/widgets the issue left; want %q:\n%s", read.what, left, moved, read.text)
		}
		if !strings.Contains(read.text, "\n"+workingCopyOwnersLine+"\n") {
			r.t.Fatalf("%s holds no owner's line %q inside the owner's text as the owner wrote it:\n%s", read.what, workingCopyOwnersLine, read.text)
		}
	}
}

// A worker's task is composed from the record when its start is written, and it is held until a
// send: in the phases row (record.PhaseRow.EntryTask), in the outbox row the lane has not run, in
// the claim's pending delivery behind a launch, a busy agent or a send the transport lost. A
// correction of the issue's repository can land in between, and the directory of the repository
// the issue left is the one a kept volume still holds and no role may work in. So every send of a
// worker's task writes its working copy line from the record as the send reads it (supervise's
// startSend, Deps.WorkingCopy, the daemon's workingCopy), under the directory the runtime gives the
// record's repository: a start written before the correction and sent after it names the workspace
// of the repository the issue moved to, and so does a task sent before the correction and again
// after it. The task's owner's text holds a line that reads like the daemon's working copy line,
// inside its fence, and every send leaves it as the owner wrote it. Both runtimes' workspaces
// follow the record.
func TestRepositoryCorrectionNamesTheNewWorkspaceInEveryWorkerTaskSentAfterIt(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"kubernetes", "tmux"} {
		t.Run(name, func(t *testing.T) {
			t.Run("a start written before the correction and sent after it", func(t *testing.T) {
				r := newWorkingCopyRig(t, name)
				start := r.start()
				r.corrects()

				r.run(start)
				readyClaimOf(t, r.sup, r.token, claim.RoleImplementer)
				r.wait()
				prompts := r.conn.Prompts()
				if len(prompts) != 1 {
					t.Fatalf("the implementer was prompted %+v, want its one start", prompts)
				}
				r.wantSentAfterTheCorrection(prompts[0].Message)
			})

			t.Run("a task sent before the correction and again after it", func(t *testing.T) {
				r := newWorkingCopyRig(t, name)
				machine, _, err := r.sup.Create(r.ctx, supervise.Claim{Token: r.token, Project: "legion", Tree: workingCopyIssue,
					Issue: workingCopyIssue, Role: claim.RoleImplementer, State: supervise.StateQueued}, "")
				if err != nil {
					t.Fatalf("create the implementer claim: %v", err)
				}
				if err := machine.Handle(r.ctx, supervise.RequestSpawn{Claim: r.token}); err != nil {
					t.Fatalf("spawn the implementer: %v", err)
				}
				readyClaimOf(t, r.sup, r.token, claim.RoleImplementer)
				r.conn.FailPrompt(errors.New("the stream to the shim closed"))
				r.run(r.start())
				prompts := r.conn.Prompts()
				if len(prompts) != 1 || !strings.Contains(prompts[0].Message, r.line("acme/widgets")) {
					t.Fatalf("the implementer was prompted %+v before the correction, want its start naming %q: the case's own premise",
						prompts, r.line("acme/widgets"))
				}

				r.corrects()
				r.conn.FailPrompt(nil)
				if err := machine.Handle(r.ctx, supervise.StreamHello{Claim: r.token, Generation: machine.Claim().Generation}); err != nil {
					t.Fatalf("the shim's hello again: %v", err)
				}
				r.wait()
				prompts = r.conn.Prompts()
				if len(prompts) != 2 {
					t.Fatalf("the implementer was prompted %+v, want its start sent again once the transport is back", prompts)
				}
				r.wantSentAfterTheCorrection(prompts[1].Message)
			})
		})
	}
}
