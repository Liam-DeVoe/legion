package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// evalPerson is the person a test's try acts as, who is none of the seed's people.
const evalPerson = "eval-person@example.com"

// seededStore is a database holding the committed seed, the seed and the committed tasks, and the
// store as the seed made it.
func seededStore(t *testing.T) (*store.Store, *Seed, []Task, *Store) {
	t.Helper()
	ctx := context.Background()
	seed, err := LoadSeed(Files)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := LoadTasks(Files)
	if err != nil {
		t.Fatal(err)
	}
	database := storetest.Open(t)
	if written, err := WriteSeed(ctx, database, seed); err != nil || !written {
		t.Fatalf("WriteSeed = %v, %v; want the seed written", written, err)
	}
	before, err := ReadStore(ctx, database.Pool)
	if err != nil {
		t.Fatal(err)
	}
	return database, seed, tasks, before
}

// taskByID is the committed task of that id.
func taskByID(t *testing.T, tasks []Task, id string) Task {
	t.Helper()
	for _, task := range tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("the eval holds no task %s", id)
	return Task{}
}

// exec runs each statement on the database, failing the test on the first that fails.
func exec(t *testing.T, database *store.Store, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := database.Pool.Exec(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// TestAChangeTaskFailsOnAChangeItDidNotAskFor: on the seeded store, change-03 ("move DEMO-8,
// DEMO-10 and DEMO-37 to todo") passes with exactly its changes made, and fails once the store
// also holds one change it does not allow, a new priority on DEMO-9, which the verdict names.
func TestAChangeTaskFailsOnAChangeItDidNotAskFor(t *testing.T) {
	ctx := context.Background()
	database, seed, tasks, before := seededStore(t)
	task := taskByID(t, tasks, "change-03")

	exec(t, database, `update issues set status = 'todo' where key in ('DEMO-8', 'DEMO-10', 'DEMO-37')`)
	after, err := ReadStore(ctx, database.Pool)
	if err != nil {
		t.Fatal(err)
	}
	if verdict := GradeEnd(seed, task, before, after, evalPerson); !verdict.Pass() {
		t.Fatalf("change-03 with exactly its changes graded %+v; want a pass", verdict)
	}

	exec(t, database, `update issues set priority = 0 where key = 'DEMO-9'`)
	after, err = ReadStore(ctx, database.Pool)
	if err != nil {
		t.Fatal(err)
	}
	verdict := GradeEnd(seed, task, before, after, evalPerson)
	if verdict.Pass() {
		t.Fatalf("change-03 with its changes and a new priority on DEMO-9 graded a pass; want a fail")
	}
	if len(verdict.Missing) != 0 || len(verdict.Unasked) != 1 || !strings.Contains(verdict.Unasked[0], "priority of DEMO-9") {
		t.Errorf("the verdict is %+v; want nothing missing and one change not asked for, the priority of DEMO-9", verdict)
	}
}

// TestTheCheckerReportsAMessageNoApprovedRowMade: on the seeded store holding one message the
// person wrote on DEMO-3 to its architect, which handoff-02 asks for, and no plan, the checker of
// c13's message clause reports the message as made by no approved row; once a run of the person
// holds an approved row whose landed request made it, the checker reports nothing; and the same
// message under change-03, which asks for no message, is reported as not asked for.
func TestTheCheckerReportsAMessageNoApprovedRowMade(t *testing.T) {
	ctx := context.Background()
	database, seed, tasks, before := seededStore(t)
	handoff := taskByID(t, tasks, "handoff-02")
	message := uuid.NewString()
	exec(t, database, `insert into messages (id, issue_key, author, body, target, created_at) values ('`+message+
		`', 'DEMO-3', '{"kind": "user", "id": "`+evalPerson+`"}', 'Please review the spec of DEMO-3.', 'session:session-architect-01', now())`)
	after, err := ReadStore(ctx, database.Pool)
	if err != nil {
		t.Fatal(err)
	}

	broken, err := CheckPersonWrites(ctx, database.Pool, seed, handoff, before, after, evalPerson)
	if err != nil {
		t.Fatal(err)
	}
	if len(broken) != 1 || !strings.Contains(broken[0], "no approved row") || !strings.Contains(broken[0], "DEMO-3") {
		t.Fatalf("the checker reported %q on a message no approved row made; want that one message", broken)
	}

	run := uuid.NewString()
	exec(t, database,
		`insert into going_runs (id, author_login, words, state) values ('`+run+`', '`+evalPerson+`', 'ask the architect to review it', 'done')`,
		`insert into going_plans (run_id, plan, state) values ('`+run+`', 1, 'done')`,
		`insert into going_plan_rows (run_id, plan, row, change, state, approval, result) values ('`+run+
			`', 1, 1, '{"tool": "dispatch_message"}', 'done', '{"by": "person"}', '{"step": 2, "call": 1, "status": 201}')`,
		`insert into going_landings (run_id, phase, step, call, request, method, path, body_digest, answer) values ('`+run+
			`', 'do', 2, 1, 0, 'POST', '/api/v1/issues/DEMO-3/messages', 'digest', '{"status": 201, "body": {"id": "`+message+`"}}')`,
	)
	broken, err = CheckPersonWrites(ctx, database.Pool, seed, handoff, before, after, evalPerson)
	if err != nil {
		t.Fatal(err)
	}
	if len(broken) != 0 {
		t.Errorf("the checker reported %q on a message an approved row made; want nothing", broken)
	}

	broken, err = CheckPersonWrites(ctx, database.Pool, seed, taskByID(t, tasks, "change-03"), before, after, evalPerson)
	if err != nil {
		t.Fatal(err)
	}
	if len(broken) != 1 || !strings.Contains(broken[0], "does not ask for it") {
		t.Errorf("the checker reported %q on a message change-03 does not ask for; want that one message", broken)
	}
}
