package writerhold

import (
	"context"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sjawhar/envoy/infra/streams"
	"github.com/sjawhar/envoy/internal/holdrecord"
)

// The census reconciles a daemon store only at the address exactly one source names: a Deployment
// of namespace legion (annotation envoy-infra.consumer-names/project) or, for a project retired after
// the cutover, its entry of ConfigMap envoy-operator/envoy-operator-retired-stores, as
// KubeDaemonDeployments reads them. A store its writer places (changed, gone or already
// reconciled) whose project both sources name, or neither, is left unreconciled, named with each
// source, and the census is UNKNOWN with no done entry, its stop naming each store the writer
// changed. A project its entry alone names has one source and is reconciled beside them. This is a
// check beside the writer's own (consumernames reads a store only where one source names it). Red
// before the check: the census was done (exit 0), with BOTH and NEITHER reconciled. The control is
// the rerun with one source for each project, which is done.
func TestHoldTheCensusReconcilesAStoreOnlyWhereOneSourceNamesIt(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	stub := func(context.Context, *nats.Conn) ([]streams.ConsumerShape, error) { return nil, nil }
	current := h.notificationsID()
	require.NotEmpty(t, current)
	h.daemonDeployments("BOTH")
	retired := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: RetiredStoresConfigMap, Namespace: RetiredStoresNamespace}, Data: map[string]string{
		"BOTH" + RetiredStoreDatabaseSuffix: "daemon_both", "BOTH" + RetiredStoreInstanceSuffix: "proj:region:daemons",
		"RETIRED" + RetiredStoreDatabaseSuffix: "daemon_retired", "RETIRED" + RetiredStoreInstanceSuffix: "proj:region:daemons",
	}}
	configMaps := h.s.Kube.Client.CoreV1().ConfigMaps(RetiredStoresNamespace)
	_, err := configMaps.Create(ctx, retired, metav1.CreateOptions{})
	require.NoError(t, err)
	stores := &fakeStores{ids: map[string]string{"BOTH": "stream-0", "NEITHER": "stream-0", "RETIRED": "stream-0"}}
	args := []string{"--daemon-database", "BOTH=/dev/null", "--daemon-database", "NEITHER=/dev/null", "--daemon-database", "RETIRED=/dev/null"}

	// A streams-census hold set without its steps, and every family's cutover row done in it.
	id := h.s.Prepare(t, holdrecord.WorkflowStreamsCensus)
	h.s.HoldWithoutTheSteps(t, id)
	for _, family := range []string{"session", "daemon/BOTH", "daemon/NEITHER", "daemon/RETIRED"} {
		_, err := h.s.Records.PutCutover(ctx, family, "done", id)
		require.NoError(t, err)
	}

	code, stdout, stderr := h.streamsRunWith(stub, stores.reconcile, append([]string{"census", "--hold", id}, args...)...)
	require.Equal(t, 2, code, "a census of stores not each named by one source: %s%s", stdout, stderr)
	require.Contains(t, stderr, "UNKNOWN")
	both := lineFrom(stderr, "daemon/BOTH: ")
	require.Contains(t, both, "Deployment legion/legion-daemon-both", "the store's sources are not named")
	require.Contains(t, both, "ConfigMap envoy-operator/envoy-operator-retired-stores", "the store's sources are not named")
	require.Contains(t, lineFrom(stderr, "daemon/NEITHER: "), "no source names its address")
	require.NotContains(t, stderr, "daemon/RETIRED: ", "a project its entry alone names was refused")
	require.NotContains(t, h.journalCommands(id), CommandStreamsCensus+" done")
	last := h.journal(id).Ops[len(h.journal(id).Ops)-1]
	require.Equal(t, CommandStreamsCensus+" "+holdrecord.StateStop, last.Command+" "+last.State)
	for _, project := range []string{"BOTH", "NEITHER", "RETIRED"} {
		require.Contains(t, last.Outputs["left"], "stream_id of daemon/"+project+": ", "the stop does not name a store the writer changed")
	}

	// One source each: BOTH's entry gone, so its Deployment alone names it, and a Deployment for
	// NEITHER. The census's rerun is done.
	delete(retired.Data, "BOTH"+RetiredStoreDatabaseSuffix)
	delete(retired.Data, "BOTH"+RetiredStoreInstanceSuffix)
	_, err = configMaps.Update(ctx, retired, metav1.UpdateOptions{})
	require.NoError(t, err)
	h.daemonDeployments("NEITHER")
	code, stdout, stderr = h.streamsRunWith(stub, stores.reconcile, append([]string{"census", "--hold", id}, args...)...)
	require.Zero(t, code, "control: the census of stores each named by one source: %s%s", stdout, stderr)
	for _, project := range []string{"BOTH", "NEITHER", "RETIRED"} {
		require.Contains(t, stdout, "RECONCILED daemon/"+project+": ")
	}
	require.Contains(t, h.journalCommands(id), CommandStreamsCensus+" done")
}

// lineFrom is the text of out from the first mark to the next "; " or line end.
func lineFrom(out, mark string) string {
	_, after, ok := strings.Cut(out, mark)
	if !ok {
		return ""
	}
	end := strings.IndexAny(after, ";\n")
	if end < 0 {
		return after
	}
	return after[:end]
}
