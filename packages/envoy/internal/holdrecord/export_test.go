package holdrecord

// WithoutEnrollStopLeavesNothing runs f with the package's table built from the current rules
// less EnrollStopLeavesNothing, so a test of the client on the broker's real routes can show that
// the flag, and nothing else, admits hold release after a stopped enroll (journal_test.go,
// TestAnEnrollStopLeavesTheHoldReleasable). It swaps the table that Decide, Final and Stoppable
// read, so only a test that runs alone may call it: no test of this package runs in parallel
// with another at the top level.
func WithoutEnrollStopLeavesNothing(f func()) {
	r := currentRules
	r.Name, r.EnrollStopLeavesNothing = r.Name+"-EnrollStopLeavesNothing", false
	saved := current
	current = tableFor(r)
	defer func() { current = saved }()
	f()
}
