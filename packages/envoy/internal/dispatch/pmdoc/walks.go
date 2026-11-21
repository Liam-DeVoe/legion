package pmdoc

import "sync/atomic"

// walked counts the children a write's positional fragment Inserts and Deletes walk past, for the
// tests that hold a write to the walks it makes. ygo's fragment Insert and Delete walk the
// fragment from its first child to the index they are given (leftChildAt, deleteChildRange, in the
// pinned sjawhar/ygo), so the index each call names is the walk it costs; a write whose children
// stand together walks to each run once, and one that walked to every child it wrote walked n²/2.
// The count is the work itself, so a busy machine cannot move it. It is nil outside those tests:
// every positional call pays one load and one comparison with nil.
var walked atomic.Pointer[atomic.Int64]

// countWalk adds index, the children a positional fragment Insert or Delete at index walks past,
// to the count a test asked for (CountWalksForTest), and does nothing when none did.
func countWalk(index int) {
	if counter := walked.Load(); counter != nil {
		counter.Add(int64(index))
	}
}

// CountWalksForTest makes every write add, to counter, the children its positional fragment
// Inserts and Deletes walk past (walked), until the function it returns is called. It is exported
// for the tests of the packages that write documents through this one, as bus.WithKeepInterval is
// for the programs that connect through bus: no deployment calls it, and no caller outside a test
// may. It counts every write in the process, so a test that reads the count runs no other write
// beside the one it measures.
func CountWalksForTest(counter *atomic.Int64) (stop func()) {
	walked.Store(counter)
	return func() { walked.Store(nil) }
}
