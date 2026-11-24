//go:build farm

package legionbench

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// The R8 Job's measurement of the board's read of the stored facts at root R14's size (spec
// Design, "How R8 is measured"): the median execution time of the statement the board's request
// ran (the drive takes it from Postgres's statement log, with its arguments, the board's keys and
// roots) under its generic plan, over boardReadRuns EXPLAIN ANALYZE runs, as api's
// TestTheBoardsFactsReadUnderAGenericPlanStaysWithinItsBudget measures it at the test rig's size;
// and that plan, of which no arm may scan rows of issues the read does not answer. It runs in the
// R8 Job only, built with the farm tag, against the Job's database:
//
//	LEGIONBENCH_DATABASE_URL=<url> LEGIONBENCH_FACTS_READ=<drive's facts-read.json> \
//	  go test -tags farm -run '^TestFarmBoardFactsRead$' -v ./internal/dispatch/legion/legionbench

// boardReadRuns is how many times the read runs under its generic plan, of which the median is
// taken: the board's read-path test's boardReadRuns.
const boardReadRuns = 11

// planNode is one node of an `EXPLAIN (ANALYZE, FORMAT JSON)` plan, in the members the plan check
// reads, with the nodes under it.
type planNode struct {
	Type          string     `json:"Node Type"`
	Relation      string     `json:"Relation Name"`
	Index         string     `json:"Index Name"`
	IndexCond     string     `json:"Index Cond"`
	Filter        string     `json:"Filter"`
	Rows          float64    `json:"Actual Rows"`
	Loops         float64    `json:"Actual Loops"`
	RemovedFilter float64    `json:"Rows Removed by Filter"`
	RemovedCheck  float64    `json:"Rows Removed by Index Recheck"`
	Total         float64    `json:"Actual Total Time"`
	Plans         []planNode `json:"Plans"`
}

// walk is n and every node under it, depth first.
func (n planNode) walk() []planNode {
	nodes := []planNode{n}
	for _, child := range n.Plans {
		nodes = append(nodes, child.walk()...)
	}
	return nodes
}

// outline is the plan one node a line, each under its parent: its type, the relation and the
// index it reads, its conditions, and what it ran.
func (n planNode) outline(depth int, lines *[]string) {
	line := strings.Repeat("  ", depth) + n.Type
	if n.Relation != "" {
		line += " on " + n.Relation
	}
	if n.Index != "" {
		line += " using " + n.Index
	}
	if n.IndexCond != "" {
		line += " cond=" + n.IndexCond
	}
	if n.Filter != "" {
		line += " filter=" + n.Filter
	}
	*lines = append(*lines, line+fmt.Sprintf(" (rows=%g loops=%g removed_by_filter=%g time=%.3f ms)", n.Rows, n.Loops, n.RemovedFilter, n.Total))
	for _, child := range n.Plans {
		child.outline(depth+1, lines)
	}
}

func TestFarmBoardFactsRead(t *testing.T) {
	url, path := os.Getenv("LEGIONBENCH_DATABASE_URL"), os.Getenv("LEGIONBENCH_FACTS_READ")
	if url == "" || path == "" {
		t.Fatal("set LEGIONBENCH_DATABASE_URL to the R8 Job's database and LEGIONBENCH_FACTS_READ to the drive's facts-read.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the board's facts read: %v", err)
	}
	var read struct {
		SQL    string   `json:"sql"`
		Params []string `json:"params"`
	}
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(read.Params) != 3 || read.SQL == "" {
		t.Fatalf("%s holds %d parameters, want the board read's project, keys and roots", path, len(read.Params))
	}
	ctx := t.Context()
	database, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	defer database.Pool.Close()
	if _, err := database.Pool.Exec(ctx, `analyze issue_facts`); err != nil {
		t.Fatalf("analyze issue_facts: %v", err)
	}
	project, keys, roots := read.Params[0], read.Params[1]+"::text[]", read.Params[2]+"::text[]"
	literals := []string{project, keys, roots}
	var rows, open, keyCount, rootCount, overlap int64
	if err := database.Pool.QueryRow(ctx, fmt.Sprintf(`
		select (select count(*) from issue_facts where project_key = %[1]s),
		       (select count(*) from issue_facts where project_key = %[1]s and closed_at is null),
		       cardinality(%[2]s), cardinality(%[3]s),
		       (select count(*) from issue_facts where project_key = %[1]s and issue_key = any(%[2]s)
		          and closed_at is not null and root_key = any(%[3]s))`, project, keys, roots)).
		Scan(&rows, &open, &keyCount, &rootCount, &overlap); err != nil {
		t.Fatalf("count the stored rows the read names: %v", err)
	}
	connection, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection for the generic plans: %v", err)
	}
	defer connection.Release()

	taken := make([]float64, 0, boardReadRuns)
	var root planNode
	for run := range boardReadRuns {
		plan := explaintest.AnalyzedGenericPlan(t, ctx, connection, fmt.Sprintf("board_facts_read_%d", run), read.SQL,
			[]string{"text", "text[]", "text[]"}, literals, "set local enable_seqscan = off")
		root = planNode{}
		if err := json.Unmarshal(plan, &root); err != nil {
			t.Fatalf("decode the plan: %v", err)
		}
		taken = append(taken, root.Total)
	}
	slices.Sort(taken)
	median := taken[len(taken)/2]
	fmt.Printf("RESULT r8 facts_read runs=%d median_ms=%.3f fastest_ms=%.3f slowest_ms=%.3f stored_rows=%d open_rows=%d keys=%d roots=%d\n",
		boardReadRuns, median, taken[0], taken[len(taken)-1], rows, open, keyCount, rootCount)
	fmt.Println("RESULT r8 facts_read plan (generic, the last run):")
	var outline []string
	root.outline(0, &outline)
	for _, line := range outline {
		fmt.Println("    " + line)
	}

	// The plan check: every node of issue_facts reads its rows through an index, and together they
	// read no row the read does not answer. The rows a node reads are those it answered and those
	// its filter or its recheck dropped, over every loop. The read answers the rows of its three
	// arms, which share no row; the one row a node may read and drop that the read still answers is
	// a closed card whose tree's root the read also names, which the arm of the keys reads by key
	// and drops (`not (root_key = any(...))`) because the arm of the roots answers it.
	answered := root.Rows * root.Loops
	var scanned float64
	arms := 0
	failed := false
	for _, node := range root.walk() {
		if node.Relation != "issue_facts" {
			continue
		}
		arms++
		read := (node.Rows + node.RemovedFilter + node.RemovedCheck) * node.Loops
		scanned += read
		fmt.Printf("RESULT r8 facts_read arm node=%q index=%q cond=%q filter=%q rows=%g loops=%g removed_by_filter=%g removed_by_recheck=%g read=%g\n",
			node.Type, node.Index, node.IndexCond, node.Filter, node.Rows, node.Loops, node.RemovedFilter, node.RemovedCheck, read)
		if strings.Contains(node.Type, "Seq Scan") {
			fmt.Printf("FAIL r8 facts_read an arm scans issue_facts whole (%s)\n", node.Type)
			failed = true
		}
	}
	fmt.Printf("RESULT r8 facts_read arms=%d rows_read=%g rows_answered=%g closed_cards_the_trees_answer=%d\n", arms, scanned, answered, overlap)
	if arms == 0 {
		fmt.Println("FAIL r8 facts_read the plan reads issue_facts in no node, so no arm was judged")
		failed = true
	}
	if scanned > answered+float64(overlap) {
		fmt.Printf("FAIL r8 facts_read the arms read %g rows, %g more than the %g the read answers and the %d closed cards it answers through their trees: an arm scans rows of issues the read does not answer\n",
			scanned, scanned-answered-float64(overlap), answered, overlap)
		failed = true
	}
	if failed {
		t.Fail()
	}
}
