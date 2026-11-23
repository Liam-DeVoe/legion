//go:build basebuild

package api

// This tree's check c-moved-read-cost: no read that R32 moves into the graph store gets slower,
// on this head after the move or on the release before after a rollback.
//
// The test is TestNoMovedReadGetsSlower at the end of this file. It carries the basebuild tag and
// stands in this package beside c-read-cost's tree_read_cost_test.go, whose servers, client,
// database and sampling it shares, because it builds the server of an earlier commit
// (testbaseline.Build), which needs a checkout that holds that commit. It writes its corpus by SQL
// from testdata/moved-read-cost-corpus.sql. It is run on its own, by name, as
//
//	STORES_BASE_SHA=$(git merge-base origin/main HEAD) go test -tags basebuild ./internal/dispatch/api/ -run '^TestNoMovedReadGetsSlower$' -timeout 90m -count=1 -v
//
// from packages/envoy, with DISPATCH_TEST_DATABASE_URL naming a Postgres server it may create a
// database on, over TCP and without TLS (the plan proxy relays that connection in the clear).
// MOVED_READ_COST_SCALE scales the corpus (movedCostScale); the check's run leaves it unset.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/testbaseline"
	"github.com/sjawhar/envoy/internal/testwait"
)

// movedCostBaseVariable names the release before R32's move: the commit `git merge-base
// origin/main HEAD` answers, which c-stores-moved boots as that release. It is a variable of its
// own and not testbaseline.Variable, because the check's run names it so.
const movedCostBaseVariable = "STORES_BASE_SHA"

// movedCostScaleVariable scales the corpus: unset or 1 is the check's corpus, 0.1 a tenth. Every
// count of issues, priorities and tuples scales; an issue keeps its runs and its children.
const movedCostScaleVariable = "MOVED_READ_COST_SCALE"

// movedCostMigrations are the three migrations a release before R32's move holds: the graph store,
// and the two stores R32 moves into it. A base that lacks one is no such release.
var movedCostMigrations = []string{
	"20261109013400_graph_store.up.sql",
	"20261112024320_priority_nodes.up.sql",
	"20261116235202_workflow_nodes.up.sql",
}

// movedCostMigrationsDir is where a commit holds its migrations, from the repository's root.
const movedCostMigrationsDir = "packages/envoy/internal/dispatch/store/migrations"

// movedCostBound is the bound of each read on both sides: twice its old side's p50, the bound
// c-read-cost holds its reads to on the new side, and R22's bound for every read on the third.
const movedCostBound = 2.0

// The projects of the corpus. The steps read and the plans read address the first of the three;
// the issues that advance priorities stand in a project of their own.
var movedCostProjects = []string{"MOVA", "MOVB", "MOVC"}

const (
	movedCostAdvancesProject = "MOVP"
	// movedCostPriority is the priority the history read and the advances filter name.
	movedCostPriority = "priority-01"
	// movedCostOtherWho is the who of every tuple of other kinds the corpus writes.
	movedCostOtherWho = `{"kind": "session", "id": "moved-read-cost"}`
)

// movedCostCorpusPath is the corpus's SQL, which the test runs whole over its own connection.
var movedCostCorpusPath = filepath.Join("testdata", "moved-read-cost-corpus.sql")

// ───── the corpus ─────────────────────────────────────────────────────────

// movedCostCorpus is the corpus's counts at the run's scale, as the corpus's SQL computes them, and
// the issues the reads address.
type movedCostCorpus struct {
	scale                                                     float64
	priorities, live, advancedIssues, advances, finished, ran int
	others                                                    int64
	// busy is the issue of 200 runs and 339 other children; advancing is an issue of the advances
	// project whose parent advances priorities too.
	busy, advancing string
}

func newMovedCostCorpus(t *testing.T, scale float64) *movedCostCorpus {
	t.Helper()
	scaled := func(full float64) int { return int(math.Round(full * scale)) }
	corpus := &movedCostCorpus{
		scale: scale, priorities: scaled(50), advancedIssues: scaled(2000), advances: scaled(5000),
		finished: scaled(3461), ran: scaled(600), others: int64(math.Round(1_000_000 * scale)),
		busy: movedCostProjects[0] + "-2", advancing: movedCostAdvancesProject + "-3",
	}
	corpus.live = corpus.priorities - corpus.priorities/25
	if corpus.priorities < 3 || corpus.advancedIssues < 4 || 5*corpus.ran > corpus.finished {
		t.Fatalf("%s=%v leaves too small a corpus: %d priorities, %d advanced issues, %d issues run of %d finished",
			movedCostScaleVariable, scale, corpus.priorities, corpus.advancedIssues, corpus.ran, corpus.finished)
	}
	return corpus
}

// movedCostScale reads movedCostScaleVariable: unset is 1, the check's corpus.
func movedCostScale(t *testing.T) float64 {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(movedCostScaleVariable))
	if raw == "" {
		return 1
	}
	scale, err := strconv.ParseFloat(raw, 64)
	if err != nil || !(scale > 0 && scale <= 1) {
		t.Fatalf("%s=%q is no scale above 0 and at most 1", movedCostScaleVariable, raw)
	}
	if scale != 1 {
		t.Logf("c-moved-read-cost: %s=%s: the corpus is that share of the check's, so this run is no reading of the check",
			movedCostScaleVariable, raw)
	}
	return scale
}

// seedMovedCostTemplates makes, through the release before's own API, the four projects and the
// first issue of each, whose row and node the corpus's SQL copies for every issue it writes.
func seedMovedCostTemplates(t *testing.T, s *readCostServer) {
	t.Helper()
	for _, person := range []string{readCostReader, readCostWriter} {
		s.expect(t, "sign in "+person, http.MethodGet, "/api/v1/whoami", person, nil, http.StatusOK)
	}
	for _, project := range append(slices.Clone(movedCostProjects), movedCostAdvancesProject) {
		s.expect(t, "create the project "+project, http.MethodPost, "/api/v1/projects", readCostWriter,
			map[string]string{"key": project, "name": "The reads R32 moves, project " + project}, http.StatusCreated)
		if key := createReadCostIssue(t, s, project, "The first issue of "+project+", whose row the corpus copies", "", readCostWriter); key != project+"-1" {
			t.Fatalf("the first issue of %s is %s, where the corpus copies %s-1", project, key, project)
		}
	}
}

// seedMovedCostCorpus runs the corpus's SQL over conn with the run's settings, and holds what it
// wrote to the counts of the check before anything is measured.
func seedMovedCostCorpus(t *testing.T, conn *pgx.Conn, corpus *movedCostCorpus) {
	t.Helper()
	ctx := context.Background()
	text, err := os.ReadFile(movedCostCorpusPath)
	if err != nil {
		t.Fatalf("read the corpus's SQL: %v", err)
	}
	if _, err := conn.Exec(ctx, `select set_config('moved_read_cost.scale', $1, false),
		set_config('moved_read_cost.projects', $2, false), set_config('moved_read_cost.advances_project', $3, false)`,
		strconv.FormatFloat(corpus.scale, 'f', -1, 64), strings.Join(movedCostProjects, ","), movedCostAdvancesProject); err != nil {
		t.Fatalf("set the corpus's settings: %v", err)
	}
	started := time.Now()
	if _, err := conn.Exec(ctx, string(text)); err != nil {
		t.Fatalf("write the corpus by SQL (%s): %v", movedCostCorpusPath, err)
	}
	t.Logf("c-moved-read-cost: wrote the corpus by SQL at scale %v in %s", corpus.scale, time.Since(started).Round(time.Millisecond))

	// corpusCount is one count of the corpus as SQL reads it, held to want within slack.
	type corpusCount struct {
		what  string
		sql   string
		args  []any
		want  int64
		slack int64
	}
	projects := len(movedCostProjects)
	counts := []corpusCount{
		{"priorities", `select count(*) from priority_nodes`, nil, int64(corpus.priorities), 0},
		{"live priorities", `select count(*) from priority_nodes where state = 'live'`, nil, int64(corpus.live), 0},
		{"advances tuples", `select count(*) from priority_tuples where kind = 'advances'`, nil, int64(corpus.advances), 0},
		{"issues that state advances", `select count(distinct from_id) from priority_tuples where kind = 'advances'`, nil, int64(corpus.advancedIssues), 0},
		{"runs", `select count(*) from workflow_nodes where kind = 'step'`, nil, int64(projects * (30*corpus.ran + 200)), 0},
		{"plans", `select count(*) from workflow_nodes where kind = 'plan'`, nil, int64(projects * (corpus.ran + 1)), 0},
		{"runs part_of " + corpus.busy, `select count(*) from workflow_tuples where from_kind = 'step' and kind = 'part_of' and to_kind = 'issue' and to_id = $1`,
			[]any{corpus.busy}, 200, 0},
		{"other part_of children of " + corpus.busy, `select count(*) from graph_tuples where kind = 'part_of' and to_kind = 'issue' and to_id = $1`,
			[]any{corpus.busy}, 339, 0},
		{"other part_of children of the issues run 30 times", `select count(*) from graph_tuples where kind = 'part_of' and to_kind = 'issue' and to_id <> all ($1)`,
			[]any{[]string{movedCostProjects[0] + "-2", movedCostProjects[1] + "-2", movedCostProjects[2] + "-2"}}, int64(projects * 100 * corpus.ran), 0},
		// Each of the 59 terms' shares is rounded on its own, so the sum may stray by half a tuple
		// for each term.
		{"tuples of other kinds", `select count(*) from graph_tuples where who = $1::jsonb`, []any{movedCostOtherWho}, corpus.others, 30},
	}
	for _, project := range movedCostProjects {
		counts = append(counts, corpusCount{"finished issues of " + project,
			`select count(*) from issues where project_key = $1 and status = 'done' and closed_at is not null`,
			[]any{project}, int64(corpus.finished), 0})
	}
	for _, count := range counts {
		var read int64
		if err := conn.QueryRow(ctx, count.sql, count.args...).Scan(&read); err != nil {
			t.Fatalf("count the corpus's %s: %v", count.what, err)
		}
		t.Logf("c-moved-read-cost: the corpus holds %d %s (the check at scale %v: %d)", read, count.what, corpus.scale, count.want)
		if read < count.want-count.slack || read > count.want+count.slack {
			t.Errorf("the corpus holds %d %s, where the check at scale %v states %d", read, count.what, corpus.scale, count.want)
		}
	}
	var issues, workflow, graph int64
	if err := conn.QueryRow(ctx, `select (select count(*) from issues), (select count(*) from workflow_tuples), (select count(*) from graph_tuples)`).
		Scan(&issues, &workflow, &graph); err != nil {
		t.Fatalf("count the corpus's tables: %v", err)
	}
	t.Logf("c-moved-read-cost: the release before's database holds %d issues, %d workflow_tuples and %d graph_tuples", issues, workflow, graph)
	if t.Failed() {
		t.FailNow()
	}
}

// ───── the seven reads ────────────────────────────────────────────────────

// movedCostRead is one read R32 moves: the route a reading GETs, and how many of what its answer
// holds, which every side must answer alike before it is measured.
type movedCostRead struct {
	name, path, unit string
	count            func(body []byte) (int, error)
	// want is the count the corpus decides, or 0 where the corpus decides only that there is one.
	want int
}

// movedCostCount decodes body into a value of T and answers the count measure takes of it.
func movedCostCount[T any](measure func(T) int) func(body []byte) (int, error) {
	return func(body []byte) (int, error) {
		var decoded T
		if err := json.Unmarshal(body, &decoded); err != nil {
			return 0, err
		}
		return measure(decoded), nil
	}
}

type movedCostPage struct {
	Total *int `json:"total"`
}

func movedCostTotal(page movedCostPage) int {
	if page.Total == nil {
		return -1
	}
	return *page.Total
}

// movedCostReads are the seven reads of the check. The two issue lists are read as the dashboard
// pages them, at contracts.DefaultIssuePageLimit: their advances terms read the moved relation of
// every issue the page answers, and the unpaged list grows faster than the issues it lists on both
// sides (the R32 bench's README).
func movedCostReads(corpus *movedCostCorpus) []movedCostRead {
	type rows = []json.RawMessage
	return []movedCostRead{
		{
			name: "priorities", path: "/api/v1/priorities", unit: "live priorities", want: corpus.live,
			count: movedCostCount(func(v struct {
				Priorities rows `json:"priorities"`
			}) int {
				return len(v.Priorities)
			}),
		},
		{
			name: "priority history", path: "/api/v1/priorities/" + movedCostPriority + "/history", unit: "acts",
			count: movedCostCount(func(v rows) int { return len(v) }),
		},
		{
			name: "issue that advances a priority", path: "/api/v1/issues/" + corpus.advancing, unit: "advances",
			count: movedCostCount(func(v struct {
				Advances rows `json:"advances"`
			}) int {
				return len(v.Advances)
			}),
		},
		{
			name: "issue list", path: "/api/v1/issues?limit=50", unit: "issues in the listing's total",
			count: movedCostCount(movedCostTotal),
		},
		{
			name: "issue list by advances", path: "/api/v1/issues?advances=" + movedCostPriority + "&limit=50", unit: "issues in the listing's total",
			count: movedCostCount(movedCostTotal),
		},
		{
			name: "steps", path: "/api/v1/issues/" + corpus.busy + "/steps", unit: "runs", want: 200,
			count: movedCostCount(func(v struct {
				Tasks []struct {
					Steps rows `json:"steps"`
				} `json:"tasks"`
			}) int {
				runs := 0
				for _, task := range v.Tasks {
					runs += len(task.Steps)
				}
				return runs
			}),
		},
		{
			name: "plans", path: "/api/v1/projects/" + movedCostProjects[0] + "/plans", unit: "finished issues", want: corpus.finished,
			count: movedCostCount(func(v struct {
				Issues rows `json:"issues"`
			}) int {
				return len(v.Issues)
			}),
		},
	}
}

// holdMovedCostReads reads each read once on s and holds its answer to the corpus, and, where old
// is given, to the count the old side answered, before the side is measured.
func holdMovedCostReads(t *testing.T, s *readCostServer, side string, reads []movedCostRead, old map[string]int) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, read := range reads {
		answer := s.expect(t, read.name+" on the "+side, http.MethodGet, read.path, readCostReader, nil, http.StatusOK)
		count, err := read.count(answer.body)
		if err != nil {
			t.Errorf("decode %s (%s) on the %s: %v: %s", read.name, read.path, side, err, answer)
			continue
		}
		counts[read.name] = count
		t.Logf("c-moved-read-cost: %s (%s) on the %s answers %d %s", read.name, read.path, side, count, read.unit)
		switch {
		case read.want > 0 && count != read.want:
			t.Errorf("%s (%s) on the %s answers %d %s, where the corpus holds %d", read.name, read.path, side, count, read.unit, read.want)
		case count <= 0:
			t.Errorf("%s (%s) on the %s answers no %s", read.name, read.path, side, read.unit)
		case old != nil && count != old[read.name]:
			t.Errorf("%s (%s) on the %s answers %d %s, where the old side answered %d", read.name, read.path, side, count, read.unit, old[read.name])
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	return counts
}

// measureMovedCostSide takes each read on s as c-read-cost takes a read (measureReadCost): one
// warm-up reading that is not counted, then readCostRuns readings over HTTP through the one client,
// with nearest-rank percentiles and the database time of those readings.
func measureMovedCostSide(t *testing.T, statements *readCostStatements, s *readCostServer, side string, reads []movedCostRead) map[string]readCostReading {
	t.Helper()
	readings := map[string]readCostReading{}
	for _, read := range reads {
		readings[read.name] = measureReadCost(t, "c-moved-read-cost", statements, s, read.name+" ("+read.path+")", "the "+side, readCostGets(read.path))
	}
	return readings
}

// ───── the plans through the views ────────────────────────────────────────

// movedCostFourNames is a statement that names one of the four names the release before reads,
// which after the move are the views. A statement a GET sends that names one reads it.
var movedCostFourNames = regexp.MustCompile(`(?i)\b(priority|workflow)_(nodes|tuples)\b`)

// movedCostProxy stands between the release before's server and Postgres while the test drives
// each read once, and keeps each statement that server sends for the read that reads one of the
// four names, with the parameters it binds. It relays every byte unchanged; it reads the
// frontend's Parse, Bind and Query messages, and nothing the backend answers.
type movedCostProxy struct {
	listener net.Listener
	upstream string
	mu       sync.Mutex
	read     string
	kept     []movedCostStatement
	seen     map[string]bool
}

// movedCostStatement is one statement the release before sent for a read, as it sent it.
type movedCostStatement struct {
	read    string
	sql     string
	oids    []uint32
	formats []int16
	values  [][]byte
}

// movedCostParsed is a statement a connection prepared: its text and the parameter types it named.
type movedCostParsed struct {
	sql  string
	oids []uint32
}

// startMovedCostProxy listens on loopback and relays each connection to the server databaseURL
// names, and answers the URL that reaches the same database through it, in the clear.
func startMovedCostProxy(t *testing.T, databaseURL string) (*movedCostProxy, string) {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Hostname() == "" {
		t.Fatalf("the plan proxy relays to the host the run's database URL names, and it names none (%v)", err)
	}
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the plan proxy: %v", err)
	}
	proxy := &movedCostProxy{listener: listener, upstream: net.JoinHostPort(parsed.Hostname(), port), seen: map[string]bool{}}
	go proxy.accept()
	t.Cleanup(func() { _ = listener.Close() })
	through := *parsed
	through.Host = listener.Addr().String()
	query := through.Query()
	query.Set("sslmode", "disable")
	through.RawQuery = query.Encode()
	return proxy, through.String()
}

func (p *movedCostProxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.serve(client)
	}
}

// serve relays one connection: the backend's bytes back as they come, and the frontend's message by
// message, each observed before it is passed on, so a statement is kept before its answer leaves.
func (p *movedCostProxy) serve(client net.Conn) {
	defer client.Close()
	server, err := net.Dial("tcp", p.upstream)
	if err != nil {
		return
	}
	defer server.Close()
	go func() {
		_, _ = io.Copy(client, server)
		_ = client.Close()
	}()
	_ = p.relay(bufio.NewReader(client), client, server)
}

// relay passes the startup phase on, refusing a request for TLS or GSS encryption so the client
// goes on in the clear, and then every typed message.
func (p *movedCostProxy) relay(from *bufio.Reader, client, server net.Conn) error {
	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(from, header); err != nil {
			return err
		}
		length := int(binary.BigEndian.Uint32(header[:4]))
		if length < 8 {
			return fmt.Errorf("a startup message of %d bytes", length)
		}
		body := make([]byte, length-8)
		if _, err := io.ReadFull(from, body); err != nil {
			return err
		}
		if code := binary.BigEndian.Uint32(header[4:]); code == 80877103 || code == 80877104 {
			if _, err := client.Write([]byte{'N'}); err != nil {
				return err
			}
			continue
		}
		if _, err := server.Write(append(header, body...)); err != nil {
			return err
		}
		break
	}
	parsed := map[string]movedCostParsed{}
	for {
		header := make([]byte, 5)
		if _, err := io.ReadFull(from, header); err != nil {
			return err
		}
		length := int(binary.BigEndian.Uint32(header[1:]))
		if length < 4 {
			return fmt.Errorf("a message of %d bytes", length)
		}
		body := make([]byte, length-4)
		if _, err := io.ReadFull(from, body); err != nil {
			return err
		}
		p.observe(parsed, header[0], body)
		if _, err := server.Write(append(header, body...)); err != nil {
			return err
		}
	}
}

// observe reads one frontend message: a Parse names a statement's text and types, a Bind runs a
// statement with its parameters, and a Query runs a text with none.
func (p *movedCostProxy) observe(parsed map[string]movedCostParsed, kind byte, body []byte) {
	message := &movedCostMessage{body: body}
	switch kind {
	case 'P':
		name, sql := message.cstring(), message.cstring()
		oids := make([]uint32, message.count())
		for index := range oids {
			oids[index] = message.word()
		}
		if !message.bad {
			parsed[name] = movedCostParsed{sql: sql, oids: oids}
		}
	case 'B':
		message.cstring()
		statement, known := parsed[message.cstring()]
		formats := make([]int16, message.count())
		for index := range formats {
			formats[index] = int16(message.half())
		}
		values := make([][]byte, message.count())
		for index := range values {
			if length := int32(message.word()); length >= 0 {
				values[index] = message.take(int(length))
			}
		}
		if known && !message.bad {
			p.keep(statement, formats, values)
		}
	case 'Q':
		if sql := message.cstring(); !message.bad {
			p.keep(movedCostParsed{sql: sql}, nil, nil)
		}
	}
}

// keep keeps a statement the read now driven sent, once for each read, where it reads one of the
// four names.
func (p *movedCostProxy) keep(statement movedCostParsed, formats []int16, values [][]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.read == "" || !movedCostFourNames.MatchString(statement.sql) {
		return
	}
	key := p.read + "\x00" + statement.sql
	if p.seen[key] {
		return
	}
	p.seen[key] = true
	p.kept = append(p.kept, movedCostStatement{read: p.read, sql: statement.sql, oids: statement.oids, formats: formats, values: values})
}

// drive names read as the one the statements that follow belong to, until drive is called again
// with "".
func (p *movedCostProxy) drive(read string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.read = read
}

func (p *movedCostProxy) statements(read string) []movedCostStatement {
	p.mu.Lock()
	defer p.mu.Unlock()
	var kept []movedCostStatement
	for _, statement := range p.kept {
		if statement.read == read {
			kept = append(kept, statement)
		}
	}
	return kept
}

// movedCostMessage reads the fields of one protocol message in order; a field past its end marks
// the message bad.
type movedCostMessage struct {
	body []byte
	bad  bool
}

func (m *movedCostMessage) take(n int) []byte {
	if n < 0 || n > len(m.body) {
		m.bad = true
		return nil
	}
	field := m.body[:n:n]
	m.body = m.body[n:]
	return field
}

func (m *movedCostMessage) cstring() string {
	end := bytes.IndexByte(m.body, 0)
	if end < 0 {
		m.bad = true
		return ""
	}
	text := string(m.body[:end])
	m.body = m.body[end+1:]
	return text
}

func (m *movedCostMessage) half() uint16 {
	if field := m.take(2); field != nil {
		return binary.BigEndian.Uint16(field)
	}
	return 0
}

func (m *movedCostMessage) word() uint32 {
	if field := m.take(4); field != nil {
		return binary.BigEndian.Uint32(field)
	}
	return 0
}

// count is an Int16 count of the fields that follow; a negative one marks the message bad.
func (m *movedCostMessage) count() int {
	count := int(int16(m.half()))
	if count < 0 {
		m.bad = true
		return 0
	}
	return count
}

// takeMovedCostPlans drives each read once on a server of the release before booted through the
// proxy, over the moved database, and stops it.
func takeMovedCostPlans(t *testing.T, proxy *movedCostProxy, s *readCostServer, reads []movedCostRead) {
	t.Helper()
	for _, read := range reads {
		proxy.drive(read.name)
		_, err := readCostGets(read.path)(s)
		proxy.drive("")
		if err != nil {
			t.Fatalf("drive %s (%s) through the plan proxy: %v", read.name, read.path, err)
		}
	}
	s.stop(t)
}

// printMovedCostPlans prints, for each read, the EXPLAIN (ANALYZE, BUFFERS) plan of each statement
// the release before sent for it that reads one of the four names, run over conn with the
// parameters that server bound, in a transaction rolled back after.
func printMovedCostPlans(t *testing.T, conn *pgx.Conn, proxy *movedCostProxy, reads []movedCostRead) {
	t.Helper()
	ctx := context.Background()
	for _, read := range reads {
		statements := proxy.statements(read.name)
		if len(statements) == 0 {
			t.Errorf("c-moved-read-cost: %s (%s) sent the release before's server no statement that reads priority_nodes, "+
				"priority_tuples, workflow_nodes or workflow_tuples, so it has no plan through the views", read.name, read.path)
			continue
		}
		for index, statement := range statements {
			plan, err := explainMovedCost(ctx, conn, statement)
			if err != nil {
				t.Errorf("c-moved-read-cost: explain statement %d of %s (%s) through the views: %v\n%s", index+1, read.name, read.path, err, statement.sql)
				continue
			}
			t.Logf("c-moved-read-cost: plan through the views of %s (%s), statement %d of %d, bound %s:\n%s\n%s",
				read.name, read.path, index+1, len(statements), movedCostParameters(statement), statement.sql, plan)
		}
	}
}

func explainMovedCost(ctx context.Context, conn *pgx.Conn, statement movedCostStatement) (string, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	oids := make([]uint32, len(statement.values))
	copy(oids, statement.oids)
	result := tx.Conn().PgConn().ExecParams(ctx, "explain (analyze, buffers) "+statement.sql, statement.values, oids, statement.formats, nil).Read()
	if result.Err != nil {
		return "", result.Err
	}
	lines := make([]string, len(result.Rows))
	for index, row := range result.Rows {
		lines[index] = string(row[0])
	}
	return strings.Join(lines, "\n"), nil
}

// movedCostParameters is each parameter a statement was bound, text quoted and binary in hex.
func movedCostParameters(statement movedCostStatement) string {
	if len(statement.values) == 0 {
		return "no parameter"
	}
	parts := make([]string, len(statement.values))
	for index, value := range statement.values {
		format := int16(0)
		switch len(statement.formats) {
		case 1:
			format = statement.formats[0]
		case len(statement.values):
			format = statement.formats[index]
		}
		switch {
		case value == nil:
			parts[index] = fmt.Sprintf("$%d=null", index+1)
		case format == 0:
			parts[index] = fmt.Sprintf("$%d=%s", index+1, strconv.Quote(string(value)))
		default:
			parts[index] = fmt.Sprintf(`$%d=\x%s`, index+1, hex.EncodeToString(value))
		}
	}
	return strings.Join(parts, " ")
}

// ───── the run ────────────────────────────────────────────────────────────

// No read that R32 moves gets slower.
//
// Seven reads are measured, each over HTTP through one client against a server process over one
// database: the priorities, a priority's history, an issue that advances a priority, the issue
// list, the issue list by one priority it advances, an issue's steps and a project's plans. The
// run has three sides over one database. First, the release before, built from STORES_BASE_SHA,
// migrates it and makes the four projects; the run stops it, writes the corpus by SQL, boots it
// again and measures the old side over the four tables. Second, this head boots, which moves both
// stores into the graph store, and the run measures the new side. Third, as a rollback does, the
// release before boots again, now reading through the four views: the run first prints the plan of
// each of its statements through the views, taken through a proxy on a boot of its own, then
// measures the third side. Each side answers each read alike before it is measured.
//
// Each read on the new side and on the third side is held to twice its old side's p50. Every p90,
// p95 and p99 is printed and bound at nothing, and so is each read's database time per reading on
// each side, and that of an idle interval after it (readCostStatements), where the server's
// pg_stat_statements can be read; where it cannot, the run says so in one line and fails on none
// of it.
//
// It fails, never skips, where STORES_BASE_SHA is unset, names no 40-character commit, or names a
// commit that lacks one of the three migrations, before it builds anything.
func TestNoMovedReadGetsSlower(t *testing.T) {
	began := time.Now()
	t.Cleanup(func() {
		t.Logf("c-moved-read-cost: TestNoMovedReadGetsSlower took %s of wall time", time.Since(began).Round(time.Second))
	})
	baseCommit := movedCostBaseCommit(t)
	corpus := newMovedCostCorpus(t, movedCostScale(t))
	headCommit := gitOutput(t, ".", "rev-parse", "HEAD")
	if !readCostFullCommit.MatchString(headCommit) || headCommit == baseCommit {
		t.Fatalf("this head's commit %q must be a whole commit other than %s=%s, or the two builds cannot be told apart",
			headCommit, movedCostBaseVariable, baseCommit)
	}
	databaseURL := readCostDatabase(t)
	baseBinary := testbaseline.Build(t, baseCommit, movedCostBaseVariable)
	headBinary := cmdtest.BuildCommand(t, cmdtest.Command{
		Name:   "envoy-dispatch-head",
		Dir:    envoyModuleRoot(t),
		Target: "./cmd/dispatch",
		Flags:  []string{"-ldflags", "-X main.buildCommit=" + headCommit},
	})
	t.Logf("c-moved-read-cost: the old side and the third side run on the build of %s, the commit %s names; the new side "+
		"on the build of this checkout's working copy, stamped with its HEAD %s", baseCommit, movedCostBaseVariable, headCommit)
	client := readCostClient()
	listener := readCostListener(t)
	conn := readCostConnect(t, databaseURL)
	statements := newReadCostStatements(t, "c-moved-read-cost", databaseURL)
	reads := movedCostReads(corpus)
	baseName := func(what string) string { return "the release before (" + baseCommit + ")" + what }

	// ───── first: the corpus and the old side, on the release before ─────
	first := startReadCostServer(t, client, baseName(", migrating"), baseBinary, baseCommit, databaseURL, listener)
	seedMovedCostTemplates(t, first)
	first.stop(t)
	seedMovedCostCorpus(t, conn, corpus)
	base := startReadCostServer(t, client, baseName(""), baseBinary, baseCommit, databaseURL, listener)
	readCostVacuum(t, conn, "old side")
	counts := holdMovedCostReads(t, base, "old side", reads, nil)
	old := measureMovedCostSide(t, statements, base, "old side", reads)
	base.stop(t)

	// ───── second: this head, which moves both stores at boot ─────
	head := startReadCostServer(t, client, "this head ("+headCommit+")", headBinary, headCommit, databaseURL, listener)
	for _, line := range strings.Split(head.process.Output.String(), "\n") {
		if strings.Contains(line, `msg="dispatch: the boot's migrations begin"`) ||
			strings.Contains(line, `msg="dispatch: the move of the priority store and the workflow store into the graph store ended"`) {
			t.Logf("c-moved-read-cost: this head's boot: %s", line)
		}
	}
	var relkind string
	if err := conn.QueryRow(context.Background(), `select relkind::text from pg_class where oid = 'priority_nodes'::regclass`).Scan(&relkind); err != nil {
		t.Fatalf("read what priority_nodes is after this head's boot: %v", err)
	}
	if relkind != "v" {
		t.Fatalf("after this head's boot priority_nodes is of relkind %q, where the move leaves a view", relkind)
	}
	if !testwait.Holds(readCostOwedWait, readCostOwedPoll, func() bool {
		return strings.Contains(head.process.Output.String(), readCostBackfillDone)
	}) {
		t.Fatalf("this head did not log %s within %s:\n%s", readCostBackfillDone, readCostOwedWait, readCostTail(head.process.Output.String()))
	}
	readCostVacuum(t, conn, "new side")
	holdMovedCostReads(t, head, "new side", reads, counts)
	now := measureMovedCostSide(t, statements, head, "new side", reads)
	head.stop(t)

	// ───── third: the release before again, through the views; the plans first ─────
	readCostVacuum(t, conn, "third side")
	proxy, throughProxy := startMovedCostProxy(t, databaseURL)
	planned := startReadCostServer(t, client, baseName(" through the plan proxy"), baseBinary, baseCommit, throughProxy, listener)
	takeMovedCostPlans(t, proxy, planned, reads)
	printMovedCostPlans(t, conn, proxy, reads)
	rollback := startReadCostServer(t, client, baseName(" after the rollback"), baseBinary, baseCommit, databaseURL, listener)
	holdMovedCostReads(t, rollback, "third side", reads, counts)
	third := measureMovedCostSide(t, statements, rollback, "third side", reads)
	rollback.stop(t)

	t.Logf("c-moved-read-cost: old side and third side %s, new side %s; %d readings of each read on each side; corpus scale %v",
		baseCommit, headCommit, readCostRuns, corpus.scale)
	t.Log("c-moved-read-cost: read\troute\t" + strings.Join(readCostHeads("old", "new", "third"), "\t") +
		"\tnew/old db\tthird/old db\tnew/old p50\tthird/old p50\tbound")
	for _, read := range reads {
		before, after, rolled := old[read.name], now[read.name], third[read.name]
		cells := append(readCostTableCells(before.cells(), after.cells(), rolled.cells()),
			readCostDatabaseRatio(before.database, after.database), readCostDatabaseRatio(before.database, rolled.database),
			fmt.Sprintf("%.2f", float64(after.p50)/float64(before.p50)), fmt.Sprintf("%.2f", float64(rolled.p50)/float64(before.p50)),
			fmt.Sprintf("%.2fx on the old side's p50", movedCostBound))
		t.Logf("c-moved-read-cost: %s\t%s\t%s", read.name, read.path, strings.Join(cells, "\t"))
	}
	sides := []struct {
		name     string
		readings map[string]readCostReading
	}{{"old side", old}, {"new side", now}, {"third side", third}}
	for _, read := range reads {
		for _, side := range sides {
			t.Logf("c-moved-read-cost: %s, %s, every reading in order, in ms: %v", read.name, side.name, readCostMilliseconds(side.readings[read.name].runs))
		}
	}

	// The bounds, one subtest for each read on each side, so a read over its bound names itself and
	// its side and the rest still report theirs.
	for _, read := range reads {
		before := old[read.name]
		for _, side := range sides[1:] {
			t.Run(read.name+" on the "+side.name, func(t *testing.T) {
				reading := side.readings[read.name]
				ratio := float64(reading.p50) / float64(before.p50)
				if ratio > movedCostBound {
					t.Errorf("%s (%s) on the %s: p50 %.1f ms is %.2fx the old side's p50 of %.1f ms, over %.2fx",
						read.name, read.path, side.name, msOf(reading.p50), ratio, msOf(before.p50), movedCostBound)
					return
				}
				t.Logf("%s (%s) on the %s: p50 %.1f ms is %.2fx the old side's p50 of %.1f ms, within %.2fx",
					read.name, read.path, side.name, msOf(reading.p50), ratio, msOf(before.p50), movedCostBound)
			})
		}
	}
}

// movedCostBaseCommit reads STORES_BASE_SHA and fails, never skips, where it is unset, names no
// 40-character commit this checkout holds, or names a commit that lacks one of the three
// migrations, naming each it lacks, before anything is built or read.
func movedCostBaseCommit(t *testing.T) string {
	t.Helper()
	named := strings.TrimSpace(os.Getenv(movedCostBaseVariable))
	if named == "" {
		t.Fatalf("%s is unset: c-moved-read-cost reads the old side and the third side on the server of the commit it names, "+
			"the release before R32's move; set it to the commit `git merge-base origin/main HEAD` answers", movedCostBaseVariable)
	}
	if !readCostFullCommit.MatchString(named) {
		t.Fatalf("%s=%q names no 40-character commit: c-moved-read-cost reads the commit the run names, whole", movedCostBaseVariable, named)
	}
	out, err := exec.Command("git", "rev-parse", "--verify", "--quiet", named+"^{commit}").Output()
	if commit := strings.TrimSpace(string(out)); err != nil || commit != named {
		t.Fatalf("%s=%s names no commit this checkout holds (git rev-parse --verify: %v); a shallow clone lacks it", movedCostBaseVariable, named, err)
	}
	for _, migration := range movedCostMigrations {
		if err := exec.Command("git", "cat-file", "-e", named+":"+movedCostMigrationsDir+"/"+migration).Run(); err != nil {
			t.Errorf("%s=%s lacks the migration %s, so it is no release before R32's move", movedCostBaseVariable, named, migration)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	return named
}
