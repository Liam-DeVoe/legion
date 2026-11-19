package session

import (
	"time"

	"github.com/sjawhar/envoy/internal/kvwatch"
)

// SetupNATS is setupNATS for the package's external tests, so they connect to the package's shared
// test NATS in an account of their own.
var SetupNATS = setupNATS

// withKnownSessionRetry shortens the waits between the background repair's attempts, so a test
// meets the attempt that repairs the store without waiting out the production schedule
// (knownBucketRetryFirst, knownBucketRetryMax). It is test-only: no deployment changes them.
func withKnownSessionRetry(first, longest time.Duration) KnownSessionsOption {
	return func(o *knownSessionsOpts) {
		o.retryFirst, o.retryMax = first, longest
	}
}

// withKnownSessionAttemptWindow bounds each attempt of the background repair at d in place of
// repairAttemptWindow (retryRepair). It is test-only.
func withKnownSessionAttemptWindow(d time.Duration) KnownSessionsOption {
	return func(o *knownSessionsOpts) { o.attemptWindow = d }
}

// withKnownSessionWatch adds options to the ones the store's watcher is built with: a clock that
// moves past the stale bound (kvwatch.WithNow), say. It is test-only: no deployment sets them.
func withKnownSessionWatch(options ...kvwatch.Option) KnownSessionsOption {
	return func(o *knownSessionsOpts) {
		o.watch = append(o.watch, options...)
	}
}
