package refs

// legion: tool - BenchmarkStatementTimings times every pair of this package's changed statements (EXPLAIN ANALYZE on one connection, the median of runs 2 to 12, both plans printed) on a cmd/dispatch-seed database, for the author of a change to those statements, who compares the new statements with main's on a seed of --scale 1, the scale the pairs were sized at, on a machine nothing else loads; run with DISPATCH_BENCH_DATABASE_URL=<seeded database> go test -count=1 -run '^$' -bench '^BenchmarkStatementTimings$' -benchtime 1x -timeout 60m ./internal/dispatch/refs

import "testing"

// BenchmarkStatementTimings measures every pair TestStatementTimings describes, on the seeded
// measurement database (benchtime), which DISPATCH_BENCH_DATABASE_URL names, and skips without it:
// a tool, whose command is the file's tool line. TestStatementTimings checks, untimed, the
// statements and subjects these pairs read.
//
// None of the pairs is a test, because a runner's load decides their verdicts. On the --scale 0.01
// seed envoy-go's statement-timing step writes (envoy-go (2/3) of run 45359337842, and a dev box
// on 2026-11-24): main's eight-hop statement read 1.1 to 3.3 ms against 7.7 to 40.5 ms for the
// whole new read and 2.1 to 4.7 ms for its out statement; each loader's list of 256 ids came
// mostly from the loader's table, which TestStatementTimings' item on the loaders calls no pair,
// and three of the four loaders read past their bound; under main's index of targets the in read
// of subjects.Issue read block_refs_node_address rather than block_refs_to, so
// requireTargetIndexRead stopped the run before the index pairs. And the backlink and edges pairs,
// which held in that CI run (after over before 0.59 to 0.86), turned over on the dev box while
// other builds ran: in three runs of the same database the after side read 1.35 times the before
// side's median for refs.BacklinkCounts over an issue, 1.22 for refs.Edges' in read of the
// anchored document and 1.10 for that of the ask, and only one of the three runs passed every
// pair.
func BenchmarkStatementTimings(b *testing.B) {
	statementTimings(b, true)
}
