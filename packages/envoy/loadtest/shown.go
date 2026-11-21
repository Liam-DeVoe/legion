package main

import (
	"regexp"
	"strings"

	"github.com/sjawhar/envoy/internal/contracts"
)

// A member of Dispatch's answer that the load test decodes as a plain string and keeps or decides
// on as it came - an issue key, an object's id - is Dispatch's words, or the words of whatever
// answers in Dispatch's place, which can echo the request back, its bearer and all. A log line, an
// error or a file the load test writes names such a member only in the one shape Dispatch gives it,
// and a fixed phrase in place of anything else, as the daemon names GitHub's members
// (githubrest.ShownSHA): nothing else the answer wrote there prints, and every decision is still
// made on the member as it came. A shape and not the rule's reading, since a member the load test
// keeps outlives the record of the request it answered: seed.json and the id maps hold it for
// later runs, and replay-fetch reads most answers back from its cache.

// issueKeyPattern is an issue key as Dispatch names one: a project key (contracts.ProjectKeyPattern),
// a hyphen and a positive decimal with no leading zero.
var issueKeyPattern = regexp.MustCompile(strings.TrimSuffix(contracts.ProjectKeyPattern, "$") + `-[1-9][0-9]*$`)

// isIssueKey says whether s is an issue key as Dispatch names one (issueKeyPattern).
func isIssueKey(s string) bool { return issueKeyPattern.MatchString(s) }

// notAnIssueKey is what shownKey prints in place of a key that is not an issue key.
const notAnIssueKey = "(a key that is not an issue key)"

// shownKey is key, an issue key a Dispatch answer names, as a message may print it: the key where
// it is an issue key (isIssueKey), and notAnIssueKey otherwise.
func shownKey(key string) string {
	if isIssueKey(key) {
		return key
	}
	return notAnIssueKey
}

// dispatchIDPattern is an object id as Dispatch names one: a UUID in the canonical lowercase form
// Postgres writes the uuid that keys every artifact, comment and ask
// (internal/dispatch/store/migrations/0001_init.up.sql).
var dispatchIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// isDispatchID says whether s is an object id as Dispatch names one (dispatchIDPattern).
func isDispatchID(s string) bool { return dispatchIDPattern.MatchString(s) }
