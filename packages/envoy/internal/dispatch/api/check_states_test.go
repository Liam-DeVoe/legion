// check_states_test.go holds the check states settler (check_states.go) to its rules: each write
// that can change the state of a `before_merge` check at an open head marks the check's issue
// owed in its own transaction, and the settler publishes one check.state_changed event for each
// change of a state, compared with the state it published last, and none where nothing changed.
// A test handoff waits until the states its results caused are published.
//
// Each test drives the settler itself: settleOwed runs its pass until no mark is due, so a test
// reads the events one write caused before it makes the next. The tests of the loop
// (TestAMarkThatNoStartSettledIsSettledByTheBackgroundPass, TestTheWritersCommitStartsTheSettle,
// TestATestHandoffComesAfterTheStatesItsResultsCaused) run it as the server does.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/pglisten"
)

// The heads of the issues with no check of their own (TestAHeadWithNoCheckGetsTheSetForm), beside
// eachHeadH1 to eachHeadH4.
const (
	checkStatesH5 = "f5f5f5f5f5f5f5f5f5f5f5f5f5f5f5f5f5f5f5f5"
	checkStatesH6 = "a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6"
)

// checkStatesBlock is check block id carrying attributes, with words as its text.
func checkStatesBlock(id, attributes, words string) string {
	return ":::check{#" + id + " " + attributes + "}\n" + words + "\n:::\n"
}

// checkStatesTwoChecks is the Verification document of most of these tests: k1 and k2, two
// ordinary `before_merge` checks, in that order.
var checkStatesTwoChecks = checkStatesBlock("k1", ciCheckOrdinary, "The server records a result against a check.") + "\n" +
	checkStatesBlock("k2", ciCheckOrdinary, "The server answers a read of the results.")

// checkStatesWithCI adds k3, a check CI proves: its ci_check names the rig's job, whose runs the
// rig's stand-in for GitHub lists at each head.
var checkStatesWithCI = checkStatesTwoChecks + "\n" +
	checkStatesBlock("k3", `negative_control="`+ciJobNegativeControl+`" ci_check="`+ciJobName+`"`, "CI builds the server at the head.")

// checkStatesTestSchedule is the production schedule with a short gap and a short poll, so a loop
// a test runs is quick to answer; the retry and its ceiling are production's, which the test of a
// failed read reads back.
var checkStatesTestSchedule = checkStatesSchedule{
	minGap: 50 * time.Millisecond, sweep: time.Minute, retry: 30 * time.Second, retryMax: 10 * time.Minute,
	fenceWait: 30 * time.Second, fencePoll: 20 * time.Millisecond, batch: 20, rounds: 5,
}

// checkStatesRig is the CI job rig (newCIJobRig), whose issue ACME-1 holds the Verification
// document a test gives it, with a settler of its own: the server under test's, on a lease
// connection of the rig's own lease pool.
type checkStatesRig struct {
	*ciJobRig
	settler *checkStatesSettler
	callers *store.LeaseCallers
}

func newCheckStatesRig(t *testing.T, verification string) *checkStatesRig {
	t.Helper()
	rig := newCIJobRig(t)
	uploadDocument(t, rig.handler, "/api/v1/issues/"+rig.issue, "verification.md", verification)
	callers, err := rig.database.Pool.Leases()
	if err != nil {
		t.Fatalf("open the lease pool: %v", err)
	}
	r := &checkStatesRig{ciJobRig: rig, callers: callers}
	r.settler = r.newSettler(t, "check states")
	r.served.checkStatesSchedule = checkStatesTestSchedule
	return r
}

// newSettler is a settler of the server under test on a reservation of its own, as another
// process's settler holds a lease connection of its own.
func (r *checkStatesRig) newSettler(t *testing.T, caller string) *checkStatesSettler {
	t.Helper()
	leases, err := r.callers.For(caller)
	if err != nil {
		t.Fatalf("reserve a lease connection for %s: %v", caller, err)
	}
	return &checkStatesSettler{server: r.served, leases: leases, exclusive: duty.Exclusive, schedule: checkStatesTestSchedule}
}

// fileIssue files one more issue of project ACME, with no Verification document, and answers it.
func (r *checkStatesRig) fileIssue(t *testing.T, title string) string {
	t.Helper()
	filed := r.asPerson(t, http.MethodPost, "/api/v1/issues", map[string]any{"project": ciJobProject, "title": title, "spec": "A spec\n"})
	if filed.Code != http.StatusCreated {
		t.Fatalf("file %q: status=%d body=%s", title, filed.Code, filed.Body.String())
	}
	return decodeBody[model.Issue](t, filed).Key
}

// checkStatesEvent is one stored check.state_changed event: its owner's sequence, its issue, its
// actor and its payload, as stored and as decoded.
type checkStatesEvent struct {
	Seq     int64
	Issue   string
	Actor   model.Actor
	Raw     json.RawMessage
	Payload model.CheckStateChangedEventPayload
}

// check is the payload's check, "" in the set form.
func (e checkStatesEvent) check() string {
	if e.Payload.Check == nil {
		return ""
	}
	return *e.Payload.Check
}

// state is the payload's state, "" in the set form.
func (e checkStatesEvent) state() string {
	if e.Payload.State == nil {
		return ""
	}
	return *e.Payload.State
}

// line is the event as one line a failure prints: issue, check, head, state and reason.
func (e checkStatesEvent) line() string {
	check := e.check()
	if check == "" {
		check = "(set " + strings.Join(e.Payload.Checks, ",") + ")"
	}
	return fmt.Sprintf("%s %s@%s %s/%s", e.Issue, check, e.Payload.Head[:6], e.state(), e.Payload.Reason)
}

func eventLines(events []checkStatesEvent) []string {
	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, event.line())
	}
	return lines
}

// checkStateEventsAfter is every check.state_changed event above id, in id order.
func checkStateEventsAfter(t *testing.T, database *store.Store, id int64) []checkStatesEvent {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `
		select seq, issue_key, actor::text, payload::text from events
		where type = 'check.state_changed' and id > $1 order by id`, id)
	if err != nil {
		t.Fatalf("read the check.state_changed events: %v", err)
	}
	defer rows.Close()
	var events []checkStatesEvent
	for rows.Next() {
		var event checkStatesEvent
		var actor, payload string
		if err := rows.Scan(&event.Seq, &event.Issue, &actor, &payload); err != nil {
			t.Fatalf("read a check.state_changed event: %v", err)
		}
		if err := json.Unmarshal([]byte(actor), &event.Actor); err != nil {
			t.Fatalf("decode the actor %s: %v", actor, err)
		}
		event.Raw = json.RawMessage(payload)
		if err := json.Unmarshal(event.Raw, &event.Payload); err != nil {
			t.Fatalf("decode the payload %s: %v", payload, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the check.state_changed events: %v", err)
	}
	return events
}

// dueCheckStateMarks is how many marks of any issue are due.
func dueCheckStateMarks(t *testing.T, database *store.Store) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(context.Background(),
		`select count(*) from check_states_owed where due_at <= now()`).Scan(&count); err != nil {
		t.Fatalf("count the due marks: %v", err)
	}
	return count
}

// marksOf is how many marks issue holds, due or not.
func marksOf(t *testing.T, database *store.Store, issue string) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(context.Background(),
		`select count(*) from check_states_owed where issue_key = $1`, issue).Scan(&count); err != nil {
		t.Fatalf("count the marks of %s: %v", issue, err)
	}
	return count
}

// settleOwed runs the rig's settler's pass until no mark is due, at most 20 passes, and answers
// the check.state_changed events those passes appended, oldest first.
func settleOwed(t *testing.T, rig *checkStatesRig) []checkStatesEvent {
	t.Helper()
	return settleOwedBy(t, rig, rig.settler)
}

// settleOwedBy is settleOwed with settler's passes.
func settleOwedBy(t *testing.T, rig *checkStatesRig, settler *checkStatesSettler) []checkStatesEvent {
	t.Helper()
	ctx := store.WithTransactionTracking(t.Context())
	after := lastEventID(t, rig.database)
	for pass := 0; dueCheckStateMarks(t, rig.database) > 0; pass++ {
		if pass == 20 {
			t.Fatalf("20 passes of the settler left %d marks due", dueCheckStateMarks(t, rig.database))
		}
		settler.pass(ctx)
	}
	return checkStateEventsAfter(t, rig.database, after)
}

// settleBy runs one settle of the rig's issue by settler, failing the test unless it ran and
// left no due mark, and answers the events it appended.
func settleBy(t *testing.T, rig *checkStatesRig, settler *checkStatesSettler) []checkStatesEvent {
	t.Helper()
	after := lastEventID(t, rig.database)
	ran, failed, owed := settler.settle(store.WithTransactionTracking(t.Context()), rig.issue)
	if !ran || failed || owed {
		t.Fatalf("the settle of %s answered ran=%v failed=%v owed=%v, want a settle that ran and left nothing owed", rig.issue, ran, failed, owed)
	}
	return checkStateEventsAfter(t, rig.database, after)
}

// stateWant is one event a test expects: the check ("" for the set form), the head, the state
// and the reason.
type stateWant struct {
	check, head, state, reason string
}

func (w stateWant) String() string {
	check := w.check
	if check == "" {
		check = "(set)"
	}
	return fmt.Sprintf("%s@%s %s/%s", check, w.head[:6], w.state, w.reason)
}

func wantOf(event checkStatesEvent) stateWant {
	return stateWant{check: event.check(), head: event.Payload.Head, state: event.state(), reason: event.Payload.Reason}
}

// wantEvents fails the test unless events are exactly want, in order.
func wantEvents(t *testing.T, what string, events []checkStatesEvent, want ...stateWant) {
	t.Helper()
	got := make([]stateWant, 0, len(events))
	for _, event := range events {
		got = append(got, wantOf(event))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s: the events are %v, want %v (%v)", what, got, want, eventLines(events))
	}
}

// wantEventSet fails the test unless events are want in any order.
func wantEventSet(t *testing.T, what string, events []checkStatesEvent, want ...stateWant) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, wantOf(event).String())
	}
	wanted := make([]string, 0, len(want))
	for _, one := range want {
		wanted = append(wanted, one.String())
	}
	slices.Sort(got)
	slices.Sort(wanted)
	if !slices.Equal(got, wanted) {
		t.Fatalf("%s: the events are %v, want %v in any order", what, got, wanted)
	}
}

// eventsOfCheck is the events of check at head among events.
func eventsOfCheck(events []checkStatesEvent, check, head string) []checkStatesEvent {
	var of []checkStatesEvent
	for _, event := range events {
		if event.check() == check && event.Payload.Head == head {
			of = append(of, event)
		}
	}
	return of
}

// publishedState is one row of check_states.
type publishedState struct {
	state           *string
	reason          string
	roles           []string
	ciCase          string
	checks          []string
	checkArtifactID *string
	checkVersion    *int
	eventSeq        int64
}

// publishedRow is the row of check_states the settler holds for check at head of repo on issue,
// "" for the set row, and false where it holds none.
func publishedRow(t *testing.T, database *store.Store, issue, repo, head, check string) (publishedState, bool) {
	t.Helper()
	var row publishedState
	err := database.Pool.QueryRow(context.Background(), `
		select state, reason, roles, ci_case, checks, check_artifact_id::text, check_version, event_seq
		from check_states where issue_key = $1 and repo = $2 and head_sha = $3 and check_block_id = $4`,
		issue, repo, head, check).Scan(&row.state, &row.reason, &row.roles, &row.ciCase, &row.checks,
		&row.checkArtifactID, &row.checkVersion, &row.eventSeq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return publishedState{}, false
		}
		t.Fatalf("read the published state of %s at %s: %v", check, head, err)
	}
	return row, true
}

// computedSubject is the state a fresh computation gives check at head of the rig's issue: its
// subject there in answerIssueChecks's answer.
func computedSubject(t *testing.T, rig *checkStatesRig, check, head string) model.PassStandsSubject {
	t.Helper()
	answer, _, err := rig.served.answerIssueChecks(store.WithTransactionTracking(t.Context()), rig.issue, noCommitNamed, nil)
	if err != nil {
		t.Fatalf("compute the checks of %s: %v", rig.issue, err)
	}
	for _, block := range answer.Checks {
		if block.BlockID != check {
			continue
		}
		for _, subject := range block.PassStands.Subjects {
			if subject.Basis.Commit == head {
				return subject
			}
		}
	}
	t.Fatalf("the computation of %s holds no state of %s at %s", rig.issue, check, head)
	return model.PassStandsSubject{}
}

// wantPublishedIsComputed fails the test unless the published state of check at head is the
// state a fresh computation gives it, and no mark of the rig's issue is left.
func wantPublishedIsComputed(t *testing.T, what string, rig *checkStatesRig, check, head string) {
	t.Helper()
	computed := computedSubject(t, rig, check, head)
	row, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, head, check)
	if !held {
		t.Fatalf("%s: no state of %s at %s is published, and Dispatch computes %s/%s", what, check, head, computed.Answer, computed.Reason)
	}
	ciCase := ""
	if computed.CI != nil {
		ciCase = computed.CI.Case
	}
	if row.state == nil || *row.state != computed.Answer || row.reason != computed.Reason || row.ciCase != ciCase ||
		!slices.Equal(slices.Sorted(slices.Values(row.roles)), slices.Sorted(slices.Values(computed.Roles))) {
		state := "null"
		if row.state != nil {
			state = *row.state
		}
		t.Fatalf("%s: the published state of %s at %s is %s/%s roles %v, and Dispatch computes %s/%s roles %v",
			what, check, head, state, row.reason, row.roles, computed.Answer, computed.Reason, computed.Roles)
	}
	if left := marksOf(t, rig.database, rig.issue); left != 0 {
		t.Fatalf("%s: %d marks of %s are left", what, left, rig.issue)
	}
}

// writerFail inserts a tester's fail of check at commit on the rig's issue with plain SQL, in tx,
// as a writer that runs no code of this build writes a result: the check's version and digest
// read from the block index.
func writerFail(t *testing.T, rig *checkStatesRig, exec func(context.Context, string, ...any) error, check, commit string) {
	t.Helper()
	if err := exec(context.Background(), `
		insert into check_results (issue_key, check_issue_key, check_artifact_id, check_block_id, check_version,
			check_sha256, role, commit_sha, verdict, failed, actor)
		select $1, $1, artifact_id, block_id, changed_version, text_sha256, 'tester', $2, 'fail', 1,
			'{"kind":"user","id":"alice"}'::jsonb
		from document_blocks where artifact_id = $3::uuid and block_id = $4`,
		rig.issue, commit, rig.verification, check); err != nil {
		t.Fatalf("insert a fail of %s at %s: %v", check, commit, err)
	}
}

// holdOnce is a settler hook that holds the first settle that reaches it until release is
// called, and closes reached when it does. A test's cleanup releases it too, so a test that
// fails never leaves a settle holding its lease.
type holdOnce struct {
	reached chan struct{}
	unblock chan struct{}
	first   sync.Once
	once    sync.Once
}

func newHoldOnce(t *testing.T) *holdOnce {
	hold := &holdOnce{reached: make(chan struct{}), unblock: make(chan struct{})}
	t.Cleanup(hold.release)
	return hold
}

func (h *holdOnce) hook(string) {
	first := false
	h.first.Do(func() { first = true })
	if !first {
		return
	}
	close(h.reached)
	<-h.unblock
}

func (h *holdOnce) release() { h.once.Do(func() { close(h.unblock) }) }

func (h *holdOnce) await(t *testing.T, what string) {
	t.Helper()
	select {
	case <-h.reached:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not reach its hook within 10s", what)
	}
}

// runCheckStatesLoop runs settler's loop on wake until the test ends or stop is called.
func runCheckStatesLoop(t *testing.T, settler *checkStatesSettler, wake <-chan struct{}) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		settler.run(ctx, wake)
	}()
	stop = sync.OnceFunc(func() {
		cancel()
		<-done
	})
	t.Cleanup(stop)
	return stop
}

// listenForCheckStates runs a pglisten.Hub on the rig's database, as each server process runs its
// one, and answers its wake on CheckStatesOwedChannel once the hub listens.
func listenForCheckStates(t *testing.T, rig *checkStatesRig) <-chan struct{} {
	t.Helper()
	hub := &pglisten.Hub{Owner: "check states test", Dial: rig.database.Pool.Listener}
	wake := hub.Wake(CheckStatesOwedChannel)
	connected := make(chan struct{})
	hub.OnConnected(sync.OnceFunc(func() { close(connected) }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("the hub did not connect within 10s")
	}
	return wake
}

// awaitEvents polls for up to within until the check.state_changed events above after hold one
// that matches, and answers them.
func awaitEvents(t *testing.T, rig *checkStatesRig, after int64, within time.Duration, matches func([]checkStatesEvent) bool) []checkStatesEvent {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		events := checkStateEventsAfter(t, rig.database, after)
		if matches(events) {
			return events
		}
		if time.Now().After(deadline) {
			return events
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAStateEventIsPublishedForEachCheckAtEachOpenHead: ACME-1 has the open pull requests #70 at
// H1 and #71 at H3. When the two heads appear, the settler publishes one state of each check at
// each head: no, for no_result. Then the tester records a fail of k1 at H1, and one more event
// comes: k1 at H1, no, for fail.
func TestAStateEventIsPublishedForEachCheckAtEachOpenHead(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	if events := settleOwed(t, rig); len(events) != 0 {
		t.Fatalf("with no open head, the settler published %v, want nothing", eventLines(events))
	}
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
	writeHead(t, rig.database, ciJobRepository, 71, eachHeadH3, "open", rig.issue)
	wantEventSet(t, "the two heads appear", settleOwed(t, rig),
		stateWant{"k1", eachHeadH1, "no", "no_result"}, stateWant{"k2", eachHeadH1, "no", "no_result"},
		stateWant{"k1", eachHeadH3, "no", "no_result"}, stateWant{"k2", eachHeadH3, "no", "no_result"})

	eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "fail", eachHeadH1)
	wantEvents(t, "the tester's fail of k1 at H1", settleOwed(t, rig), stateWant{"k1", eachHeadH1, "no", "fail"})
}

// TestAChangeOfStatePublishesOneCheckStateChangedEvent: the tester's pass of k1 at H1 moves its
// state from no to yes, and the event log of ACME-1 then holds exactly one new event, on ACME-1,
// of the type check.state_changed, by the system actor of the check states.
func TestAChangeOfStatePublishesOneCheckStateChangedEvent(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
	settleOwed(t, rig)
	after := lastEventID(t, rig.database)

	eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH1)
	settleOwed(t, rig)
	var types []string
	rows, err := rig.database.Pool.Query(context.Background(),
		`select type from events where issue_key = $1 and id > $2 and type <> 'result.recorded' order by id`, rig.issue, after)
	if err != nil {
		t.Fatalf("read the new events of %s: %v", rig.issue, err)
	}
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("read an event type: %v", err)
		}
		types = append(types, eventType)
	}
	rows.Close()
	if !slices.Equal(types, []string{model.CheckStateChangedEventType}) {
		t.Fatalf("the pass of k1 at H1 appended %v on %s beside its result.recorded, want exactly one %s", types, rig.issue, model.CheckStateChangedEventType)
	}
	events := checkStateEventsAfter(t, rig.database, after)
	wantEvents(t, "the tester's pass of k1 at H1", events, stateWant{"k1", eachHeadH1, "yes", ""})
	if events[0].Issue != rig.issue || events[0].Actor != model.CheckStatesActor {
		t.Fatalf("the event is on %s by %+v, want on %s by %+v", events[0].Issue, events[0].Actor, rig.issue, model.CheckStatesActor)
	}
}

// TestTheStateEventNamesTheCheckTheHeadItsPullRequestTheStateAndTheReason: the tester records a
// fail of k1 at H1 before the first settle. The event of k1 names the check and its version, the
// repository and the head H1, the pull requests [70], the state no and the reason fail, the
// result that decides the state, the list [k1, k2], an empty ci_case and the serial 1, and
// @legion/contracts' CheckStateChangedEventPayloadSchema parses the payload as stored.
func TestTheStateEventNamesTheCheckTheHeadItsPullRequestTheStateAndTheReason(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	settleOwed(t, rig)
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
	fail := eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "fail", eachHeadH1)
	version := checkOf(t, readResults(t, rig.handler, rig.issue), "k1").Version

	events := eventsOfCheck(settleOwed(t, rig), "k1", eachHeadH1)
	if len(events) != 1 {
		t.Fatalf("the settle published %d events of k1 at H1, want one", len(events))
	}
	got := events[0].Payload
	artifact, check, state := rig.verification, "k1", "no"
	want := model.CheckStateChangedEventPayload{
		Issue: rig.issue, CheckArtifactID: &artifact, Repo: ciJobRepository, Head: eachHeadH1,
		PullRequests: []int{70}, Checks: []string{"k1", "k2"}, Check: &check, CheckVersion: &version,
		State: &state, Reason: "fail", Roles: []string{}, Results: []string{fail}, CICase: "", Serial: 1,
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("the payload of k1 at H1 is\n%s\nwant\n%s", gotJSON, wantJSON)
	}
	parseWithContractsSchema(t, events[0].Raw)
}

// parseWithContractsSchema parses payload, as the event log stores it, with @legion/contracts'
// CheckStateChangedEventPayloadSchema, through bun, and fails the test where the schema refuses
// it. bun is required: every CI job that runs this package sets it up.
func parseWithContractsSchema(t *testing.T, payload []byte) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatalf("bun is not on PATH, and the stored payload must be parsed with @legion/contracts' schema: %v", err)
	}
	file := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(file, payload, 0o600); err != nil {
		t.Fatalf("write the payload: %v", err)
	}
	contracts, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "contracts"))
	if err != nil {
		t.Fatalf("find packages/contracts: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), bun, "-e", `import { CheckStateChangedEventPayloadSchema } from "./src/index.ts";
const payload = JSON.parse(await Bun.file(process.env.CHECK_STATE_PAYLOAD).text());
CheckStateChangedEventPayloadSchema.parse(payload);
console.log("parsed");`)
	cmd.Dir = contracts
	cmd.Env = append(os.Environ(), "CHECK_STATE_PAYLOAD="+file)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "parsed") {
		t.Fatalf("CheckStateChangedEventPayloadSchema refuses the stored payload %s: %v\n%s", payload, err, out)
	}
}

// TestEachKindOfChangePublishesTheNewState: each kind of write that can change a state publishes
// it, each in a rig of its own: a result recorded, a result retracted, the words of a check moved
// by a document version, a head moved by delivery.UpsertPullRequestHead, a CI run the stand-in for
// GitHub concluded, whose settlement marks the issue with check_states_owe, a check removed, which
// publishes the head's new set of checks in the set form, and a read of GitHub that failed, which
// publishes unknown for read_failed and keeps one mark for a later read.
func TestEachKindOfChangePublishesTheNewState(t *testing.T) {
	t.Run("recorded", func(t *testing.T) {
		rig := newCheckStatesRig(t, checkStatesTwoChecks)
		writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
		settleOwed(t, rig)
		eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "fail", eachHeadH1)
		wantEvents(t, "a recorded fail", settleOwed(t, rig), stateWant{"k1", eachHeadH1, "no", "fail"})
	})
	t.Run("retracted", func(t *testing.T) {
		rig := newCheckStatesRig(t, checkStatesTwoChecks)
		writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
		fail := eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "fail", eachHeadH1)
		settleOwed(t, rig)
		if retracted := retractAsPerson(t, rig.handler, fail, "The run was against the wrong build."); retracted.Code != http.StatusCreated {
			t.Fatalf("retract the fail: status=%d body=%s", retracted.Code, retracted.Body.String())
		}
		wantEvents(t, "the fail retracted", settleOwed(t, rig), stateWant{"k1", eachHeadH1, "no", "no_result"})
	})
	t.Run("words moved through a document version", func(t *testing.T) {
		rig := newCheckStatesRig(t, checkStatesTwoChecks)
		writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
		writeHead(t, rig.database, ciJobRepository, 71, eachHeadH3, "open", rig.issue)
		eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH1)
		eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH3)
		settleOwed(t, rig)
		before := checkOf(t, readResults(t, rig.handler, rig.issue), "k1").Version
		uploadDocument(t, rig.handler, "/api/v1/issues/"+rig.issue, "verification.md",
			checkStatesBlock("k1", ciCheckOrdinary, "The server records a result against a check, named by its block id.")+"\n"+
				checkStatesBlock("k2", ciCheckOrdinary, "The server answers a read of the results."))
		version := checkOf(t, readResults(t, rig.handler, rig.issue), "k1").Version
		if version == before {
			t.Fatalf("the new words left k1 at version %d", version)
		}
		events := settleOwed(t, rig)
		wantEventSet(t, "k1's words moved", events, stateWant{"k1", eachHeadH1, "no", "no_result"}, stateWant{"k1", eachHeadH3, "no", "no_result"})
		for _, event := range events {
			if event.Payload.CheckVersion == nil || *event.Payload.CheckVersion != version {
				t.Fatalf("the event %s names version %v of k1, want %d", event.line(), event.Payload.CheckVersion, version)
			}
		}
	})
	t.Run("head moved through delivery.UpsertPullRequestHead", func(t *testing.T) {
		rig := newCheckStatesRig(t, checkStatesTwoChecks)
		upsertHead(t, rig, 70, eachHeadH1, rig.issue, "A change")
		settleOwed(t, rig)
		upsertHead(t, rig, 70, eachHeadH2, rig.issue, "A change")
		wantEventSet(t, "a push that moves #70 to H2", settleOwed(t, rig),
			stateWant{"k1", eachHeadH2, "no", "no_result"}, stateWant{"k2", eachHeadH2, "no", "no_result"})
		if _, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, eachHeadH1, "k1"); held {
			t.Fatal("the state of k1 at H1, which no pull request heads now, is still published")
		}
	})
	t.Run("CI settled through check_states_owe", func(t *testing.T) {
		rig := newCheckStatesRig(t, checkStatesWithCI)
		rig.github.listings.set(eachHeadH1, pendingRun(901, ciJobName, "in_progress"))
		writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
		started := eventsOfCheck(settleOwed(t, rig), "k3", eachHeadH1)
		if len(started) != 1 || started[0].state() != "no" || started[0].Payload.CICase != ciCaseNotCompleted {
			t.Fatalf("k3 at H1 while its run goes on: %v, want one event of no with ci_case %s", eventLines(started), ciCaseNotCompleted)
		}
		rig.github.listings.set(eachHeadH1, completedRun(901, ciJobName, "success"))
		if _, err := rig.database.Pool.Exec(context.Background(), `select check_states_owe($1)`, rig.issue); err != nil {
			t.Fatalf("mark %s owed as the settlement of its pull request's checks does: %v", rig.issue, err)
		}
		events := settleOwed(t, rig)
		wantEvents(t, "k3's run concluded", events, stateWant{"k3", eachHeadH1, "yes", ""})
		if !slices.Equal(events[0].Payload.Roles, []string{"ci"}) || events[0].Payload.CICase != "success" {
			t.Fatalf("k3 at H1 once its run passed: roles %v, ci_case %q, want [ci] and success", events[0].Payload.Roles, events[0].Payload.CICase)
		}
	})
	t.Run("a removed check", func(t *testing.T) {
		rig := newCheckStatesRig(t, checkStatesTwoChecks)
		writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
		settleOwed(t, rig)
		uploadDocument(t, rig.handler, "/api/v1/issues/"+rig.issue, "verification.md",
			checkStatesBlock("k1", ciCheckOrdinary, "The server records a result against a check."))
		events := settleOwed(t, rig)
		wantEvents(t, "k2 removed", events, stateWant{"", eachHeadH1, "", ""})
		if !slices.Equal(events[0].Payload.Checks, []string{"k1"}) {
			t.Fatalf("the set form names the checks %v, want [k1]", events[0].Payload.Checks)
		}
		if _, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, eachHeadH1, "k2"); held {
			t.Fatal("the state of k2, which the document no longer holds, is still published")
		}
	})
	t.Run("a read that failed", func(t *testing.T) {
		rig := newCheckStatesRig(t, checkStatesWithCI)
		rig.github.listings.failWith(http.StatusServiceUnavailable)
		writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
		events := eventsOfCheck(settleOwed(t, rig), "k3", eachHeadH1)
		wantEvents(t, "GitHub answering 503", events, stateWant{"k3", eachHeadH1, "unknown", "read_failed"})
		var due float64
		var failures, marks int
		if err := rig.database.Pool.QueryRow(context.Background(), `
			select count(*), coalesce(max(failures), 0), coalesce(extract(epoch from max(due_at) - now()), 0)::float8
			from check_states_owed where issue_key = $1`, rig.issue).Scan(&marks, &failures, &due); err != nil {
			t.Fatalf("read the marks of %s: %v", rig.issue, err)
		}
		if marks != 1 || failures != 1 || due < 20 || due > 30 {
			t.Fatalf("after the failed read %s holds %d marks, failures %d, due in %.1fs; want one mark, failures 1, due 30s on", rig.issue, marks, failures, due)
		}
	})
}

// upsertHead writes #number of the rig's repository at sha, open and attributed to issue, through
// the writer every intake and reconcile write goes through.
func upsertHead(t *testing.T, rig *checkStatesRig, number int, sha, issue, title string) {
	t.Helper()
	attributed := issue
	if err := delivery.UpsertPullRequestHead(context.Background(), rig.database.Pool, nil, delivery.PullRequestHead{
		Repo: ciJobRepository, Number: number, HeadSHA: sha, HeadRef: "legion/work", BaseRef: "main", State: "open",
		IssueKey: &attributed, AttributionSource: "title", AttributionComplete: true, Title: title,
		URL: fmt.Sprintf("https://github.com/%s/pull/%d", ciJobRepository, number),
	}); err != nil {
		t.Fatalf("write #%d at %s: %v", number, sha, err)
	}
}

// TestTwoWritersAndTheSettlerEndWithThePublishedStateDispatchComputes: two settlers, A and B, as
// two processes, each on a lease connection of its own. Writer 1 records a pass of k1 at H1 as the
// implementer, writer 2 retracts it, and the settles run in a fixed order, held at the settler's
// hooks. After each step the published state of k1 at H1 is the one a fresh computation gives and
// no mark of ACME-1 is left, and the events of k1 at H1 are yes, no, yes, no and no for fail.
func TestTwoWritersAndTheSettlerEndWithThePublishedStateDispatchComputes(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	a, b := rig.settler, rig.newSettler(t, "check states of a second process")
	settleOwedBy(t, rig, b)
	ctx := store.WithTransactionTracking(t.Context())
	var k1 []checkStatesEvent

	// 1. B publishes the first state of k1 at H1.
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
	wantEvents(t, "step 1", eventsOfCheck(settleBy(t, rig, b), "k1", eachHeadH1), stateWant{"k1", eachHeadH1, "no", "no_result"})
	wantPublishedIsComputed(t, "step 1", rig, "k1", eachHeadH1)

	// 2. Writer 1 records; A computes yes and is held; writer 2 retracts; B skips, since A holds
	// the lease; A publishes yes, and its next round computes no and publishes it.
	pass := eachHeadResult(t, rig.handler, rig.issue, "k1", "implementer", "pass", eachHeadH1)
	hold := newHoldOnce(t)
	a.hooks.afterCompute = hold.hook
	after := lastEventID(t, rig.database)
	settled := make(chan [3]bool, 1)
	go func() {
		ran, failed, owed := a.settle(ctx, rig.issue)
		settled <- [3]bool{ran, failed, owed}
	}()
	hold.await(t, "A's settle")
	if retracted := retractAsPerson(t, rig.handler, pass, "Writer 2 retracts the pass."); retracted.Code != http.StatusCreated {
		t.Fatalf("writer 2's retraction: status=%d body=%s", retracted.Code, retracted.Body.String())
	}
	if ran, _, _ := b.settle(ctx, rig.issue); ran {
		t.Fatal("B settled ACME-1 while A held its lease")
	}
	hold.release()
	if outcome := <-settled; outcome != [3]bool{true, false, false} {
		t.Fatalf("A's settle answered ran, failed, owed = %v, want a settle that ran and left nothing", outcome)
	}
	a.hooks.afterCompute = nil
	step := eventsOfCheck(checkStateEventsAfter(t, rig.database, after), "k1", eachHeadH1)
	wantEvents(t, "step 2", step, stateWant{"k1", eachHeadH1, "yes", ""}, stateWant{"k1", eachHeadH1, "no", "no_result"})
	k1 = append(k1, step...)
	wantPublishedIsComputed(t, "step 2", rig, "k1", eachHeadH1)

	// 3. Writer 1 records again and A settles it; writer 2 retracts and B settles it.
	pass = eachHeadResult(t, rig.handler, rig.issue, "k1", "implementer", "pass", eachHeadH1)
	step = eventsOfCheck(settleBy(t, rig, a), "k1", eachHeadH1)
	wantEvents(t, "step 3, A", step, stateWant{"k1", eachHeadH1, "yes", ""})
	k1 = append(k1, step...)
	if retracted := retractAsPerson(t, rig.handler, pass, "Writer 2 retracts the pass again."); retracted.Code != http.StatusCreated {
		t.Fatalf("writer 2's second retraction: status=%d body=%s", retracted.Code, retracted.Body.String())
	}
	step = eventsOfCheck(settleBy(t, rig, b), "k1", eachHeadH1)
	wantEvents(t, "step 3, B", step, stateWant{"k1", eachHeadH1, "no", "no_result"})
	k1 = append(k1, step...)
	wantPublishedIsComputed(t, "step 3", rig, "k1", eachHeadH1)

	// 4. Writers 1 and 2 record and retract before any settle: one compute, no event.
	pass = eachHeadResult(t, rig.handler, rig.issue, "k1", "implementer", "pass", eachHeadH1)
	if retracted := retractAsPerson(t, rig.handler, pass, "Writer 2 retracts before a settle."); retracted.Code != http.StatusCreated {
		t.Fatalf("writer 2's third retraction: status=%d body=%s", retracted.Code, retracted.Body.String())
	}
	if events := settleBy(t, rig, a); len(events) != 0 {
		t.Fatalf("step 4: a record and its retraction settled together published %v, want nothing", eventLines(events))
	}
	wantPublishedIsComputed(t, "step 4", rig, "k1", eachHeadH1)

	// 5. Writer 3's fail of k1, inserted in a transaction held open, draws the lower mark id; the
	// implementer's pass of k2 commits. A reads the mark of k2 alone, computes and is held while
	// writer 3 commits, publishes k2, and its next round publishes k1, no for fail.
	writer3, err := rig.database.Pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin writer 3: %v", err)
	}
	t.Cleanup(func() { _ = writer3.Rollback(context.Background()) })
	writerFail(t, rig, func(ctx context.Context, sql string, args ...any) error {
		_, err := writer3.Exec(ctx, sql, args...)
		return err
	}, "k1", eachHeadH1)
	eachHeadResult(t, rig.handler, rig.issue, "k2", "implementer", "pass", eachHeadH1)
	hold = newHoldOnce(t)
	a.hooks.afterCompute = hold.hook
	after = lastEventID(t, rig.database)
	go func() {
		ran, failed, owed := a.settle(ctx, rig.issue)
		settled <- [3]bool{ran, failed, owed}
	}()
	hold.await(t, "A's settle of k2")
	if err := writer3.Commit(context.Background()); err != nil {
		t.Fatalf("commit writer 3: %v", err)
	}
	hold.release()
	if outcome := <-settled; outcome != [3]bool{true, false, false} {
		t.Fatalf("A's settle in step 5 answered ran, failed, owed = %v, want a settle that ran and left nothing", outcome)
	}
	a.hooks.afterCompute = nil
	events := checkStateEventsAfter(t, rig.database, after)
	wantEvents(t, "step 5", events, stateWant{"k2", eachHeadH1, "yes", ""}, stateWant{"k1", eachHeadH1, "no", "fail"})
	k1 = append(k1, eventsOfCheck(events, "k1", eachHeadH1)...)
	wantPublishedIsComputed(t, "step 5", rig, "k1", eachHeadH1)

	wantEvents(t, "the events of k1 at H1 after step 1", k1,
		stateWant{"k1", eachHeadH1, "yes", ""}, stateWant{"k1", eachHeadH1, "no", "no_result"},
		stateWant{"k1", eachHeadH1, "yes", ""}, stateWant{"k1", eachHeadH1, "no", "no_result"},
		stateWant{"k1", eachHeadH1, "no", "fail"})
}

// TestAMarkThatNoStartSettledIsSettledByTheBackgroundPass: the loop runs with a wake that nothing
// signals, as in a process whose listener lost the notification of a write, and with a sweep of
// 200 ms. A fail the tester records after the loop's start is published by the pass at its due
// time within 2 s, and no mark is left. A loop started after the write, as a process starts after
// a crash, publishes it in its first pass.
func TestAMarkThatNoStartSettledIsSettledByTheBackgroundPass(t *testing.T) {
	t.Run("a write after the loop's start", func(t *testing.T) {
		rig := newCheckStatesRig(t, checkStatesTwoChecks)
		writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
		settleOwed(t, rig)
		rig.settler.schedule.sweep = 200 * time.Millisecond
		runCheckStatesLoop(t, rig.settler, make(chan struct{}))
		// The loop's first pass finds nothing due; the write comes after it.
		time.Sleep(2 * rig.settler.schedule.minGap)
		after := lastEventID(t, rig.database)
		eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "fail", eachHeadH1)
		events := awaitEvents(t, rig, after, 2*time.Second, func(events []checkStatesEvent) bool { return len(events) > 0 })
		wantEvents(t, "the background pass", events, stateWant{"k1", eachHeadH1, "no", "fail"})
		if left := marksOf(t, rig.database, rig.issue); left != 0 {
			t.Fatalf("the background pass left %d marks of %s", left, rig.issue)
		}
	})
	t.Run("a write before the loop's start", func(t *testing.T) {
		rig := newCheckStatesRig(t, checkStatesTwoChecks)
		writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
		settleOwed(t, rig)
		after := lastEventID(t, rig.database)
		eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "fail", eachHeadH1)
		// A sweep far past the wait, so only the loop's first pass can publish within it.
		rig.settler.schedule.sweep = 10 * time.Minute
		runCheckStatesLoop(t, rig.settler, make(chan struct{}))
		events := awaitEvents(t, rig, after, 2*time.Second, func(events []checkStatesEvent) bool { return len(events) > 0 })
		wantEvents(t, "the first pass", events, stateWant{"k1", eachHeadH1, "no", "fail"})
		if left := marksOf(t, rig.database, rig.issue); left != 0 {
			t.Fatalf("the first pass left %d marks of %s", left, rig.issue)
		}
	})
}

// TestTheWritersCommitStartsTheSettle: the loop runs on a real listener hub on the test database,
// with a sweep of 10 minutes, so only a notification can start a pass. A recorded result publishes
// its event within 2 s. A result inserted in a transaction the test rolls back leaves no mark and
// publishes nothing.
func TestTheWritersCommitStartsTheSettle(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
	settleOwed(t, rig)
	wake := listenForCheckStates(t, rig)
	rig.settler.schedule.sweep = 10 * time.Minute
	runCheckStatesLoop(t, rig.settler, wake)
	// The loop's first pass and the pass the hub's signal after its LISTEN earned find nothing.
	time.Sleep(4 * rig.settler.schedule.minGap)

	after := lastEventID(t, rig.database)
	eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "fail", eachHeadH1)
	events := awaitEvents(t, rig, after, 2*time.Second, func(events []checkStatesEvent) bool { return len(events) > 0 })
	wantEvents(t, "the commit's notification", events, stateWant{"k1", eachHeadH1, "no", "fail"})

	after = lastEventID(t, rig.database)
	rolled, err := rig.database.Pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin the rolled-back writer: %v", err)
	}
	writerFail(t, rig, func(ctx context.Context, sql string, args ...any) error {
		_, err := rolled.Exec(ctx, sql, args...)
		return err
	}, "k2", eachHeadH1)
	if err := rolled.Rollback(context.Background()); err != nil {
		t.Fatalf("roll the writer back: %v", err)
	}
	if left := marksOf(t, rig.database, rig.issue); left != 0 {
		t.Fatalf("a rolled-back result left %d marks of %s", left, rig.issue)
	}
	if events := awaitEvents(t, rig, after, 2*time.Second, func(events []checkStatesEvent) bool { return len(events) > 0 }); len(events) != 0 {
		t.Fatalf("a rolled-back result published %v", eventLines(events))
	}
}

// TestAWriteOfTheReleaseBeforeIsPublished: a pull_request_heads row and a check_results row
// inserted with plain SQL, as the release before this one writes them with no code of this build,
// each publish their states: the triggers mark the issue whoever wrote the row.
func TestAWriteOfTheReleaseBeforeIsPublished(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	settleOwed(t, rig)
	if _, err := rig.database.Pool.Exec(context.Background(), `
		insert into pull_request_heads (repo, number, head_sha, head_ref, base_ref, state, issue_key, event_at, checked_at)
		values ($1, 70, $2, 'legion/work', 'main', 'open', $3, now(), now())`, ciJobRepository, eachHeadH1, rig.issue); err != nil {
		t.Fatalf("insert the head of #70: %v", err)
	}
	wantEventSet(t, "a head row of the release before", settleOwed(t, rig),
		stateWant{"k1", eachHeadH1, "no", "no_result"}, stateWant{"k2", eachHeadH1, "no", "no_result"})

	writerFail(t, rig, func(ctx context.Context, sql string, args ...any) error {
		_, err := rig.database.Pool.Exec(ctx, sql, args...)
		return err
	}, "k1", eachHeadH1)
	wantEvents(t, "a result row of the release before", settleOwed(t, rig), stateWant{"k1", eachHeadH1, "no", "fail"})
}

// TestOneEventForEachChangeOfAStateAndNoneWhenAResultChangesNoState: the settler settles after
// each write. The implementer's pass of k1 at H1 publishes one event; a second pass of the
// implementer, which leaves the state yes from the implementer, publishes none; the tester's pass
// changes the roles and publishes one; a fail of k2 publishes one, and a second fail of k2 none.
func TestOneEventForEachChangeOfAStateAndNoneWhenAResultChangesNoState(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
	settleOwed(t, rig)
	var counts []int
	for _, write := range []struct{ check, role, verdict string }{
		{"k1", "implementer", "pass"}, {"k1", "implementer", "pass"}, {"k1", "tester", "pass"},
		{"k2", "tester", "fail"}, {"k2", "tester", "fail"},
	} {
		eachHeadResult(t, rig.handler, rig.issue, write.check, write.role, write.verdict, eachHeadH1)
		counts = append(counts, len(settleOwed(t, rig)))
	}
	if want := []int{1, 0, 1, 1, 0}; !slices.Equal(counts, want) {
		t.Fatalf("the writes published %v events, want %v", counts, want)
	}
}

// TestEveryTableAStateReadsHasItsTrigger is the inventory of the writes that mark an issue owed:
// each table the state of a check at a head is computed from holds an enabled trigger whose
// function marks the issue (check_states_owe_*). A table that a later change adds to the state's
// computation joins this list.
func TestEveryTableAStateReadsHasItsTrigger(t *testing.T) {
	database := storetest.Open(t)
	tables := []string{"check_results", "ci_check_results", "check_result_retractions", "ci_check_result_retractions", "document_blocks", "pull_request_heads"}
	rows, err := database.Pool.Query(context.Background(), `
		select distinct c.relname from pg_trigger g
		join pg_class c on c.oid = g.tgrelid
		join pg_proc p on p.oid = g.tgfoid
		where not g.tgisinternal and g.tgenabled <> 'D' and p.proname like 'check\_states\_owe\_%' and c.relname = any($1)`, tables)
	if err != nil {
		t.Fatalf("read the triggers: %v", err)
	}
	var marked []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("read a trigger's table: %v", err)
		}
		marked = append(marked, table)
	}
	rows.Close()
	for _, table := range tables {
		if !slices.Contains(marked, table) {
			t.Errorf("%s holds no enabled trigger of a check_states_owe_ function: a write to it changes a state and marks no issue owed", table)
		}
	}
}

// TestATestHandoffComesAfterTheStatesItsResultsCaused: with the loop running, the tester records a
// pass of k1 at H1 and at once writes its test handoff. The handoff answers after the settle: the
// event of k1 at H1 has a sequence below handoff.recorded's, and the handoff answers its serial as
// check_states_serial. A plan handoff waits for nothing and answers no serial. With the loop
// stopped and a fence of 1 s, a test handoff after a recorded result answers 503
// CHECK_STATES_UNSETTLED and records nothing, and a mark due 5 minutes on, as a failed read
// leaves, does not hold it.
func TestATestHandoffComesAfterTheStatesItsResultsCaused(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
	settleOwed(t, rig)

	// The settle of the tester's result is held after it reads its marks until the handoff's
	// fence is waiting, so a fence that did not wait would answer the serial from before it.
	var gated atomic.Bool
	hold := newHoldOnce(t)
	rig.settler.hooks.afterMarks = func(issue string) {
		if gated.Load() {
			hold.hook(issue)
		}
	}
	fenceWaits := make(chan struct{}, 1)
	rig.served.checkStatesFenceWaits = func(string) {
		select {
		case fenceWaits <- struct{}{}:
		default:
		}
	}
	wake := listenForCheckStates(t, rig)
	stop := runCheckStatesLoop(t, rig.settler, wake)
	time.Sleep(4 * rig.settler.schedule.minGap)

	gated.Store(true)
	eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH1)
	hold.await(t, "the settle of the tester's pass")
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- postHandoff(t, rig.handler, rig.issue, "test", `{"tested":"H1"}`) }()
	var response *httptest.ResponseRecorder
	select {
	case response = <-answered:
		hold.release()
	case <-fenceWaits:
		hold.release()
		select {
		case response = <-answered:
		case <-time.After(10 * time.Second):
			t.Fatal("the test handoff did not answer within 10s of the settle")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the test handoff neither answered nor waited within 10s")
	}
	if response.Code != http.StatusCreated {
		t.Fatalf("the test handoff: status=%d body=%s", response.Code, response.Body.String())
	}
	handoff := decodeBody[model.HandoffSummary](t, response)
	events := awaitEvents(t, rig, 0, 2*time.Second, func(events []checkStatesEvent) bool {
		return len(eventsOfCheck(events, "k1", eachHeadH1)) == 2
	})
	passed := eventsOfCheck(events, "k1", eachHeadH1)
	if len(passed) != 2 || passed[1].state() != "yes" {
		t.Fatalf("the events of k1 at H1 are %v, want no and then yes", eventLines(passed))
	}
	var recordedSeq int64
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select seq from events where issue_key = $1 and type = 'handoff.recorded' and payload->>'handoff_id' = $2`,
		rig.issue, handoff.ID).Scan(&recordedSeq); err != nil {
		t.Fatalf("read handoff.recorded of %s: %v", handoff.ID, err)
	}
	read := readHandoffJSON(t, rig, handoff.ID)
	serial, held := read["check_states_serial"]
	if passed[1].Seq >= recordedSeq || !held || string(serial) != strconv.FormatInt(passed[1].Payload.Serial, 10) {
		t.Fatalf("the state event of the tester's pass has seq %d and serial %d, and handoff.recorded seq %d with check_states_serial %s; want the state event first and the serial its own",
			passed[1].Seq, passed[1].Payload.Serial, recordedSeq, serial)
	}

	planned := postHandoff(t, rig.handler, rig.issue, "plan", `{"plan":"one"}`)
	if planned.Code != http.StatusCreated {
		t.Fatalf("the plan handoff: status=%d body=%s", planned.Code, planned.Body.String())
	}
	if _, held := readHandoffJSON(t, rig, decodeBody[model.HandoffSummary](t, planned).ID)["check_states_serial"]; held {
		t.Fatal("the plan handoff answers a check_states_serial")
	}

	stop()
	rig.served.checkStatesSchedule.fenceWait = time.Second
	eachHeadResult(t, rig.handler, rig.issue, "k2", "tester", "pass", eachHeadH1)
	refused := postHandoff(t, rig.handler, rig.issue, "test", `{"tested":"H1"}`)
	if code, _ := refusalOf(t, refused); refused.Code != http.StatusServiceUnavailable || code != "CHECK_STATES_UNSETTLED" {
		t.Fatalf("a test handoff with the settler stopped: status=%d body=%s, want 503 CHECK_STATES_UNSETTLED", refused.Code, refused.Body.String())
	}
	var tests int
	if err := rig.database.Pool.QueryRow(context.Background(),
		`select count(*) from handoffs where issue_key = $1 and phase = 'test'`, rig.issue).Scan(&tests); err != nil {
		t.Fatalf("count the test handoffs: %v", err)
	}
	if tests != 1 {
		t.Fatalf("%s holds %d test handoffs after the refusal, want the one written before it", rig.issue, tests)
	}

	settleOwed(t, rig)
	if _, err := rig.database.Pool.Exec(context.Background(), `
		insert into check_states_owed (issue_key, due_at, failures) values ($1, now() + interval '5 minutes', 1)`, rig.issue); err != nil {
		t.Fatalf("leave a retry mark: %v", err)
	}
	if again := postHandoff(t, rig.handler, rig.issue, "test", `{"tested":"H1"}`); again.Code != http.StatusCreated {
		t.Fatalf("a test handoff beside a mark due 5 minutes on: status=%d body=%s, want 201", again.Code, again.Body.String())
	}
}

// readHandoffJSON is GET /api/v1/handoffs/{id}'s answer, member by member.
func readHandoffJSON(t *testing.T, rig *checkStatesRig, id string) map[string]json.RawMessage {
	t.Helper()
	response := dispatchRequest(t, rig.handler, http.MethodGet, "/api/v1/handoffs/"+id, nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read handoff %s: status=%d body=%s", id, response.Code, response.Body.String())
	}
	return decodeBody[map[string]json.RawMessage](t, response)
}

// TestTheSerialCountsEachStateEventOfTheIssue: the events of two heads carry the serials 1, 2, 3
// and on, in the order the issue's log holds them, with no gap and no repeat.
func TestTheSerialCountsEachStateEventOfTheIssue(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH1, "open", rig.issue)
	writeHead(t, rig.database, ciJobRepository, 71, eachHeadH3, "open", rig.issue)
	settleOwed(t, rig)
	eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH1)
	settleOwed(t, rig)
	eachHeadResult(t, rig.handler, rig.issue, "k2", "tester", "fail", eachHeadH3)
	settleOwed(t, rig)
	writeHead(t, rig.database, ciJobRepository, 71, eachHeadH2, "open", rig.issue)
	settleOwed(t, rig)

	var serials []int64
	events := checkStateEventsAfter(t, rig.database, 0)
	slices.SortFunc(events, func(a, b checkStatesEvent) int { return int(a.Seq - b.Seq) })
	for _, event := range events {
		serials = append(serials, event.Payload.Serial)
	}
	if len(serials) < 8 {
		t.Fatalf("the two heads published %d events, want at least 8: %v", len(serials), eventLines(events))
	}
	for index, serial := range serials {
		if serial != int64(index+1) {
			t.Fatalf("the serials in the log's order are %v, want 1 to %d", serials, len(serials))
		}
	}
}

// TestAHeadWithNoCheckGetsTheSetForm: ACME-2 has no Verification document and #80 open at H5. One
// settle publishes one event, the set form, naming no check and no document, and the stored set
// row holds no document either. A second write to #80 that moves no head publishes nothing. An
// issue whose Verification document holds only an after_merge check gets the same set form, naming
// that document.
func TestAHeadWithNoCheckGetsTheSetForm(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	settleOwed(t, rig)
	second := rig.fileIssue(t, "With no Verification document")
	writeHead(t, rig.database, ciJobRepository, 80, checkStatesH5, "open", second)
	events := settleOwed(t, rig)
	wantEvents(t, "a head of an issue with no check", events, stateWant{"", checkStatesH5, "", ""})
	set := events[0].Payload
	if events[0].Issue != second || set.Issue != second || set.CheckArtifactID != nil || len(set.Checks) != 0 ||
		set.CheckVersion != nil || !slices.Equal(set.PullRequests, []int{80}) || set.Repo != ciJobRepository {
		t.Fatalf("the set form of %s is %s, want no document, no check, at %s of #80", second, events[0].Raw, checkStatesH5)
	}
	if !strings.Contains(string(events[0].Raw), `"checks": []`) && !strings.Contains(string(events[0].Raw), `"checks":[]`) {
		t.Fatalf("the stored set form %s does not write checks as an empty list", events[0].Raw)
	}
	row, held := publishedRow(t, rig.database, second, ciJobRepository, checkStatesH5, "")
	if !held || row.checkArtifactID != nil || row.state != nil {
		t.Fatalf("the stored set row of %s at %s is %+v (held %v), want one with no document and no state", second, checkStatesH5, row, held)
	}

	upsertHead(t, rig, 80, checkStatesH5, second, "A new title")
	if _, err := rig.database.Pool.Exec(context.Background(), `select check_states_owe($1)`, second); err != nil {
		t.Fatalf("mark %s owed: %v", second, err)
	}
	if events := settleOwed(t, rig); len(events) != 0 {
		t.Fatalf("a write to #80 that moves no head published %v", eventLines(events))
	}

	third := rig.fileIssue(t, "With an after_merge check alone")
	document := uploadDocument(t, rig.handler, "/api/v1/issues/"+third, "verification.md",
		":::check{#k9 when=\"after_merge\"}\nProduction answers at the merged commit.\n:::\n").Artifact.ID
	writeHead(t, rig.database, ciJobRepository, 81, checkStatesH6, "open", third)
	events = settleOwed(t, rig)
	wantEvents(t, "a head of an issue with an after_merge check alone", events, stateWant{"", checkStatesH6, "", ""})
	if got := events[0].Payload; got.CheckArtifactID == nil || *got.CheckArtifactID != document || len(got.Checks) != 0 {
		t.Fatalf("the set form of %s is %s, want document %s and no check", third, events[0].Raw, document)
	}
}

// TestTheLeasePoolCountsTheCheckStatesLoop: the lease pool's callers are complete with the
// reservations cmd/dispatch's main makes, read from its source, beside the intakes' own, and
// incomplete with the check states settler's taken out.
func TestTheLeasePoolCountsTheCheckStatesLoop(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "..", "..", "cmd", "dispatch", "main.go"), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse cmd/dispatch/main.go: %v", err)
	}
	var names []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		if name, ok := call.Fun.(*ast.Ident); !ok || name.Name != "reserve" {
			return true
		}
		if literal, ok := call.Args[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
			if value, err := strconv.Unquote(literal.Value); err == nil {
				names = append(names, value)
			}
		}
		return true
	})
	if len(names) == 0 {
		t.Fatal("cmd/dispatch/main.go makes no reserve(\"...\") call the test can read")
	}
	if !slices.Contains(names, "check states") {
		t.Fatalf("cmd/dispatch/main.go reserves %v, and no connection for the check states settler", names)
	}
	reserveAll := func(skip string) error {
		callers, err := storetest.Open(t).Pool.Leases()
		if err != nil {
			t.Fatalf("open the lease pool: %v", err)
		}
		for _, name := range names {
			if name == skip {
				continue
			}
			if _, err := callers.For(name); err != nil {
				t.Fatalf("reserve %q: %v", name, err)
			}
		}
		if _, err := delivery.ReserveIntakeLeases(callers); err != nil {
			t.Fatalf("reserve the intakes' callers: %v", err)
		}
		return callers.Complete()
	}
	if err := reserveAll(""); err != nil {
		t.Fatalf("the build's reservations do not complete the lease pool: %v", err)
	}
	if err := reserveAll("check states"); err == nil {
		t.Fatal("the reservations without the check states settler's complete the lease pool: its connection is counted for nothing")
	}
}
