package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/blockers"
	"github.com/sjawhar/legion/daemon/internal/capabilities"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testgolden"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden fixture this package pins")

// stateEventsGoldenName is the state lane's daemon API fixture, the cross-language pin of the lane:
// Go writes it (`-update`), `packages/contracts/src/legion-api.test.ts` parses every message of it
// through legionStateMessage and every state through LegionStateResponse, and Dispatch's intake
// test (packages/envoy/internal/dispatch/legion) applies its messages and reads its states, holding
// the rows the two write to each other.
const stateEventsGoldenName = "state-events.json"

// stateEventsGolden is the fixture: the steps a daemon's records are driven through, in order.
type stateEventsGolden struct {
	Steps []stateEventsStep `json:"steps"`
}

// stateEventsStep is one step of the fixture: what was done, every message the state lane
// published for it as the Envoy listener's publish route stores its envelope, and the document
// GET /legion/v1/state served once the lane had published them.
type stateEventsStep struct {
	Name     string             `json:"name"`
	Messages []listenerEnvelope `json:"messages"`
	State    json.RawMessage    `json:"state"`
}

// stateGoldenStart is the fixed clock's instant at the boot. Each step runs one minute after the
// one before it, and the seed one minute before the boot.
var stateGoldenStart = time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC)

// stateGoldenSchemaVersion is the schema version the fixture's states carry: the version of the
// migration that gave the daemon its state events (20261118164019_state_events). A state serves the
// newest migration's version, which every later migration moves, and no reader of the fixture reads
// it, so the file carries this one rather than change with each migration.
const stateGoldenSchemaVersion = 20261118164019

// stateGoldenSettingsFile is the legion.yaml of the fixture's daemon: a tmux daemon at every default
// running the workflow of project LEGION, which is what a daemon that publishes state events is
// (a daemon without the workflow publishes none), admitting one root at a time. Every path is an
// absolute literal and every host a placeholder, so the settings do not change with the machine.
// Each App takes its tokens from a token_command, which the loader accepts without running, so the
// file holds no private key, not even a placeholder one.
const stateGoldenSettingsFile = `project: LEGION
state_dir: /var/lib/legion
admission_cap: 1
postgres_dsn: postgres://legion@127.0.0.1:5432/legion
dispatch_url: https://dispatch.internal.example
dispatch_token_file: /var/run/legion/DISPATCH_TOKEN
envoy_url: http://envoy-listener.internal.example:9020
nats_urls: [nats://nats.internal.example:4222]
projects:
  LEGION: {}
github_apps:
  implement:
    app_id: "1001"
    token_command: /usr/local/bin/app-token implement
  review:
    app_id: "1002"
    token_command: /usr/local/bin/app-token review
`

// stateGoldenRig is a stateRig whose writes run on a fixed clock: the record store and the claim
// store of project LEGION, both with the state events on under the run stateTestRun, and
// GET /legion/v1/state served over the same database by the daemon's own state source, configured
// from stateGoldenSettingsFile as the daemon's serve configures it.
type stateGoldenRig struct {
	*stateRig
	// scoped is the stateRig's record store scoped to the project, as the workflow writes through
	// it (projectRecords), with no wake: record drains the state lane itself.
	scoped   projectRecords
	claims   *store.Store
	workflow *workflowRuntime
	state    *httptest.Server
	cfg      config.Config
	// now is the fixed clock: every time a write is handed is now.
	now time.Time
	// windows is, in order, when the seed and each step began on the database's clock, and the
	// fixed instant it ran at (begin).
	windows []stateGoldenWindow
	// published is how many publishes the steps before this one took.
	published int
	// seq is the last sequence published so far.
	seq int64
}

// stateGoldenWindow is one step on both clocks: live, the database's clock as it began, and fixed,
// the instant the rig's clock held through it.
type stateGoldenWindow struct {
	live, fixed time.Time
}

func newStateGoldenRig(t *testing.T) *stateGoldenRig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legion.yaml")
	if err := os.WriteFile(path, []byte(stateGoldenSettingsFile), 0o600); err != nil {
		t.Fatalf("write legion.yaml: %v", err)
	}
	cfg, err := config.Load(path, func(string) string { return "" })
	if err != nil {
		t.Fatalf("load legion.yaml: %v", err)
	}
	r := &stateGoldenRig{stateRig: newStateRig(t), cfg: cfg}
	r.scoped = projectRecords{Store: r.records, project: cfg.Project}
	st, err := store.Open(context.Background(), r.pool.Config().ConnString())
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(st.Close)
	st.PublishState(cfg.Project, stateTestRun, nil)
	r.claims = st
	r.workflow = &workflowRuntime{pool: r.pool, records: r.scoped, dispatchProject: cfg.Project}
	view := &source{
		store: st, records: r.records, project: cfg.Project, projectToken: "legion", runtime: cfg.Runtime.Name,
		admissionCap: cfg.AdmissionCap, startedAt: stateGoldenStart,
		deployment:  func() capabilities.Deployment { return capabilities.Deployment{} },
		reports:     func() map[claim.Token]capabilities.Report { return nil },
		gatedSteps:  cfg.Gates.Steps(),
		experiments: cfg.ExperimentsDeclared(),
		settings:    api.SettingsOf(cfg),
	}
	r.state = httptest.NewServer(api.NewServer("127.0.0.1", 0, api.Options{State: view, StateTransactions: st, Log: quietLogger()}).Handler)
	t.Cleanup(r.state.Close)
	return r
}

// begin starts a step, or the seed, at the fixed instant at: the clock holds at from here on, and
// the database's own clock now is where the step begins on it.
func (r *stateGoldenRig) begin(t *testing.T, at time.Time) {
	t.Helper()
	var live time.Time
	if err := r.pool.QueryRow(context.Background(), `select clock_timestamp()`).Scan(&live); err != nil {
		t.Fatalf("read the database's clock: %v", err)
	}
	r.now = at
	r.windows = append(r.windows, stateGoldenWindow{live: live, fixed: at})
}

// stamped is the fixed instant of a time the database stamped as it ran, which is the start of the
// transaction that wrote it (now()): the instant of the step that ran that transaction, since each
// step begins its transaction at its own clock's instant.
func (r *stateGoldenRig) stamped(t *testing.T, live time.Time) time.Time {
	t.Helper()
	for i := len(r.windows) - 1; i >= 0; i-- {
		if !live.Before(r.windows[i].live) {
			return r.windows[i].fixed
		}
	}
	t.Fatalf("the database stamped %s, before the seed began at %s", live, r.windows[0].live)
	return time.Time{}
}

// seed writes the records the daemon boots over through a record store with no state events, as a
// daemon's records stand before a boot turns them on: what the lane publishes is what the steps
// change, and the boot's state is where a reader of the lane starts.
func (r *stateGoldenRig) seed(t *testing.T, write func(ctx context.Context, tx pgx.Tx, records *record.Postgres) error) {
	t.Helper()
	if err := pgx.BeginFunc(context.Background(), r.pool, func(tx pgx.Tx) error {
		return write(context.Background(), tx, record.NewStore())
	}); err != nil {
		t.Fatalf("seed the records: %v", err)
	}
}

// pass commits write in one transaction that ends as every admission pass ends a fact (admit's
// queueAdmissionBlock): the project's admission block judged afresh at the clock's instant, through
// the one judgement the state route serves it from, and written where it moved, which queues its
// state event in the same transaction.
func (r *stateGoldenRig) pass(t *testing.T, write func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := write(ctx, tx); err != nil {
			return err
		}
		judged, err := blockers.JudgeAdmission(ctx, tx, r.scoped, r.cfg.Project, r.cfg.Gates.Steps(), r.cfg.AdmissionCap, r.now)
		if err != nil {
			return err
		}
		_, err = r.scoped.QueueAdmissionBlock(ctx, tx, r.cfg.Project, stateTestRun, judged.Block)
		return err
	}); err != nil {
		t.Fatalf("commit the pass: %v", err)
	}
}

// architect is the architect's claim of issue, the root of its own tree, at state under session.
func (r *stateGoldenRig) architect(t *testing.T, issue string, state supervise.ClaimState, session string) supervise.Claim {
	t.Helper()
	token, err := claim.NewToken("legion", issue, claim.RoleArchitect)
	if err != nil {
		t.Fatalf("the token of %s's architect: %v", issue, err)
	}
	return supervise.Claim{Token: token, Project: "legion", Tree: issue, Issue: issue, Role: claim.RoleArchitect, Generation: 1,
		State: state, Session: session}
}

// stateGoldenID is the id the listener's route would mint for one envelope (id.New, 32 hex digits),
// fixed by what it is the id of and the message's first sequence, so every envelope of the fixture
// has ids of its own.
func stateGoldenID(of string, firstSeq int64) string {
	sum := sha256.Sum256([]byte(of + ":" + strconv.FormatInt(firstSeq, 10)))
	return hex.EncodeToString(sum[:16])
}

// record runs the state lane until it has published every row the step committed and answers the
// step: each message the lane published, held to the contract as Dispatch reads it (decodeState),
// as the listener's publish route stores its envelope, and the state GET /legion/v1/state serves
// after it, with the step's times on the fixed clock.
//
// The times the database stamps are put on the fixed clock as they related live. Each transaction
// of the step commits (the message's at, OutboxRow.StateAt) one millisecond after the one before
// it, the first one millisecond after the step's instant, which is when the transaction began; the
// envelope is issued at its commit's millisecond; a time the database stamped at a transaction's
// start (an issue's first recording, its tree's treeMovedAt) is the instant of the step that ran
// it (stamped); and the state is read one millisecond after the step's last commit, which is the
// instant its quietHours is measured at (record.QuietTrees). So within each step every write's own
// time comes before its commit, the commit before the state that holds it, and nothing after the
// next step's instant.
func (r *stateGoldenRig) record(t *testing.T, name string) stateEventsStep {
	t.Helper()
	r.drain(t)
	published := r.publisher.publishes()[r.published:]
	r.published += len(published)
	if len(published) == 0 {
		t.Fatalf("step %q published no state message", name)
	}
	step := stateEventsStep{Name: name, Messages: make([]listenerEnvelope, 0, len(published))}
	var commits []string
	for _, publish := range published {
		message, ok := publish.payload.(stateMessage)
		if !ok {
			t.Fatalf("step %q published a %T, want a state message", name, publish.payload)
		}
		if message.FirstSeq != r.seq+1 {
			t.Fatalf("step %q published %d-%d after sequence %d: the sequence runs on with no gap", name, message.FirstSeq, message.Seq, r.seq)
		}
		r.seq = message.Seq
		if len(commits) == 0 || commits[len(commits)-1] != message.At {
			commits = append(commits, message.At)
		}
		at := r.now.Add(time.Duration(len(commits)) * time.Millisecond)
		message.At = at.Format(stateAtLayout)
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("encode the state message as the listener's publisher does: %v", err)
		}
		decodeState(t, publish.topic, publish.message, publish.key, encoded)
		step.Messages = append(step.Messages, listenerEnvelope{
			EventID: stateGoldenID("event", message.FirstSeq), Source: "agent", SourceEventID: stateGoldenID("source event", message.FirstSeq),
			Topic: publish.topic, DedupeKey: publish.key, IssuedAt: at.UnixMilli(), PayloadSummary: publish.message,
			Payload: string(encoded), TraceID: stateGoldenID("trace", message.FirstSeq),
		})
	}
	read := r.now.Add(time.Duration(len(commits)+1) * time.Millisecond)

	response, err := http.Get(r.state.URL + "/legion/v1/state")
	if err != nil {
		t.Fatalf("GET the state: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GET the state = %d %s (%v), want the state", response.StatusCode, body, err)
	}
	// The state is rewritten through the type the route encodes, so it must come back from it as
	// the route served it, byte for byte.
	var state api.State
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode the state: %v", err)
	}
	if again, err := json.Marshal(state); err != nil || !bytes.Equal(append(again, '\n'), body) {
		t.Fatalf("the state does not encode again as served (%v):\n got: %s\nwant: %s", err, again, body)
	}
	if state.Daemon.StateSeq == nil || *state.Daemon.StateSeq != r.seq {
		t.Fatalf("after step %q the state serves daemon.stateSeq %v, want %d, the last sequence the step published", name, state.Daemon.StateSeq, r.seq)
	}
	state.Daemon.SchemaVersion = stateGoldenSchemaVersion
	// Left out: each contract bump would stale it, at a path check-daemon-api-contract.sh cannot name.
	state.Contract = nil
	for key, issue := range state.Issues {
		if issue.TreeMovedAt == nil {
			continue
		}
		moved := r.stamped(t, *issue.TreeMovedAt)
		quiet := max(0, read.Sub(moved)).Hours()
		issue.TreeMovedAt, issue.QuietHours = &moved, &quiet
		state.Issues[key] = issue
	}
	if step.State, err = json.Marshal(state); err != nil {
		t.Fatalf("encode the state: %v", err)
	}
	return step
}

// The fixture of the state lane (packages/contracts/fixtures/daemon-api/state-events.json): a
// daemon of project LEGION boots over a seeded store and its records are written through one step
// after another, each a write that queues a state event, and after each step the file holds every
// message the lane published for it, as the Envoy listener stores its envelope, and the state the
// route served once they were out. The seed is what the daemon recorded before this boot, written
// with no state events: LEGION-2, a root waiting in the line behind LEGION-8, which Dispatch shows
// in progress; LEGION-3, a root that shipped and still names its tree; and LEGION-8 and LEGION-9,
// Dispatch issues the daemon does not run. The cap is one slot.
//
// Every step writes as the daemon does: a fact's writes end with the admission pass's block
// (stateGoldenRig.pass), a claim's state is written by the claim store, and the boot event by the
// boot's own queueStateBoot. Each step is one transaction, so the state after it is the state its
// messages leave, and the fixture runs on a fixed clock under a fixed run id, so it is the same on
// every run (stateGoldenRig.record says how the database's own times are put on that clock).
func TestStateEventsGolden(t *testing.T) {
	r := newStateGoldenRig(t)
	ctx := context.Background()
	issue := func(key string, at phase.Phase, status, rank string) record.Issue {
		return record.Issue{Key: key, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION",
			Title: key, Tree: key, Phase: at, Generation: 1, Status: status, Rank: rank, HandedOver: true}
	}
	root, shipped := issue("LEGION-1", phase.Admitted, "in_progress", "A"), issue("LEGION-3", phase.Done, "done", "C")
	architect := r.architect(t, root.Key, supervise.StateLaunching, "")

	r.begin(t, stateGoldenStart.Add(-time.Minute))
	r.seed(t, func(ctx context.Context, tx pgx.Tx, records *record.Postgres) error {
		for _, seeded := range []record.Issue{issue("LEGION-2", phase.Admitted, "todo", "B"), shipped} {
			if err := records.PutIssue(ctx, tx, seeded); err != nil {
				return err
			}
		}
		for _, mirrored := range []record.DispatchIssueRow{
			{Key: "LEGION-2", Project: "LEGION", Status: "todo", Blockers: []string{"LEGION-8"}, Seq: 1},
			{Key: "LEGION-3", Project: "LEGION", Status: "done", Blockers: []string{}, Seq: 1},
			{Key: "LEGION-8", Project: "LEGION", Status: "in_progress", Blockers: []string{}, Seq: 1},
			{Key: "LEGION-9", Project: "LEGION", Status: "in_progress", Blockers: []string{}, Seq: 1},
		} {
			if err := records.PutDispatchIssue(ctx, tx, mirrored); err != nil {
				return err
			}
		}
		return nil
	})

	steps := []struct {
		name  string
		write func(t *testing.T)
	}{
		{"the daemon boots", func(t *testing.T) {
			if err := r.workflow.queueStateBoot(ctx, stateTestRun, api.SettingsOf(r.cfg), false); err != nil {
				t.Fatalf("queue the boot event: %v", err)
			}
			if _, err := r.claims.RecordBoot(ctx, r.cfg.Project, r.now); err != nil {
				t.Fatalf("record the boot: %v", err)
			}
		}},
		// A root handed to Legion while the slot is free is recorded and promoted by one fact: its
		// phase and tree, its slot and its tree's lifecycle, and the block it changes.
		{"a root is admitted", func(t *testing.T) {
			r.pass(t, func(ctx context.Context, tx pgx.Tx) error {
				if err := r.scoped.PutDispatchIssue(ctx, tx, record.DispatchIssueRow{Key: root.Key, Project: "LEGION", Status: "todo",
					Blockers: []string{}, Seq: 1}); err != nil {
					return err
				}
				if err := r.scoped.PutIssue(ctx, tx, root); err != nil {
					return err
				}
				if err := r.scoped.PutSlot(ctx, tx, record.Slot{Issue: root.Key, Index: 0, AdmittedAt: r.now}); err != nil {
					return err
				}
				_, err := r.scoped.OpenTreeLifecycle(ctx, tx, "legion", root.Key, treelifecycle.AuthorityWorkflow)
				return err
			})
		}},
		{"the root's architect claim is launching", func(t *testing.T) {
			admitted, err := r.claims.AdmitClaim(ctx, architect)
			if err != nil {
				t.Fatalf("admit the architect's claim: %v", err)
			}
			architect = admitted
		}},
		{"the root's architect claim is working", func(t *testing.T) {
			architect.State, architect.Session = supervise.StateWorking, "ses_legion_1_architect"
			if err := r.claims.PutClaim(ctx, architect); err != nil {
				t.Fatalf("write the architect's claim: %v", err)
			}
		}},
		{"the root's architect claim is idle", func(t *testing.T) {
			architect.State = supervise.StateIdle
			if err := r.claims.PutClaim(ctx, architect); err != nil {
				t.Fatalf("write the architect's claim: %v", err)
			}
		}},
		{"the root waits on a person", func(t *testing.T) {
			root.Phase, root.Awaiting = phase.AwaitingPerson, &record.Awaiting{Ask: "ask-1", Since: record.EventMark{Seq: 2, At: r.now}}
			r.pass(t, func(ctx context.Context, tx pgx.Tx) error { return r.scoped.PutIssue(ctx, tx, root) })
		}},
		{"the person answers the root", func(t *testing.T) {
			answered := r.now
			root.Awaiting.AnsweredAt, root.Awaiting.AnsweredBy, root.Awaiting.AnsweredDetail = &answered, record.AnsweredByAsk, "ask-1"
			r.pass(t, func(ctx context.Context, tx pgx.Tx) error { return r.scoped.PutIssue(ctx, tx, root) })
		}},
		// One pass takes the root's slot away for a wait and grants it again, as promotion does,
		// clearing the wait's mark: the slot the transaction commits is the one it began with.
		{"the root's slot is released for a wait and granted again in one transaction", func(t *testing.T) {
			r.pass(t, func(ctx context.Context, tx pgx.Tx) error {
				if err := r.scoped.ReleaseSlotForWait(ctx, tx, root.Key, r.now); err != nil {
					return err
				}
				if err := r.scoped.PutSlot(ctx, tx, record.Slot{Issue: root.Key, Index: 0, AdmittedAt: r.now}); err != nil {
					return err
				}
				return r.scoped.ClearReleasedForWait(ctx, tx, root.Key)
			})
		}},
		{"the shipped root's tree is cleared", func(t *testing.T) {
			shipped.Tree = ""
			r.pass(t, func(ctx context.Context, tx pgx.Tx) error { return r.scoped.PutIssue(ctx, tx, shipped) })
		}},
		// An operator's claim on an issue the workflow does not record lists the issue at
		// unrecorded (projection/project.go), so its first claim write names that phase too.
		{"a claim is launching on an issue the daemon does not record", func(t *testing.T) {
			if err := r.claims.PutClaim(ctx, r.architect(t, "LEGION-7", supervise.StateLaunching, "")); err != nil {
				t.Fatalf("write the claim of LEGION-7: %v", err)
			}
		}},
		{"a blocker link moves what the waiting root is blocked by", func(t *testing.T) {
			r.pass(t, func(ctx context.Context, tx pgx.Tx) error {
				return r.scoped.PutDispatchIssue(ctx, tx, record.DispatchIssueRow{Key: "LEGION-2", Project: "LEGION", Status: "todo",
					Blockers: []string{"LEGION-8", "LEGION-9"}, Seq: 2})
			})
		}},
	}
	var golden stateEventsGolden
	for i, step := range steps {
		r.begin(t, stateGoldenStart.Add(time.Duration(i)*time.Minute))
		step.write(t)
		golden.Steps = append(golden.Steps, r.record(t, step.name))
	}

	testgolden.Check(t, stateEventsGoldenName, golden, *updateGolden, "go test ./internal/daemon/ -run '^TestStateEventsGolden$' -update")
}
