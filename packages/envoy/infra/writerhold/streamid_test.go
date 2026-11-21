package writerhold

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sjawhar/envoy/infra/streams"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
)

// fakeStores are daemon stores whose StreamIDs the census reconciles, by family daemon/<P>, as
// consumernames.ReconcileDaemonStreamIDs does: it reads ENVOY_NOTIFICATIONS' identity on the
// census's connection and the daemons' Deployments through the listing the census hands it, and
// answers its StreamIDResult: each store gone (gone) and each left unreconciled (unreconciled, and
// each family no --daemon-database entry names); while any is unreconciled it writes nothing, and
// otherwise it writes the identity where a store's differs, a write failing for each store
// failWrite names, which leaves it and each store after it unreconciled.
type fakeStores struct {
	mu           sync.Mutex
	ids          map[string]string
	gone         map[string]GoneStore
	unreconciled map[string]string
	failWrite    map[string]string
	// listed are the names of the Deployments the last call's listing answered.
	listed []string
}

func (f *fakeStores) reconcile(ctx context.Context, nc *nats.Conn, deployments DaemonDeployments, families, daemonDatabases []string) (StreamIDResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	js, err := jetstream.New(nc)
	if err != nil {
		return StreamIDResult{}, err
	}
	s, err := js.Stream(ctx, bus.Stream)
	if err != nil {
		return StreamIDResult{}, err
	}
	id := s.CachedInfo().Config.Metadata[bus.StreamIDMetadataKey]
	if id == "" {
		return StreamIDResult{}, bus.ErrNoStreamID
	}
	if deployments == nil {
		return StreamIDResult{}, errors.New("no listing of the daemons' Deployments")
	}
	listed, err := deployments(ctx)
	if err != nil {
		return StreamIDResult{}, err
	}
	f.listed = nil
	for _, d := range listed {
		f.listed = append(f.listed, d.Name)
	}
	databases := map[string]bool{}
	for _, entry := range daemonDatabases {
		project, _, _ := strings.Cut(entry, "=")
		databases[project] = true
	}
	var projects []string
	for _, family := range families {
		if project, ok := strings.CutPrefix(family, "daemon/"); ok {
			projects = append(projects, project)
		}
	}
	sort.Strings(projects)
	var result StreamIDResult
	var stale []string
	for _, project := range projects {
		why, unreconciled := f.unreconciled[project]
		gone, isGone := f.gone[project]
		switch {
		case !databases[project]:
			result.Unreconciled = append(result.Unreconciled, UnreconciledStore{Project: project, Reason: "no --daemon-database names it"})
		case unreconciled:
			result.Unreconciled = append(result.Unreconciled, UnreconciledStore{Project: project, Reason: why})
		case isGone:
			result.Gone = append(result.Gone, gone)
		case f.ids[project] != id:
			stale = append(stale, project)
		}
	}
	if len(result.Unreconciled) > 0 {
		return result, nil
	}
	for i, project := range stale {
		if why, ok := f.failWrite[project]; ok {
			result.Unreconciled = append(result.Unreconciled, UnreconciledStore{Project: project, Reason: why})
			for _, rest := range stale[i+1:] {
				result.Unreconciled = append(result.Unreconciled, UnreconciledStore{Project: rest, Reason: "not written"})
			}
			return result, nil
		}
		result.Changed = append(result.Changed, StreamIDChange{Project: project, From: f.ids[project], To: id})
		f.ids[project] = id
	}
	return result, nil
}

// snapshot is the stores' ids now.
func (f *fakeStores) snapshot() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.ids {
		out[k] = v
	}
	return out
}

// set runs change on the stores under their lock.
func (f *fakeStores) set(change func(f *fakeStores)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// listedNames are the Deployments the last call's listing answered.
func (f *fakeStores) listedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.listed...)
}

// notificationsID reads ENVOY_NOTIFICATIONS' identity as the operator.
func (h *heldStack) notificationsID() string {
	h.t.Helper()
	nc, err := h.s.ConnectOperator(context.Background())
	require.NoError(h.t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(h.t, err)
	s, err := js.Stream(context.Background(), bus.Stream)
	require.NoError(h.t, err)
	return s.CachedInfo().Config.Metadata[bus.StreamIDMetadataKey]
}

// doneOutputs are command's latest done entry's outputs in d.
func doneOutputs(d holdrecord.Done, command string) map[string]string {
	var outputs map[string]string
	for _, op := range d.Ops {
		if op.Command == command && op.State == "done" {
			outputs = op.Outputs
		}
	}
	return outputs
}

// The census reconciles each daemon store's StreamID with ENVOY_NOTIFICATIONS' identity. After the
// stream is recreated (a new identity), the census has every daemon store's StreamID rewritten,
// prints each change as a CHANGED line and names it in its done entry (stream_id.daemon/<P>). The
// control is a census with no writer, which is UNKNOWN (exit 2), writes no done entry and leaves
// every store at the old identity. Run again on the stream unchanged, the census changes nothing
// and names no store. It fails when a recreated stream leaves a daemon store with the identity of a
// stream that is gone.
func TestHoldTheCensusReconcilesTheDaemonStoresStreamID(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	stub := func(context.Context, *nats.Conn) ([]streams.ConsumerShape, error) { return nil, nil }
	old := h.notificationsID()
	require.NotEmpty(t, old)
	stores := &fakeStores{ids: map[string]string{"A": old, "B": old}}
	families := []string{"--family", "session", "--family", "daemon/A", "--family", "daemon/B",
		"--daemon-database", "A=/dev/null", "--daemon-database", "B=/dev/null"}

	// ENVOY_NOTIFICATIONS recreated, as a hub-restore's snapshot or a streams update that made it
	// again would leave it: deleted under a hold of no steps, then made by `streams update`.
	id := h.s.Prepare(t, holdrecord.WorkflowStreamsUpdate)
	h.s.HoldWithoutTheSteps(t, id)
	nc, err := h.s.ConnectOperator(ctx)
	require.NoError(t, err)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	require.NoError(t, js.DeleteStream(ctx, bus.Stream))
	nc.Close()
	code, _, _ := h.streamsRun(nil, "update", "--hold", id)
	require.Zero(t, code, "the update that makes the stream again")
	code, _, _ = h.run(actionRelease, "--hold", id, "--reason", "the stream is made again")
	require.Zero(t, code)
	current := h.notificationsID()
	require.NotEqual(t, old, current, "the recreated stream kept its identity")
	h.awaitStreamLeaders()

	// The first census sets its hold and stops at its convert check; every family's cutover row is
	// then written done under that hold, so each census after it reaches its reconcile.
	code, _, _ = h.streamsRun(stub, append([]string{"census"}, families...)...)
	require.Equal(t, 1, code, "the census before any family is converted")
	census := h.row().HoldID
	for _, family := range []string{"session", "daemon/A", "daemon/B"} {
		_, err := h.s.Records.PutCutover(ctx, family, "done", census)
		require.NoError(t, err)
	}

	// The control: no writer.
	code, _, stderr := h.streamsRun(stub, append([]string{"census", "--hold", census}, families...)...)
	require.Equal(t, 2, code, "control: a census with no writer of the stores")
	require.Contains(t, stderr, ErrNoStoreWriter.Error())
	require.NotContains(t, h.journalCommands(census), "streams census done")
	require.Equal(t, map[string]string{"A": old, "B": old}, stores.snapshot(), "control: the stores keep the old identity")

	code, stdout, _ := h.streamsRunWith(stub, stores.reconcile, append([]string{"census", "--hold", census}, families...)...)
	require.Zero(t, code, "the census with a writer")
	require.Equal(t, map[string]string{"A": current, "B": current}, stores.snapshot())
	require.Contains(t, stdout, fmt.Sprintf("CHANGED daemon/A: stream_id %q -> %q", old, current))
	require.Contains(t, stdout, fmt.Sprintf("CHANGED daemon/B: stream_id %q -> %q", old, current))
	require.NotContains(t, stdout, "GONE ", "a census with no store gone printed one")
	outputs := doneOutputs(h.journal(census), CommandStreamsCensus)
	require.Contains(t, outputs, "stream_id.daemon/A")
	require.Contains(t, outputs, "stream_id.daemon/B")
	require.Equal(t, fmt.Sprintf("daemon/A: stream_id %q -> %q", old, current), outputs["stream_id.daemon/A"])
	code, _, _ = h.run(actionRelease, "--hold", census, "--reason", "the census is done")
	require.Zero(t, code)
	h.awaitStreamLeaders()

	// The stream unchanged: the next census changes nothing and names no store.
	code, stdout, _ = h.streamsRunWith(stub, stores.reconcile, append([]string{"census"}, families...)...)
	require.Zero(t, code, "the census of an unchanged stream")
	again := h.row().HoldID
	require.NotContains(t, stdout, "CHANGED daemon/")
	for key := range doneOutputs(h.journal(again), CommandStreamsCensus) {
		require.NotContains(t, key, "stream_id.", "the census of an unchanged stream named a store")
	}
}

// The census's word with its writer's typed result (StreamIDResult). A store the writer finds
// gone is printed as `GONE daemon/<P>: database <db> on instance <instance> (the operator's file
// states <instance>): <answer>` and named in the census's done entry as gone.daemon/<P>, beside
// each store it changed, and the census is done. The writer is handed the daemons' Deployments as
// the hold's own Kubernetes client lists them (KubeDaemonDeployments of namespace legion), which
// holds the Deployment the row renders there. A store the writer leaves unreconciled makes the
// census UNKNOWN (exit 2), naming the store and why, with no done entry and a stop that records the
// run unchanged; a store changed before one whose write failed is what that stop records the run
// left, so the hold is not released over it, and the census's rerun is done. Controls: the first
// census, with no store gone, prints no GONE line (TestHoldTheCensusReconcilesTheDaemonStoresStreamID),
// and each assertion here fails against a writerhold that drops the GONE line, ignores an
// unreconciled store, or records the stop of a partial write as unchanged.
func TestHoldTheCensusReportsGoneAndUnreconciledStores(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	stub := func(context.Context, *nats.Conn) ([]streams.ConsumerShape, error) { return nil, nil }
	current := h.notificationsID()
	require.NotEmpty(t, current)
	_, err := h.s.Kube.Client.AppsV1().Deployments(DaemonNamespace).Create(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "legion-daemon-g", Namespace: DaemonNamespace, Annotations: map[string]string{DaemonProjectAnnotation: "G"},
	}}, metav1.CreateOptions{})
	require.NoError(t, err)
	gone := GoneStore{Project: "G", Database: "daemon_g", Instance: "proj:region:daemons", StatedInstance: "proj:region:daemons",
		Answer: `FATAL: database "daemon_g" does not exist (SQLSTATE 3D000)`}
	stores := &fakeStores{ids: map[string]string{"A": "stream-0", "B": current}, gone: map[string]GoneStore{"G": gone}}
	families := []string{"--family", "session", "--family", "daemon/A", "--family", "daemon/B", "--family", "daemon/G",
		"--daemon-database", "A=/dev/null", "--daemon-database", "B=/dev/null", "--daemon-database", "G=/dev/null"}

	// The first census sets its hold and stops at its convert check; every family's cutover row is
	// then written done, so each census after it reaches its reconcile.
	code, _, _ := h.streamsRun(stub, append([]string{"census"}, families...)...)
	require.Equal(t, 1, code, "the census before any family is converted")
	census := h.row().HoldID
	for _, family := range []string{"session", "daemon/A", "daemon/B", "daemon/G"} {
		_, err := h.s.Records.PutCutover(ctx, family, "done", census)
		require.NoError(t, err)
	}

	code, stdout, stderr := h.streamsRunWith(stub, stores.reconcile, append([]string{"census", "--hold", census}, families...)...)
	require.Zero(t, code, "the census beside a gone store: %s", stderr)
	text := "database daemon_g on instance proj:region:daemons (the operator's file states proj:region:daemons): " + gone.Answer
	require.Contains(t, stdout, "GONE daemon/G: "+text+"\n")
	require.Contains(t, stdout, fmt.Sprintf("CHANGED daemon/A: stream_id %q -> %q", "stream-0", current))
	outputs := doneOutputs(h.journal(census), CommandStreamsCensus)
	require.Equal(t, text, outputs["gone.daemon/G"])
	require.Contains(t, outputs, "stream_id.daemon/A")
	require.NotContains(t, outputs, "stream_id.daemon/G", "a gone store's identity was named as written")
	require.Contains(t, stores.listedNames(), "legion-daemon-g", "the writer was not handed the hold's own Deployments")
	code, _, _ = h.run(actionRelease, "--hold", census, "--reason", "the census beside a gone store is done")
	require.Zero(t, code)
	h.awaitStreamLeaders()

	// A store left unreconciled: UNKNOWN, no done entry, a stop recording the run unchanged.
	stores.set(func(f *fakeStores) {
		f.ids["A"] = "stream-0"
		f.unreconciled = map[string]string{"A": `Deployment legion/legion-daemon-a reads database "other"`}
	})
	code, stdout, stderr = h.streamsRunWith(stub, stores.reconcile, append([]string{"census"}, families...)...)
	unknown := h.row().HoldID
	require.Equal(t, 2, code, "a census with a store unreconciled: %s%s", stdout, stderr)
	require.Contains(t, stderr, "UNKNOWN")
	require.Contains(t, stderr, `daemon/A: Deployment legion/legion-daemon-a reads database "other"`)
	require.NotContains(t, h.journalCommands(unknown), CommandStreamsCensus+" done")
	last := h.journal(unknown).Ops[len(h.journal(unknown).Ops)-1]
	require.Equal(t, CommandStreamsCensus+" stop", last.Command+" "+last.State)
	require.Equal(t, holdrecord.LeftUnchanged, last.Outputs["left"])
	require.Equal(t, "stream-0", stores.snapshot()["A"], "a store was written while one was unreconciled")

	// A write that failed after another: the store changed before it is what the stop records.
	stores.set(func(f *fakeStores) {
		f.unreconciled = nil
		f.ids["B"] = "stream-0"
		f.failWrite = map[string]string{"B": "its envoy.sentinel row's write failed: refused"}
	})
	code, stdout, stderr = h.streamsRunWith(stub, stores.reconcile, append([]string{"census", "--hold", unknown}, families...)...)
	require.Equal(t, 2, code, "a census whose write failed after another: %s%s", stdout, stderr)
	require.Contains(t, stdout, fmt.Sprintf("CHANGED daemon/A: stream_id %q -> %q", "stream-0", current))
	require.Contains(t, stderr, "daemon/B: its envoy.sentinel row's write failed: refused")
	last = h.journal(unknown).Ops[len(h.journal(unknown).Ops)-1]
	require.Equal(t, CommandStreamsCensus+" stop", last.Command+" "+last.State)
	require.Contains(t, last.Outputs["left"], fmt.Sprintf("stream_id of daemon/A: stream_id %q -> %q", "stream-0", current),
		"the stop recorded a run that rewrote a store as unchanged")
	code, _, _ = h.run(actionRelease, "--hold", unknown, "--reason", "over a census that rewrote a store and stopped")
	require.NotZero(t, code, "release over a stop that changed a store")

	stores.set(func(f *fakeStores) { f.failWrite = nil })
	code, stdout, stderr = h.streamsRunWith(stub, stores.reconcile, append([]string{"census", "--hold", unknown}, families...)...)
	require.Zero(t, code, "the census's rerun: %s%s", stdout, stderr)
	require.Contains(t, stdout, fmt.Sprintf("CHANGED daemon/B: stream_id %q -> %q", "stream-0", current))
	require.Contains(t, h.journalCommands(unknown), CommandStreamsCensus+" done")
}
