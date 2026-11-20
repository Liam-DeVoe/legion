//go:build memory

package main

// agent_load_test.go holds checks c26 and c29, which the dispatch-memory job runs (-tags memory):
// the server's heap and the agent's memory under 8 Deep runs at their limits on a Dispatch seeded
// as large as production, and the server's CPU while the agent works at its soft limit. Each
// starts a real envoy-dispatch server whose own agent calls a fake chat model over https, the
// soak's script (agent_soak_test.go) answering it.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/going/agent/agenttest"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
)

// The seed of c26: as large as production for each kind the runs' routes read where a figure of
// production gives the size, and otherwise the size named here.
const (
	// loadRosterSessions is the sessions the Envoy listener lists. No figure of production's roster
	// is recorded, so it is the 200,000 c26's negative control names.
	loadRosterSessions = 200_000
	// loadIssues is the issues of the project the runs list: production's largest project held at
	// least 4,580 on 2026-11-20, the number of the latest issue its records name
	// (dispatch://INFRA-4580), since an issue's key numbers it within its project.
	loadIssues = 5_000
	// loadComments is the comments of the issue whose events the runs read, each an event, which
	// the search finds too. No figure of production's is recorded.
	loadComments = 1_000
	// loadRuns is the Deep runs that work at once, each reading its own spec of 1 MiB and editing
	// it.
	loadRuns = 8
	// loadSpec is the length of each run's spec, which a read of its text answers whole within the
	// 1 MiB a call's answer may carry.
	loadSpec = 1_000_000
)

// The bounds of c26.
const (
	// loadServerRise is the most the server's live heap outside the document rooms may rise above
	// its level before the runs, and loadSettleShare the most it may stay above that level once
	// the rooms have closed.
	loadServerRise  = 1 << 30
	loadSettleShare = 0.05
	// loadClaimGate is the share of the agent's GOMEMLIMIT its claim gate stands at (going/agent's
	// claimGate), from which the slots are set.
	loadClaimGate = 0.7
)

// The shape of a run at its limits: each model reply carries loadReplyText of prose and pads its
// stream to loadPadding, under the 8 MiB a stream may carry. The last 8 steps of a run's history go
// to the model whole, each a reply and a read's 32 KiB, so 8 replies of 160 KiB stay under the
// 2 MiB a history may hold.
const (
	loadReplyText = 160 << 10
	loadPadding   = 6 << 20
)

// loadRooms matches a frame of the document rooms, ygo's and the docs package's room code, whose
// heap dispatch://INFRA-179's room budget bounds: c26 bounds the heap outside them.
var loadRooms = regexp.MustCompile(`reearth/ygo|internal/dispatch/docs\.\(\*room`)

// loadRig is c26's server, its profiler, the fake model's script, the agent's limits, and what the
// runs read: the project they list, the issue whose events they read, and each run's spec, which
// the run reads and edits.
type loadRig struct {
	server   agentServer
	model    *soakModel
	profiler string
	limits   agentMemory
	project  string
	events   string
	specs    []loadSpecOf
}

// loadSpecOf is the spec of one run: its issue and its document.
type loadSpecOf struct {
	issue, document string
}

// startLoad starts the server of c26 beside an Envoy listener that lists loadRosterSessions
// sessions, and seeds it.
func startLoad(t *testing.T) *loadRig {
	t.Helper()
	listener := rosterListener(t, loadRosterSessions)
	fake, model := newSoakModel(t)
	env := modelEnv(t, fake)
	env["DISPATCH_PPROF_PORT"] = "0"
	env["ENVOY_URL"] = listener
	rig := &loadRig{server: startAgentServer(t, env), model: model, project: "LOAD"}
	rig.server.waitForAgentRenewal(t)
	listening := pprofListening.FindStringSubmatch(rig.server.Output.String())
	if listening == nil {
		t.Fatalf("the server logged no line naming its profiler's address:\n%s", rig.server.Output.String())
	}
	rig.profiler = listening[1]
	container, err := containerMemoryLimit()
	if err != nil {
		t.Fatal(err)
	}
	rig.limits = agentLimits(container)
	rig.seed(t)
	return rig
}

// rosterListener is an Envoy listener whose session list holds sessions sessions, newest seen
// first, and answers its URL.
func rosterListener(t *testing.T, sessions int) string {
	t.Helper()
	now := time.Now().UnixMilli()
	var listed strings.Builder
	listed.WriteByte('[')
	for index := range sessions {
		if index > 0 {
			listed.WriteByte(',')
		}
		fmt.Fprintf(&listed, `{"session_id":"load-session-%06d","title":"A worker of the load test, number %d","state":"live","last_seen":%d,"roles":["worker"],"capabilities":["chat"],"machine_id":"load-machine","host":"load-host"}`,
			index, index, now-int64(index))
	}
	listed.WriteByte(']')
	body := []byte(listed.String())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// seed writes the project of loadIssues issues, the issue of loadComments comments, and a spec of
// 1 MiB for each run, and fills the document read cache to its budget, as production's is
// (/debug/vars of 2026-11-20: 268,213,454 bytes of its 268,435,456).
func (r *loadRig) seed(t *testing.T) {
	t.Helper()
	s := r.server
	s.createProject(t, r.project, "Load")
	r.events, _ = s.createIssueIn(t, r.project, "The issue whose events the runs read", "Context.\n")
	r.parallel(t, loadIssues, func(index int) (string, string) {
		return "/api/v1/issues", fmt.Sprintf(`{"project":%q,"title":"Issue %d of the load test","force":true}`, r.project, index)
	})
	r.parallel(t, loadComments, func(index int) (string, string) {
		return "/api/v1/issues/" + r.events + "/comments", fmt.Sprintf(`{"body":"Load comment %d, which the runs search for."}`, index)
	})
	spec := loadMarkdown(loadSpec)
	for index := range loadRuns {
		issue, document := s.createIssueIn(t, r.project, fmt.Sprintf("The spec run %d reads and edits", index), spec)
		r.specs = append(r.specs, loadSpecOf{issue: issue, document: document})
	}
	r.fillReadCache(t, spec)
}

// loadMarkdown is prose of at least length bytes, in paragraphs of about a kilobyte, with the word
// sentinel in its first, which each run's edit replaces.
func loadMarkdown(length int) string {
	var markdown strings.Builder
	markdown.WriteString("The sentinel of this spec.\n\n")
	for paragraph := 0; markdown.Len() < length; paragraph++ {
		fmt.Fprintf(&markdown, "Paragraph %d: %s\n\n", paragraph, strings.Repeat("a run at its limits reads this spec whole. ", 23))
	}
	return markdown.String()
}

// parallel sends count requests of the test's login, eight at a time, each the POST at the path
// with the body request names for its index, and fails the test unless each is answered 2xx.
func (r *loadRig) parallel(t *testing.T, count int, request func(index int) (path, body string)) {
	t.Helper()
	var mu sync.Mutex
	var failures []string
	indexes := make(chan int)
	var senders sync.WaitGroup
	for range 8 {
		senders.Go(func() {
			for index := range indexes {
				path, body := request(index)
				answered, err := r.server.trySend(http.MethodPost, path, body, asUser(dispatchTestLogin))
				if err != nil || answered.status/100 != 2 {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("POST %s: %s %v", path, answered, err))
					mu.Unlock()
				}
			}
		})
	}
	for index := range count {
		indexes <- index
	}
	close(indexes)
	senders.Wait()
	if len(failures) > 0 {
		t.Fatalf("%d of %d seed requests failed, the first: %s", len(failures), count, failures[0])
	}
}

// fillReadCache uploads one document of markdown, copies it in the database until the copies weigh
// more than the read cache's budget, and reads each copy's text, which the cold read puts in the
// cache, until the cache is within two entries of its budget.
func (r *loadRig) fillReadCache(t *testing.T, markdown string) {
	t.Helper()
	weight, err := docs.DocumentReadWeightForTest(markdown)
	if err != nil {
		t.Fatalf("weigh the document that fills the read cache: %v", err)
	}
	budget := roomFigures(t, r.profiler).ReadCache.Budget
	body, err := json.Marshal(map[string]string{"name": "read-cache-fill.md", "content": markdown})
	if err != nil {
		t.Fatal(err)
	}
	var uploaded struct {
		Artifact struct{ ID string } `json:"artifact"`
	}
	if err := json.Unmarshal(r.server.call(t, http.MethodPost, "/api/v1/issues/"+r.events+"/artifacts", string(body), http.StatusCreated), &uploaded); err != nil {
		t.Fatal(err)
	}
	r.waitSettled(t, uploaded.Artifact.ID)
	copies := int(budget/weight) + 4
	for number := range copies {
		copied := cloneArtifact(t, r.server.database, uploaded.Artifact.ID, fmt.Sprintf("read-cache-fill-%03d", number))
		r.server.call(t, http.MethodGet, "/api/v1/artifacts/"+copied+"/text", "", http.StatusOK)
	}
	held := roomFigures(t, r.profiler).ReadCache
	t.Logf("the read cache holds %d entries of %d MiB, its budget %d MiB, after %d reads of documents weighing %d MiB", held.Entries, held.Bytes>>20, held.Budget>>20, copies, weight>>20)
	if held.Entries == 0 || held.Bytes <= held.Budget-2*(held.Bytes/int64(held.Entries)) {
		t.Fatalf("the read cache holds %d entries of %d MiB, want within two entries of its %d MiB budget", held.Entries, held.Bytes>>20, held.Budget>>20)
	}
}

// waitSettled waits up to 2 minutes for the document to settle every write it has had.
func (r *loadRig) waitSettled(t *testing.T, artifactID string) {
	t.Helper()
	r.server.eventually(t, 2*time.Minute, "document "+artifactID+" to settle", func() bool {
		var owed bool
		if err := r.server.database.Pool.QueryRow(t.Context(),
			`select exists(select 1 from doc_settlements_pending where artifact_id = $1)`, artifactID).Scan(&owed); err != nil {
			t.Fatal(err)
		}
		return !owed
	})
}

// script scripts the run whose words are words as a Deep run at its limits on spec: ten reads -
// the project's issue listing, a search, the roster, the events of the issue of comments and its
// own spec of 1 MiB, twice over - each reply carrying loadReplyText of prose in a stream padded to
// loadPadding, then a plan that edits its spec, then its answer.
func (r *loadRig) script(words string, spec loadSpecOf) {
	reads := []agenttest.Reply{
		readCall("dispatch_issues", map[string]any{"project": r.project}),
		readCall("dispatch_search", map[string]any{"query": "load comment"}),
		readCall("read_roster", map[string]any{}),
		readCall("dispatch_read", map[string]any{"issue": r.events, "all_events": true}),
		readCall("dispatch_doc_read", map[string]any{"artifact": spec.document}),
	}
	prose := strings.Repeat("A Deep run at its limits thinks aloud. ", loadReplyText/39)
	r.model.set(words, func(n int) agenttest.Reply {
		var reply agenttest.Reply
		switch {
		case n <= 2*len(reads):
			reply = reads[(n-1)%len(reads)]
		case n == 2*len(reads)+1:
			reply = toolCall("propose_plan", map[string]any{"summary": "Edit the spec.", "changes": []map[string]any{{
				"tool": "dispatch_doc_edit", "arguments": map[string]any{"issue": spec.issue, "artifact": "spec",
					"ops": []map[string]any{{"op": "replace", "find": "sentinel", "with": "marker"}}},
			}}})
		default:
			return answer("Read everything and edited the spec.")
		}
		reply.Text, reply.Padding = prose, loadPadding
		return reply
	})
}

// loadPeaks is the most the server's live heap outside the rooms and the agent's total Go memory
// reached while runs worked.
type loadPeaks struct {
	serverOutside, agentTotal int64
}

// work starts a Deep run at its limits on each of specs, approves each plan as it waits, and
// fails the test unless each run ends done. While they work it reads the server's live heap outside
// the rooms every 2 s and the agent's total Go memory, as its renews carry it, every 500 ms, and
// once they have ended it reads the agent's for two renews more, since a renew carries it at most
// every 10 s and the runtime's total does not fall; it answers the most of each.
func (r *loadRig) work(t *testing.T, label string, specs []loadSpecOf) loadPeaks {
	t.Helper()
	starts := make([]agentRun, len(specs))
	for index, spec := range specs {
		words := fmt.Sprintf("%s, run %d", label, index)
		r.script(words, spec)
		starts[index] = agentRun{login: fmt.Sprintf("load%d", index), words: words, level: "deep"}
	}
	ids := r.server.begin(t, starts...)
	var peaks loadPeaks
	sampleAgent := func() {
		peaks.agentTotal = max(peaks.agentTotal, r.server.scrape(t)[`envoy_dispatch_process_memory_total_bytes{process="agent"}`])
	}
	approved := map[string]bool{}
	var heapAt, agentAt time.Time
	deadline := time.Now().Add(15 * time.Minute)
	for {
		if time.Since(heapAt) >= 2*time.Second {
			peaks.serverOutside = max(peaks.serverOutside, r.heapOutsideRooms(t))
			heapAt = time.Now()
		}
		if time.Since(agentAt) >= 500*time.Millisecond {
			sampleAgent()
			agentAt = time.Now()
		}
		ended := 0
		for id, row := range r.server.runRows(t, ids) {
			switch row.state {
			case "waiting":
				if !approved[id] {
					r.server.send(t, http.MethodPost, "/api/v1/going/runs/"+id+"/plan/approve", `{"plan":1}`, asUser(starts[slices.Index(ids, id)].login))
					approved[id] = true
				}
			case "done":
				ended++
			case "stopped", "failed":
				t.Fatalf("%s: run %s ended %s with %q, want done", label, id, row.state, row.reason)
			}
		}
		if ended == len(ids) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d of %d runs ended within 15 minutes:\n%s", label, ended, len(ids), r.server.Output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	for renewed := time.Now(); time.Since(renewed) < 25*time.Second; time.Sleep(500 * time.Millisecond) {
		sampleAgent()
	}
	return peaks
}

// heapOutsideRooms is the server's live heap outside the document rooms as one collection finds
// it: its profiler's heap profile makes it collect (gc=1, takeC28Set), /metrics shows the heap that
// collection found live, and what that profile's samples with a frame of the rooms hold is taken
// from it.
func (r *loadRig) heapOutsideRooms(t *testing.T) int64 {
	t.Helper()
	set := takeC28Set(t, r.profiler)
	live := r.server.scrape(t)[`envoy_dispatch_process_heap_live_bytes{process="server"}`]
	return live - set.inuse(t, loadRooms, nil)
}

// heapOutsideRoomsAtRest is the least of three reads of heapOutsideRooms 200 ms apart, so a
// request in flight at one of them is not counted.
func (r *loadRig) heapOutsideRoomsAtRest(t *testing.T) int64 {
	t.Helper()
	least := r.heapOutsideRooms(t)
	for range 2 {
		time.Sleep(200 * time.Millisecond)
		least = min(least, r.heapOutsideRooms(t))
	}
	return least
}

// TestTheServerHeapUnderEightDeepRuns: the server's memory for the agent's calls, and the agent's
// memory at its slots, have the bounds a load test measures (check c26). The seeded Dispatch holds
// as many rows as production of each kind the runs' routes read (the constants above say how many
// and why), and its read cache is full. One Deep run at its limits measures one run's peak - the
// most the agent's total Go memory rises while it works - which sets the slots: the claim gate,
// 70% of the agent's GOMEMLIMIT, less the agent's base, divided by that peak, 8 at the most; the
// agent's slots (agentSlots) hold that value. Then 8 Deep runs at once each read the issue
// listing, search, the roster, an issue's events and a spec of 1 MiB, twice, and edit their spec:
// the server's live heap outside the document rooms stays under 1 GiB above its level before them
// and, once the rooms have closed, comes back to within 5% of it, and the agent's total Go memory
// stays under its GOMEMLIMIT.
func TestTheServerHeapUnderEightDeepRuns(t *testing.T) {
	rig := startLoad(t)

	base := rig.server.scrape(t)[`envoy_dispatch_process_memory_total_bytes{process="agent"}`]
	one := rig.work(t, "one run's peak", rig.specs[:1])
	peak := one.agentTotal - base
	gate := int64(loadClaimGate * float64(rig.limits.AgentLimit))
	slots := loadRuns
	if peak > 0 {
		slots = int(min(int64(loadRuns), max(1, (gate-base)/peak)))
	}
	t.Logf("one Deep run at its limits raised the agent's total Go memory from its base of %d MiB by %d MiB; the claim gate of %d MiB (70%% of the agent's GOMEMLIMIT of %d MiB) less the base, divided by that peak, sets %d slots (8 at the most)",
		base>>20, peak>>20, gate>>20, rig.limits.AgentLimit>>20, slots)
	if slots != agentSlots {
		t.Errorf("one run's measured peak of %d MiB sets %d slots, and the agent works %d (agentSlots): set agentSlots to %d, citing this measurement", peak>>20, slots, agentSlots, slots)
	}

	waitForResidentRooms(t, rig.profiler, 0)
	before := rig.heapOutsideRoomsAtRest(t)
	eight := rig.work(t, "eight Deep runs", rig.specs)
	waitForResidentRooms(t, rig.profiler, 0)
	after := rig.heapOutsideRoomsAtRest(t)
	t.Logf("the server's live heap outside the rooms: %d MiB before the 8 runs, %d MiB at most while they worked (%+d MiB), %d MiB once the rooms closed; the agent's total Go memory at most %d MiB with its %d slots full, its GOMEMLIMIT %d MiB",
		before>>20, eight.serverOutside>>20, (eight.serverOutside-before)>>20, after>>20, eight.agentTotal>>20, slots, rig.limits.AgentLimit>>20)
	if rise := eight.serverOutside - before; rise >= loadServerRise {
		t.Errorf("under 8 Deep runs the server's live heap outside the rooms rose %d MiB above its level before them, want under %d MiB", rise>>20, loadServerRise>>20)
	}
	if settled := float64(after-before) / float64(before); settled > loadSettleShare {
		t.Errorf("once the rooms closed the server's live heap outside them is %d MiB, %.1f%% above its %d MiB before the runs, want within %.0f%%",
			after>>20, 100*settled, before>>20, 100*loadSettleShare)
	}
	if eight.agentTotal >= rig.limits.AgentLimit {
		t.Errorf("with its slots full the agent's total Go memory reached %d MiB, want under its GOMEMLIMIT of %d MiB", eight.agentTotal>>20, rig.limits.AgentLimit>>20)
	}
}

// The load of c29.
const (
	// cpuAgentLimit is the GOMEMLIMIT the agent works at the soft limit of: a gigabyte, so the
	// runner holds it with room to spare.
	cpuAgentLimit = 1 << 30
	// cpuReads is how many times the fixed read is timed, idle and busy.
	cpuReads = 500
	// cpuLivez is the longest /livez may take to answer while the agent works.
	cpuLivez = time.Second
)

// readTimes is how long each of count reads of path, one after another as the test's login, took
// to answer 200.
func (s agentServer) readTimes(t *testing.T, path string, count int) []time.Duration {
	t.Helper()
	times := make([]time.Duration, 0, count)
	for range count {
		started := time.Now()
		s.call(t, http.MethodGet, path, "", http.StatusOK)
		times = append(times, time.Since(started))
	}
	return times
}

// p99 is the 99th percentile of times.
func p99(times []time.Duration) time.Duration {
	sorted := slices.Clone(times)
	slices.Sort(sorted)
	return sorted[(len(sorted)*99+99)/100-1]
}

// TestTheAgentLeavesTheServerItsCPU: the agent cannot take the server's CPU (check c29). The
// agent works at its soft limit, its garbage collection included (going/agent's test_soft_limit
// hook: a live heap of nine tenths of its GOMEMLIMIT, and allocation without pause on each CPU its
// runtime schedules on). Meanwhile the p99 of one fixed read of the server, an issue, stays under
// twice its p99 with the agent idle, and /livez, asked every 100 ms, answers each time in under
// 1 s. The agent runs on at most 4 CPUs (GOMAXPROCS) at the lower priority of nice 10.
func TestTheAgentLeavesTheServerItsCPU(t *testing.T) {
	fake, model := newSoakModel(t)
	env := modelEnv(t, fake)
	env["DISPATCH_TEST_AGENT_GOMEMLIMIT"] = strconv.Itoa(cpuAgentLimit)
	server := startAgentServer(t, env)
	server.waitForAgentRenewal(t)
	server.createProject(t, "CPU", "CPU")
	key, _ := server.createIssueIn(t, "CPU", "The issue the fixed read reads", "Context.\n")
	read := "/api/v1/issues/" + key
	server.readTimes(t, read, 100)
	idle := p99(server.readTimes(t, read, cpuReads))

	const words = "work at the soft limit"
	model.set(words, func(n int) agenttest.Reply {
		if n == 1 {
			return toolCall("test_soft_limit", map[string]any{})
		}
		return answer("Done.")
	})
	run := server.begin(t, agentRun{login: dispatchTestLogin, words: words, level: "deep"})[0]
	server.waitForLog(t, 2*time.Minute, `msg="agent: test_soft_limit: the agent works at its soft limit"`)
	// The hook's heap is built once its line is written; the collector reaches the limit within
	// a few of its cycles.
	time.Sleep(5 * time.Second)

	var livez []time.Duration
	var livezFailures []string
	asking := make(chan struct{})
	var asked sync.WaitGroup
	asked.Go(func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-asking:
				return
			case <-ticker.C:
			}
			started := time.Now()
			response, err := http.Get(server.url("/livez"))
			took := time.Since(started)
			if err != nil {
				livezFailures = append(livezFailures, err.Error())
				continue
			}
			_ = response.Body.Close()
			if response.StatusCode/100 != 2 {
				livezFailures = append(livezFailures, response.Status)
			}
			livez = append(livez, took)
		}
	})
	busy := p99(server.readTimes(t, read, cpuReads))
	close(asking)
	asked.Wait()
	figures := server.scrape(t)
	server.send(t, http.MethodPost, "/api/v1/going/runs/"+run+"/stop", "", asUser(dispatchTestLogin))
	server.ended(t, run, time.Minute)

	slowest := slices.Max(append(livez, 0))
	t.Logf("the fixed read's p99: %v with the agent idle, %v with the agent at its soft limit (%.2f times); /livez answered %d times, the slowest in %v; the agent's live heap %d MiB and total Go memory %d MiB of its GOMEMLIMIT of %d MiB, as its last renew carried them",
		idle, busy, float64(busy)/float64(idle), len(livez), slowest,
		figures[`envoy_dispatch_process_heap_live_bytes{process="agent"}`]>>20, figures[`envoy_dispatch_process_memory_total_bytes{process="agent"}`]>>20, cpuAgentLimit>>20)
	if busy >= 2*idle {
		t.Errorf("with the agent at its soft limit the fixed read's p99 is %v, want under twice its %v with the agent idle", busy, idle)
	}
	if len(livezFailures) > 0 || len(livez) == 0 || slowest >= cpuLivez {
		t.Errorf("with the agent at its soft limit /livez answered %d times, the slowest in %v, and failed %d times (%v), want every answer under %v",
			len(livez), slowest, len(livezFailures), livezFailures, cpuLivez)
	}
}
