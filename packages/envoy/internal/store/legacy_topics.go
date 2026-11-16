package store

import "github.com/sjawhar/envoy/internal/contracts"

// The registry's side of the rename that put a project into an issue's, an agent's and a role's
// names (contracts.CanonicalTopic, contracts.CanonicalRole): while every reader takes both forms,
// a name a caller gives in the old form is taken, counted (LegacySubjectCounter) and named back to
// the caller with its new form, and nothing is refused.
//
// A record stores each topic as its caller wrote it. Rewriting a topic into its new form would put
// in the record a topic the plugins that wrote it do not recognise: claude-envoy unsubscribes
// every topic of its record it does not follow (syncRegisteredInterests in
// packages/claude-envoy/src/envoy-channel-server.ts), and pi-envoy reads a role off its record's
// role topic by cutting the lane's prefix (desiredRole in packages/pi-envoy/extensions/envoy.ts).
// So the registry's readers take both forms instead: the interest index matches every form of a
// stored topic (subjectIndex), an unsubscribe removes every form (Remove), a role topic names its
// role in either form (roleOfTopic), and a session's consumer filters on every form
// (bus.SessionConsumers).

// LegacyTopicWarnings names each of topics a registration of sessionID gave in the old form, with
// its new form, "<old> is the old form of <new>; register <new>", which the subscribe route passes
// on in its answer's warnings, and counts each (kind="interest"). The session's own agent subject
// in the old form is counted and named in no warning: a plugin registers it itself on every
// heartbeat, and the caller named no such topic.
func (r *Registry) LegacyTopicWarnings(sessionID string, topics []string) []string {
	own := contracts.LegacyAgentSubject(sessionID)
	var warnings []string
	for _, topic := range topics {
		canonical, legacy := contracts.CanonicalTopic(topic)
		if !legacy {
			continue
		}
		r.countLegacy("interest")
		if topic != own {
			warnings = append(warnings, topic+" is the old form of "+canonical+"; register "+canonical)
		}
	}
	return warnings
}

// ClaimedRole reads a role a claim names in either form (contracts.CanonicalRole) and answers the
// role's name, which is what its claim record is keyed by, with a warning for the old form,
// "<old> is the old form of <project>.<name>; claim <project>.<name>", counted (kind="role"). A
// role in the new form answers its name and no warning.
func (r *Registry) ClaimedRole(role string) (name, warning string) {
	project, name, legacy := contracts.CanonicalRole(role)
	if !legacy {
		return name, ""
	}
	r.countLegacy("role")
	current := project + "." + name
	return name, role + " is the old form of " + current + "; claim " + current
}

// countLegacy counts one old-form name of kind, when the registry was opened with a counter.
func (r *Registry) countLegacy(kind string) {
	if r.legacySubjects != nil {
		r.legacySubjects.Inc([2]string{"kind", kind})
	}
}
