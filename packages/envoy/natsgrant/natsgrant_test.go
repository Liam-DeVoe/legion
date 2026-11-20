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

	"github.com/nats-io/nats-server/v2/server"
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
	}
	for name, grant := range grants {
		require.NotEmpty(t, slices.Concat(grant.Publish, grant.Subscribe), name)
		entry, found := natsgrant.NamesSystemSubject(grant)
		require.False(t, found, "%s names %q", name, entry)
	}
}

// grantTable is every grant this package builds, one row per exported function that returns
// Permissions and takes none, keyed by the function's name, so a test that holds every grant to a
// rule reads them here. TestTheGrantTableNamesEveryGrant checks the rows against natsgrant.go's
// declarations, so a grant added there fails it until it is listed here.
func grantTable(t *testing.T) map[string]natsgrant.Permissions {
	t.Helper()
	session, err := natsgrant.Session("ses_a")
	require.NoError(t, err)
	return map[string]natsgrant.Permissions{
		"Listener":       natsgrant.Listener("example-hub"),
		"ListenerHost":   natsgrant.ListenerHost("example-hub"),
		"Dispatch":       natsgrant.Dispatch("example-dispatch"),
		"SessionPattern": natsgrant.SessionPattern(),
		"Session":        session,
		"Daemon":         natsgrant.Daemon(),
		"Host":           natsgrant.Host("example-host-a"),
		"Measure":        natsgrant.Measure(),
	}
}

// grantDeclarations are the names of natsgrant.go's exported functions that build a grant: their
// first result is Permissions and no parameter is one. ForCallout, which narrows a grant it is
// given, builds none.
func grantDeclarations(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "natsgrant.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)
	isPermissions := func(field *ast.Field) bool {
		ident, ok := field.Type.(*ast.Ident)
		return ok && ident.Name == "Permissions"
	}
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil || !isPermissions(fn.Type.Results.List[0]) {
			continue
		}
		if slices.ContainsFunc(fn.Type.Params.List, isPermissions) {
			continue
		}
		names = append(names, fn.Name.Name)
	}
	require.NotEmpty(t, names, "natsgrant.go declares no grant, so the table is checked against nothing")
	return names
}

// The grant table holds one row for each grant natsgrant.go declares, and none it does not. A row
// may carry its argument after the name, "Operator(held)", and is counted under the name.
func TestTheGrantTableNamesEveryGrant(t *testing.T) {
	var rows []string
	for name := range grantTable(t) {
		function, _, _ := strings.Cut(name, "(")
		if !slices.Contains(rows, function) {
			rows = append(rows, function)
		}
	}
	declared := grantDeclarations(t)
	slices.Sort(rows)
	slices.Sort(declared)
	require.Equal(t, declared, rows, "the grant table and natsgrant.go's grants differ")
}

// streamConfigurationCalls are the JetStream API calls that make a stream, change its
// configuration or remove it: a create, an update, a delete, and a restore, which makes the stream
// a snapshot carries with that snapshot's configuration. A create or an update carries a
// configuration of the caller's choosing, and a source or a republish in it reads every stream of
// the account, since nats-server checks a republish destination only against the stream's own
// subjects.
var streamConfigurationCalls = []string{
	"$JS.API.STREAM.CREATE.>",
	"$JS.API.STREAM.UPDATE.>",
	"$JS.API.STREAM.DELETE.>",
	"$JS.API.STREAM.RESTORE.>",
}

// streamConfigurationEntries are the entries of publish that can reach a stream configuration
// call, by nats-server's own reading of two subjects (server.SubjectsCollide).
func streamConfigurationEntries(publish []string) []string {
	var entries []string
	for _, entry := range publish {
		if slices.ContainsFunc(streamConfigurationCalls, func(call string) bool { return server.SubjectsCollide(entry, call) }) {
			entries = append(entries, entry)
		}
	}
	return entries
}

// No grant the auth service answers can make, change or remove a stream: for every grant this
// package builds, ForCallout's answer holds no publish entry that reaches a stream create, update,
// delete or restore. The static sets keep theirs (TestEachGrantMatchesTheSetItReplaces), since a
// hub that runs no auth service renders them and its programs make their own streams.
func TestOnlyTheOperatorCreatesOrChangesAStream(t *testing.T) {
	var offenders []string
	for name, grant := range grantTable(t) {
		for _, entry := range streamConfigurationEntries(natsgrant.ForCallout(grant).Publish) {
			offenders = append(offenders, name+": "+entry)
		}
	}
	slices.Sort(offenders)
	require.Empty(t, offenders, "the auth service's answer may make, change or remove a stream")
}

// ForCallout removes the stream configuration calls and nothing else, and returns lists of its
// own: a bucket's writer keeps its reads, its key writes and its watchers' calls, and the
// measurement job keeps its stream infos and its publishes, losing its stream create, update and
// delete. A wildcard that reaches a stream configuration call goes whole, whatever else it reaches.
func TestForCalloutKeepsEveryOtherCall(t *testing.T) {
	for name, grant := range grantTable(t) {
		answered := natsgrant.ForCallout(grant)
		removed := streamConfigurationEntries(grant.Publish)
		kept := slices.DeleteFunc(slices.Clone(grant.Publish), func(entry string) bool { return slices.Contains(removed, entry) })
		require.Equal(t, kept, answered.Publish, "%s publish", name)
		require.Equal(t, grant.Subscribe, answered.Subscribe, "%s subscribe", name)
		require.Equal(t, grant.PublishDeny, answered.PublishDeny, "%s publish deny", name)
		require.Equal(t, grant.SubscribeDeny, answered.SubscribeDeny, "%s subscribe deny", name)
	}

	require.Equal(t, []string{
		"$JS.API.STREAM.INFO.KV_b",
		"$JS.API.DIRECT.GET.KV_b.>",
		"$JS.API.STREAM.MSG.GET.KV_b",
		"$KV.b.>",
		"$JS.API.CONSUMER.CREATE.KV_b.>",
		"$JS.API.CONSUMER.DELETE.KV_b.>",
		"$JS.FC.KV_b.>",
	}, natsgrant.ForCallout(natsgrant.Permissions{Publish: natsgrant.KVWrite("b")}).Publish)

	require.Equal(t, natsgrant.Permissions{
		Publish: []string{
			"$JS.API.INFO",
			"$JS.API.STREAM.NAMES",
			"$JS.API.STREAM.LIST",
			"$JS.API.STREAM.INFO.*",
			"$JS.API.CONSUMER.NAMES.*",
			"$JS.API.CONSUMER.LIST.*",
			"$JS.API.CONSUMER.INFO.*.*",
			"$JS.API.DIRECT.GET.KV_envoy_listener.$KV.envoy_listener.lease.>",
			"envoy-measure.>",
		},
		Subscribe: []string{"_INBOX_envoy_measure.>"},
	}, natsgrant.ForCallout(natsgrant.Measure()))

	wide := natsgrant.Permissions{
		Publish:     []string{"$JS.API.>", "$JS.API.STREAM.*.X", "$JS.API.STREAM.INFO.X", ">", "$JS.API.STREAM.RESTORE.X"},
		Subscribe:   []string{"_INBOX.>"},
		PublishDeny: []string{"$JS.API.STREAM.PURGE.X"},
	}
	answered := natsgrant.ForCallout(wide)
	require.Equal(t, []string{"$JS.API.STREAM.INFO.X"}, answered.Publish)
	require.Equal(t, []string{"$JS.API.>", "$JS.API.STREAM.*.X", "$JS.API.STREAM.INFO.X", ">", "$JS.API.STREAM.RESTORE.X"}, wide.Publish, "ForCallout changed the grant it was given")
	answered.Subscribe[0], answered.PublishDeny[0] = "changed", "changed"
	require.Equal(t, "_INBOX.>", wide.Subscribe[0], "ForCallout's subscribe list is the grant's own")
	require.Equal(t, "$JS.API.STREAM.PURGE.X", wide.PublishDeny[0], "ForCallout's deny list is the grant's own")
}
