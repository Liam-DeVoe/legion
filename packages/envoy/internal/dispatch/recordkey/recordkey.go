// Package recordkey builds and reads the keys Dispatch stores for a destination of an event:
// what events.published_destinations, events.owed_keys and comment_fallback_copies.destination
// hold, what the outbox's chains are kept on, and what every reader of those columns matches
// (model.MentionCopyHolderSQL, internal/dispatch/sessionanswers, internal/dispatch/graphcensus).
//
// A record key is not a bus subject. It keeps its bytes for ever: rows every earlier build wrote
// hold them, no migration rewrites them, and a release reads another's records while the two roll
// side by side, so a reached destination stays reached whichever wrote it. Its prefixes are this
// package's own constants, never contracts' bus prefixes, so no change of a bus subject moves a
// stored key. The outbox sends each copy on the subject that names the event's project, which it
// makes from the key it reads back here (Parse) and the event's project; no record holds that
// subject.
package recordkey

import (
	"fmt"
	"strings"
)

const (
	// IssuePrefix begins an issue's topic family's key: notifications.dispatch.issue.<KEY>.<type>.
	IssuePrefix = "notifications.dispatch.issue."
	// SessionPrefix begins one session's own destination's key: notifications.agent.<session>.
	SessionPrefix = "notifications.agent."
	// RolePrefix begins a role route's key, notifications.role.<role>, which model's
	// MentionCopyHolderSQL reads a role key by.
	RolePrefix = "notifications.role."
)

// Issue is the key of an issue's topic family for an event of type typ.
func Issue(key, typ string) string {
	return IssuePrefix + key + "." + typ
}

// Session is the key of session's own destination.
func Session(session string) string {
	return SessionPrefix + session
}

// Role is the key of a route to role.
func Role(role string) string {
	return RolePrefix + role
}

// Kind is what a session's or a role's key names (Parse).
type Kind int

const (
	// SessionKind is a session's own destination: Session.
	SessionKind Kind = iota + 1
	// RoleKind is a role route: Role.
	RoleKind
)

// Destination is a stored key read back: its kind and the session or role it names.
type Destination struct {
	Kind Kind
	Name string
}

// Parse reads a stored session or role key back to its kind and name, and answers an error for
// any other key: one of no known prefix, one that names nothing, and one whose name holds a dot,
// which no session or role name does (roles.PatternString) and every bus subject that names a
// project does (notifications.agent.<PROJECT>.<session>), so a bus subject stored by mistake is
// refused rather than read as a session called <PROJECT>.<session>.
func Parse(key string) (Destination, error) {
	for _, form := range []struct {
		prefix string
		kind   Kind
	}{{SessionPrefix, SessionKind}, {RolePrefix, RoleKind}} {
		if name, ok := strings.CutPrefix(key, form.prefix); ok {
			if name == "" || strings.Contains(name, ".") {
				return Destination{}, fmt.Errorf("recordkey: %q names no session or role", key)
			}
			return Destination{Kind: form.kind, Name: name}, nil
		}
	}
	return Destination{}, fmt.Errorf("recordkey: %q is no stored session or role key", key)
}

// ParseIssue reads an issue's topic family's key back to the issue key and the event type, and
// answers false for any other key. The issue key is the token after the prefix, which holds a
// hyphen and no dot (the issues table's key, <PROJECT>-<n>); the type is the rest, which may hold
// dots (message.created). A bus subject that names a project
// (notifications.dispatch.issue.<PROJECT>.<KEY>.<type>) has a project key, which holds no hyphen,
// in that token, so it is refused rather than read as an issue called <PROJECT>.
func ParseIssue(key string) (issue, typ string, ok bool) {
	rest, found := strings.CutPrefix(key, IssuePrefix)
	if !found {
		return "", "", false
	}
	issue, typ, found = strings.Cut(rest, ".")
	if !found || typ == "" || !strings.Contains(issue, "-") {
		return "", "", false
	}
	return issue, typ, true
}
