package refs

// legion: tool - BenchmarkStatementTimings times every pair of this package's changed statements (EXPLAIN ANALYZE on one connection, the median of runs 2 to 12, both plans printed) on a cmd/dispatch-seed database, the references read's, the four loaders', the two index pairs and the probe fixture's among them, for the author of a change to those statements, who compares the new statements with main's on a seed of --scale 1, the scale those pairs were sized at; run with DISPATCH_BENCH_DATABASE_URL=<seeded database> go test -count=1 -run '^$' -bench '^BenchmarkStatementTimings$' -benchtime 1x -timeout 60m ./internal/dispatch/refs

import "testing"

// BenchmarkStatementTimings measures every pair TestStatementTimings describes, on the seeded
// measurement database (benchtime), which DISPATCH_BENCH_DATABASE_URL names, and skips without it:
// a tool, whose command is the file's tool line. TestStatementTimings measures the backlink
// counts' and refs.Edges' in reads' pairs alone, which hold on the --scale 0.01 seed envoy-go's
// statement-timing step writes. The pairs after those - the references read's, the four loaders',
// the two index pairs on subjects.Issue and the probe fixture's - were sized on a seed of
// --scale 1 with jit off, and do not hold at 0.01, the only seed a CI job's time holds. There
// (envoy-go (2/3) of run 45359337842, and a dev box on 2026-11-24): main's eight-hop statement
// read 1.1 to 3.3 ms against 7.7 to 40 ms for the whole new read and 2.1 to 4.7 ms for its out
// statement; each loader's list of 256 ids came mostly from the loader's table, which
// TestStatementTimings' item on the loaders calls no pair, and three of the four loaders read
// past their bound there; and under main's index of targets the in read of subjects.Issue read
// block_refs_node_address rather than block_refs_to, so requireTargetIndexRead stopped the run
// before the index pairs.
func BenchmarkStatementTimings(b *testing.B) {
	statementTimings(b, true)
}
