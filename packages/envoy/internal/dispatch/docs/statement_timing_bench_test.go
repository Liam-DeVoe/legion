package docs

// legion: tool - BenchmarkStatementTimings times each of this package's six anchor reads against main's text (EXPLAIN ANALYZE on one connection, the median of runs 2 to 12, both plans printed) on a cmd/dispatch-seed database, for the author of a change to those statements, who compares the new statements with main's on a seed of --scale 0.01 or more, on a machine nothing else loads; run with DISPATCH_BENCH_DATABASE_URL=<seeded database> go test -count=1 -run '^$' -bench '^BenchmarkStatementTimings$' -benchtime 1x -timeout 30m ./internal/dispatch/docs

import "testing"

// BenchmarkStatementTimings measures every pair TestStatementTimings describes, on the seeded
// measurement database (benchtime), which DISPATCH_BENCH_DATABASE_URL names, and skips without it:
// a tool, whose command is the file's tool line. TestStatementTimings checks, untimed, the
// subjects these pairs read and that each pair's two texts answer the same rows.
//
// None of the pairs is a test, because a runner's load decides their verdicts. On the --scale 0.01
// seed envoy-go's statement-timing step writes, the after side of docs.openAnchoredMarks asks read
// 0.78 of the before side's median in envoy-go (2/3) of run 45359337842, and 0.88 to 0.98 of it in
// five runs on a dev box on 2026-11-24 while other builds ran, 2% from turning over: its after
// text saves about 0.03 ms of execution, of the 0.15 to 0.19 ms each side's median reads with its
// planning. The other five pairs read 0.33 to 0.83 there, checkTableAnchorRows asks the highest.
// And the two pairs of internal/dispatch/refs that read 0.85 and 0.86 in that CI run read 1.22
// and 1.35 on the same dev box (refs' BenchmarkStatementTimings), so no margin these pairs show
// holds against a runner's load.
func BenchmarkStatementTimings(b *testing.B) {
	statementTimings(b, true)
}
