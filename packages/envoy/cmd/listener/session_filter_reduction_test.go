package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
)

// A session's consumer filter reaches no other session's private subjects even after the server's
// filter reduction merges its topics: bus.SessionConsumerFilters replaces two overlapping topics
// that neither contains with their longest common prefix of whole tokens followed by `>`, so two
// wildcard topics that each pass the private rule alone can reduce to one that reaches every
// session's inbox. The filter holds the row's topics whatever the deployed stream captures, so it
// must hold this line itself.
//
// Two rows show the two ways it happens. A wildcard first token takes the reduction out of the
// notification namespace altogether: *.team.foo, which a registration stores (it is outside
// notifications., so the publish verdict sends it through JetStream and ReachesNoSession is false
// for it), beside notifications.team.*, which the stream captures, reduce to `>`, the whole stream.
// The stream stores nothing outside notifications., so such a topic delivers nothing and is left
// out. Inside the namespace, two wildcards an earlier build's row may hold,
// notifications.envoy.exceptions.foo.* and notifications.envoy.exceptions.*.bar, reduce to
// notifications.envoy.exceptions.>, which reaches every session's failed deliveries; both are left
// out, and the filter keeps everything else.
func TestAFilterWhoseTopicsReduceToAnotherSessionsSubjectsLeavesThemOut(t *testing.T) {
	const session = "ses_reduction"
	for _, tc := range []struct {
		name   string
		topics []string
		kept   []string
		left   []string
	}{
		{
			name:   "a wildcard first token beside a team wildcard",
			topics: []string{"*.team.foo", "notifications.team.*"},
			kept:   []string{"notifications.team.*"},
			left:   []string{"*.team.foo"},
		},
		{
			name:   "two exception-lane wildcards an earlier build's row holds",
			topics: []string{"notifications.envoy.exceptions.foo.*", "notifications.envoy.exceptions.*.bar", "notifications.team.deploy"},
			kept:   []string{"notifications.team.deploy"},
			left:   []string{"notifications.envoy.exceptions.foo.*", "notifications.envoy.exceptions.*.bar"},
		},
		{
			name: "the same pair beside an exception-lane wildcard that merges with neither",
			topics: []string{
				"notifications.envoy.exceptions.foo.*", "notifications.envoy.exceptions.*.bar",
				"notifications.envoy.exceptions.notifications.github.>", "notifications.team.deploy",
			},
			kept: []string{"notifications.envoy.exceptions.notifications.github.>", "notifications.team.deploy"},
			left: []string{"notifications.envoy.exceptions.foo.*", "notifications.envoy.exceptions.*.bar"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, topic := range tc.topics {
				if bus.ReachesPrivateStreamSubject(session, topic, nil) {
					t.Fatalf("%s reaches a private subject by itself; the row needs topics that pass the rule alone", topic)
				}
			}
			filter := sessionConsumerFilter(session, tc.topics)
			for _, reduced := range bus.SessionConsumerFilters(filter) {
				if strings.ContainsAny(reduced, "*>") && bus.ReachesPrivateStreamSubject(session, reduced, nil) {
					t.Errorf("the filter %v reduces to %s, which reaches another session's private subjects", filter, reduced)
				}
			}
			for _, topic := range tc.kept {
				if !slices.Contains(filter, topic) {
					t.Errorf("the filter %v lacks %s, which reaches no other session", filter, topic)
				}
			}
			for _, topic := range tc.left {
				if slices.Contains(filter, topic) {
					t.Errorf("the filter %v holds %s", filter, topic)
				}
			}
			if !slices.Contains(filter, contracts.LegacyAgentSubject(session)) {
				t.Errorf("the filter %v lacks the session's own agent subject", filter)
			}
		})
	}
}
