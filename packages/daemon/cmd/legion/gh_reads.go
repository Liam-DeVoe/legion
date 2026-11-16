package main

import (
	"slices"

	"github.com/sjawhar/legion/daemon/internal/ghroute"
)

// ghCheckedOnlyVisibility is the call log's visibility for a call the shared check alone read
// (leakguard.GhAnswer.CheckedOnly): a call that writes nothing, under the check the rule was
// proven against, for which nothing read its repository's visibility. It is neither `github` nor
// `kept`, and not the empty value of a call the guard read no visibility for because it named no
// repository.
const ghCheckedOnlyVisibility = "checked-only"

// ghProvenCheckSHA256 is the sha256 runGh names to the guard as the shared check its read rule
// (ghroute.Target.WritesNothing) was proven against (leakguard.GhRequest.ProvenCheck): the
// pinned check's, ghroute.ReadsProvenCheckSHA256, which this package's test binary, run as
// `legion`, replaces with its stand-in check's (TestMain, testProvenCheckEnv).
var ghProvenCheckSHA256 = ghroute.ReadsProvenCheckSHA256

// ghRateLimitRead is the exact `gh api rate_limit`, a GitHub API call with no repository content,
// which reaches gh before its command is read: it reads no help and no git remote, so a working
// directory whose remotes name two owners does not refuse it. Any option changes the request's
// method or shape and is read as every other call is.
func ghRateLimitRead(argv []string) bool {
	return slices.Equal(argv, []string{"api", "rate_limit"})
}

// ghUnguardedUnset are the variables a call no private-name pass reads runs gh without: the
// browser programs gh would hand a page to (go-gh's browser.New reads GH_BROWSER, then gh's
// config `browser`, then BROWSER).
var ghUnguardedUnset = map[string]bool{"GH_BROWSER": true, "BROWSER": true}
