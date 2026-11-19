package admit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// registerController records the project's controller the way the daemon does when
// `legion controller start` mints a capability and its session registers with it: through the
// store's own MintController and RegisterController, under the project token, never the Dispatch
// key.
func registerController(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer st.Close()
	token, err := claim.ProjectToken(testProject)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := st.MintController(ctx, token, []byte("capability"))
	if err != nil {
		t.Fatalf("mint the controller capability: %v", err)
	}
	if ok, err := st.RegisterController(ctx, token, generation, "ses-controller", []byte("secret"), time.Now(), 0); err != nil || !ok {
		t.Fatalf("register the controller = %t, %v", ok, err)
	}
}

func tick(t *testing.T, pool *pgxpool.Pool, admission *Admission, eventID string) {
	t.Helper()
	if _, err := intake.ApplyFact(context.Background(), pool, "controller", eventID, intake.ControllerTick{}, engineStub{}, admission); err != nil {
		t.Fatalf("ApplyFact %s: %v", eventID, err)
	}
}

func unlabelledTodo(key string) intake.DispatchIssue {
	return intake.DispatchIssue{Key: key, ResolvedRepo: "acme/widgets", Seq: 1, Type: "issue.updated", Status: "todo", Title: key, Rank: "M"}
}

// An issue nobody handed to Legion moving into todo wakes a registered controller, which takes
// nothing for it (skill://legion-controller, "Free slots"). It wakes a registered controller
// while a slot stands free, and never while every slot is taken or held for a waiting root. The
// wake is due todoWakeDelay after the event, and until then every later event finds it pending,
// so an edit burst is one wake even though the outbox publishes whatever is due at once.
func TestATodoIssueNotHandedToLegionWakesTheControllerWhileASlotIsFree(t *testing.T) {
	todo := func(issue string) []effect {
		return []effect{{kind: record.OutboxKindControllerNotice, issue: issue, payload: record.ControllerNotice{Kind: record.TodoNotice}}}
	}
	t.Run("no controller registered", func(t *testing.T) {
		pool := migratedPool(t)
		admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
		apply(t, pool, admission, "todo-1", unlabelledTodo("LEGION-1"), engineStub{})
		assertControllerNotices(t, pool, nil)
	})
	t.Run("a free slot, one wake for a burst", func(t *testing.T) {
		pool := migratedPool(t)
		admission := newAdmission(t, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
		registerController(t, pool)
		seedSlotted(t, pool, "LEGION-RUNNING", "A")
		apply(t, pool, admission, "todo-1", unlabelledTodo("LEGION-1"), engineStub{})
		apply(t, pool, admission, "todo-2", unlabelledTodo("LEGION-2"), engineStub{})
		assertControllerNotices(t, pool, todo("LEGION-1"))
		var due time.Time
		if err := pool.QueryRow(context.Background(), "select next_at from outbox where kind = $1", string(record.OutboxKindControllerNotice)).Scan(&due); err != nil {
			t.Fatalf("read the todo notice's due time: %v", err)
		}
		if !due.Equal(fixedNow.Add(todoWakeDelay)) {
			t.Fatalf("the todo notice is due %s, want %s: todoWakeDelay after the event", due, fixedNow.Add(todoWakeDelay))
		}
		if got := maybeIssue(t, pool, "LEGION-1"); got != nil {
			t.Fatalf("the unlabelled todo issue was recorded: %#v", got)
		}
	})
	t.Run("every slot taken or held for a waiting root", func(t *testing.T) {
		pool := migratedPool(t)
		admission := newAdmission(t, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
		registerController(t, pool)
		seedSlotted(t, pool, "LEGION-RUNNING", "A")
		seedWaiting(t, pool, "LEGION-WAITING", "B")
		apply(t, pool, admission, "todo-1", unlabelledTodo("LEGION-1"), engineStub{})
		assertControllerNotices(t, pool, nil)
	})
}

// The daemon's periodic tick wakes a registered controller whatever the slots, one wake while that
// wake is still unpublished, named by the project. With every slot taken, it is the only wake a
// tree waiting on a refused root claim gets: that tree holds its slot, its architect started
// nothing, and only the controller's turn rechecks the claim. It is also the turn that posts the
// day's report on a day nothing else wakes the controller.
func TestTheControllerTickWakesARegisteredControllerWhateverTheSlots(t *testing.T) {
	tickNotice := []effect{{kind: record.OutboxKindControllerNotice, issue: testProject, payload: record.ControllerNotice{Kind: record.TickNotice}}}
	t.Run("no controller registered", func(t *testing.T) {
		pool := migratedPool(t)
		admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
		tick(t, pool, admission, "tick-1")
		assertControllerNotices(t, pool, nil)
	})
	t.Run("a free slot, one wake for repeated ticks", func(t *testing.T) {
		pool := migratedPool(t)
		admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
		registerController(t, pool)
		tick(t, pool, admission, "tick-1")
		tick(t, pool, admission, "tick-2")
		assertControllerNotices(t, pool, tickNotice)
	})
	t.Run("the only slot held by a tree whose root has not started", func(t *testing.T) {
		pool := migratedPool(t)
		admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
		registerController(t, pool)
		seedSlotted(t, pool, "LEGION-WAITING-ON-CLAIM", "A")
		for i := range 24 {
			tick(t, pool, admission, fmt.Sprintf("tick-%d", i))
		}
		assertControllerNotices(t, pool, tickNotice)
	})
	t.Run("the only slot held by a started tree", func(t *testing.T) {
		pool := migratedPool(t)
		admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
		registerController(t, pool)
		seedSlotted(t, pool, "LEGION-RUNNING", "A")
		running := issue(t, pool, "LEGION-RUNNING")
		running.Phase = phase.Planning
		putIssue(t, pool, running)
		tick(t, pool, admission, "tick-1")
		assertControllerNotices(t, pool, tickNotice)
	})
}

// The tick is the controller's turn for the day's report, so it alone carries the daemon's open
// capabilities (ReportCapabilities): the names the registered function answers, as the deployment
// stands when the tick is queued; a todo wake carries none, and a tick before the daemon registers
// the function, or when it answers none, carries none either.
func TestTheTickAloneCarriesTheOpenCapabilities(t *testing.T) {
	t.Run("a tick names the gaps, a todo wake does not", func(t *testing.T) {
		pool := migratedPool(t)
		admission := newAdmission(t, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
		admission.ReportCapabilities(func() []string { return []string{"secrets", "resource-limits"} })
		registerController(t, pool)
		apply(t, pool, admission, "todo-1", unlabelledTodo("LEGION-1"), engineStub{})
		tick(t, pool, admission, "tick-1")
		assertControllerNotices(t, pool, []effect{
			{kind: record.OutboxKindControllerNotice, issue: "LEGION-1", payload: record.ControllerNotice{Kind: record.TodoNotice}},
			{kind: record.OutboxKindControllerNotice, issue: testProject, payload: record.ControllerNotice{Kind: record.TickNotice, OpenCapabilities: []string{"secrets", "resource-limits"}}},
		})
	})
	t.Run("no gap, and no function registered", func(t *testing.T) {
		for name, report := range map[string]func() []string{"none open": func() []string { return nil }, "not registered": nil} {
			t.Run(name, func(t *testing.T) {
				pool := migratedPool(t)
				admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if report != nil {
					admission.ReportCapabilities(report)
				}
				registerController(t, pool)
				tick(t, pool, admission, "tick-1")
				assertControllerNotices(t, pool, []effect{{kind: record.OutboxKindControllerNotice, issue: testProject, payload: record.ControllerNotice{Kind: record.TickNotice}}})
			})
		}
	})
}

// The tick is the turn that posts the day's report, so it alone tells the controller what Legion
// waits on a Dispatch issue nobody has finished for: here the roots promotion skips and the
// children the workflow holds at `admitted` (a started issue that gave its slot back for a wait
// is named the same way), by issue key, with what each waits on. The wait
// is the controller's to name in the report and nobody else's: a todo wake and a slot-free wake
// carry none, and a tick with nothing blocked carries none either.
func TestTheTickAloneCarriesWhatLegionWaitsOn(t *testing.T) {
	t.Run("a tick names them, a todo wake does not", func(t *testing.T) {
		pool := migratedPool(t)
		admission := quiet(t, 2)
		registerController(t, pool)
		seedSlotted(t, pool, "LEGION-208", "A")
		seedWaiting(t, pool, "LEGION-211", "B")
		// A child of the running tree, held at admitted behind an issue still in progress.
		putIssue(t, pool, record.Issue{
			Key: "LEGION-209", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
			Project: testProject, Title: "LEGION-209", Tree: "LEGION-208", Phase: phase.Admitted, Generation: 1, Status: "todo", Rank: "C",
		})
		seedMirror(t, pool, "LEGION-209", "todo", "LEGION-300")
		seedMirror(t, pool, "LEGION-211", "todo", "LEGION-301")
		seedMirror(t, pool, "LEGION-300", "in_progress")

		apply(t, pool, admission, "todo-1", unlabelledTodo("LEGION-1"), engineStub{})
		tick(t, pool, admission, "tick-1")

		// LEGION-301 has no mirror row of its own, which is no finished issue: the wait stands.
		// The tick carries what the project holds beside what it waits on: the held child stands
		// in neither the active set nor the line, and its tree's gate is what holds it there
		// (TestTheTickNoticeCarriesTheHeldIssues), while the waiting root keeps its place in the
		// line and takes a blocked row alone.
		assertControllerNotices(t, pool, []effect{
			{kind: record.OutboxKindControllerNotice, issue: "LEGION-1", payload: record.ControllerNotice{Kind: record.TodoNotice}},
			{kind: record.OutboxKindControllerNotice, issue: testProject, payload: record.ControllerNotice{Kind: record.TickNotice, Blocked: []record.BlockedIssue{
				{Issue: "LEGION-209", BlockedBy: []string{"LEGION-300"}},
				{Issue: "LEGION-211", BlockedBy: []string{"LEGION-301"}},
			}, Held: []record.HeldIssue{
				{Issue: "LEGION-209", Reason: record.HeldDesignGate, HeldBy: []string{"LEGION-208"}},
			}}},
		})
	})
	t.Run("a slot-free wake does not", func(t *testing.T) {
		pool := migratedPool(t)
		admission := quiet(t, 2)
		registerController(t, pool)
		seedSlotted(t, pool, "LEGION-208", "A")
		seedWaiting(t, pool, "LEGION-211", "B")
		seedMirror(t, pool, "LEGION-211", "todo", "LEGION-301")

		apply(t, pool, admission, "phase-done", intake.DispatchIssue{Key: "LEGION-208", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated", Status: "in_progress", Title: "LEGION-208", Rank: "A", HandedOver: handed}, engineStub{store: record.NewStore(), done: "LEGION-208"})

		assertControllerNotices(t, pool, []effect{
			{kind: record.OutboxKindControllerNotice, issue: "LEGION-208", payload: record.ControllerNotice{Kind: record.SlotFreeNotice}},
		})
	})
	t.Run("nothing blocked", func(t *testing.T) {
		pool := migratedPool(t)
		admission := quiet(t, 1)
		registerController(t, pool)
		seedSlotted(t, pool, "LEGION-208", "A")

		tick(t, pool, admission, "tick-1")

		assertControllerNotices(t, pool, []effect{{kind: record.OutboxKindControllerNotice, issue: testProject, payload: record.ControllerNotice{Kind: record.TickNotice}}})
	})
}

// providerErrorsReason is what each notice below tells the controller.
const providerErrorsReason = "6 turns in a row ended in a provider error"

// oweProviderErrors records a claim of role on issue whose run run of provider-error turns owes
// the project's controller its notice, as the claim's machine writes it at the run's sixth turn
// (supervise's Claim.ProviderErrorsOwed), and answers the claim's token. A second call for the
// same claim and a later run is that claim's next run reaching six.
func oweProviderErrors(t *testing.T, pool *pgxpool.Pool, issue string, role claim.Role, run uint64) claim.Token {
	t.Helper()
	project, err := claim.ProjectToken(testProject)
	if err != nil {
		t.Fatal(err)
	}
	token, err := claim.NewToken(project, issue, role)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `insert into claims (token, project, tree, issue, role, generation, session,
		session_file, state, launch_failures, prompt_failures, prompt_retires, uncertain_streak,
		provider_error_run, provider_error_run_id, provider_errors_owed_run, provider_errors_owed_reason)
		values ($1, $2, $3, $3, $4, 1, 'ses', '', 'idle', 0, 0, 0, 0, 6, $5, $5, $6)
		on conflict (token) do update set provider_error_run_id = excluded.provider_error_run_id,
		provider_errors_owed_run = excluded.provider_errors_owed_run,
		provider_errors_owed_reason = excluded.provider_errors_owed_reason`,
		string(token), project, issue, string(role), int64(run), providerErrorsReason); err != nil {
		t.Fatalf("record the notice %s's %s owes of its run %d: %v", issue, role, run, err)
	}
	return token
}

// offerProviderErrors is the claim's machine offering the notice its run owes, as internal/daemon's
// providerErrorsOwed does: outside any fact, in a transaction of the caller's own that opens its
// own commit scope (intake.WithCommitHooks). It answers what the offer answered.
func offerProviderErrors(pool *pgxpool.Pool, admission *Admission, token claim.Token) error {
	scoped, committed := intake.WithCommitHooks(context.Background())
	if err := pgx.BeginFunc(scoped, pool, func(tx pgx.Tx) error {
		return admission.TellOfProviderErrors(scoped, tx, token)
	}); err != nil {
		return err
	}
	committed()
	return nil
}

func mustOfferProviderErrors(t *testing.T, pool *pgxpool.Pool, admission *Admission, token claim.Token) {
	t.Helper()
	if err := offerProviderErrors(pool, admission, token); err != nil {
		t.Fatalf("offer the notice of %s's provider errors: %v", token, err)
	}
}

// providerErrorsNotice is the outbox row of one provider-errors notice for the role of issue.
func providerErrorsNotice(issue string, role claim.Role) effect {
	return effect{kind: record.OutboxKindControllerNotice, issue: issue, payload: record.ControllerNotice{
		Kind: record.ProviderErrorsNotice, Role: role, Reason: providerErrorsReason}}
}

// assertProviderErrorsNotices compares the provider-errors notices in the outbox, oldest first,
// with want; the tick's own notice and every other kind are not these tests'.
func assertProviderErrorsNotices(t *testing.T, pool *pgxpool.Pool, want []effect) {
	t.Helper()
	var got []effect
	for _, row := range effects(t, pool) {
		if notice, ok := row.payload.(record.ControllerNotice); ok && notice.Kind == record.ProviderErrorsNotice {
			got = append(got, row)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider-errors notices = %#v, want %#v", got, want)
	}
}

// A provider-errors notice is one run's, so each claim's stands beside every other's: in an
// outage every claim's turns fail, and each claim owes a notice of its own. Two claims of one
// issue — its architect and its worker — are two such claims. One run that is offered again
// is told once: the second offer of the implementer of LEGION-209 reads the run told.
func TestEachClaimsProviderErrorsNoticeStandsWhileAnothersWaits(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	registerController(t, pool)
	implementer := oweProviderErrors(t, pool, "LEGION-209", claim.RoleImplementer, 1)
	other := oweProviderErrors(t, pool, "LEGION-210", claim.RoleImplementer, 1)
	architect := oweProviderErrors(t, pool, "LEGION-209", claim.RoleArchitect, 1)

	for _, token := range []claim.Token{implementer, other, architect, implementer} {
		mustOfferProviderErrors(t, pool, admission, token)
	}

	assertProviderErrorsNotices(t, pool, []effect{
		providerErrorsNotice("LEGION-209", claim.RoleImplementer),
		providerErrorsNotice("LEGION-210", claim.RoleImplementer),
		providerErrorsNotice("LEGION-209", claim.RoleArchitect),
	})
}

// Each run of one claim is told on its own: the second run's notice is written while the first
// run's still waits unsent in the outbox, since the told mark, not the outbox, says which run was
// told. A pending notice that held the second one back would lose it whenever the second run
// ended before the first notice was published.
func TestASecondRunIsToldWhileTheFirstRunsNoticeWaitsUnsent(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	registerController(t, pool)
	token := oweProviderErrors(t, pool, "LEGION-209", claim.RoleImplementer, 1)
	mustOfferProviderErrors(t, pool, admission, token)
	assertProviderErrorsNotices(t, pool, []effect{providerErrorsNotice("LEGION-209", claim.RoleImplementer)})

	oweProviderErrors(t, pool, "LEGION-209", claim.RoleImplementer, 2)
	mustOfferProviderErrors(t, pool, admission, token)
	mustOfferProviderErrors(t, pool, admission, token)

	assertProviderErrorsNotices(t, pool, []effect{
		providerErrorsNotice("LEGION-209", claim.RoleImplementer),
		providerErrorsNotice("LEGION-209", claim.RoleImplementer),
	})
}

// An offer names a claim, and the claim's row holds the mark that tells its run once. A token no
// claim row holds has no mark to read or write, so an offer of one would be written at every
// offer: it is refused, by name, and writes nothing, and so is a mark of such a token.
func TestAnOfferOfATokenNoClaimHoldsIsRefused(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	registerController(t, pool)
	project, err := claim.ProjectToken(testProject)
	if err != nil {
		t.Fatal(err)
	}
	token, err := claim.NewToken(project, "LEGION-209", claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := offerProviderErrors(pool, admission, token); !errors.Is(err, record.ErrNoClaim) {
			t.Fatalf("the offer of a token no claim holds = %v, want record.ErrNoClaim", err)
		}
	}
	assertProviderErrorsNotices(t, pool, nil)
	inTx(t, pool, func(tx pgx.Tx) {
		if err := record.NewStore().MarkProviderErrorsTold(context.Background(), tx, token, 1); !errors.Is(err, record.ErrNoClaim) {
			t.Fatalf("the mark of a token no claim holds = %v, want record.ErrNoClaim", err)
		}
	})
}

// A notice owed while no controller is registered waits on the claim's row, and the first pass
// after a controller registers writes it: the pass of every fact, tick and boot tells every
// owed notice of the project once (TellOwedProviderErrors), so nothing waits on another turn of
// the claim. A pass with no controller registered writes nothing and marks nothing, and a pass
// after the notice was written writes no second one.
func TestAnOwedNoticeIsWrittenAtThePassAfterAControllerRegisters(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	token := oweProviderErrors(t, pool, "LEGION-209", claim.RoleImplementer, 1)
	oweProviderErrors(t, pool, "LEGION-210", claim.RoleTester, 3)

	mustOfferProviderErrors(t, pool, admission, token)
	tick(t, pool, admission, "tick-before")
	assertProviderErrorsNotices(t, pool, nil)

	registerController(t, pool)
	tick(t, pool, admission, "tick-after")
	want := []effect{
		providerErrorsNotice("LEGION-209", claim.RoleImplementer),
		providerErrorsNotice("LEGION-210", claim.RoleTester),
	}
	assertProviderErrorsNotices(t, pool, want)

	tick(t, pool, admission, "tick-again")
	mustOfferProviderErrors(t, pool, admission, token)
	assertProviderErrorsNotices(t, pool, want)
}

// Two offers of one run at once write one notice: the read of what the claim owes locks the
// claim's row (record.Store.ProviderErrorsOwed), so the second offer waits for the first to commit
// and then reads the run told. The first offer's transaction is held open until the second is
// seen waiting on a lock, and only then committed. Without the lock the second offer reads the
// run untold beside the first and writes a second notice of it, and its own mark then waits on the
// first's row lock all the same, which is what this case waits for in both.
func TestTwoOffersOfOneRunAtOnceWriteOneNotice(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	registerController(t, pool)
	token := oweProviderErrors(t, pool, "LEGION-209", claim.RoleImplementer, 1)
	ctx := context.Background()

	first, firstCommitted := intake.WithCommitHooks(ctx)
	tx, err := pool.Begin(first)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := admission.TellOfProviderErrors(first, tx, token); err != nil {
		t.Fatalf("the first offer: %v", err)
	}

	second := make(chan error, 1)
	go func() { second <- offerProviderErrors(pool, admission, token) }()
	testwait.Eventually(t, "the second offer to wait on a lock the first offer holds", func() bool {
		var waiting int
		if err := pool.QueryRow(ctx, `select count(*) from pg_stat_activity
			where datname = current_database() and wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Errorf("read who waits on a lock: %v", err)
			return true
		}
		return waiting == 1
	})
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the first offer: %v", err)
	}
	firstCommitted()
	if err := <-second; err != nil {
		t.Fatalf("the second offer: %v", err)
	}

	assertProviderErrorsNotices(t, pool, []effect{providerErrorsNotice("LEGION-209", claim.RoleImplementer)})
}

// The slot wakes keep their own rule: one pending notice of the kind per project, whatever issue
// each names. Two roots whose slots are released in two facts wake the controller once, and the
// second release's issue is not a second wake.
func TestASlotFreeWakeStaysOnePerProject(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 3)
	registerController(t, pool)
	seedSlotted(t, pool, "LEGION-208", "A")
	seedSlotted(t, pool, "LEGION-212", "B")

	apply(t, pool, admission, "first-done", intake.DispatchIssue{Key: "LEGION-208", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated", Status: "in_progress", Title: "LEGION-208", Rank: "A", HandedOver: handed}, engineStub{store: record.NewStore(), done: "LEGION-208"})
	apply(t, pool, admission, "second-done", intake.DispatchIssue{Key: "LEGION-212", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated", Status: "in_progress", Title: "LEGION-212", Rank: "B", HandedOver: handed}, engineStub{store: record.NewStore(), done: "LEGION-212"})

	assertControllerNotices(t, pool, []effect{
		{kind: record.OutboxKindControllerNotice, issue: "LEGION-208", payload: record.ControllerNotice{Kind: record.SlotFreeNotice}},
	})
}
