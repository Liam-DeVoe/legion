package natsgrant_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
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

// Measure is the measurement user's grant as the deployment renders it once its stream calls and
// envoy-measure.> are removed and its cutover inbox is added, which is not yet deployed: eight
// JetStream reads and its own cutover inbox. The copy there goes when the job moves to the auth
// service, and this test with it.
func TestMeasureMatchesTheDeployedList(t *testing.T) {
	var deployed set
	readJSON(t, "testdata/deployment-measure-grant.json", &deployed)
	requireSet(t, deployed, natsgrant.Measure(), "Measure()")
}

// edgeGolden is testdata/edge.golden.json: the edge listener's grant for the machine id and the
// projects it names, which the deployment repository's hub user for an edge listener copies entry
// for entry. It is written by hand and never regenerated, so a change to Edge, or to a name it is
// built from, fails here until the golden and that copy change with it.
type edgeGolden struct {
	MachineID string   `json:"machine_id"`
	Projects  []string `json:"projects"`
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
}

// edge is natsgrant.Edge(machineID, projects), which must take the list.
func edge(t *testing.T, machineID string, projects ...string) natsgrant.Permissions {
	t.Helper()
	grant, err := natsgrant.Edge(machineID, projects)
	require.NoError(t, err, "Edge(%q, %v)", machineID, projects)
	return grant
}

// The edge listener's grant is the golden, element for element and in order, with no deny list.
func TestTheEdgeGrantIsTheGolden(t *testing.T) {
	var golden edgeGolden
	readJSON(t, "testdata/edge.golden.json", &golden)
	require.Equal(t, "m", golden.MachineID)
	require.Equal(t, []string{"PRIV"}, golden.Projects)
	grant := edge(t, golden.MachineID, golden.Projects...)
	requireSet(t, set{Publish: golden.Publish, Subscribe: golden.Subscribe}, grant, "Edge("+strconv.Quote(golden.MachineID)+", "+strings.Join(golden.Projects, ",")+")")
	require.Empty(t, grant.PublishDeny)
	require.Empty(t, grant.SubscribeDeny)
}

// Edge is Listener with the edge's changes and nothing else. In publish: in place of every
// notification topic, the finite list of the edge's own subjects - the agent subjects, each
// project's role subjects, the relay subjects, every edge machine's relay receipts, the resume
// lane, and the exception lanes of the agent subjects and of each project's role subjects - and no
// create or update of the stream or of any bucket's stream, and no direct get of any bucket. In
// subscribe: its own machine's inbox prefix in place of every
// reply inbox, every session's relay subject and its own machine's relay receipts, and neither
// role-lane queue group. So a change to Listener reaches Edge, and the golden above says that it
// did.
func TestTheEdgeGrantIsTheListenersWithTheEdgesChanges(t *testing.T) {
	for _, id := range []string{"m", "example-hub"} {
		require.Equal(t, "_INBOX_envoy_edge_"+id, natsgrant.EdgeInboxPrefix(id), "EdgeInboxPrefix(%q)", id)
		require.Equal(t, "notifications.envoy.relayreply."+id, natsgrant.EdgeRelayReplyPrefix(id), "EdgeRelayReplyPrefix(%q)", id)
		for _, projects := range [][]string{{"PRIV"}, {"OTHER", "PRIV"}} {
			listener, edge := natsgrant.Listener(id), edge(t, id, projects...)
			require.Equal(t, "notifications.>", listener.Publish[0])
			kept := slices.DeleteFunc(slices.Clone(listener.Publish[1:]), func(entry string) bool {
				return strings.HasPrefix(entry, "$JS.API.STREAM.CREATE.") || strings.HasPrefix(entry, "$JS.API.STREAM.UPDATE.") || strings.HasPrefix(entry, "$JS.API.DIRECT.GET.")
			})
			require.Len(t, kept, len(listener.Publish)-1-2*(1+len(bus.ListenerBuckets()))-len(bus.ListenerBuckets()), "Listener(%q) holds every notification topic, the create and update of the stream and of each bucket's stream, and each bucket's direct get", id)
			own := []string{"notifications.agent.>"}
			for _, project := range projects {
				own = append(own, "notifications.role."+project+".>")
			}
			own = append(own, "notifications.envoy.deliver.*", "notifications.envoy.relayreply.>", "notifications.envoy.resume.>", "notifications.envoy.exceptions.notifications.agent.>")
			for _, project := range projects {
				own = append(own, "notifications.envoy.exceptions.notifications.role."+project+".>")
			}
			require.Equal(t, append(own, kept...), edge.Publish, "Edge(%q, %v) publish", id, projects)
			require.Equal(t, []string{"_INBOX_envoy_edge_" + id + ".>", "notifications.envoy.deliver.*", "notifications.envoy.relayreply." + id + ".>"}, edge.Subscribe, "Edge(%q, %v) subscribe", id, projects)
		}
	}
}

// Two edge machines subscribe inbox prefixes and receipt families of their own, so neither is
// delivered the other's replies nor reads the other's receipts: neither's subscribe entries match
// a subject the other names. Each publishes the other's receipt, as the listener serving a
// session's relay socket answers on the family of the machine holding the session's consumer. A
// machine whose id begins another's (edge-a, edge-ab) is no exception, since a NATS wildcard
// matches whole tokens. The other machine's own entries match the same subjects, which shows the
// matcher finds one.
func TestEachEdgeMachineHoldsItsOwnInboxesAndReceipts(t *testing.T) {
	for _, pair := range [][2]string{{"edge-a", "edge-b"}, {"edge-a", "edge-ab"}} {
		a, b := edge(t, pair[0], "PRIV"), edge(t, pair[1], "PRIV")
		inboxOfB := natsgrant.EdgeInboxPrefix(pair[1]) + ".nuid"
		receiptOfB := natsgrant.EdgeRelayReplyPrefix(pair[1]) + ".nuid"
		admits := func(entries []string, subject string) bool {
			return slices.ContainsFunc(entries, func(entry string) bool { return bus.SubjectContains(entry, subject) })
		}
		require.True(t, admits(b.Subscribe, inboxOfB), "%s does not subscribe its own inbox %s", pair[1], inboxOfB)
		require.False(t, admits(a.Subscribe, inboxOfB), "%s subscribes %s's inbox %s", pair[0], pair[1], inboxOfB)
		require.True(t, admits(b.Subscribe, receiptOfB), "%s does not subscribe its own receipt %s", pair[1], receiptOfB)
		require.False(t, admits(a.Subscribe, receiptOfB), "%s subscribes %s's receipt %s", pair[0], pair[1], receiptOfB)
		require.True(t, admits(a.Publish, receiptOfB), "%s does not publish %s's receipt %s, which the relay socket's answer needs", pair[0], pair[1], receiptOfB)
	}
}

// Edge refuses, naming why and building no subject, a machine id that is not one subject token,
// whose prefixes would cover or share another machine's (a.b's inbox is under a's, and a space is
// no token), and a project list it cannot name: none, which would render the whole consumer and
// key-value reach serving no project, or one oauthwire.ValidProjects refuses (a wildcard here;
// its own tests read every rule).
func TestTheEdgeGrantRefusesAMachineOrProjectsItCannotName(t *testing.T) {
	for _, machineID := range []string{"a.b", "example hub", "*", ">", ""} {
		grant, err := natsgrant.Edge(machineID, []string{"PRIV"})
		require.ErrorContains(t, err, strconv.Quote(machineID), "Edge(%q, [PRIV])", machineID)
		require.Equal(t, natsgrant.Permissions{}, grant)
	}
	for name, projects := range map[string][]string{"no list": nil, "an empty list": {}, "a wildcard": {"*"}} {
		grant, err := natsgrant.Edge("m", projects)
		require.Error(t, err, "Edge(m, %s)", name)
		require.Equal(t, natsgrant.Permissions{}, grant)
	}
	for _, projects := range [][]string{{"PRIV"}, {"OTHER", "PRIV"}} {
		_, err := natsgrant.Edge("m", projects)
		require.NoError(t, err, "Edge(m, %q)", projects)
	}
}

// The edge publishes each of its own lanes and nothing else of the notification topics: no event
// of another source, no Legion wake, no role subject of another project or in the form that names
// none, no kept role lane or drain cursor, no exception lane but its own two, no reply to another
// client's request and no agent stream subject, and it subscribes no agent stream subject either.
// The listener's own grant admits each publish the edge's refuses, which shows the matcher can
// find one.
func TestTheEdgeGrantPublishesNothingOutsideItsLanes(t *testing.T) {
	edge, listener := edge(t, "m", "PRIV"), natsgrant.Listener("m")
	admits := func(entries []string, subject string) bool {
		return slices.ContainsFunc(entries, func(pattern string) bool { return bus.SubjectContains(pattern, subject) })
	}
	for _, subject := range []string{
		"notifications.dispatch.issue.PRIV.PRIV-1.comment.created",
		"notifications.github.acme.pub.pr.1.opened",
		"notifications.slack.example-channel",
		"notifications.legion.wake",
		"notifications.team.deploy-squad",
		"notifications.role.INFRA.x",
		"notifications.role.legion-infra-controller",
		"notifications.envoy.kept.role.x",
		"notifications.envoy.kept.cursor.x",
		"notifications.envoy.exceptions.notifications.github.acme.pub.pr.1.opened",
		"notifications.envoy.exceptions.notifications.role.INFRA.x",
		"notifications.envoy.exceptions.notifications.team.deploy-squad",
		"notifications.envoy.relayreply",
		"$JS.API.STREAM.CREATE.KV_envoy_interests",
		"$JS.API.STREAM.UPDATE.KV_envoy_interests",
		"$JS.API.STREAM.UPDATE.KV_envoy_dpop_proofs",
		"$JS.API.DIRECT.GET.KV_envoy_interests.$KV.envoy_interests.k",
		"$JS.API.DIRECT.GET.KV_envoy_dpop_proofs.$KV.envoy_dpop_proofs.k",
	} {
		require.False(t, admits(edge.Publish, subject), "Edge publishes %s", subject)
		require.True(t, admits(listener.Publish, subject), "Listener does not publish %s, so the matcher finds nothing", subject)
	}
	for _, subject := range []string{
		"notifications.agent.ses_a",
		"notifications.role.PRIV.reviewer",
		"notifications.envoy.deliver.ses_a",
		"notifications.envoy.relayreply.m.t1",
		"notifications.envoy.relayreply.other.t1",
		"notifications.envoy.resume.ses_a",
		"notifications.envoy.exceptions.notifications.agent.ses_a",
		"notifications.envoy.exceptions.notifications.role.PRIV.reviewer",
	} {
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
	grant := edge(t, "m", "PRIV")
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
	grants := grantTable(t)
	grants["KVRead"] = natsgrant.Permissions{Publish: natsgrant.KVRead(bus.InterestBucket)}
	grants["KVWrite"] = natsgrant.Permissions{Publish: natsgrant.KVWrite(bus.InterestBucket)}
	for name, grant := range grants {
		require.NotEmpty(t, slices.Concat(grant.Publish, grant.Subscribe), name)
		entry, found := natsgrant.NamesSystemSubject(grant)
		require.False(t, found, "%s names %q", name, entry)
	}
}

// grantTable is every grant this package builds, keyed by the call that builds it: the function's
// name, with its argument in parentheses where it takes one. TestTheGrantTableNamesEveryGrant holds
// it to natsgrant.go's declarations, so a new grant function fails that test until it is listed
// here, and every test that walks the table walks the new grant too.
func grantTable(t *testing.T) map[string]natsgrant.Permissions {
	t.Helper()
	session, err := natsgrant.Session("ses_a")
	require.NoError(t, err)
	grants := map[string]natsgrant.Permissions{
		"SessionPattern()":   natsgrant.SessionPattern(),
		"Session(ses_a)":     session,
		"Daemon()":           natsgrant.Daemon(),
		"Measure()":          natsgrant.Measure(),
		"HoldProbe()":        natsgrant.HoldProbe(),
		"Operator(released)": natsgrant.Operator(natsgrant.OperatorPhaseReleased),
		"Operator(prepare)":  natsgrant.Operator(natsgrant.OperatorPhasePrepare),
		"Operator(held)":     natsgrant.Operator(natsgrant.OperatorPhaseHeld),
	}
	for _, id := range []string{"example-host-a", "example-dispatch", "example-hub"} {
		grants["Listener("+id+")"] = natsgrant.Listener(id)
		grants["ListenerHost("+id+")"] = natsgrant.ListenerHost(id)
		grants["Dispatch("+id+")"] = natsgrant.Dispatch(id)
		grants["Host("+id+")"] = natsgrant.Host(id)
		grants["Edge("+id+", PRIV)"] = edge(t, id, "PRIV")
	}
	return grants
}

// Every exported function natsgrant.go declares that returns Permissions, read from the source by
// go/ast, has a row in grantTable, and every row names one of them: a grant added to the package
// is walked by every test that reads the table, or this test names it.
func TestTheGrantTableNamesEveryGrant(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "natsgrant.go", nil, 0)
	require.NoError(t, err)
	declared := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil {
			continue
		}
		if result, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok && result.Name == "Permissions" {
			declared[fn.Name.Name] = true
		}
	}
	require.NotEmpty(t, declared, "natsgrant.go declares no grant function: the reading enumerated nothing")
	listed := map[string]bool{}
	for call := range grantTable(t) {
		name, _, _ := strings.Cut(call, "(")
		require.True(t, declared[name], "grantTable lists %s, which natsgrant.go does not declare as a grant", call)
		listed[name] = true
	}
	for name := range declared {
		require.True(t, listed[name], "natsgrant.go declares the grant %s, which grantTable does not list", name)
	}
}

// The buckets envoy-infra buckets copies, whose infos the operator reads outside a hold and whose
// streams it snapshots, makes, changes, removes and restores only inside one.
var copiedBuckets = []string{"envoy_interests", "envoy_roles", "envoy_sessions", "envoy_known_sessions", "envoy_ci_state"}

// operatorReleased is Operator(OperatorPhaseReleased)'s publish list: reads only, no snapshot and
// no stream create, each of which writes a subject its requester chooses.
func operatorReleased() []string {
	publish := []string{
		"$JS.API.INFO",
		"$JS.API.STREAM.INFO.ENVOY_NOTIFICATIONS",
		"$JS.API.STREAM.INFO.KV_envoy_session_consumers",
	}
	for _, b := range copiedBuckets {
		publish = append(publish, "$JS.API.STREAM.INFO.KV_"+b)
	}
	return append(publish,
		"$JS.API.STREAM.MSG.GET.KV_envoy_session_consumers",
		"$JS.API.CONSUMER.NAMES.ENVOY_NOTIFICATIONS",
		"$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS.*",
		"$JS.API.STREAM.INFO.ENVOY_MEASURE",
	)
}

// operatorPrepare is Operator(OperatorPhasePrepare)'s publish list as the plan writes it.
func operatorPrepare() []string {
	return append(operatorReleased(),
		"$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.*",
		"$JS.API.STREAM.INFO.KV_legion_agents",
		"$JS.API.STREAM.INFO.KV_envoy_dpop_proofs",
		"$JS.API.STREAM.INFO.KV_envoy_webhook_redelivery",
	)
}

// operatorHeld is Operator(OperatorPhaseHeld)'s publish list as the plan writes it, with every
// snapshot and the create of ENVOY_MEASURE, which released held before.
func operatorHeld() []string {
	publish := append(operatorPrepare(),
		"$JS.API.STREAM.CREATE.ENVOY_HOLD_BARRIER",
		"$JS.API.STREAM.DELETE.ENVOY_HOLD_BARRIER",
		"$JS.API.STREAM.LIST",
		"$JS.API.STREAM.NAMES",
		"$JS.API.CONSUMER.LIST.*",
		"$JS.API.CONSUMER.DELETE.ENVOY_NOTIFICATIONS.*",
		"$JS.API.STREAM.CREATE.ENVOY_MEASURE",
		"$KV.envoy_session_consumers.>",
		"$JS.API.STREAM.CREATE.KV_envoy_session_consumers",
		"$JS.API.STREAM.SNAPSHOT.KV_envoy_session_consumers",
		"$JS.SNAPSHOT.ACK.KV_envoy_session_consumers.>",
		"$JS.API.STREAM.RESTORE.KV_envoy_session_consumers",
		"$JS.SNAPSHOT.RESTORE.KV_envoy_session_consumers.*",
	)
	for _, b := range copiedBuckets {
		publish = append(publish,
			"$JS.API.STREAM.CREATE.KV_"+b,
			"$JS.API.STREAM.UPDATE.KV_"+b,
			"$JS.API.STREAM.DELETE.KV_"+b,
			"$JS.API.STREAM.SNAPSHOT.KV_"+b,
			"$JS.SNAPSHOT.ACK.KV_"+b+".>",
			"$JS.API.STREAM.RESTORE.KV_"+b,
			"$JS.SNAPSHOT.RESTORE.KV_"+b+".*",
			"$KV."+b+".hold.barrier.*",
		)
	}
	publish = append(publish,
		"$JS.API.STREAM.CREATE.ENVOY_NOTIFICATIONS",
		"$JS.API.STREAM.CREATE.KV_legion_agents",
		"$JS.API.STREAM.CREATE.KV_envoy_dpop_proofs",
		"$JS.API.STREAM.CREATE.KV_envoy_webhook_redelivery",
		"$JS.API.STREAM.UPDATE.ENVOY_NOTIFICATIONS",
		"$JS.API.STREAM.UPDATE.KV_legion_agents",
		"$JS.API.STREAM.UPDATE.KV_envoy_dpop_proofs",
		"$JS.API.STREAM.UPDATE.KV_envoy_webhook_redelivery",
		"$JS.API.STREAM.DELETE.*",
		"$JS.API.CONSUMER.DELETE.*.*",
	)
	for _, s := range []string{"ENVOY_NOTIFICATIONS", "KV_legion_agents", "KV_envoy_dpop_proofs", "KV_envoy_webhook_redelivery"} {
		publish = append(publish,
			"$JS.API.STREAM.SNAPSHOT."+s,
			"$JS.SNAPSHOT.ACK."+s+".>",
			"$JS.API.STREAM.RESTORE."+s,
			"$JS.SNAPSHOT.RESTORE."+s+".*",
		)
	}
	return publish
}

// Operator's three phases and HoldProbe are each exactly the lists the plan writes, entry for
// entry and in order, so a subject added to any phase, or a phase that grants another's entry, fails
// here naming it. Every operator connection subscribes its own inbox prefix alone.
func TestOperatorIsExactly(t *testing.T) {
	inbox := []string{"_INBOX_envoy._operator.>"}
	for phase, want := range map[natsgrant.OperatorPhase][]string{
		natsgrant.OperatorPhaseReleased: operatorReleased(),
		natsgrant.OperatorPhasePrepare:  operatorPrepare(),
		natsgrant.OperatorPhaseHeld:     operatorHeld(),
	} {
		got := natsgrant.Operator(phase)
		for _, entry := range got.Publish {
			require.Contains(t, want, entry, "Operator(%s) publishes %q, which the plan's list does not hold", phase, entry)
		}
		for _, entry := range want {
			require.Contains(t, got.Publish, entry, "Operator(%s) does not publish %q, which the plan's list holds", phase, entry)
		}
		require.Equal(t, natsgrant.Permissions{Publish: want, Subscribe: inbox}, got, "Operator(%s)", phase)
	}
	require.Equal(t, natsgrant.Operator(natsgrant.OperatorPhaseReleased), natsgrant.Operator("paused"),
		"a phase outside the three is answered with the released grant")
	require.Equal(t, natsgrant.Permissions{
		Publish:   []string{"_ENVOY_HOLD_PROBE.none"},
		Subscribe: []string{"_ENVOY_HOLD_PROBE.none"},
	}, natsgrant.HoldProbe())
	require.Equal(t, "_INBOX_envoy._operator", natsgrant.OperatorInbox)
	require.Equal(t, "ENVOY_HOLD_BARRIER", natsgrant.HoldBarrierStream)
}

// The operator's inbox prefix sits under the one reply root, "_INBOX_envoy", as a token of its
// own, so a render that moves JetStream into an account of its own and exports only the reply
// root reaches the operator's replies through that one export. It fails when the operator's prefix
// leaves the root, as "_INBOX_envoy_operator" did, which shares the root's characters but not its
// token.
func TestTheOperatorInboxIsUnderTheReplyRoot(t *testing.T) {
	require.Equal(t, "_INBOX_envoy", natsgrant.ReplyRoot)
	require.True(t, strings.HasPrefix(natsgrant.OperatorInbox, natsgrant.ReplyRoot+"."),
		"OperatorInbox %q is not under the reply root %q", natsgrant.OperatorInbox, natsgrant.ReplyRoot)
	require.True(t, natsserver.SubjectMatchesFilter(natsgrant.OperatorInbox+".x", natsgrant.ReplyRoot+".>"),
		"the reply root's export %s.> does not reach an operator reply", natsgrant.ReplyRoot)
}

// No grant the package builds names the retired hold bucket, KV_envoy_maintenance or its keys:
// the hold lives in the broker's Postgres, and a NATS record of it could be stored over by any
// user with a JetStream API grant.
func TestNoGrantNamesTheRetiredHoldBucket(t *testing.T) {
	for name, grant := range grantTable(t) {
		for _, entry := range slices.Concat(grant.Publish, grant.Subscribe, grant.PublishDeny, grant.SubscribeDeny) {
			require.False(t, strings.Contains(entry, "KV_envoy_maintenance") || strings.Contains(entry, "$KV.envoy_maintenance."),
				"%s names the retired hold bucket in %q", name, entry)
		}
	}
}

// No grant the package builds subscribes a subject any KV bucket's key can have: each subscribe
// entry is held to $KV.> by NATS's own rule for two subjects that one literal subject could both
// match (nats-server's SubjectsCollide), so a $KV.<bucket>.> of any bucket, a $KV.*.>, a *.> or a
// > is caught as a literal key is. A push consumer of ENVOY_NOTIFICATIONS, which the prepare and
// held operator can create, becomes active only while some client subscribes exactly its deliver
// subject (nats-server v2.15.1 server/sublist.go:186-193), and then delivers each message under its
// own subject to every matching subscriber (server/stream.go:8620-8621), the bucket's stream, which
// captures $KV.<bucket>.>, among them: measured on one server, a notification was stored inside
// KV_envoy_sessions. A subscribe of a key subject in any grant would be that client.
func TestNoGrantSubscribesAKeySubject(t *testing.T) {
	const keys = "$KV.>"
	for name, grant := range grantTable(t) {
		for _, entry := range grant.Subscribe {
			fields := strings.Fields(entry)
			require.NotEmpty(t, fields, "%s subscribes a blank entry", name)
			require.False(t, natsserver.SubjectsCollide(fields[0], keys),
				"%s subscribes %q, which a key subject of some bucket (%s) matches", name, entry, keys)
		}
	}
	for _, pattern := range []string{"$KV.envoy_roles.>", "$KV.envoy_sessions.k", "$KV.*.>", ">", "*.>", "$KV.envoy_sessions.*"} {
		require.True(t, natsserver.SubjectsCollide(pattern, keys), "the rule misses %q", pattern)
	}
	for _, pattern := range []string{"_INBOX.>", "notifications.>", "$KV", "KV.envoy_roles.>"} {
		require.False(t, natsserver.SubjectsCollide(pattern, keys), "the rule refuses %q, which no key subject matches", pattern)
	}
}
