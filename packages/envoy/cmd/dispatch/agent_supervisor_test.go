package main

// agent_supervisor_test.go holds the server's supervision of its agent process as a deployment
// meets it: a real envoy-dispatch server on a real database, which starts its own envoy-dispatch
// agent, kills it and starts it again, and shows both processes on /metrics. This build's agent
// has no model loop (going/agent): it claims each run and ends it failed, so the rows that need a
// loop that works a run are the loop's, named t7 where they are skipped.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/google/uuid"

	"github.com/sjawhar/envoy/going/agent"
	"github.com/sjawhar/envoy/going/agentwire"
	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/githubapp/githubapptest"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
	"github.com/sjawhar/envoy/internal/testtmp"
)

// serveAgentHellos listens for an agent at path as a server's end of agent/v1 does, answers each
// hello and sends the secret it carried on the channel it returns; every other call it holds
// until the agent closes its connection, as a claim and a renew wait at a server.
func serveAgentHellos(t *testing.T, path string) <-chan []byte {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen for the agent at %s: %v", path, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	hellos := make(chan []byte, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				payload, err := agentwire.ReadFrame(conn, agentwire.MaxRequestLimit)
				var request agentwire.Request
				if err != nil || json.Unmarshal(payload, &request) != nil {
					return
				}
				if request.Call != agentwire.CallHello {
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				select {
				case hellos <- request.Secret:
				default:
				}
				answer, _ := json.Marshal(agentwire.Answer{Body: json.RawMessage(`{"freed":0}`)})
				_ = agentwire.WriteFrame(conn, answer, agentwire.AnswerLimit(agentwire.CallHello))
			}()
		}
	}()
	return hellos
}

// The shapes of a test run: one this build reads (going_runs.shape, api's goingRunShape), and one
// above it, which no claim of this build takes, so a test holds a run back from the agent.
const (
	readShape   = 1
	unreadShape = 99
)

// agentServer is a Dispatch process a test started, which runs its agent process, and the
// database it serves.
type agentServer struct {
	*dispatchProcess
	database *store.Store
}

// startAgentServer starts Dispatch with env on a database of its own, in a process group of its
// own, so a SIGSTOP of its agent reaches neither the test's group nor a group the kernel hangs
// up, and returns once it is healthy.
func startAgentServer(t *testing.T, env map[string]string) agentServer {
	t.Helper()
	database := storetest.Open(t)
	process := startDispatchProcessWith(t, database.Pool.Config().ConnString(), dispatchOptions{Env: env, OwnProcessGroup: true})
	process.waitHealthy(t)
	return agentServer{dispatchProcess: process, database: database}
}

// agentStartedLine is the line the server writes each time it starts an agent, with its pid.
var agentStartedLine = regexp.MustCompile(`msg="dispatch: started the agent process" pid=(\d+)`)

// agentPids is the pid of each agent the server started, in order.
func (s agentServer) agentPids() []int {
	var pids []int
	for _, match := range agentStartedLine.FindAllStringSubmatch(s.Output.String(), -1) {
		pid, err := strconv.Atoi(match[1])
		if err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// agentStarted waits up to within for the server's count-th agent to start, and answers its pid.
func (s agentServer) agentStarted(t *testing.T, count int, within time.Duration) int {
	t.Helper()
	var pid int
	s.eventually(t, within, fmt.Sprintf("the server's agent number %d to start", count), func() bool {
		pids := s.agentPids()
		if len(pids) < count {
			return false
		}
		pid = pids[count-1]
		return true
	})
	return pid
}

// eventually waits up to within for done, failing the test with the server's output when it is
// not, or when the server exits first.
func (s agentServer) eventually(t *testing.T, within time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !done() {
		select {
		case <-s.Exited:
			t.Fatalf("the server exited while the test waited for %s:\n%s", what, s.Output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %v:\n%s", what, within, s.Output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// scrape is the server's /metrics, each whole-number sample by its name and labels as the text
// format writes them.
func (s agentServer) scrape(t *testing.T) map[string]int64 {
	t.Helper()
	response, err := http.Get(s.url("/metrics"))
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: %d %v", response.StatusCode, err)
	}
	samples := map[string]int64{}
	for line := range strings.Lines(string(body)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		at := strings.LastIndexByte(line, ' ')
		if value, err := strconv.ParseInt(line[at+1:], 10, 64); at > 0 && err == nil {
			samples[line[:at]] = value
		}
	}
	return samples
}

// The /metrics samples of the agent process.
const (
	agentGoroutines  = `envoy_dispatch_process_goroutines{process="agent"}`
	agentMemoryKills = `envoy_dispatch_agent_memory_kills`
	agentSlotsSample = `envoy_dispatch_agent_slots`
)

// waitForAgentRenewal waits for the running agent's first renew, which carries its health, so
// its claim waits at the server too.
func (s agentServer) waitForAgentRenewal(t *testing.T) {
	t.Helper()
	s.eventually(t, 30*time.Second, "the agent's first renew", func() bool {
		_, renewed := s.scrape(t)[agentGoroutines]
		return renewed
	})
}

// requireServing fails the test unless the server still runs and answers /healthz 200.
func (s agentServer) requireServing(t *testing.T) {
	t.Helper()
	if err := s.healthyWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func (s agentServer) exec(t *testing.T, statement string, arguments ...any) {
	t.Helper()
	if _, err := s.database.Pool.Exec(context.Background(), statement, arguments...); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

// recordRun records a run of login's in state, of shape, as a run's start records one, and
// answers its id; a working run is held under a lease of another server's agent for an hour.
func (s agentServer) recordRun(t *testing.T, login, state string, shape int) string {
	t.Helper()
	id := uuid.NewString()
	s.exec(t, `
		insert into going_runs (id, author_login, words, conversation_id, level, mode, state, shape, created_at,
		  lease_token, lease_owner, lease_ends_at)
		select $1::uuid, $2, 'the words of a test run', $1::uuid, 'standard', 'ask', $3, $4, clock_timestamp(),
		  case when $3 = 'working' then gen_random_uuid() end,
		  case when $3 = 'working' then 'an agent of another server' end,
		  case when $3 = 'working' then now() + interval '1 hour' end
	`, id, login, state, shape)
	return id
}

// runRow is what a test reads of a run.
type runRow struct {
	state, reason, owner string
	kills                int
}

func (s agentServer) run(t *testing.T, id string) runRow {
	t.Helper()
	var row runRow
	if err := s.database.Pool.QueryRow(context.Background(), `
		select state, coalesce(stop_reason, ''), coalesce(lease_owner, ''), kills from going_runs where id = $1::uuid
	`, id).Scan(&row.state, &row.reason, &row.owner, &row.kills); err != nil {
		t.Fatalf("read run %s: %v", id, err)
	}
	return row
}

// stopHolding stops agent pid with SIGSTOP while its claim waits at the server, then makes run,
// recorded at unreadShape, claimable, so the server leases it to the stopped agent, which can do
// nothing with it; it answers the run's lease owner and leaves the agent stopped. A stop that
// lands between two of the agent's claims leases nothing: the run is held back again, the agent
// continued, and the stop tried again.
func (s agentServer) stopHolding(t *testing.T, pid int, run string) string {
	t.Helper()
	for range 5 {
		time.Sleep(200 * time.Millisecond)
		signalProcess(t, pid, syscall.SIGSTOP)
		s.exec(t, `update going_runs set shape = $2 where id = $1::uuid`, run, readShape)
		s.exec(t, `select pg_notify($1, 'ready')`, api.GoingRunsChannel)
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if row := s.run(t, run); row.state == "working" {
				return row.owner
			}
		}
		s.exec(t, `update going_runs set shape = $2 where id = $1::uuid`, run, unreadShape)
		if row := s.run(t, run); row.state == "working" {
			return row.owner
		}
		signalProcess(t, pid, syscall.SIGCONT)
	}
	t.Fatalf("the server never leased run %s to the stopped agent %d:\n%s", run, pid, s.Output.String())
	return ""
}

// signalProcess sends sig to process pid. A process the test stops is killed when the test ends
// if it is still stopped then, so no stopped agent outlives its test.
func signalProcess(t *testing.T, pid int, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(pid, sig); err != nil {
		t.Fatalf("send %v to process %d: %v", sig, pid, err)
	}
	if sig == syscall.SIGSTOP {
		t.Cleanup(func() {
			if processState(pid) == 'T' {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		})
	}
}

// processState is the state /proc/<pid>/stat gives process pid ('R', 'S', 'T', 'Z' and so on),
// 0 where the process is gone.
func processState(pid int) byte {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// The command name in parentheses may hold anything, ") " among it: the state follows the
	// last closing parenthesis.
	at := strings.LastIndexByte(string(stat), ')')
	if at < 0 || at+2 >= len(stat) {
		return 0
	}
	return stat[at+2]
}

// processGone reports whether process pid has exited: its /proc entry is gone, or it is a zombie
// its parent has not reaped yet.
func processGone(pid int) bool {
	switch processState(pid) {
	case 0, 'Z', 'X':
		return true
	}
	return false
}

// logTime is the time= attribute of a server's log line.
func logTime(t *testing.T, line string) time.Time {
	t.Helper()
	match := regexp.MustCompile(`time=(\S+)`).FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("the line carries no time: %s", line)
	}
	at, err := time.Parse(time.RFC3339Nano, match[1])
	if err != nil {
		t.Fatalf("the line's time %q: %v", match[1], err)
	}
	return at
}

// killedMemory is the memory= attribute of an "agent killed for memory" line.
func killedMemory(t *testing.T, line string) int64 {
	t.Helper()
	match := regexp.MustCompile(` memory=(\d+) `).FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("the kill names no memory: %s", line)
	}
	memory, _ := strconv.ParseInt(match[1], 10, 64)
	return memory
}

// recordingAgentEnd is a server's end of agent/v1 that answers no call: the supervisor's agent,
// given a socket nobody listens on, fails its hello and exits at once. It counts the exits the
// supervisor reports.
type recordingAgentEnd struct {
	path    string
	mu      sync.Mutex
	closeIn func()
	exits   int
}

func (e *recordingAgentEnd) Path() string                     { return e.path }
func (e *recordingAgentEnd) Secret() []byte                   { return make([]byte, agentwire.SecretSize) }
func (e *recordingAgentEnd) LastRenewal() time.Time           { return time.Time{} }
func (e *recordingAgentEnd) Health() (agentwire.Health, bool) { return agentwire.Health{}, false }
func (e *recordingAgentEnd) SetRunSlots(int)                  {}

func (e *recordingAgentEnd) SetCloseStandardInput(closeIn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeIn = closeIn
}

func (e *recordingAgentEnd) AgentExited(context.Context, bool) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.exits++
	return false, nil
}

func (e *recordingAgentEnd) Stop() {
	e.mu.Lock()
	closeIn := e.closeIn
	e.mu.Unlock()
	if closeIn != nil {
		closeIn()
	}
}

func (e *recordingAgentEnd) Close(time.Time) { e.Stop() }

// requireTheRestartWaits runs the supervisor over the real agent binary, which exits at once each
// time, and holds the wait before each start again: 1 s after the first exit, then twice as long
// after each quick exit, 30 s at the most. The waits are recorded, not slept.
func requireTheRestartWaits(t *testing.T) {
	t.Helper()
	end := &recordingAgentEnd{path: testtmp.Socket(t, "agent.sock")}
	supervisor := newAgentSupervisor(end, []string{cmdtest.Build(t, "envoy-dispatch"), "agent"},
		[]string{"DISPATCH_AGENT_SOCKET=" + end.path}, agentLimits(0))
	waits := make(chan time.Duration, 16)
	supervisor.after = func(wait time.Duration) <-chan time.Time {
		waits <- wait
		now := make(chan time.Time, 1)
		now <- time.Now()
		return now
	}
	go supervisor.run()
	t.Cleanup(func() { supervisor.Close(time.Now()) })
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	var got []time.Duration
	for range want {
		select {
		case wait := <-waits:
			got = append(got, wait)
		case <-time.After(30 * time.Second):
			t.Fatalf("the supervisor started the agent again after %v, then no more within 30 s", got)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the waits before each start again: %v, want %v", got, want)
	}
	end.mu.Lock()
	exits := end.exits
	end.mu.Unlock()
	if exits < len(want) {
		t.Errorf("the supervisor reported %d exits of the agent to the server's end, want at least %d", exits, len(want))
	}
}

// TestAStuckOrPanickingRunStopsOnlyTheAgent: a kill of the agent process stops no request of the
// server. An agent killed for memory is started again while the server serves on; a SIGSTOP holds
// the agent's renewals, and the server kills it once it has missed 3 of them and starts another
// within 40 s; the close of the agent's standard input at the server's stop ends the agent within
// 1 s; and each quick exit doubles the wait before the next start, up to 30 s (check c19).
func TestAStuckOrPanickingRunStopsOnlyTheAgent(t *testing.T) {
	t.Run("an agent killed for memory is started again and the server serves on", func(t *testing.T) {
		server := startAgentServer(t, map[string]string{"DISPATCH_TEST_AGENT_CEILING": "1"})
		first := server.agentStarted(t, 1, 30*time.Second)
		server.waitForLog(t, 10*time.Second, `msg="dispatch: agent killed for memory" pid=`+strconv.Itoa(first)+" ")
		second := server.agentStarted(t, 2, 10*time.Second)
		if second == first {
			t.Fatalf("the agent started again under the pid of the one killed, %d", first)
		}
		server.requireServing(t)
	})

	t.Run("a stopped agent is killed once it has missed 3 renewals, and another starts within 40 s", func(t *testing.T) {
		server := startAgentServer(t, nil)
		stoppedAgent := server.agentStarted(t, 1, 30*time.Second)
		server.waitForAgentRenewal(t)
		stopped := time.Now()
		signalProcess(t, stoppedAgent, syscall.SIGSTOP)
		next := server.agentStarted(t, 2, 40*time.Second)
		started := time.Since(stopped)
		if started > 40*time.Second {
			t.Fatalf("the next agent started %v after the SIGSTOP, want within 40 s", started)
		}
		// The stopped agent's last renew came at most 10 s before the stop, so it missed its third
		// renewal 20 s after it at the earliest.
		if started < 19*time.Second {
			t.Errorf("the next agent started %v after the SIGSTOP, before the stopped one could miss 3 renewals", started)
		}
		if !server.logged(`msg="dispatch: agent killed: it renewed nothing for 30s" pid=` + strconv.Itoa(stoppedAgent) + " ") {
			t.Errorf("the server did not log the stopped agent %d killed for its renewals:\n%s", stoppedAgent, server.Output.String())
		}
		if !processGone(stoppedAgent) {
			t.Errorf("the stopped agent %d still runs beside the next one, %d", stoppedAgent, next)
		}
		server.requireServing(t)
		server.waitForAgentRenewal(t)
	})

	t.Run("the agent exits within 1 s of the server closing its standard input", func(t *testing.T) {
		server := startAgentServer(t, nil)
		pid := server.agentStarted(t, 1, 30*time.Second)
		server.waitForAgentRenewal(t)
		server.Terminate(t)
		var gone time.Time
		for deadline := time.Now().Add(30 * time.Second); gone.IsZero(); time.Sleep(5 * time.Millisecond) {
			if processGone(pid) {
				gone = time.Now()
			} else if time.Now().After(deadline) {
				t.Fatalf("the agent %d still ran 30 s after the server's SIGTERM:\n%s", pid, server.Output.String())
			}
		}
		closed := `msg="dispatch: closed the agent's standard input" pid=` + strconv.Itoa(pid)
		server.waitForLog(t, 10*time.Second, closed)
		if exited := gone.Sub(logTime(t, server.logLines(closed)[0])); exited > time.Second {
			t.Errorf("the agent exited %v after its standard input closed, want within 1 s", exited)
		}
		server.WaitExit(t, "SIGTERM")
	})

	t.Run("each quick exit doubles the wait before the agent starts again, up to 30 s", requireTheRestartWaits)

	for _, row := range []string{
		"a tool and a model stream that ignore their context: the watchdog gives up the lease, and after 3 the agent exits and starts again",
		"a panic in a model-loop callback: the run goes on and the agent's process id does not change",
		"one run that panics past its recovery 3 times fails with that reason, and a run beside it is not counted",
		"a route that panics during a call of the agent answers an error, and the server's process id does not change",
	} {
		t.Run(row, func(t *testing.T) {
			t.Skip("t7: the agent's model loop drives this row, and this build's agent has none")
		})
	}
}

// TestMetricsShowBothProcesses: /metrics shows the goroutines, live heap, total memory and memory
// limit of the server and of its agent, the bytes of the server's three caches, the runs working,
// queued and waiting, the agent's slots, the slots in use and the stuck goroutines, the agent's
// kills for memory and when each process started; each kill writes the line agent killed for
// memory; and container limits of 24 GiB, 8 GiB and none give the limits of section 5 (check c23).
func TestMetricsShowBothProcesses(t *testing.T) {
	t.Run("the figures of both processes, the caches and the runs", func(t *testing.T) {
		before := time.Now()
		server := startAgentServer(t, nil)
		// Three runs work under another server's agent, so the two queued behind them for the same
		// person stay queued: a person works 3 runs at once.
		for range 3 {
			server.recordRun(t, "alice", "working", readShape)
		}
		for range 2 {
			server.recordRun(t, "alice", "queued", readShape)
		}
		server.recordRun(t, "alice", "waiting", readShape)
		server.waitForAgentRenewal(t)
		figures := server.scrape(t)

		for _, process := range []string{"server", "agent"} {
			for _, name := range []string{"goroutines", "heap_live_bytes", "memory_total_bytes", "memory_limit_bytes"} {
				sample := `envoy_dispatch_process_` + name + `{process="` + process + `"}`
				if value, shown := figures[sample]; !shown || value <= 0 {
					t.Errorf("%s = %d (shown %t), want a figure above 0", sample, value, shown)
				}
			}
		}
		container, err := containerMemoryLimit()
		if err != nil {
			t.Fatal(err)
		}
		limits := agentLimits(container)
		wantServerLimit := limits.ServerLimit
		if wantServerLimit == 0 {
			wantServerLimit = math.MaxInt64
		}
		if got := figures[`envoy_dispatch_process_memory_limit_bytes{process="server"}`]; got != wantServerLimit {
			t.Errorf("the server's memory limit is %d, want %d for a container limit of %d", got, wantServerLimit, container)
		}
		if got := figures[`envoy_dispatch_process_memory_limit_bytes{process="agent"}`]; got != limits.AgentLimit {
			t.Errorf("the agent's memory limit is %d, want its GOMEMLIMIT %d for a container limit of %d", got, limits.AgentLimit, container)
		}
		serverStart, agentStart := figures[`envoy_dispatch_process_start_time_seconds{process="server"}`], figures[`envoy_dispatch_process_start_time_seconds{process="agent"}`]
		if serverStart < before.Unix()-1 || serverStart > time.Now().Unix() {
			t.Errorf("the server started at %d, want between %d and now", serverStart, before.Unix())
		}
		if agentStart < serverStart || agentStart > time.Now().Unix() {
			t.Errorf("the agent started at %d, want between the server's start %d and now", agentStart, serverStart)
		}
		for _, cache := range []string{"read", "display_tree", "list_answer"} {
			if value, shown := figures[`envoy_dispatch_cache_bytes{cache="`+cache+`"}`]; !shown || value < 0 {
				t.Errorf("the %s cache's bytes = %d (shown %t)", cache, value, shown)
			}
		}
		for state, want := range map[string]int64{"working": 3, "queued": 2, "waiting": 1} {
			if got, shown := figures[`envoy_dispatch_going_runs{state="`+state+`"}`]; !shown || got != want {
				t.Errorf("runs %s = %d (shown %t), want %d", state, got, shown, want)
			}
		}
		for sample, want := range map[string]int64{
			agentSlotsSample:                        int64(limits.Slots),
			`envoy_dispatch_agent_slots_in_use`:     0,
			`envoy_dispatch_agent_stuck_goroutines`: 0,
			agentMemoryKills:                        0,
		} {
			if got, shown := figures[sample]; !shown || got != want {
				t.Errorf("%s = %d (shown %t), want %d", sample, got, shown, want)
			}
		}
	})

	t.Run("each kill for memory is counted and logs agent killed for memory with the memory", func(t *testing.T) {
		server := startAgentServer(t, map[string]string{"DISPATCH_TEST_AGENT_CEILING": "1"})
		server.eventually(t, 30*time.Second, "two kills for memory", func() bool { return server.scrape(t)[agentMemoryKills] >= 2 })
		// The log and the count are read between two scrapes that agree, so no kill came between
		// them: the kills are 1 s, then 2 s, 4 s and 8 s apart.
		var kills int64
		var lines []string
		server.eventually(t, 30*time.Second, "a log read between two kills", func() bool {
			time.Sleep(300 * time.Millisecond)
			kills = server.scrape(t)[agentMemoryKills]
			lines = server.logLines(`msg="dispatch: agent killed for memory"`)
			return server.scrape(t)[agentMemoryKills] == kills
		})
		if int64(len(lines)) != kills {
			t.Errorf("/metrics counts %d kills for memory and the log %d:\n%s", kills, len(lines), strings.Join(lines, "\n"))
		}
		for _, line := range lines {
			if memory := killedMemory(t, line); memory <= 1 {
				t.Errorf("a kill above the ceiling of 1 byte names the memory %d: %s", memory, line)
			}
		}
	})

	t.Run("container limits of 24 GiB, 8 GiB and none give the agent's ceiling, its GOMEMLIMIT and the server's limit", requireTheContainerLimits)

	t.Run("the container's limit as automemlimit reads it, no limit and no control group reading as none", func(t *testing.T) {
		for _, c := range []struct {
			name  string
			limit uint64
			err   error
			want  int64
		}{
			{"a limit", 24 << 30, nil, 24 << 30},
			{"no limit", 0, memlimit.ErrNoLimit, 0},
			{"no control group", 0, memlimit.ErrNoCgroup, 0},
			{"no control groups on the system", 0, memlimit.ErrCgroupsNotSupported, 0},
		} {
			got, err := readContainerMemoryLimit(func() (uint64, error) { return c.limit, c.err })
			if err != nil || got != c.want {
				t.Errorf("%s: %d, %v; want %d", c.name, got, err, c.want)
			}
		}
		if _, err := readContainerMemoryLimit(func() (uint64, error) { return 0, os.ErrPermission }); err == nil {
			t.Error("a control group the process may not read reads as no limit, not as a failure")
		}
	})
}

// requireTheContainerLimits holds agentLimits to the three cases of section 5: each container
// limit gives the agent's ceiling, its GOMEMLIMIT, the server's limit and the slots.
func requireTheContainerLimits(t *testing.T) {
	for _, c := range []struct {
		name                               string
		container, ceiling, agent, servers int64
	}{
		{"24 GiB", 24 << 30, 4 << 30, 3 << 30, 18 << 30},
		{"8 GiB", 8 << 30, (8 << 30) / 6, 1 << 30, 6 << 30},
		{"no limit", 0, 4 << 30, 3 << 30, 0},
	} {
		got := agentLimits(c.container)
		want := agentMemory{Ceiling: c.ceiling, AgentLimit: c.agent, ServerLimit: c.servers, Slots: 8}
		if got != want {
			t.Errorf("%s: agentLimits = %+v, want %+v", c.name, got, want)
		}
	}
}

// TestTheAgentLimitsOfAContainer: container limits of 24 GiB, 8 GiB and none give the agent a
// ceiling of 4 GiB, L/6 and 4 GiB, a GOMEMLIMIT of 3 GiB, 1 GiB and 3 GiB, the server a limit of
// 18 GiB, 6 GiB and none, and 8 slots (plan settlement 12, the background-runs design's section 5).
func TestTheAgentLimitsOfAContainer(t *testing.T) {
	requireTheContainerLimits(t)
}

// TestTheAgentHoldsNoSecret: the server starts its agent with the chat model's settings and the
// three it sets itself, and nothing of its own environment, its secrets among them; the agent's
// socket is in a directory only the server's user can open, and a call with no secret, or with
// another one, is refused (check c24).
func TestTheAgentHoldsNoSecret(t *testing.T) {
	signIn := oidctest.New(t)
	signIn.EnableCodeFlow(signIn.PublishKey(t, "signing-key"), cookieClientID, cookieClientSecret)
	github := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(github.Close)
	_, pem := githubapptest.Key(t)
	tokenFile := filepath.Join(t.TempDir(), "chat-token")
	const signingKey = "agent-holds-no-secret-signing-key"
	secrets := map[string]string{
		"DISPATCH_SIGNING_KEY":          signingKey,
		"DISPATCH_APP_PEM_B64":          pem,
		"DISPATCH_SIGNIN_CLIENT_SECRET": cookieClientSecret,
		"ENVOY_TOKEN":                   "agent-holds-no-secret-envoy-token",
		"DISPATCH_AGENT_TOKEN":          dispatchTestAgentToken,
	}
	database := storetest.Open(t)
	databaseURL := database.Pool.Config().ConnString()
	secrets["DATABASE_URL"] = databaseURL
	process := startDispatchProcessWith(t, databaseURL, dispatchOptions{
		SigningKey:      signingKey,
		OwnProcessGroup: true,
		Env: map[string]string{
			"DISPATCH_IDENTITY":             "cookie",
			"DISPATCH_SIGNIN_ISSUER":        signIn.URL(),
			"DISPATCH_SIGNIN_CLIENT_ID":     cookieClientID,
			"DISPATCH_SIGNIN_CLIENT_SECRET": cookieClientSecret,
			"DISPATCH_SIGNIN_GROUP":         cookieGroup,
			"DISPATCH_INSECURE_COOKIE":      "1",
			"DISPATCH_APP_CLIENT_ID":        "Iv1.agentholdsnosecret",
			"DISPATCH_APP_PEM_B64":          pem,
			"DISPATCH_GITHUB_API_BASE":      github.URL,
			"ENVOY_TOKEN":                   secrets["ENVOY_TOKEN"],
			"DISPATCH_ANSWERER":             "endpoint",
			"DISPATCH_CHAT_ENDPOINT":        "https://chat.example/v1",
			"DISPATCH_CHAT_TOKEN_FILE":      tokenFile,
			"DISPATCH_CHAT_MODEL":           "a-chat-model",
		},
	})
	server := agentServer{dispatchProcess: process, database: database}
	server.waitHealthy(t)
	pid := server.agentStarted(t, 1, 30*time.Second)
	environ, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		t.Fatalf("read the agent's environment: %v", err)
	}
	variables := map[string]string{}
	for entry := range strings.SplitSeq(strings.TrimRight(string(environ), "\x00"), "\x00") {
		name, value, _ := strings.Cut(entry, "=")
		variables[name] = value
	}
	for name, value := range secrets {
		if _, held := variables[name]; held {
			t.Errorf("the agent's environment holds %s, a secret of the server's", name)
		}
		for held, heldValue := range variables {
			if strings.Contains(heldValue, value) {
				t.Errorf("the agent's %s holds the value of the server's %s", held, name)
			}
		}
	}
	socket := variables["DISPATCH_AGENT_SOCKET"]
	want := map[string]string{
		"DISPATCH_AGENT_SOCKET":    socket,
		"GOMAXPROCS":               variables["GOMAXPROCS"],
		"GOMEMLIMIT":               variables["GOMEMLIMIT"],
		"DISPATCH_ANSWERER":        "endpoint",
		"DISPATCH_CHAT_ENDPOINT":   "https://chat.example/v1",
		"DISPATCH_CHAT_TOKEN_FILE": tokenFile,
		"DISPATCH_CHAT_MODEL":      "a-chat-model",
	}
	if socket == "" || variables["GOMAXPROCS"] == "" || variables["GOMEMLIMIT"] == "" || !maps.Equal(variables, want) {
		t.Errorf("the agent's environment is %v, want the chat model's settings and the socket, GOMAXPROCS and GOMEMLIMIT alone", variables)
	}
	if socket == "" {
		t.FailNow()
	}
	info, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		t.Fatalf("stat the agent's socket directory: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("the agent's socket directory %s has mode %#o, want 0700", filepath.Dir(socket), mode)
	}
	for name, secret := range map[string][]byte{
		"no secret":      nil,
		"a wrong secret": bytes.Repeat([]byte{1}, agentwire.SecretSize),
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := agentwire.Client{Path: socket, Secret: secret}.Hello(ctx, agentwire.HelloRequest{Instance: "a caller that is no agent"})
		cancel()
		var refused *agentwire.Error
		if !errors.As(err, &refused) || refused.Code != agentwire.CodeUnauthorized {
			t.Errorf("a hello with %s was answered %v, want %s", name, err, agentwire.CodeUnauthorized)
		}
	}
}

// sectionFiveRows is every limit of section 5 of the background-runs design (v11), both its tables,
// by the words of its Limit column: TestEachBoundOfABackgroundRun holds a subtest for each.
var sectionFiveRows = []string{
	"Runs working at once, in the agent",
	"Runs working at once, and queued, for one person",
	"Model calls, time, output tokens",
	"One model call",
	"One read or write, to the model",
	"One run's history to the model",
	"One step's time",
	"Memory of the agent",
	"A run that kills the agent",
	"Kills for memory that no run alone causes",
	"Readers of a run's events",
	"calls that the API runs for its agents at once",
	"One call in the API",
	"A second try of one idempotency key",
	"A lease",
	"One claim",
	"A restart of the agent",
	"The short answer and Jev's call, in the API",
}

// TestEachBoundOfABackgroundRun: each limit of section 5 of the background-runs design is crossed
// once, and the run waits or stops with the reason the section names (check c22). It fails on a
// row of the section with no subtest. The rows the agent's model loop crosses are the loop's (t7),
// the readers of a run's events the run stream's (t14), and the short answer's bounds the short
// answer's (t12); each of those is skipped until its task lands.
func TestEachBoundOfABackgroundRun(t *testing.T) {
	skip := func(owner, why string) func(*testing.T) {
		return func(t *testing.T) { t.Skip(owner + ": " + why) }
	}
	subtests := map[string]func(*testing.T){
		"Runs working at once, in the agent": skip("t7", "the agent's loop holds its 8 slots"),
		"Runs working at once, and queued, for one person": skip("t7",
			"two agents that hold runs, and the route that starts a run, are the loop's and the routes'"),
		"Model calls, time, output tokens":               skip("t7", "the loop spends a run's budget"),
		"One model call":                                 skip("t7", "the loop reads the model's stream"),
		"One read or write, to the model":                skip("t7", "the loop gives the model a call's answer"),
		"One run's history to the model":                 requireTheHistoryBound,
		"One step's time":                                skip("t7", "the loop's watchdog times a step"),
		"Memory of the agent":                            requireTheAgentsMemoryBound,
		"A run that kills the agent":                     requireARunThatKillsTheAgentThreeTimesFails,
		"Kills for memory that no run alone causes":      requireUnexplainedKillsHalveTheSlots,
		"Readers of a run's events":                      skip("t14", "the run stream's readers are the stream route's"),
		"calls that the API runs for its agents at once": skip("t7", "the loop makes the calls"),
		"One call in the API":                            skip("t7", "the loop makes the calls"),
		"A second try of one idempotency key":            skip("t7", "the loop makes the calls"),
		"A lease":                                        requireAStoppedOwnerLosesItsRun,
		"One claim":                                      skip("t7", "the loop reads a claim that found no run and claims again"),
		"A restart of the agent":                         requireTheRestartBound,
		"The short answer and Jev's call, in the API":    skip("t12", "the short answer's bounds are its own task's"),
	}
	for _, row := range sectionFiveRows {
		test, ok := subtests[row]
		if !ok {
			t.Errorf("section 5's row %q has no subtest here", row)
			continue
		}
		t.Run(row, test)
		delete(subtests, row)
	}
	for row := range subtests {
		t.Errorf("a subtest for %q, which section 5 does not list", row)
	}
}

// requireTheHistoryBound: a run whose last 8 steps pass 2 MiB fails with that reason when the agent
// claims it, and one whose last 8 steps fit is given to the agent.
func requireTheHistoryBound(t *testing.T) {
	server := startAgentServer(t, nil)
	server.waitForAgentRenewal(t)
	record := func(stepBytes int) string {
		t.Helper()
		ctx := context.Background()
		tx, err := server.database.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		id := uuid.NewString()
		if _, err := tx.Exec(ctx, `
			insert into going_runs (id, author_login, words, conversation_id, level, mode, state, shape, created_at)
			values ($1::uuid, 'alice', 'the words of a test run', $1::uuid, 'deep', 'ask', 'queued', $2, clock_timestamp())
		`, id, readShape); err != nil {
			t.Fatal(err)
		}
		body := `{"reply": "` + strings.Repeat("x", stepBytes) + `"}`
		for step := 1; step <= 8; step++ {
			if _, err := tx.Exec(ctx, `insert into going_run_steps (run_id, step, kind, body, note) values ($1::uuid, $2, 'model', $3::jsonb, 'a step')`,
				id, step, body); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(ctx, `select pg_notify($1, 'ready')`, api.GoingRunsChannel); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tooLarge, fits := record(300<<10), record(200<<10)
	server.eventually(t, 30*time.Second, "both runs to end", func() bool {
		return server.run(t, tooLarge).state == "failed" && server.run(t, fits).state == "failed"
	})
	if row := server.run(t, tooLarge); !strings.HasPrefix(row.reason, "HISTORY_TOO_LARGE") {
		t.Errorf("the run whose last 8 steps hold 2.4 MiB stopped with %q, want HISTORY_TOO_LARGE", row.reason)
	}
	if row := server.run(t, fits); row.reason != agent.ReasonUnbuilt {
		t.Errorf("the run whose last 8 steps hold 1.6 MiB stopped with %q, want the agent's own end of it, %q", row.reason, agent.ReasonUnbuilt)
	}
}

// requireTheAgentsMemoryBound: the agent runs with the GOMEMLIMIT and the CPUs its limits give
// it, and the server kills it within its first reads, every 50 ms, once it is above its ceiling.
func requireTheAgentsMemoryBound(t *testing.T) {
	t.Run("the agent runs with GOMEMLIMIT 75% of its ceiling and at most 4 CPUs", func(t *testing.T) {
		server := startAgentServer(t, nil)
		pid := server.agentStarted(t, 1, 30*time.Second)
		environ, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			t.Fatalf("read the agent's environment: %v", err)
		}
		variables := map[string]string{}
		for entry := range strings.SplitSeq(strings.TrimRight(string(environ), "\x00"), "\x00") {
			name, value, _ := strings.Cut(entry, "=")
			variables[name] = value
		}
		container, err := containerMemoryLimit()
		if err != nil {
			t.Fatal(err)
		}
		if want := strconv.FormatInt(agentLimits(container).AgentLimit, 10); variables["GOMEMLIMIT"] != want {
			t.Errorf("the agent's GOMEMLIMIT is %q, want %s for a container limit of %d", variables["GOMEMLIMIT"], want, container)
		}
		if procs, err := strconv.Atoi(variables["GOMAXPROCS"]); err != nil || procs < 1 || procs > agentMaxProcs {
			t.Errorf("the agent's GOMAXPROCS is %q, want 1 to %d", variables["GOMAXPROCS"], agentMaxProcs)
		}
	})
	t.Run("the server kills the agent above its ceiling at its first reads, 50 ms apart", func(t *testing.T) {
		server := startAgentServer(t, map[string]string{"DISPATCH_TEST_AGENT_CEILING": "1"})
		pid := strconv.Itoa(server.agentStarted(t, 1, 30*time.Second))
		killed := `msg="dispatch: agent killed for memory" pid=` + pid + " "
		server.waitForLog(t, 10*time.Second, killed)
		line := server.logLines(killed)[0]
		if memory := killedMemory(t, line); memory <= 1 {
			t.Errorf("the kill names the memory %d, not above the ceiling of 1 byte: %s", memory, line)
		}
		started := logTime(t, server.logLines(`msg="dispatch: started the agent process" pid=` + pid + " ")[0])
		if after := logTime(t, line).Sub(started); after > 500*time.Millisecond {
			t.Errorf("the agent was killed %v after it started, above its ceiling from the start, want within its first reads", after)
		}
		server.requireServing(t)
	})
	t.Run("no new claim above 70% of the agent's GOMEMLIMIT", skipT7("the loop's claim gate reads the agent's own memory"))
}

// skipT7 skips a row the agent's model loop drives.
func skipT7(why string) func(*testing.T) {
	return func(t *testing.T) { t.Skip("t7: " + why) }
}

// requireARunThatKillsTheAgentThreeTimesFails: a run the agent held alone each time it died is
// charged each death, and fails with that reason at the third.
func requireARunThatKillsTheAgentThreeTimesFails(t *testing.T) {
	t.Run("a run the agent held alone at each of 3 deaths fails with that reason", func(t *testing.T) {
		server := startAgentServer(t, nil)
		run := server.recordRun(t, "alice", "queued", unreadShape)
		for death := 1; death <= 3; death++ {
			pid := server.agentStarted(t, death, 30*time.Second)
			server.waitForAgentRenewal(t)
			server.stopHolding(t, pid, run)
			// Held back again, so the next agent cannot take it before the test hands it over.
			server.exec(t, `update going_runs set shape = $2 where id = $1::uuid`, run, unreadShape)
			signalProcess(t, pid, syscall.SIGKILL)
			server.eventually(t, 15*time.Second, fmt.Sprintf("death %d charged to the run", death), func() bool {
				return server.run(t, run).kills == death
			})
		}
		row := server.run(t, run)
		if row.state != "failed" || !strings.HasPrefix(row.reason, "AGENT_KILLED") {
			t.Errorf("the run the agent died on 3 times is %s with %q, want failed with AGENT_KILLED", row.state, row.reason)
		}
		server.requireServing(t)
	})
	t.Run("a run beside it is not counted", skipT7("only the loop holds two runs at once"))
}

// requireUnexplainedKillsHalveTheSlots: two kills for memory within an hour that no run alone
// caused halve the agent's slots, and each later one halves them again, to 1 at the least. The
// agent is killed at each start, the kills 1 s, 2 s, 4 s and 8 s apart; the slots are read once
// each count of kills has stood 300 ms, since they follow the kill once the server has read who
// held what.
func requireUnexplainedKillsHalveTheSlots(t *testing.T) {
	server := startAgentServer(t, map[string]string{"DISPATCH_TEST_AGENT_CEILING": "1"})
	want := map[int64]int64{1: 8, 2: 4, 3: 2, 4: 1, 5: 1}
	read := map[int64]int64{}
	var kills int64
	var since time.Time
	server.eventually(t, 60*time.Second, "5 kills for memory", func() bool {
		figures := server.scrape(t)
		if figures[agentMemoryKills] != kills {
			kills, since = figures[agentMemoryKills], time.Now()
			return false
		}
		if _, done := read[kills]; !done && kills > 0 && time.Since(since) >= 300*time.Millisecond {
			read[kills] = figures[agentSlotsSample]
		}
		_, fifth := read[5]
		return fifth
	})
	// The first kill can come before the server answers its first scrape; each later count stands
	// 2 s at the least.
	for count := int64(2); count <= 5; count++ {
		if got, done := read[count]; !done || got != want[count] {
			t.Errorf("after %d kills for memory with no run held, the agent's slots are %d (read %t), want %d", count, got, done, want[count])
		}
	}
	if got, done := read[1]; done && got != want[1] {
		t.Errorf("after 1 kill for memory, the agent's slots are %d, want %d", got, want[1])
	}
}

// requireAStoppedOwnerLosesItsRun: an agent that renews nothing holds its run no longer: it is
// killed once it has missed 3 renewals, and the next agent claims the run.
func requireAStoppedOwnerLosesItsRun(t *testing.T) {
	server := startAgentServer(t, nil)
	pid := server.agentStarted(t, 1, 30*time.Second)
	server.waitForAgentRenewal(t)
	run := server.recordRun(t, "alice", "queued", unreadShape)
	owner := server.stopHolding(t, pid, run)
	server.agentStarted(t, 2, 40*time.Second)
	server.eventually(t, 30*time.Second, "the next agent to claim the run", func() bool { return server.run(t, run).state == "failed" })
	if row := server.run(t, run); row.reason != agent.ReasonUnbuilt {
		t.Errorf("the run the stopped agent %s held stopped with %q, want the next agent's end of it, %q", owner, row.reason, agent.ReasonUnbuilt)
	}
	if !server.logged(`msg="dispatch: agent killed: it renewed nothing for 30s" pid=` + strconv.Itoa(pid) + " ") {
		t.Errorf("the stopped agent %d was not killed for its renewals:\n%s", pid, server.Output.String())
	}
}

// requireTheRestartBound: the agent starts again 1 s after an exit, then twice as long after each
// quick exit, 30 s at the most, while Dispatch serves on. That the runs wait for the next agent is
// "A lease"'s: this build's agent ends each run it claims, so a run left queued here would say
// only that no agent lived long enough to claim it.
func requireTheRestartBound(t *testing.T) {
	t.Run("each quick exit doubles the wait, up to 30 s", requireTheRestartWaits)
	t.Run("Dispatch serves on while the agent starts again", func(t *testing.T) {
		server := startAgentServer(t, map[string]string{"DISPATCH_TEST_AGENT_CEILING": "1"})
		server.agentStarted(t, 3, 30*time.Second)
		server.requireServing(t)
	})
}
