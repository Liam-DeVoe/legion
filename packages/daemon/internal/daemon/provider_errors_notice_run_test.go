package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/admit"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// The Dispatch project and the issue whose implementer the cases below drive.
const (
	errorRunProject = "CAPTURE"
	errorRunIssue   = "CAPTURE-7"
)

// After six turns in a row of one worker end in a provider error, the project's controller is
// told once, with the issue, the role and the error. By default an operator launches the
// controller (config's `controller: operator`), so no controller may be registered at the sixth
// turn. The notice is then owed, not dropped: the controller registered later is told at the
// worker's next turn that fails the same way, and told once — neither a later turn of the same
// run nor the publish of the notice tells it again.
func TestAControllerRegisteredAfterTheSixthProviderErrorTurnIsToldOnce(t *testing.T) {
	r := newErrorRunRig(t)
	for range 6 {
		r.errorTurn()
	}
	if notices := r.notices(); len(notices) != 0 {
		t.Fatalf("with no controller registered the outbox holds %d provider-errors notices, want none: %+v", len(notices), notices)
	}

	r.registerController()
	r.errorTurn()
	r.wantOneNotice("after a controller registered and the seventh turn in a row ended in a provider error,", 7)

	r.errorTurn()
	r.wantOneNotice("after the eighth turn,", 7)
	r.publish()
	r.errorTurn()
	r.errorTurn()
	if notices := r.notices(); len(notices) != 0 {
		t.Fatalf("after the notice was published and two more turns of the same run failed, the outbox holds %d provider-errors notices, want none: the controller is told of a run once: %+v",
			len(notices), notices)
	}
}

// A notice whose write failed is not lost with it. The controller is registered from the start,
// and the transaction that would write the notice at the sixth turn fails, as a database that does
// not answer fails it. The worker's next turn that fails the same way writes it.
func TestAProviderErrorsNoticeWhoseWriteFailedIsWrittenAtTheNextTurn(t *testing.T) {
	r := newErrorRunRig(t)
	r.registerController()
	for range 5 {
		r.errorTurn()
	}
	for _, statement := range []string{
		`create function refuse_the_notice() returns trigger language plpgsql as $$
			begin raise exception 'the notice''s write did not commit'; end $$`,
		`create trigger refuse_the_notice before insert on outbox for each row
			when (new.kind = 'controller_notice') execute function refuse_the_notice()`,
	} {
		if _, err := r.pool.Exec(r.ctx, statement); err != nil {
			t.Fatalf("make the notice's write fail: %v", err)
		}
	}
	r.errorTurn()
	if notices := r.notices(); len(notices) != 0 {
		t.Fatalf("the outbox holds %d provider-errors notices while their write fails, want none: %+v", len(notices), notices)
	}
	for _, statement := range []string{"drop trigger refuse_the_notice on outbox", "drop function refuse_the_notice()"} {
		if _, err := r.pool.Exec(r.ctx, statement); err != nil {
			t.Fatalf("let the outbox take writes again: %v", err)
		}
	}

	r.errorTurn()
	r.wantOneNotice("after the sixth turn's notice failed to write and the seventh turn in a row ended in a provider error,", 7)
}

// The count starts again after a clean turn, and the controller can be told of the next run: a
// run of six that follows a clean turn is a run of its own, told once as the first was.
func TestARunOfProviderErrorsAfterACleanTurnIsToldAgain(t *testing.T) {
	r := newErrorRunRig(t)
	r.registerController()
	for range 6 {
		r.errorTurn()
	}
	r.wantOneNotice("after the sixth turn in a row ended in a provider error,", 6)
	r.publish()
	r.errorTurn()
	if notices := r.notices(); len(notices) != 0 {
		t.Fatalf("a seventh turn of the run that was told wrote %d more provider-errors notices, want none: %+v", len(notices), notices)
	}

	r.cleanTurn()
	r.give()
	for turn := range 6 {
		r.errorTurn()
		if turn < 5 {
			if notices := r.notices(); len(notices) != 0 {
				t.Fatalf("turn %d of the new run wrote %d provider-errors notices, want none before the sixth: %+v", turn+1, len(notices), notices)
			}
		}
	}
	r.wantOneNotice("after a clean turn and six more turns in a row that ended in a provider error,", 6)
}

// A run whose notice finds the claim's earlier notice still unsent is not lost behind it. The
// first run is told, and its notice still waits in the outbox when a clean turn ends that run and
// six more turns fill the next. Once the first notice is published, the next turn that fails the
// same way tells the controller of the second run.
func TestARunWhoseNoticeFoundTheEarlierOneUnsentIsToldOnceThatOneIsPublished(t *testing.T) {
	r := newErrorRunRig(t)
	r.registerController()
	for range 6 {
		r.errorTurn()
	}
	r.wantOneNotice("after the sixth turn in a row ended in a provider error,", 6)

	r.cleanTurn()
	r.give()
	for range 6 {
		r.errorTurn()
	}
	r.wantOneNotice("while the first run's notice waits unsent and the second run reaches six,", 6)
	r.publish()
	r.errorTurn()
	r.wantOneNotice("after the first run's notice was published and the second run's seventh turn ended in a provider error,", 7)
}

// errorRunRig is one implementer whose turns end in a provider error, over the parts the daemon
// wires between such a turn and the controller's notice of a run of them: the implementer's
// machine on the daemon's own claim store, whose Deps.ProviderErrorRunReached is the workflow
// runtime's own (providerErrorRunReached, as daemon.go wires it), and admission over the record
// store, on a database of the rig's own. The machine's clock is held, so the ladder's next send
// goes when a case fires the wait the error armed.
type errorRunRig struct {
	t       *testing.T
	ctx     context.Context
	pool    *store.Pool
	st      *store.Store
	project string
	clock   *heldClock
	machine *supervise.Machine
	conn    *fake.Conn
	token   claim.Token
	// tasks counts the tasks the implementer has been given, so each has an id of its own.
	tasks int
	// offered counts the machine's calls of providerErrorRunReached, and ended the transactions
	// of the workflow runtime's pool that have ended. Each call writes on a goroutine of its own,
	// so a case reads the outbox only once every call it made has ended (settled): a read before
	// that could miss a notice still on its way, and a check that no second notice was written
	// would then pass for that reason alone.
	offered, ended atomic.Int64
}

func newErrorRunRig(t *testing.T) *errorRunRig {
	t.Helper()
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	project, err := claim.ProjectToken(errorRunProject)
	if err != nil {
		t.Fatal(err)
	}
	records := record.NewStore()
	putOutboxIssue(t, pool, records, record.Issue{Key: errorRunIssue, Repo: "acme/widgets", DefaultBranch: "main",
		GitHubRepo: "acme/widgets", Project: errorRunProject, Tree: errorRunIssue, Title: "Implement it",
		Phase: phase.Implementing, Generation: 1, Status: "in_progress", Rank: "A", HandedOver: true})
	st, err := store.Open(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(st.Close)
	if _, err := st.OpenTreeLifecycle(ctx, project, errorRunIssue, treelifecycle.AuthorityWorkflow); err != nil {
		t.Fatalf("admit the tree of %s: %v", errorRunIssue, err)
	}
	r := &errorRunRig{t: t, ctx: ctx, pool: pool, st: st, project: project, clock: &heldClock{}, conn: fake.NewConn()}

	// The workflow runtime writes on a pool of its own, which counts each transaction it ends.
	poolConfig, err := pgxpool.ParseConfig(pool.Config().ConnString())
	if err != nil {
		t.Fatalf("read the database's address: %v", err)
	}
	poolConfig.AfterRelease = func(*pgx.Conn) bool {
		r.ended.Add(1)
		return true
	}
	written, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("open the workflow runtime's pool: %v", err)
	}
	t.Cleanup(written.Close)
	log := quietLogger()
	engine := workflow.New(records, workflow.Config{Project: errorRunProject, Repositories: testRepositories,
		Steps: config.DesignGateRootIssues.Steps()}, log)
	admission := admit.New(records, engine, 1, errorRunProject, log)
	w := &workflowRuntime{pool: store.NewPool(written), records: records, admission: admission, engine: engine,
		handlers: []intake.Handler{engine, admission}, dispatchProject: errorRunProject, log: log}

	sup, _ := newOutboxSupervisor(t, project, t.TempDir())
	sup.deps.Store = st
	sup.deps.Clock = r.clock
	sup.deps.ProviderErrorRunReached = func(c supervise.Claim, reason string) {
		r.offered.Add(1)
		w.providerErrorRunReached(c, reason)
	}
	r.token, err = claim.NewToken(project, errorRunIssue, claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	sup.deps.Conns.(*fake.Conns).Register(r.token, r.conn)
	r.machine, _, err = sup.Create(ctx, supervise.Claim{Token: r.token, Project: project, Tree: errorRunIssue,
		Issue: errorRunIssue, Role: claim.RoleImplementer, State: supervise.StateQueued}, "")
	if err != nil {
		t.Fatalf("create the implementer: %v", err)
	}
	r.handle(supervise.RequestSpawn{Claim: r.token})
	generation := r.machine.Claim().Generation
	r.handle(supervise.StreamHello{Claim: r.token, Generation: generation},
		supervise.RequestRegister{Claim: r.token, Generation: generation, Session: "ses-implementer", SessionFile: "/tmp/implementer.jsonl"},
		supervise.RequestReady{Claim: r.token, Generation: generation, Session: "ses-implementer"})
	r.give()
	return r
}

// handle hands the implementer's machine each of events in turn, and waits out any send one starts.
func (r *errorRunRig) handle(events ...supervise.Event) {
	r.t.Helper()
	for _, ev := range events {
		if err := r.machine.Handle(r.ctx, ev); err != nil {
			r.t.Fatalf("handle %T: %v", ev, err)
		}
		r.machine.Wait()
	}
}

// give hands the implementer a task of its phase, and the agent starts its turn on it.
func (r *errorRunRig) give() {
	r.t.Helper()
	r.tasks++
	sent := len(r.conn.Prompts())
	r.handle(supervise.RequestDeliver{Claim: r.token, Task: "Implement the plan.", ID: fmt.Sprintf("outbox:%d", r.tasks),
		Phase: phase.Implementing, Generation: 1})
	if got := len(r.conn.Prompts()); got != sent+1 {
		r.t.Fatalf("the implementer was sent %d prompts for its task, want 1", got-sent)
	}
	r.handle(supervise.StreamTurnStart{Claim: r.token})
}

// errorTurn ends the turn the implementer is working in an overload the provider answered 529, as
// Oh My Pi reports it: the agent's end of turn, then the prompt's result. The ladder's wait for
// the next send is then fired, and the agent starts its turn on the task sent again.
func (r *errorRunRig) errorTurn() {
	r.t.Helper()
	pending := r.machine.Claim().Pending
	if pending == nil {
		r.t.Fatal("the implementer holds no task, so no turn of it can end in a provider error")
	}
	run := r.machine.Claim().ProviderErrorRun
	r.handle(supervise.StreamTurnEnd{Claim: r.token},
		supervise.StreamPromptResult{Claim: r.token, DeliveryID: pending.ID, AgentInvoked: true,
			Error: "overloaded_error", HTTPStatus: 529, Provider: "anthropic", Model: "claude-opus-5"})
	if got := r.machine.Claim().ProviderErrorRun; got != run+1 {
		r.t.Fatalf("the run of provider errors is %d after one more such turn, want %d", got, run+1)
	}
	pending = r.machine.Claim().Pending
	if pending == nil {
		r.t.Fatal("the turn's provider error took the task away, want it kept on the ladder")
	}
	sent := len(r.conn.Prompts())
	wait := ladderWait(pending.ProviderErrorRetries)
	if fired := r.clock.fire(wait); fired != 1 {
		r.t.Fatalf("fired %d timers armed for the ladder's wait of %s, want the one the provider error armed", fired, wait)
	}
	r.machine.Wait()
	if got := len(r.conn.Prompts()); got != sent+1 {
		r.t.Fatalf("the ladder's wait sent %d prompts, want the task once", got-sent)
	}
	r.handle(supervise.StreamTurnStart{Claim: r.token})
}

// cleanTurn ends the turn the implementer is working with no error: the task is done, and the run
// of provider errors goes back to zero.
func (r *errorRunRig) cleanTurn() {
	r.t.Helper()
	pending := r.machine.Claim().Pending
	if pending == nil {
		r.t.Fatal("the implementer holds no task to end a turn on")
	}
	r.handle(supervise.StreamTurnEnd{Claim: r.token},
		supervise.StreamPromptResult{Claim: r.token, DeliveryID: pending.ID, AgentInvoked: true})
	if got := r.machine.Claim().ProviderErrorRun; got != 0 {
		r.t.Fatalf("the run of provider errors is %d after a turn with no error, want 0", got)
	}
}

// ladderWait is the wait before the attempt-th send of a task a provider error took back, as the
// daemon's ladder arms it: 30 s, doubling to at most 10 minutes.
func ladderWait(attempt int) time.Duration {
	return min(30*time.Second<<min(max(attempt-1, 0), 5), 10*time.Minute)
}

// registerController records a session as the project's controller, as its registration does
// (api's registerController).
func (r *errorRunRig) registerController() {
	r.t.Helper()
	generation, err := r.st.MintController(r.ctx, r.project, []byte("capability"))
	if err != nil {
		r.t.Fatalf("mint the controller capability: %v", err)
	}
	if ok, err := r.st.RegisterController(r.ctx, r.project, generation, "ses-controller", []byte("secret"), time.Now(), 0); err != nil || !ok {
		r.t.Fatalf("register the controller = %t, %v", ok, err)
	}
}

// settled waits until every call the machine made of providerErrorRunReached has ended its
// transaction, so what it wrote, if anything, can be read.
func (r *errorRunRig) settled() {
	r.t.Helper()
	testwait.Eventually(r.t, "every notice the machine offered to end its transaction", func() bool {
		return r.ended.Load() >= r.offered.Load()
	})
}

// errorRunNotice is one provider-errors notice the outbox holds for the controller.
type errorRunNotice struct {
	issue  string
	role   claim.Role
	reason string
}

// notices are the provider-errors notices the outbox holds for the controller, read once every
// notice the machine offered has ended its transaction.
func (r *errorRunRig) notices() []errorRunNotice {
	r.t.Helper()
	r.settled()
	rows, err := r.pool.Query(r.ctx, "select id, kind, issue, payload from outbox where kind = $1 order by id",
		string(record.OutboxKindControllerNotice))
	if err != nil {
		r.t.Fatalf("read the controller notices: %v", err)
	}
	defer rows.Close()
	var notices []errorRunNotice
	for rows.Next() {
		var row record.OutboxRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.Issue, &row.Payload); err != nil {
			r.t.Fatalf("scan a controller notice: %v", err)
		}
		payload, err := record.DecodeOutboxPayload(row)
		if err != nil {
			r.t.Fatalf("decode controller notice %d: %v", row.ID, err)
		}
		if notice, ok := payload.(record.ControllerNotice); ok && notice.Kind == record.ProviderErrorsNotice {
			notices = append(notices, errorRunNotice{issue: row.Issue, role: notice.Role, reason: notice.Reason})
		}
	}
	if err := rows.Err(); err != nil {
		r.t.Fatalf("read the controller notices: %v", err)
	}
	return notices
}

// publish finishes every controller notice the outbox holds, as its lane does once the publish
// is made (FinishOutbox deletes the row).
func (r *errorRunRig) publish() {
	r.t.Helper()
	r.settled()
	if _, err := r.pool.Exec(r.ctx, "delete from outbox where kind = $1", string(record.OutboxKindControllerNotice)); err != nil {
		r.t.Fatalf("publish the controller notices: %v", err)
	}
}

// wantOneNotice fails the case unless the outbox holds exactly one provider-errors notice: for the
// implementer of the issue, naming the run of turns it was written at, the model and the error.
func (r *errorRunRig) wantOneNotice(when string, run int) {
	r.t.Helper()
	notices := r.notices()
	if len(notices) != 1 {
		r.t.Fatalf("%s the outbox holds %d provider-errors notices for the controller, want one: %+v", when, len(notices), notices)
	}
	notice := notices[0]
	if notice.issue != errorRunIssue || notice.role != claim.RoleImplementer {
		r.t.Errorf("the notice names the %s of %s, want the implementer of %s", notice.role, notice.issue, errorRunIssue)
	}
	for _, want := range []string{fmt.Sprintf("%d turns in a row", run), "claude-opus-5", "ended in a provider error (HTTP 529)"} {
		if !strings.Contains(notice.reason, want) {
			r.t.Errorf("the notice's reason is %q, want it to name %q", notice.reason, want)
		}
	}
}
