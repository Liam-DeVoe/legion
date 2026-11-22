package natsgrant_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/natsgrant"
)

// A Legion daemon on fresh consumer names reaches each fresh name, legion-go-<P>-<kind>~<gen>, on a
// connection named for that generation, legion-go-<P>-<kind>/<gen>. Held to its projects, its
// grant names only the fixed names, so the auth service passes the connection's name in the scope
// (Scope.Connection) and WithinProjects grants that one consumer's calls beside them: the create
// on the kind's one filter under the project, the info, the pull and the acknowledgements, and
// nothing of any other generation. The control is the same scope with no connection, which admits
// none of the four.
func TestWithinProjectsGrantsTheConsumerOfTheConnectionsGeneration(t *testing.T) {
	const generation, other = "RFuDfoOI3HLgXUdIZvZnZx", "RFuDfoOI3HLgXUdIZvZsCP"
	for kind, filter := range map[string]string{
		"dispatch": "notifications.dispatch.issue.PUB.>",
		"wake":     "notifications.legion.pub.wake",
		"ready":    "notifications.legion.wake",
		"github":   "notifications.github.>",
	} {
		t.Run(kind, func(t *testing.T) {
			physical := "legion-go-PUB-" + kind + "~" + generation
			calls := func(physical string) []string {
				return []string{
					"$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS." + physical + "." + filter,
					"$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS." + physical,
					"$JS.API.CONSUMER.MSG.NEXT.ENVOY_NOTIFICATIONS." + physical,
					"$JS.ACK.ENVOY_NOTIFICATIONS." + physical + ".1.2.3.4.5.6",
				}
			}
			scope := pubScope
			scope.Connection = natsgrant.DaemonGenerationConnection(physical)
			require.Equal(t, "legion-go-PUB-"+kind+"/"+generation, scope.Connection)
			held, err := natsgrant.WithinProjects(natsgrant.Daemon(), scope)
			require.NoError(t, err)
			without, err := natsgrant.WithinProjects(natsgrant.Daemon(), pubScope)
			require.NoError(t, err)
			for _, subject := range calls(physical) {
				require.True(t, admits(held.Publish, subject), "the generation's connection is granted %s", subject)
				require.False(t, admits(without.Publish, subject), "the control, no connection, admits %s", subject)
			}
			for _, subject := range calls("legion-go-PUB-" + kind + "~" + other) {
				require.False(t, admits(held.Publish, subject), "the generation's connection is granted another generation's %s", subject)
			}
			added := slices.DeleteFunc(slices.Clone(held.Publish), func(entry string) bool { return slices.Contains(without.Publish, entry) })
			require.Len(t, added, 4, "the connection adds the generation's four calls and nothing else: %v", added)
			for _, entry := range added {
				require.Contains(t, entry, physical, "an added entry names the generation's consumer")
			}
			require.Equal(t, without.Subscribe, held.Subscribe, "the connection changes no subscribe entry")
		})
	}
}

// The generation's create is granted on its kind's one filter under the scope's project alone: a
// create of the fresh name with no filter in its subject is refused, as is one on another
// project's filter. The control is a grant written with the unfiltered form, the consumer name and
// `>`, which admits the other project's filter: the filtered entry is what refuses it.
func TestTheGenerationsCreateIsGrantedOnItsOwnFilterAlone(t *testing.T) {
	const physical = "legion-go-PUB-dispatch~RFuDfoOI3HLgXUdIZvZnZx"
	const create = "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS." + physical
	scope := pubScope
	scope.Connection = natsgrant.DaemonGenerationConnection(physical)
	held, err := natsgrant.WithinProjects(natsgrant.Daemon(), scope)
	require.NoError(t, err)
	require.True(t, admits(held.Publish, create+".notifications.dispatch.issue.PUB.>"), "its own filter is granted")
	require.False(t, admits(held.Publish, create), "an unfiltered create of the generation is granted")
	require.False(t, admits(held.Publish, create+".notifications.dispatch.issue.OTHER.>"), "a create of the generation on another project's filter is granted")
	unfiltered := []string{create + ".>"}
	require.True(t, admits(unfiltered, create+".notifications.dispatch.issue.OTHER.>"), "the control, the unfiltered form, admits the other filter")
}

// A connection name that is no generation of a daemon consumer the scope holds adds nothing: one
// of a project the scope does not hold, an unknown kind, a generation that is no NUID (a character
// no consumer name token takes, fewer or more than 22 letters and digits), a fixed name alone, a
// fresh name, and another family's connection names.
func TestAConnectionNamingNoGenerationOfTheScopeAddsNothing(t *testing.T) {
	without, err := natsgrant.WithinProjects(natsgrant.Daemon(), pubScope)
	require.NoError(t, err)
	for _, name := range []string{
		"", "legion-daemon PUB",
		"legion-go-OTHER-github/RFuDfoOI3HLgXUdIZvZnZx",
		"legion-go-PUB-other/RFuDfoOI3HLgXUdIZvZnZx",
		"legion-go-PUB-github", "legion-go-PUB-github/", "legion-go-PUB-github~RFuDfoOI3HLgXUdIZvZnZx",
		"legion-go-PUB-github/a.b", "legion-go-PUB-github/a*", "legion-go-PUB-github/a>", "legion-go-PUB-github/a~b",
		"legion-go-PUB-github/a/b", "legion-go-PUB-github/a b", "legion-go-PUB-github/" + strings.Repeat("a", 21),
		"legion-go-PUB-github/" + strings.Repeat("a", 23), "legion-go-PUB-github/RFuDfoOI3HLgXUdIZvZn.x",
		"legion-go-PUB-github~x/RFuDfoOI3HLgXUdIZvZnZx", "legion-go-pub-github/RFuDfoOI3HLgXUdIZvZnZx",
		"dispatch-intake/delivery-events/RFuDfoOI3HLgXUdIZvZnZx",
	} {
		scope := pubScope
		scope.Connection = name
		got, err := natsgrant.WithinProjects(natsgrant.Daemon(), scope)
		require.NoError(t, err, "connection %q", name)
		require.Equal(t, without, got, "connection %q", name)
	}
}

// DaemonGenerationConnection names each daemon fresh name's connection so that the grant reads that
// very name back from it: a scope holding the project, with that connection, grants the fresh
// name, for any project key and kind of the held scope's table. A name of no daemon consumer's
// generation adds nothing.
func TestDaemonGenerationConnectionRoundTrips(t *testing.T) {
	for _, row := range []struct{ project, physical string }{
		{"PUB", "legion-go-PUB-dispatch~RFuDfoOI3HLgXUdIZvZnZx"},
		{"ABCDEFGHIJ", "legion-go-ABCDEFGHIJ-ready~0000000000000000000000"},
		{"A0", "legion-go-A0-github~" + strings.Repeat("Z", 22)},
	} {
		name := natsgrant.DaemonGenerationConnection(row.physical)
		got, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Projects: []string{row.project}, Identity: "legion-daemon-" + row.project, Connection: name})
		require.NoError(t, err)
		require.True(t, admits(got.Publish, "$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS."+row.physical), "%s, named %s", row.physical, name)
	}
	without, err := natsgrant.WithinProjects(natsgrant.Daemon(), pubScope)
	require.NoError(t, err)
	for _, name := range []string{"session/abc", "delivery-events/abc", "legion-go-PUB-dispatch"} {
		scope := pubScope
		scope.Connection = name
		got, err := natsgrant.WithinProjects(natsgrant.Daemon(), scope)
		require.NoError(t, err)
		require.Equal(t, without, got, name)
	}
}

// A fixed name, legion-go-<P>-<kind>, is no generation, so the connection DaemonGenerationConnection
// names for it is the name itself, never <name>/<name>: that form would read as a generation called
// <name> of a consumer <name>~<name> that no daemon ever makes. A connection carrying the fixed
// name gets the held grant alone, as the daemon's own connection does; the daemon dials no
// connection for a fixed name (its durableNames.on refuses one). The control is the form
// <name>/<name> the daemon's own rule once gave, which differs from the name for every kind.
func TestTheConnectionOfAFixedNameIsTheNameItself(t *testing.T) {
	without, err := natsgrant.WithinProjects(natsgrant.Daemon(), pubScope)
	require.NoError(t, err)
	for _, kind := range []string{"dispatch", "github", "wake", "ready"} {
		fixed := "legion-go-PUB-" + kind
		name := natsgrant.DaemonGenerationConnection(fixed)
		require.Equal(t, fixed, name, "the connection of the fixed name %s", fixed)
		require.NotEqual(t, fixed+"/"+fixed, name, "control: the doubled form for %s", fixed)
		scope := pubScope
		scope.Connection = name
		got, err := natsgrant.WithinProjects(natsgrant.Daemon(), scope)
		require.NoError(t, err)
		require.Equal(t, without, got, "a connection named for the fixed name %s gets the held grant alone", fixed)
	}
}
