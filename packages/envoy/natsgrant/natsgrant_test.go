package natsgrant_test

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/natsgrant"
)

// set is one grant as the fixtures write it.
type set struct {
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
}

// mergeBase is testdata/merge-base-grants.json: every set infra/core returned at 545b2116, before
// the sets moved here, for each machine id the fixture names. It was written once, by a throwaway
// program in the infra module calling core.ListenerPermissions, core.ListenerHostPermissions,
// core.DispatchPermissions, core.HostPermissions, core.SessionPermissions, core.DaemonPermissions
// and core.DispatchDurables at that commit, and is never regenerated: it is the record of what the
// hub rendered before this package existed.
type mergeBase struct {
	MachineIDs       []string       `json:"machine_ids"`
	Listener         map[string]set `json:"listener"`
	ListenerHost     map[string]set `json:"listener_host"`
	Dispatch         map[string]set `json:"dispatch"`
	Host             map[string]set `json:"host"`
	SessionPattern   set            `json:"session_pattern"`
	Daemon           set            `json:"daemon"`
	DispatchDurables []string       `json:"dispatch_durables"`
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, into))
}

// requireSet compares got with want element for element, after refusing an empty fixture set: an
// empty want would compare equal to an empty grant and prove nothing.
func requireSet(t *testing.T, want set, got natsgrant.Permissions, name string) {
	t.Helper()
	require.NotEmpty(t, want.Publish, "the fixture holds no publish list for %s", name)
	require.Equal(t, want.Publish, got.Publish, "%s publish", name)
	require.Equal(t, want.Subscribe, got.Subscribe, "%s subscribe", name)
}

// Every set this package builds is, element for element and in order, the set infra/core built at
// the merge base, so moving them here rewrites no hub's rendered authorization.
func TestEachGrantMatchesTheSetItReplaces(t *testing.T) {
	var base mergeBase
	readJSON(t, "testdata/merge-base-grants.json", &base)
	require.Equal(t, []string{"example-host-a", "example-dispatch", "example-hub"}, base.MachineIDs)
	for _, id := range base.MachineIDs {
		for name, pair := range map[string]struct {
			want set
			got  natsgrant.Permissions
		}{
			"Listener":     {base.Listener[id], natsgrant.Listener(id)},
			"ListenerHost": {base.ListenerHost[id], natsgrant.ListenerHost(id)},
			"Dispatch":     {base.Dispatch[id], natsgrant.Dispatch(id)},
			"Host":         {base.Host[id], natsgrant.Host(id)},
		} {
			requireSet(t, pair.want, pair.got, name+"("+strconv.Quote(id)+")")
		}
	}
	requireSet(t, base.SessionPattern, natsgrant.SessionPattern(), "SessionPattern()")
	requireSet(t, base.Daemon, natsgrant.Daemon(), "Daemon()")
	require.NotEmpty(t, base.DispatchDurables)
	require.Equal(t, base.DispatchDurables, natsgrant.DispatchDurables())
}

// Measure is the measurement job's grant as the deployment repository deploys it today
// (envoy-cluster/hub.go, hubMeasurePublish and hubMeasureSubscribe at fe26eb31). The copy there
// goes when the job moves to the auth service, and this test with it.
func TestMeasureMatchesTheDeployedList(t *testing.T) {
	var deployed set
	readJSON(t, "testdata/deployment-measure-grant.json", &deployed)
	requireSet(t, deployed, natsgrant.Measure(), "Measure()")
}

// edgeGolden is testdata/edge.golden.json: the edge listener's grant for the machine id it names,
// which the deployment repository's hub user for an edge listener copies entry for entry. It is
// written by hand and never regenerated, so a change to Edge, or to a name it is built from,
// fails here until the golden and that copy change with it.
type edgeGolden struct {
	MachineID string   `json:"machine_id"`
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
}

// The edge listener's grant is the golden, element for element and in order, with no deny list.
func TestTheEdgeGrantIsTheGolden(t *testing.T) {
	var golden edgeGolden
	readJSON(t, "testdata/edge.golden.json", &golden)
	require.Equal(t, "example-edge-a", golden.MachineID)
	grant := natsgrant.Edge(golden.MachineID)
	requireSet(t, set{Publish: golden.Publish, Subscribe: golden.Subscribe}, grant, "Edge("+strconv.Quote(golden.MachineID)+")")
	require.Empty(t, grant.PublishDeny)
	require.Empty(t, grant.SubscribeDeny)
}

// Edge is Listener with the edge's changes and nothing else. In publish: the agent, role and
// Envoy families in place of every notification topic, and no create or update of the stream. In
// subscribe: its own inbox prefix in place of every reply inbox, every session's relay subject
// and the relay receipts, and neither role-lane queue group. So a change to Listener reaches Edge,
// and the golden above says that it did.
func TestTheEdgeGrantIsTheListenersWithTheEdgesChanges(t *testing.T) {
	require.Equal(t, "_INBOX_envoy_edge", natsgrant.EdgeInboxPrefix)
	for _, id := range []string{"example-edge-a", "example-hub"} {
		listener, edge := natsgrant.Listener(id), natsgrant.Edge(id)
		require.Equal(t, "notifications.>", listener.Publish[0])
		kept := slices.DeleteFunc(slices.Clone(listener.Publish[1:]), func(entry string) bool {
			return entry == "$JS.API.STREAM.CREATE.ENVOY_NOTIFICATIONS" || entry == "$JS.API.STREAM.UPDATE.ENVOY_NOTIFICATIONS"
		})
		require.Len(t, kept, len(listener.Publish)-3, "Listener(%q) holds every notification topic, the stream's create and its update", id)
		require.Equal(t, append([]string{"notifications.agent.>", "notifications.role.>", "notifications.envoy.>"}, kept...), edge.Publish, "Edge(%q) publish", id)
		require.Equal(t, []string{"_INBOX_envoy_edge.>", "notifications.envoy.deliver.*", "notifications.envoy.relayreply.>"}, edge.Subscribe, "Edge(%q) subscribe", id)
	}
}

// matchesSubject reports whether the NATS subject pattern pattern admits subject: token by
// token, * for any one token and > for one or more tokens at the end.
func matchesSubject(pattern, subject string) bool {
	patterns, tokens := strings.Split(pattern, "."), strings.Split(subject, ".")
	for i, p := range patterns {
		switch {
		case p == ">":
			return len(tokens) > i
		case i >= len(tokens):
			return false
		case p != "*" && p != tokens[i]:
			return false
		}
	}
	return len(patterns) == len(tokens)
}

// The edge publishes no event of another source, no Legion wake, no reply to another client's
// request and no agent stream subject, and subscribes no agent stream subject either; the
// listener's own grant admits each publish, which shows the matcher can find one.
func TestTheEdgeGrantPublishesNothingOutsideItsLanes(t *testing.T) {
	edge, listener := natsgrant.Edge("example-edge-a"), natsgrant.Listener("example-edge-a")
	admits := func(entries []string, subject string) bool {
		return slices.ContainsFunc(entries, func(pattern string) bool { return matchesSubject(pattern, subject) })
	}
	for _, subject := range []string{
		"notifications.dispatch.issue.PRIV.PRIV-1.comment.created",
		"notifications.github.acme.pub.pr.1.opened",
		"notifications.slack.example-channel",
		"notifications.legion.wake",
		"notifications.team.deploy-squad",
	} {
		require.False(t, admits(edge.Publish, subject), "Edge publishes %s", subject)
		require.True(t, admits(listener.Publish, subject), "Listener does not publish %s, so the matcher finds nothing", subject)
	}
	for _, subject := range []string{"notifications.agent.ses_a", "notifications.role.PRIV.reviewer", "notifications.envoy.relayreply.n1.t1"} {
		require.True(t, admits(edge.Publish, subject), "Edge does not publish %s", subject)
	}
	for _, entries := range [][]string{edge.Publish, edge.Subscribe} {
		for _, entry := range entries {
			require.False(t, strings.HasPrefix(entry, "agentstream."), "Edge names the agent stream subject %q", entry)
		}
	}
	for _, entry := range edge.Publish {
		require.False(t, strings.HasPrefix(entry, "_INBOX"), "Edge publishes to the reply inboxes %q", entry)
	}
}

// The auth service's guard passes the edge's grant: it opens neither direction and names no system
// subject. The same check on a copy with no subscribe list finds that direction open.
func TestTheEdgeGrantOpensNoDirection(t *testing.T) {
	grant := natsgrant.Edge("example-edge-a")
	direction, open := natsgrant.OpenDirection(grant)
	require.False(t, open, "Edge opens its %s direction", direction)
	entry, named := natsgrant.NamesSystemSubject(grant)
	require.False(t, named, "Edge names %q", entry)
	grant.Subscribe = nil
	direction, open = natsgrant.OpenDirection(grant)
	require.True(t, open)
	require.Equal(t, "subscribe", direction)
}

// A session's grant names its own subjects and no other session's, and an id that is not one
// subject token is refused, because a dot, a wildcard or a space in it would widen the grant.
func TestSessionGrantNamesOnlyItsOwnSession(t *testing.T) {
	grant, err := natsgrant.Session("ses_a")
	require.NoError(t, err)
	require.Equal(t, natsgrant.Permissions{
		Publish:   []string{"_INBOX.>", contracts.AgentStreamFramesSubject("ses_a")},
		Subscribe: []string{bus.DeliverSubject("ses_a"), contracts.AgentStreamControlSubject("ses_a")},
	}, grant)
	for _, id := range []string{"a.b", "*", ">", "", "a b", "a\tb", "a\x00b"} {
		_, err := natsgrant.Session(id)
		require.ErrorContains(t, err, strconv.Quote(id), "Session(%q)", id)
		require.False(t, natsgrant.SessionToken(id), "SessionToken(%q)", id)
	}
	require.True(t, natsgrant.SessionToken("ses_a"))
}

// The read-only bucket grant reads a bucket and watches it, and writes nothing: no key, no stream
// create or update.
func TestKVReadIsReadOnly(t *testing.T) {
	read := natsgrant.KVRead("b")
	require.Equal(t, []string{
		"$JS.API.STREAM.INFO.KV_b",
		"$JS.API.DIRECT.GET.KV_b.>",
		"$JS.API.STREAM.MSG.GET.KV_b",
		"$JS.API.CONSUMER.CREATE.KV_b.>",
		"$JS.API.CONSUMER.DELETE.KV_b.>",
		"$JS.FC.KV_b.>",
	}, read)
	for _, entry := range read {
		require.NotEqual(t, "$KV.b.>", entry)
		require.NotContains(t, entry, "STREAM.CREATE")
		require.NotContains(t, entry, "STREAM.UPDATE")
	}
	require.Subset(t, natsgrant.KVWrite("b"), read, "the write grant holds every read call")
	require.Contains(t, natsgrant.KVWrite("b"), "$KV.b.>")
}

// NamesSystemSubject finds an entry that reaches a system subject, the auth service's own
// authorization subject among them, in either list and with or without a queue group, whatever
// whitespace stands before the queue group: nats-server splits an entry with strings.Fields
// (server/client.go, splitSubjectQueue), so a tab or a no-break space separates it as a space
// does. And it finds none in any grant this package returns.
func TestNamesSystemSubject(t *testing.T) {
	for _, entry := range []string{
		"$SYS.REQ.USER.AUTH",
		"$SYS.>",
		">",
		"*.REQ.USER.AUTH",
		"*.>",
		"$SYS.REQ.USER.AUTH envoy-auth",
		">\tenvoy-auth",
		"$SYS.REQ.USER.AUTH\tenvoy-auth",
		">\u00a0envoy-auth",
	} {
		for _, p := range []natsgrant.Permissions{
			{Publish: []string{"_INBOX.>", entry}},
			{Subscribe: []string{"notifications.envoy.deliver.ses_a", entry}},
		} {
			got, found := natsgrant.NamesSystemSubject(p)
			require.True(t, found, "%q in %v", entry, p)
			require.Equal(t, entry, got)
		}
	}
	session, err := natsgrant.Session("ses_a")
	require.NoError(t, err)
	grants := map[string]natsgrant.Permissions{
		"SessionPattern()": natsgrant.SessionPattern(),
		"Session(ses_a)":   session,
		"Daemon()":         natsgrant.Daemon(),
		"Measure()":        natsgrant.Measure(),
		"KVRead":           {Publish: natsgrant.KVRead(bus.InterestBucket)},
		"KVWrite":          {Publish: natsgrant.KVWrite(bus.InterestBucket)},
	}
	for _, id := range []string{"example-host-a", "example-dispatch", "example-hub"} {
		grants["Listener("+id+")"] = natsgrant.Listener(id)
		grants["ListenerHost("+id+")"] = natsgrant.ListenerHost(id)
		grants["Dispatch("+id+")"] = natsgrant.Dispatch(id)
		grants["Host("+id+")"] = natsgrant.Host(id)
		grants["Edge("+id+")"] = natsgrant.Edge(id)
	}
	for name, grant := range grants {
		require.NotEmpty(t, slices.Concat(grant.Publish, grant.Subscribe), name)
		entry, found := natsgrant.NamesSystemSubject(grant)
		require.False(t, found, "%s names %q", name, entry)
	}
}
