package replay

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The span every collect of these tests reads: from 2026-11-20 to 09:00 the next day, its inputs
// reaching four days before, so every captured line from 2026-11-16 on (the oldest of them from
// 2026-11-16T18:01Z) is in it; the samples of the two older gate registration shapes, from
// 2026-10-27 and 2026-11-10, are before it.
var (
	collectFrom   = time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC)
	collectTo     = time.Date(2026, time.November, 21, 9, 0, 0, 0, time.UTC)
	collectBefore = 96 * time.Hour
)

// The tokens the stubs take; a request with another is refused, so the test reads that the
// collect sent its token, and only as the Authorization header.
const (
	stubDispatchToken = "dispatch-test-token"
	stubGitHubToken   = "github-test-token"
)

// stubRequest is one request a stub served: its method and its path with its query.
type stubRequest struct {
	Method, Target string
}

// stubRequests is every request a stub served, in the order served.
type stubRequests struct {
	mu   sync.Mutex
	seen []stubRequest
}

func (s *stubRequests) add(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, stubRequest{Method: r.Method, Target: r.URL.RequestURI()})
}

func (s *stubRequests) all() []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// dispatchStub serves Dispatch's issue event log (GET /api/v1/issues/<KEY>/events?after=&limit=)
// from events, each issue's oldest first, and 404 for an issue it holds none of. The first read
// of each issue refused names is answered with that status, a refusal a later attempt clears.
func dispatchStub(t *testing.T, events map[string][]json.RawMessage, refused map[string]int) (*httptest.Server, *stubRequests) {
	t.Helper()
	requests := &stubRequests{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r)
		if r.Header.Get("Authorization") != "Bearer "+stubDispatchToken {
			http.Error(w, `{"code":"UNAUTHORIZED","error":"no token"}`, http.StatusUnauthorized)
			return
		}
		key, isEvents := strings.CutPrefix(r.URL.Path, "/api/v1/issues/")
		key, isEvents = strings.CutSuffix(key, "/events")
		if r.Method != http.MethodGet || !isEvents {
			http.Error(w, `{"code":"NOT_FOUND","error":"no such route"}`, http.StatusNotFound)
			return
		}
		mu.Lock()
		status, refuse := refused[key]
		delete(refused, key)
		mu.Unlock()
		if refuse {
			http.Error(w, `{"error":"try again"}`, status)
			return
		}
		log, known := events[key]
		if !known {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":"ISSUE_NOT_FOUND","error":"issue not found"}`)
			return
		}
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page := []json.RawMessage{}
		for _, event := range log {
			var head struct {
				Seq int64 `json:"seq"`
			}
			if err := json.Unmarshal(event, &head); err != nil {
				t.Errorf("a stub event is no event: %v", err)
			}
			if head.Seq > after && len(page) < limit {
				page = append(page, event)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(page); err != nil {
			t.Errorf("serve the events of %s: %v", key, err)
		}
	}))
	t.Cleanup(server.Close)
	return server, requests
}

// githubStub serves GitHub's commit listing of acme/widgets's main from pages, each page after the
// first reached through the Link header of the one before, as GitHub pages a listing. It refuses
// a request for another listing or without the query collect-merges sends.
func githubStub(t *testing.T, pages [][]byte) (*httptest.Server, *stubRequests) {
	t.Helper()
	requests := &stubRequests{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r)
		query := r.URL.Query()
		switch {
		case r.Header.Get("Authorization") != "Bearer "+stubGitHubToken:
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		case r.Method != http.MethodGet || r.URL.Path != "/repos/acme/widgets/commits", query.Get("sha") != capturedBranch,
			query.Get("since") != "2026-11-20T00:00:00Z", query.Get("until") != "2026-11-20T06:00:00Z", query.Get("per_page") != "100":
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		page := 1
		if text := query.Get("page"); text != "" {
			page, _ = strconv.Atoi(text)
		}
		if page < len(pages) {
			next := *r.URL
			query.Set("page", strconv.Itoa(page+1))
			next.RawQuery = query.Encode()
			w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next", <%s%s>; rel="last"`, server.URL, next.RequestURI(), server.URL, next.RequestURI()))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(pages[page-1])
	}))
	t.Cleanup(server.Close)
	return server, requests
}

// capturedEvents is every captured Dispatch event by its issue, each issue's oldest first.
func capturedEvents(t *testing.T) map[string][]json.RawMessage {
	t.Helper()
	events := make(map[string][]json.RawMessage)
	for _, name := range capturedFiles(t) {
		if !strings.HasPrefix(name, "events/") {
			continue
		}
		event := capturedOne[json.RawMessage](t, name)
		var head struct {
			Key string `json:"issue_key"`
		}
		if err := json.Unmarshal(event, &head); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		events[head.Key] = append(events[head.Key], event)
	}
	for _, log := range events {
		slices.SortFunc(log, func(a, b json.RawMessage) int { return cmp.Compare(seqOf(t, a), seqOf(t, b)) })
	}
	return events
}

func seqOf(t *testing.T, event json.RawMessage) int64 {
	t.Helper()
	var head struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal(event, &head); err != nil {
		t.Fatal(err)
	}
	return head.Seq
}

// capturedEntries is every captured log entry, by its file.
func capturedEntries(t *testing.T) map[string]capturedEntry {
	t.Helper()
	entries := make(map[string]capturedEntry)
	for _, name := range capturedFiles(t) {
		if strings.HasPrefix(name, "log/") {
			entries[name] = capturedOne[capturedEntry](t, name)
		}
	}
	return entries
}

// daemonOrder is line, a daemon line as Cloud Logging keeps it (its attributes sorted), as the
// daemon's JSON handler wrote it on stderr: time, level and msg first, then the rest.
func daemonOrder(t *testing.T, line []byte) []byte {
	t.Helper()
	value, err := decodeOrdered(line)
	if err != nil {
		t.Fatal(err)
	}
	object := value.(jsonObject)
	head := map[string]int{"time": 0, "level": 1, "msg": 2}
	slices.SortStableFunc(object, func(a, b jsonMember) int {
		rank := func(m jsonMember) int {
			if r, ok := head[m.key]; ok {
				return r
			}
			return len(head)
		}
		return rank(a) - rank(b)
	})
	var out bytes.Buffer
	if err := encodeOrdered(&out, object); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// compactJSON is data compact, as a window's file holds each line.
func compactJSON(t *testing.T, data []byte) json.RawMessage {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, data); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// The lines a collect finds only in the kubectl capture, beside the captured ones: a slot taken
// two days before the captured cap, under another cap, and an issue that waits on another; each
// names an issue no captured line names. The first is written with --prefix as well as
// --timestamps.
const (
	collectSlotTaken = `{"time":"2026-11-19T00:00:00.5Z","level":"INFO","msg":"admission: slot taken","issue":"ACME-58","slots":150,"cap":150}`
	collectWaitsOn   = `{"time":"2026-11-19T01:00:00Z","level":"INFO","msg":"admission waits for what this issue waits on","issue":"ACME-27","blockers":["ACME-42"]}`
)

// collectChildAdded is the child.added event of ACME-32 naming the child ACME-60 that Dispatch
// answers a collect of the captured samples with, beside the captured events.
const collectChildAdded = `{"id":900001,"issue_key":"ACME-32","artifact_id":null,"project":"ACME","seq":5,"type":"child.added",` +
	`"actor":{"kind":"session","id":"legion-daemon:ACME"},"notify":false,"created_at":"2026-11-20T10:00:00Z","payload":{"child_key":"ACME-60"}}`

// collectRun is one collect and collect-merges of the captured samples against the stubs, and
// what each stub saw.
type collectRun struct {
	out       string
	collected Collected
	merges    []Merge
	dispatch  []stubRequest
	github    []stubRequest
	waits     []time.Duration
	captured  map[string]capturedEntry
	events    map[string][]json.RawMessage
	settings  Settings
	parts     []Part
}

// collectCaptured writes the captured daemon lines as the three forms a person collects them in —
// a gzipped Cloud Logging export of JSON lines, a Cloud Logging export as one JSON array, and
// `kubectl logs --timestamps` output under a directory of its own — and collects the window from
// them against a Dispatch stub answering the captured events and a GitHub stub answering the
// captured commit page in two pages, then collect-merges main of acme/widgets into it. The export
// holds every captured line, a row with no jsonPayload and a line of a kind the trace does not
// read; the array a line before the span and one at its end; the kubectl output every captured
// line again (its attributes in the daemon's own order), the two lines of its own, a line of other
// output and a line of a kind the trace does not read. Dispatch also answers a child.added event
// of ACME-32 naming the child ACME-60, and an event of ACME-29 after the span; it refuses the first
// read of ACME-29 with 503 and of ACME-58 with 429.
func collectCaptured(t *testing.T) collectRun {
	t.Helper()
	run := collectRun{captured: capturedEntries(t), events: capturedEvents(t)}
	logs := t.TempDir()

	var export bytes.Buffer
	zipped := gzip.NewWriter(&export)
	var kubectl bytes.Buffer
	for _, name := range slices.Sorted(maps.Keys(run.captured)) {
		entry := run.captured[name]
		fmt.Fprintf(zipped, "{\"timestamp\":%q,\"jsonPayload\":%s}\n", entry.Timestamp, compactJSON(t, entry.JSONPayload))
		fmt.Fprintf(&kubectl, "%s %s\n", entry.Timestamp, daemonOrder(t, entry.JSONPayload))
	}
	fmt.Fprintln(zipped, `{"timestamp":"2026-11-20T01:00:00Z","textPayload":"a line the daemon wrote that is no JSON"}`)
	fmt.Fprintln(zipped, `{"timestamp":"2026-11-20T01:00:01Z","jsonPayload":{"time":"2026-11-20T01:00:01Z","level":"INFO","msg":"daemon: the image probe's attempt outlives slow_command_timeout_seconds"}}`)
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&kubectl, "[pod/legion-acme-daemon-0/legion] 2026-11-19T00:00:00.6Z %s\n", collectSlotTaken)
	fmt.Fprintf(&kubectl, "2026-11-19T01:00:00.1Z %s\n", collectWaitsOn)
	fmt.Fprintln(&kubectl, "2026-11-19T02:00:00Z goroutine dump follows: this is no JSON line")
	fmt.Fprintln(&kubectl, `2026-11-19T03:00:00Z {"time":"2026-11-19T03:00:00Z","level":"INFO","msg":"legion workflow boot stage done elsewhere"}`)
	array := `[
  {"timestamp":"2026-11-10T00:00:01Z","jsonPayload":{"time":"2026-11-10T00:00:00Z","level":"INFO","msg":"admission: slot taken","issue":"ACME-70","slots":1,"cap":9}},
  {"timestamp":"2026-11-21T09:00:01Z","jsonPayload":{"time":"2026-11-21T09:00:00Z","level":"INFO","msg":"admission: slot taken","issue":"ACME-71","slots":1,"cap":9}}
]`
	for name, data := range map[string][]byte{
		"a-export.jsonl.gz":   export.Bytes(),
		"b-export-array.json": []byte(array),
		"c/kubectl.log":       kubectl.Bytes(),
	} {
		file := filepath.Join(logs, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	served := maps.Clone(run.events)
	served["ACME-32"] = []json.RawMessage{json.RawMessage(collectChildAdded)}
	served["ACME-29"] = append(slices.Clone(served["ACME-29"]), json.RawMessage(`{"id":900002,"issue_key":"ACME-29","artifact_id":null,"project":"ACME","seq":71,`+
		`"type":"message.created","actor":{"kind":"session","id":"legion-daemon:ACME"},"notify":false,"created_at":"2026-11-21T10:00:00Z","payload":{"body":"after the span"}}`))
	dispatchServer, dispatchSeen := dispatchStub(t, served, map[string]int{"ACME-29": http.StatusServiceUnavailable, "ACME-58": http.StatusTooManyRequests})

	var page []json.RawMessage
	if err := json.Unmarshal(capturedFile(t, "github/commits.json"), &page); err != nil {
		t.Fatal(err)
	}
	var pages [][]byte
	for _, part := range [][]json.RawMessage{page[:3], page[3:]} {
		data, err := json.Marshal(part)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, data)
	}
	githubServer, githubSeen := githubStub(t, pages)

	settings, err := DaemonSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.TickInterval = 30 * time.Minute
	settings.GateSteps = []record.DesignStep{record.DesignStepSpec}
	run.settings = settings
	run.parts = []Part{{From: collectFrom, To: collectTo, Release: "legion-v19.41.3", Commit: record.MainRule.Commit, Rule: record.MainRule.Name}}
	var waitsMu sync.Mutex
	wait := func(_ context.Context, d time.Duration) error {
		waitsMu.Lock()
		defer waitsMu.Unlock()
		run.waits = append(run.waits, d)
		return nil
	}
	run.out = filepath.Join(t.TempDir(), "window")
	run.collected, err = Collect(context.Background(), CollectConfig{
		Project: "ACME", From: collectFrom, To: collectTo, Before: collectBefore, Logs: []string{logs},
		Dispatch: dispatch.New(dispatchServer.URL, stubDispatchToken), Concurrency: 3, Settings: settings, Parts: run.parts,
		Collector: "0123456789abcdef0123456789abcdef01234567", Out: run.out, Wait: wait,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	run.merges, err = CollectMerges(context.Background(), MergesConfig{Repo: "acme/widgets", Branch: capturedBranch,
		From: collectFrom, To: time.Date(2026, time.November, 20, 6, 0, 0, 0, time.UTC),
		Token: stubGitHubToken, API: githubServer.URL, Window: run.out, Wait: wait})
	if err != nil {
		t.Fatalf("CollectMerges: %v", err)
	}
	run.dispatch, run.github = dispatchSeen.all(), githubSeen.all()
	return run
}

// A collect of the captured samples writes a window whose every file reads back through
// ReadWindow as written: the log holds each captured line written in the collect's reach once, as
// Cloud Logging kept it, with the kubectl capture's own two lines, in time order, and no line
// before the reach (two captured lines among them), at the span's end, of a kind the trace does
// not read, or that is no daemon line; window.json holds the project, the
// span, the reach, the cap of each change (the earlier line's 150, then the daemon's start line's
// 157), the kinds the log holds, the settings, the part and the collector; the events are each
// captured issue's that a line names, up to the span's end, the blocker's and parent's those
// events name, and the child a child.added names, every other key Dispatch did not have listed as
// missing; and the merges are main's five first-parent commits, read over two pages. Every request
// the collect and the collect-merges send is a GET: the stubs' record of what each served holds no
// other method, and holds requests (a collect that read nothing would pass a check of its methods
// alone).
func TestCollectWritesAWindowThatReadsBackAndSendsGetAlone(t *testing.T) {
	run := collectCaptured(t)
	read, err := ReadWindow(run.out)
	if err != nil {
		t.Fatalf("ReadWindow of the collected window: %v", err)
	}

	type timed struct {
		at   time.Time
		line json.RawMessage
	}
	reach := collectFrom.Add(-collectBefore)
	var want []timed
	// add holds line among the lines the log keeps where it was written in the collect's reach,
	// [reach, collectTo), and says whether it was.
	add := func(line []byte) bool {
		var head struct {
			Time time.Time `json:"time"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			t.Fatal(err)
		}
		if head.Time.Before(reach) || !head.Time.Before(collectTo) {
			return false
		}
		want = append(want, timed{at: head.Time, line: compactJSON(t, line)})
		return true
	}
	inReach := 0
	for _, entry := range run.captured {
		if add(entry.JSONPayload) {
			inReach++
		}
	}
	add([]byte(collectSlotTaken))
	add([]byte(collectWaitsOn))
	slices.SortFunc(want, func(a, b timed) int { return a.at.Compare(b.at) })
	if len(read.Log) != len(want) {
		t.Errorf("the log holds %d lines, want %d", len(read.Log), len(want))
	}
	kinds := make(map[string]bool)
	for i, line := range want {
		if i < len(read.Log) && !bytes.Equal(read.Log[i], line.line) {
			t.Errorf("log line %d is\n  %s\nwant\n  %s", i+1, read.Log[i], line.line)
		}
		var head struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal(line.line, &head); err != nil {
			t.Fatal(err)
		}
		kinds[head.Msg] = true
	}
	if got, wantKinds := read.Lines, slices.Sorted(maps.Keys(kinds)); !slices.Equal(got, wantKinds) {
		t.Errorf("window.json's lines are %q, want %q", got, wantKinds)
	}
	if c := run.collected; c.Kept != len(want) || c.Duplicates != inReach || c.NotLines != 2 || c.Read != 2*len(run.captured)+6 {
		t.Errorf("the collect read %d lines, kept %d, left %d out as read twice and %d as no daemon line; want %d, %d, %d and 2",
			c.Read, c.Kept, c.Duplicates, c.NotLines, 2*len(run.captured)+6, len(want), inReach)
	}

	wantCaps := []CapAt{{At: time.Date(2026, time.November, 19, 0, 0, 0, 5e8, time.UTC), Cap: 150},
		{At: capturedTime(t, "2026-11-21T02:01:27.894781099Z"), Cap: 157}}
	if !sameReading(read.Caps, wantCaps) {
		t.Errorf("window.json's caps are %+v, want %+v", read.Caps, wantCaps)
	}
	s := run.settings
	if read.Project != "ACME" || !read.From.Equal(collectFrom) || !read.To.Equal(collectTo) || time.Duration(read.Before) != collectBefore ||
		time.Duration(read.TickInterval) != s.TickInterval || !slices.Equal(read.GateSteps, s.GateSteps) || time.Duration(read.Linger) != s.Linger ||
		read.ReviewRoundCap != s.ReviewRoundCap || time.Duration(read.StalledTreeNotice) != s.StalledTreeNotice ||
		time.Duration(read.WorkerSilence) != s.WorkerSilence || time.Duration(read.WorkerBackground) != s.WorkerBackground ||
		read.Collector != "0123456789abcdef0123456789abcdef01234567" || !sameReading(read.Parts, run.parts) || len(read.Experiments) != 0 {
		t.Errorf("window.json reads back as %+v, want the project, span, reach, settings, part and collector the collect was given", read)
	}

	wantEvents := map[string][]json.RawMessage{
		"ACME-32": {compactJSON(t, []byte(collectChildAdded))},
	}
	for _, key := range []string{"ACME-29", "ACME-27", "ACME-58"} {
		for _, event := range run.events[key] {
			wantEvents[key] = append(wantEvents[key], compactJSON(t, event))
		}
	}
	if !reflect.DeepEqual(read.Events, wantEvents) {
		t.Errorf("the window's events are of %v, want of %v (each issue's captured events, ACME-29's without the one after the span)",
			slices.Sorted(maps.Keys(read.Events)), slices.Sorted(maps.Keys(wantEvents)))
		for key, events := range wantEvents {
			if !reflect.DeepEqual(read.Events[key], events) {
				t.Errorf("the events of %s are\n  %s\nwant\n  %s", key, read.Events[key], events)
			}
		}
	}
	missing := map[string]bool{"ACME-24": true, "ACME-15": true, "ACME-52": true, "ACME-53": true, "ACME-60": true, "ACME-42": true, "ACME-41": true}
	for _, n := range []int{1, 2, 3, 4, 5, 6, 7, 8, 10, 11, 12, 13, 14, 16, 18, 22, 26, 28, 30, 31, 33, 34, 35, 36, 37, 38, 39, 40, 43, 44, 45, 46, 47, 48, 49, 50, 51, 56, 57} {
		missing[fmt.Sprintf("ACME-%d", n)] = true
	}
	wantMissing := slices.Sorted(maps.Keys(missing))
	if !slices.Equal(run.collected.Missing, wantMissing) {
		t.Errorf("the collect lists as missing %v, want %v", run.collected.Missing, wantMissing)
	}
	for _, request := range run.dispatch {
		for _, unread := range []string{"ACME-55", "ACME-70", "ACME-71"} {
			if strings.Contains(request.Target, "/"+unread+"/") {
				t.Errorf("the collect read %s, which no kept line and no event of a named issue names", request.Target)
			}
		}
	}
	if !slices.Equal(run.waits, []time.Duration{time.Second, time.Second}) {
		t.Errorf("the reads Dispatch refused for now waited %v, want one second each before their second attempt", run.waits)
	}

	if want := capturedMergeReadings(t)["commits.json"]; len(read.Merges) != 1 || !sameReading(read.Merges["acme/widgets"], want) || !sameReading(run.merges, want) {
		t.Errorf("the window's merges are %+v (collect-merges answered %+v), want acme/widgets's %+v", read.Merges, run.merges, want)
	}
	if len(run.github) != 2 {
		t.Errorf("collect-merges read %d pages, want the listing's 2: %v", len(run.github), run.github)
	}

	if len(run.dispatch) == 0 || len(run.github) == 0 {
		t.Fatalf("the stubs served %d Dispatch and %d GitHub requests; the collect read nothing", len(run.dispatch), len(run.github))
	}
	for stub, requests := range map[string][]stubRequest{"Dispatch": run.dispatch, "GitHub": run.github} {
		for _, request := range requests {
			if request.Method != http.MethodGet {
				t.Errorf("the collect sent %s %s %s; it sends GET alone", stub, request.Method, request.Target)
			}
		}
	}
}

// The settings a collect records where the deployment set none are the daemon's own defaults, read
// from the configuration's key table: an hour's tick, the gate on spec, a 72-hour linger, three
// review rounds, a six-hour stalled-tree notice, 30 minutes of worker silence and four hours of
// background work.
func TestCollectDefaultsToTheDaemonsSettings(t *testing.T) {
	got, err := DaemonSettings()
	if err != nil {
		t.Fatalf("DaemonSettings: %v", err)
	}
	want := Settings{TickInterval: time.Hour, GateSteps: []record.DesignStep{record.DesignStepSpec}, Linger: 72 * time.Hour,
		ReviewRoundCap: 3, StalledTreeNotice: 6 * time.Hour, WorkerSilence: 30 * time.Minute, WorkerBackground: 4 * time.Hour}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DaemonSettings = %+v, want %+v", got, want)
	}
}

// A collect writes a window into a directory of its own: an output that holds a file already is
// refused before anything is read, and the file is left as it was.
func TestCollectRefusesAnOutputThatHoldsFiles(t *testing.T) {
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := DaemonSettings()
	if err != nil {
		t.Fatal(err)
	}
	server, seen := dispatchStub(t, map[string][]json.RawMessage{}, nil)
	_, err = Collect(context.Background(), CollectConfig{Project: "ACME", From: collectFrom, To: collectTo, Logs: []string{out},
		Dispatch: dispatch.New(server.URL, stubDispatchToken), Settings: settings, Collector: "abc", Out: out})
	if err == nil || !strings.Contains(err.Error(), "notes.txt") {
		t.Errorf("Collect into a directory holding notes.txt = %v, want a refusal naming it", err)
	}
	if len(seen.all()) != 0 {
		t.Errorf("the refused collect read Dispatch: %v", seen.all())
	}
	if data, err := os.ReadFile(filepath.Join(out, "notes.txt")); err != nil || string(data) != "mine" {
		t.Errorf("notes.txt reads %q, %v after the refused collect; want it as it was", data, err)
	}
}

// A collect with a run-out reads past the window's end (the architect's D12): the daemon's lines and
// each issue's Dispatch events written in [To, To + after) join the window, which records the margin
// in window.json, and a line or an event written at the run-out's end or later stays out, as one
// written at To does without a run-out. The test collects three slot lines of ACME-9, an hour before
// the span's end, an hour after it and two hours after it, with a run-out of two hours, against a
// Dispatch stub answering three events of ACME-9 at the same moments. It fails where a collect stops
// at the span's end or records no margin.
func TestCollectReadsTheRunOutAfterTheWindow(t *testing.T) {
	moments := []time.Time{collectTo.Add(-time.Hour), collectTo.Add(time.Hour), collectTo.Add(2 * time.Hour)}
	var log bytes.Buffer
	var lines, events []json.RawMessage
	for i, at := range moments {
		line := fmt.Sprintf(`{"time":%q,"level":"INFO","msg":"admission: slot taken","issue":"ACME-9","slots":%d,"cap":9}`,
			at.Format(time.RFC3339Nano), i+1)
		fmt.Fprintln(&log, line)
		lines = append(lines, compactJSON(t, []byte(line)))
		events = append(events, compactJSON(t, fmt.Appendf(nil, `{"id":%d,"issue_key":"ACME-9","artifact_id":null,"project":"ACME","seq":%d,`+
			`"type":"message.created","actor":{"kind":"session","id":"legion-daemon:ACME"},"notify":false,"created_at":%q,"payload":{"body":"note %d"}}`,
			900101+i, i+1, at.Format(time.RFC3339Nano), i+1)))
	}
	logs := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(logs, log.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	server, _ := dispatchStub(t, map[string][]json.RawMessage{"ACME-9": events}, nil)
	settings, err := DaemonSettings()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "window")
	if _, err := Collect(context.Background(), CollectConfig{Project: "ACME", From: collectFrom, To: collectTo, Before: time.Hour,
		After: 2 * time.Hour, Logs: []string{logs}, Dispatch: dispatch.New(server.URL, stubDispatchToken), Settings: settings,
		Collector: "0123456789abcdef0123456789abcdef01234567", Out: out}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	read, err := ReadWindow(out)
	if err != nil {
		t.Fatalf("ReadWindow of the collected window: %v", err)
	}
	if time.Duration(read.After) != 2*time.Hour || !read.To.Equal(collectTo) {
		t.Errorf("window.json reads back a span to %s with a run-out of %s, want to %s with one of 2h0m0s", stamp(read.To),
			time.Duration(read.After), stamp(collectTo))
	}
	if !reflect.DeepEqual(read.Log, lines[:2]) {
		t.Errorf("the log holds\n  %s\nwant the lines before the run-out's end\n  %s", read.Log, lines[:2])
	}
	if !reflect.DeepEqual(read.Events["ACME-9"], events[:2]) {
		t.Errorf("ACME-9's events are\n  %s\nwant those before the run-out's end\n  %s", read.Events["ACME-9"], events[:2])
	}
}
