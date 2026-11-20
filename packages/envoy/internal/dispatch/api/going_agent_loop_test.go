package api

// going_agent_loop_test.go holds a run as the person meets it: the agent's real loop
// (going/agent) runs in the test process, connected to the server over its real socket, and calls
// a fake chat model (going/agent/agenttest) that answers what each row scripts and counts its
// calls.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/going/agent"
	"github.com/sjawhar/envoy/going/agent/agenttest"
	"github.com/sjawhar/envoy/going/agentwire"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// goingScript is what a test's fake chat model answers, by the person's words of the run that
// asks: each row's reply to the n-th request of its run, from 1.
type goingScript struct {
	mu   sync.Mutex
	rows map[string]func(n int) agenttest.Reply
}

// set scripts the replies to the run whose person's words are words.
func (s *goingScript) set(words string, reply func(n int) agenttest.Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[words] = reply
}

// goingLoop is a fake chat model that answers each request as its script says, and the agent's
// loop working the rig's runs against it.
type goingLoop struct {
	fake   *agenttest.Model
	script *goingScript
	stop   func()
}

// newGoingLoop is the fake and the agent's loop, which stops when the test ends.
func newGoingLoop(t *testing.T, rig goingAgentRig) *goingLoop {
	t.Helper()
	loop := &goingLoop{script: &goingScript{rows: map[string]func(int) agenttest.Reply{}}}
	loop.fake = agenttest.New(t, func(request *agenttest.Request) agenttest.Reply {
		loop.script.mu.Lock()
		defer loop.script.mu.Unlock()
		for words, reply := range loop.script.rows {
			if request.Asks(words) {
				return reply(len(loop.fake.Asking(words)))
			}
		}
		return agenttest.Reply{Text: "No row of this test asks that."}
	})
	loop.start(rig)
	t.Cleanup(func() { loop.stop() })
	return loop
}

// start starts an agent's loop, a new instance, at the rig's socket. Its stop waits for the loop
// to return; the loop fails only where the server refuses its hello, which every row's run then
// shows by never ending.
func (l *goingLoop) start(rig goingAgentRig) {
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- agent.Run(ctx, rig.client, agent.Config{Model: l.fake.Model(), Slots: 8}) }()
	l.stop = sync.OnceFunc(func() {
		cancel()
		<-ran
	})
}

// toolCall is the reply that calls tool with arguments.
func toolCall(tool string, arguments map[string]any) agenttest.Reply {
	return agenttest.Reply{Calls: []agenttest.Call{{Name: tool, Arguments: arguments}}}
}

// answered is the reply that answers text and calls nothing.
func answered(text string) agenttest.Reply { return agenttest.Reply{Text: text} }

// goingLoopRun is a run a row starts, as the route that starts a run records one: login's words,
// at level, following continues where it is set, with what it already spent, granted to the
// header identity or, where token is set, to that agent token.
type goingLoopRun struct {
	login, words, level, continues, token string
	workingMS                             int64
}

// begin records run queued and announces it, and answers its id.
func (rig goingAgentRig) begin(t *testing.T, run goingLoopRun) string {
	t.Helper()
	id := uuid.NewString()
	if run.level == "" {
		run.level = "standard"
	}
	var continues *string
	if run.continues != "" {
		continues = &run.continues
	}
	rig.exec(t, `
		insert into going_runs (id, author_login, words, conversation_id, continues, level, mode, state, shape, created_at, budget_used)
		values ($1::uuid, $2, $3, coalesce((select coalesce(conversation_id, id) from going_runs where id = $4::uuid), $1::uuid), $4::uuid,
		  $5, 'ask', 'queued', $6, clock_timestamp(), jsonb_build_object('calls', 0, 'working_ms', $7::bigint, 'tokens', 0))
	`, id, run.login, run.words, continues, run.level, goingRunShape, run.workingMS)
	if run.token != "" {
		rig.exec(t, `insert into going_run_grants (run_id, kind, login, token_id) values ($1::uuid, 'token', $2, $3::uuid)`, id, run.login, run.token)
	} else {
		rig.exec(t, `insert into going_run_grants (run_id, kind, login) values ($1::uuid, 'header', $2)`, id, run.login)
	}
	rig.exec(t, `select pg_notify($1, 'ready')`, GoingRunsChannel)
	return id
}

// goingRunRecord is what a test reads of a run's row.
type goingRunRecord struct {
	state, reason string
	used          agentwire.Budget
}

// record reads run's row.
func (rig goingAgentRig) record(t *testing.T, run string) goingRunRecord {
	t.Helper()
	var record goingRunRecord
	var used []byte
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select state, coalesce(stop_reason, ''), budget_used from going_runs where id = $1::uuid
	`, run).Scan(&record.state, &record.reason, &used); err != nil {
		t.Fatalf("read run %s: %v", run, err)
	}
	if err := json.Unmarshal(used, &record.used); err != nil {
		t.Fatalf("read what run %s spent: %v", run, err)
	}
	return record
}

// ended waits up to within for run to leave queued and working, and answers its row.
func (rig goingAgentRig) ended(t *testing.T, run string, within time.Duration) goingRunRecord {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		record := rig.record(t, run)
		if record.state != "queued" && record.state != "working" {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is still %s after %s", run, record.state, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// toolResults is what the model was told of each tool call of run, in the order of its steps.
func (rig goingAgentRig) toolResults(t *testing.T, run string) []string {
	t.Helper()
	rows, err := rig.database.Pool.Query(context.Background(), `
		select body->>'result' from going_run_steps where run_id = $1::uuid and kind = 'tool' order by step
	`, run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var results []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
	}
	return results
}

// endFrame is the end frame of run's events.
func (rig goingAgentRig) endFrame(t *testing.T, run string) agentwire.EndFrame {
	t.Helper()
	var data []byte
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select data from going_run_events where run_id = $1::uuid and name = $2 order by sequence desc limit 1
	`, run, agentwire.FrameEnd).Scan(&data); err != nil {
		t.Fatalf("read run %s's end frame: %v", run, err)
	}
	var frame agentwire.EndFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

// TestTheAgentActsAsThePerson: the agent acts as the person who started the run, with a tool for
// each thing the person can read or change (check c12): every route of the table a person's
// credential can call is a row of a tool, or a route R12 leaves out; a run of A reads the Inbox
// through its tool, and the answer holds A's rows and no row of B's; a read of
// http://169.254.169.254/, and of an address in no word and no tool result, are refused; and a
// GitHub read of a repository no project maps is refused.
func TestTheAgentActsAsThePerson(t *testing.T) {
	t.Run("every route a person can call has a tool, or is left out with its reason", goingRouteInventory)

	rig := newGoingAgentRig(t, nil)
	loop := newGoingLoop(t, rig)
	// read runs a run of login's whose model makes call, then answers, and answers what the
	// model was told of the call.
	read := func(t *testing.T, login, words string, call map[string]any) string {
		t.Helper()
		loop.script.set(words, func(n int) agenttest.Reply {
			if n == 1 {
				return toolCall("call_tool", call)
			}
			return answered("Read.")
		})
		run := rig.begin(t, goingLoopRun{login: login, words: words})
		if record := rig.ended(t, run, time.Minute); record.state != "done" {
			t.Fatalf("the run ended %s with %q, want done", record.state, record.reason)
		}
		results := rig.toolResults(t, run)
		if len(results) != 1 {
			t.Fatalf("the run saved %d tool results, want 1: %q", len(results), results)
		}
		if told := loop.fake.Asking(words); len(told) != 2 || len(told[1].ToolResults()) != 1 {
			t.Fatalf("the model was not told the result of the call: %d model calls", len(told))
		}
		return results[0]
	}

	t.Run("a run of A reads A's Inbox through its tool, with no row of B's", func(t *testing.T) {
		rig.ask(t, rig.issue(t, "Alice's roof"), "Which tiles does the roof take?")
		bobs := decodeBody[model.Issue](t, rig.fixture(t, "bob", http.MethodPost, "/api/v1/issues", map[string]any{
			"project": "DEMO", "title": "Bob's cellar", "force": true,
		})).Key
		rig.ask(t, bobs, "How deep is the cellar?")
		result := read(t, "alice", "show me my inbox", map[string]any{"tool": "dispatch_inbox", "arguments": map[string]any{"assignee": "me"}})
		if !strings.Contains(result, "status 200") || !strings.Contains(result, "Which tiles does the roof take?") {
			t.Errorf("alice's run read her Inbox as %q, want 200 with her question", result)
		}
		if strings.Contains(result, "How deep is the cellar?") {
			t.Errorf("alice's run read bob's question in her Inbox: %q", result)
		}
	})

	t.Run("a read of http://169.254.169.254/ is refused", func(t *testing.T) {
		const address = "http://169.254.169.254/latest/meta-data/"
		result := read(t, "alice", "what is at "+address, map[string]any{"tool": "web_read", "arguments": map[string]any{"url": address}})
		if !strings.Contains(result, agentwire.CodeAddressRefused) {
			t.Errorf("the read of %s was told %q, want %s", address, result, agentwire.CodeAddressRefused)
		}
	})

	t.Run("a read of an address in no word and no tool result is refused", func(t *testing.T) {
		result := read(t, "alice", "read the page I mean", map[string]any{"tool": "web_read", "arguments": map[string]any{"url": "https://example.com/never-named"}})
		if !strings.Contains(result, agentwire.CodeAddressUnseen) {
			t.Errorf("the read of an address nobody named was told %q, want %s", result, agentwire.CodeAddressUnseen)
		}
	})

	t.Run("a GitHub read of a repository no project maps is refused", func(t *testing.T) {
		result := read(t, "alice", "read the pull request of acme/unmapped", map[string]any{
			"tool": "github_read", "arguments": map[string]any{"repository": "acme/unmapped", "path": "pulls/1"},
		})
		if !strings.Contains(result, agentwire.CodeRepositoryUnmapped) {
			t.Errorf("the read of an unmapped repository was told %q, want %s", result, agentwire.CodeRepositoryUnmapped)
		}
	})
}

// TestARunKeepsItsBoundsAndItsRecord: a run keeps its bounds, its record and its credential, and
// survives a restart (check c11). A Fast run whose model never ends stops after 6 calls, says the
// call budget stopped it, and offers Continue; a Standard run lasts past 30 s; a run cut by a
// restart in its model loop resumes from its last stored turn; a follow-up's request holds the
// turns before it; Continue starts a follow-up with a budget of its own; and a run whose token is
// revoked stops at its next call, naming the revoked token. The rows that need a plan's approval
// (t8) or the routes of History and Stop (t14) are those tasks'.
func TestARunKeepsItsBoundsAndItsRecord(t *testing.T) {
	rig := newGoingAgentRig(t, nil)
	loop := newGoingLoop(t, rig)
	neverEnds := func(int) agenttest.Reply {
		return toolCall("find_tools", map[string]any{"need": "an issue's status"})
	}

	t.Run("a Fast run whose model never ends stops after 6 calls, names the call budget and offers Continue", func(t *testing.T) {
		const words = "keep looking for ever"
		loop.script.set(words, neverEnds)
		run := rig.begin(t, goingLoopRun{login: "alice", words: words, level: "fast"})
		record := rig.ended(t, run, time.Minute)
		if record.state != "stopped" || !strings.HasPrefix(record.reason, agent.ReasonCallBudget) {
			t.Fatalf("the run ended %s with %q, want stopped with %s", record.state, record.reason, agent.ReasonCallBudget)
		}
		if calls := len(loop.fake.Asking(words)); calls != 6 || record.used.Calls != 6 {
			t.Errorf("the run made %d model calls and counts %d, want 6", calls, record.used.Calls)
		}
		if frame := rig.endFrame(t, run); frame.State != "stopped" || len(frame.Offers) != 1 || frame.Offers[0] != agentwire.OfferContinue {
			t.Errorf("the run's end frame is %+v, want stopped offering continue", frame)
		}
	})

	t.Run("a plan that waits 10 minutes uses none of Fast's 90 s", func(t *testing.T) {
		t.Skip("t8: the approval that ends the wait is the plan's route")
	})

	t.Run("a Standard run lasts past 30 s", func(t *testing.T) {
		const words = "think about it for a while"
		wait := make(chan struct{})
		time.AfterFunc(31*time.Second, func() { close(wait) })
		loop.script.set(words, func(int) agenttest.Reply { return agenttest.Reply{Hold: wait, Text: "Thought it over."} })
		started := time.Now()
		run := rig.begin(t, goingLoopRun{login: "alice", words: words})
		record := rig.ended(t, run, 2*time.Minute)
		if record.state != "done" || time.Since(started) < 30*time.Second {
			t.Errorf("the run ended %s with %q after %s, want done past 30 s", record.state, record.reason, time.Since(started))
		}
	})

	t.Run("a run cut by a restart in its model loop resumes from its last stored turn", func(t *testing.T) {
		const words = "find the tools, then wait"
		never := make(chan struct{})
		loop.script.set(words, func(n int) agenttest.Reply {
			switch n {
			case 1:
				return toolCall("find_tools", map[string]any{"need": "an issue"})
			case 2:
				return agenttest.Reply{Hold: never}
			}
			return answered("Resumed.")
		})
		run := rig.begin(t, goingLoopRun{login: "alice", words: words})
		goingEventually(t, "the run's second model call", func() bool { return len(loop.fake.Asking(words)) == 2 })
		loop.stop()
		loop.start(rig)
		if record := rig.ended(t, run, time.Minute); record.state != "done" {
			t.Fatalf("the run ended %s with %q after the restart, want done", record.state, record.reason)
		}
		requests := loop.fake.Asking(words)
		if len(requests) != 3 {
			t.Fatalf("the run made %d model calls, want 3: the tool call, the call the restart cut, and the one that resumed", len(requests))
		}
		if _, left := requests[1].Left(); !left {
			t.Error("the restart did not cut the model call in flight")
		}
		if results := requests[2].ToolResults(); len(results) != 1 || !strings.Contains(results[0], "dispatch_") {
			t.Errorf("the call after the restart gave the model the tool results %q, want the find_tools result it stored", results)
		}
		if results := rig.toolResults(t, run); len(results) != 1 {
			t.Errorf("the run saved %d tool steps, want the one it made before the restart and none again", len(results))
		}
	})

	t.Run("a run whose bar closed goes on, and its result shows in History", func(t *testing.T) {
		t.Skip("t14: History is the conversations route's")
	})
	t.Run("a run 2 hours old shows in History, and a run 8 days old does not", func(t *testing.T) {
		t.Skip("t14: History and its sweep are the conversations route's")
	})
	t.Run("a run stopped by a restart between two writes of its plan resumes, and each change runs once", func(t *testing.T) {
		t.Skip("t8: the plan runner makes a plan's writes")
	})
	t.Run("a second person reads no conversation of the first (404)", func(t *testing.T) {
		t.Skip("t14: the conversation's read is the conversations route's")
	})
	t.Run("a plan that waits shows in the person's Inbox", func(t *testing.T) {
		t.Skip("t14: the Inbox's plans are the conversations route's")
	})

	t.Run("a follow-up's request holds the turns before it", func(t *testing.T) {
		const first, second = "which colour are plums?", "and pears?"
		loop.script.set(first, func(int) agenttest.Reply { return answered("Plums are purple.") })
		loop.script.set(second, func(int) agenttest.Reply { return answered("Pears are green.") })
		earlier := rig.begin(t, goingLoopRun{login: "alice", words: first})
		if record := rig.ended(t, earlier, time.Minute); record.state != "done" {
			t.Fatalf("the first run ended %s with %q", record.state, record.reason)
		}
		follow := rig.begin(t, goingLoopRun{login: "alice", words: second, continues: earlier})
		if record := rig.ended(t, follow, time.Minute); record.state != "done" {
			t.Fatalf("the follow-up ended %s with %q", record.state, record.reason)
		}
		requests := loop.fake.Asking(second)
		if len(requests) != 1 {
			t.Fatalf("the follow-up made %d model calls, want 1", len(requests))
		}
		if text := requests[0].Text(); !strings.Contains(text, first) || !strings.Contains(text, "Plums are purple.") {
			t.Errorf("the follow-up's request does not hold the turn before it: %s", text)
		}
	})

	t.Run("Continue starts a follow-up with a new budget", func(t *testing.T) {
		const first, next = "look until the budget ends", "go on from there"
		loop.script.set(first, neverEnds)
		loop.script.set(next, func(n int) agenttest.Reply {
			if n == 1 {
				return toolCall("find_tools", map[string]any{"need": "an issue"})
			}
			return answered("Found it.")
		})
		earlier := rig.begin(t, goingLoopRun{login: "alice", words: first, level: "fast"})
		if record := rig.ended(t, earlier, time.Minute); record.state != "stopped" || rig.endFrame(t, earlier).Offers == nil {
			t.Fatalf("the first run ended %s with %q, want stopped offering Continue", record.state, record.reason)
		}
		follow := rig.begin(t, goingLoopRun{login: "alice", words: next, level: "fast", continues: earlier})
		record := rig.ended(t, follow, time.Minute)
		if record.state != "done" || record.used.Calls != 2 || len(loop.fake.Asking(next)) != 2 {
			t.Errorf("the follow-up ended %s with %q after %d calls, want done after 2 calls of a budget of its own",
				record.state, record.reason, record.used.Calls)
		}
	})

	t.Run("when the token that started a run is revoked, its next call fails, and the run stops naming the revoked token", func(t *testing.T) {
		const words = "who am I acting as?"
		hash := make([]byte, 32)
		_, _ = rand.Read(hash)
		var token string
		if err := rig.database.Pool.QueryRow(context.Background(), `
			insert into agent_tokens (owner, name, token_hash, prefix) values ('alice', 'a script of alice''s', $1, 'dsp_test') returning id::text
		`, hash).Scan(&token); err != nil {
			t.Fatal(err)
		}
		revoked := make(chan struct{})
		loop.script.set(words, func(n int) agenttest.Reply {
			if n == 1 {
				return agenttest.Reply{Hold: revoked, Calls: []agenttest.Call{{Name: "call_tool", Arguments: map[string]any{"tool": "dispatch_whoami", "arguments": map[string]any{}}}}}
			}
			return answered("You are alice.")
		})
		run := rig.begin(t, goingLoopRun{login: "alice", words: words, token: token})
		goingEventually(t, "the run's first model call", func() bool { return len(loop.fake.Asking(words)) == 1 })
		rig.exec(t, `update agent_tokens set revoked_at = now() where id = $1::uuid`, token)
		close(revoked)
		record := rig.ended(t, run, time.Minute)
		if record.state != "stopped" || !strings.HasPrefix(record.reason, goingGrantTokenRevoked) {
			t.Errorf("the run ended %s with %q, want stopped with %s", record.state, record.reason, goingGrantTokenRevoked)
		}
		if calls := len(loop.fake.Asking(words)); calls != 1 {
			t.Errorf("the run made %d model calls, want none after the refused call", calls)
		}
	})
}
