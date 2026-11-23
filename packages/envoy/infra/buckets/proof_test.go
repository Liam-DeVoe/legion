package buckets

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/bus"
)

// fakeContainer is a pod's container as the fake Kubernetes API lists it.
type fakeContainer struct {
	Name    string           `json:"name"`
	Command []string         `json:"command,omitempty"`
	Env     []map[string]any `json:"env,omitempty"`
}

// fakePod is a pod as the fake Kubernetes API lists it; each container named in terminated has
// ended.
type fakePod struct {
	namespace, name string
	containers      []fakeContainer
	terminated      []string
}

// podsJSON is the PodList the API answers for pods.
func podsJSON(t *testing.T, pods ...fakePod) string {
	t.Helper()
	items := []map[string]any{}
	for _, pod := range pods {
		var statuses []map[string]any
		for _, container := range pod.containers {
			state := map[string]any{"running": map[string]any{}}
			if slices.Contains(pod.terminated, container.Name) {
				state = map[string]any{"terminated": map[string]any{}}
			}
			statuses = append(statuses, map[string]any{"name": container.Name, "state": state})
		}
		items = append(items, map[string]any{
			"metadata": map[string]any{"name": pod.name, "namespace": pod.namespace, "uid": "uid-" + pod.name},
			"spec":     map[string]any{"nodeName": "node-a", "containers": pod.containers},
			"status":   map[string]any{"containerStatuses": statuses},
		})
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatalf("encode the pods: %v", err)
	}
	return string(raw)
}

func env(pairs ...string) []map[string]any {
	var vars []map[string]any
	for i := 0; i+1 < len(pairs); i += 2 {
		vars = append(vars, map[string]any{"name": pairs[i], "value": pairs[i+1]})
	}
	return vars
}

// The pods of the dispatch cluster's namespaces as the API listed them on 2026-11-23, by name,
// containers, command and environment names (the values here are placeholders), and an Actions
// runner of arc-systems, whose container is named listener.
var (
	webhookListener = fakePod{namespace: "envoy", name: "envoy-listener-76b45db96f-86qg8", containers: []fakeContainer{
		{Name: "envoy-listener", Env: env("ENVOY_MACHINE_ID", "gke", "NATS_URLS", "nats://hub:4222", "ENVOY_WEBHOOKS", "github,slack")}}}
	sessionListener = fakePod{namespace: "envoy", name: "envoy-nats-listener-65f6b7d67d-f5d7p", containers: []fakeContainer{
		{Name: "listener", Env: env("ENVOY_MACHINE_ID", "envoy-hub", "NATS_URLS", "nats://hub:4222")}}}
	hubServer = fakePod{namespace: "envoy", name: "envoy-nats-0", containers: []fakeContainer{
		{Name: "nats"}, {Name: "reloader"}, {Name: "prom-exporter"}}}
	hubTailnet = fakePod{namespace: "envoy", name: "envoy-nats-tailnet-655b788889-5g4nw", containers: []fakeContainer{
		{Name: "tailscale"}, {Name: "advertiser"}}}
	dispatchListener = fakePod{namespace: "dispatch", name: "dispatch-listener-5f96c5869d-dfs4l", containers: []fakeContainer{
		{Name: "envoy-listener", Env: env("ENVOY_MACHINE_ID", "dispatch", "NATS_URLS", "nats://hub:4222")}}}
	dispatchApp = fakePod{namespace: "dispatch", name: "dispatch-567ddd8688-wm8r6", containers: []fakeContainer{
		{Name: "envoy-dispatch", Command: []string{"/usr/local/bin/envoy-dispatch"}, Env: env("DATABASE_URL", "postgres://", "NATS_URLS", "nats://hub:4222")}}}
	actionsRunner = fakePod{namespace: "arc-systems", name: "arc-runner-set-754b578d-listener", containers: []fakeContainer{
		{Name: "listener", Command: []string{"/ghalistener"}}}}
)

// recreateIn runs recreate of bucket with the proof of context prod in namespaces, on a server of
// its own, against w.
func recreateIn(t *testing.T, w *world, bucket string, namespaces ...string) (int, string, string) {
	t.Helper()
	uri, _ := server(t)
	args := []string{"recreate", "--nats", uri, "--bucket", bucket, "--kube-context", "prod"}
	for _, namespace := range namespaces {
		args = append(args, "--namespace", namespace)
	}
	return runCommand(t, w, nil, args...)
}

// The arc-systems Actions runner's container named listener does not block: the proof reads the
// pods of the namespaces it is given alone, and sends arc-systems no request. The control names
// arc-systems too, and the same pod then blocks, so the pass came from the namespaces read, not a
// fake that hides the pod. At b20f45e30a (#4296's head) the proof listed every namespace's pods
// and blocked on any container named listener, this runner's included.
func TestAnActionsRunnersListenerContainerDoesNotBlock(t *testing.T) {
	w := &world{podsIn: map[string]string{
		"arc-systems": podsJSON(t, actionsRunner),
		"envoy":       podsJSON(t, hubServer, hubTailnet),
		"dispatch":    noPods,
	}}
	code, stdout, stderr := recreateIn(t, w, bus.KnownSessionBucket, "envoy", "dispatch")
	if code != 0 || !strings.Contains(stdout, "proven: kubernetes prod: none of the 2 pods in namespace envoy, dispatch runs a writer of "+bus.KnownSessionBucket) {
		t.Fatalf("recreate beside the runner = %d, want 0 with the namespaces proven\n%s%s", code, stdout, stderr)
	}
	if calls := strings.Join(w.calls, "\n"); strings.Contains(calls, "arc-systems") {
		t.Fatalf("the proof read arc-systems, a namespace it was not given:\n%s", calls)
	}
	w.calls = nil
	code, stdout, stderr = recreateIn(t, w, bus.KnownSessionBucket, "envoy", "dispatch", "arc-systems")
	if want := "pod arc-systems/" + actionsRunner.name + " (uid uid-" + actionsRunner.name + ", node node-a) runs container listener"; code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("control: recreate with arc-systems named = %d, want 1 naming %q\n%s%s", code, want, stdout, stderr)
	}
}

// An envoy-listener pod blocks each bucket it writes: the webhook listener, which mounts the
// GitHub route, blocks the known-session bucket and the CI state bucket, naming its pod, its
// container and the bucket. So does a listener whose ENVOY_WEBHOOKS comes from a Secret, whose
// routes the pod does not show.
func TestAnEnvoyListenerPodBlocksABucketItWrites(t *testing.T) {
	w := &world{podsIn: map[string]string{"envoy": podsJSON(t, webhookListener, hubServer)}}
	for _, bucket := range []string{bus.KnownSessionBucket, bus.CIStateBucket} {
		code, stdout, stderr := recreateIn(t, w, bucket, "envoy")
		want := "pod envoy/" + webhookListener.name + " (uid uid-" + webhookListener.name + ", node node-a) runs container envoy-listener, which writes " + bucket
		if code != 1 || !strings.Contains(stderr, want) {
			t.Fatalf("recreate of %s beside the webhook listener = %d, want 1 naming %q\n%s%s", bucket, code, want, stdout, stderr)
		}
	}
	secret := fakePod{namespace: "envoy", name: "listener-from-secret", containers: []fakeContainer{{Name: "envoy-listener", Env: []map[string]any{
		{"name": "ENVOY_MACHINE_ID", "value": "gke"},
		{"name": "ENVOY_WEBHOOKS", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "routes", "key": "webhooks"}}},
	}}}}
	w = &world{podsIn: map[string]string{"envoy": podsJSON(t, secret)}}
	if code, stdout, stderr := recreateIn(t, w, bus.CIStateBucket, "envoy"); code != 1 || !strings.Contains(stderr, "which writes "+bus.CIStateBucket) {
		t.Fatalf("recreate of the CI state bucket beside a listener whose routes come from a Secret = %d, want 1\n%s%s", code, stdout, stderr)
	}
}

// A pod that does not write the named bucket does not block it: a listener with no GitHub route
// (the session listener, Dispatch's listener) does not write the CI state bucket, envoy-dispatch
// writes only that one, so it does not block the known-session bucket, and a listener container
// that has terminated writes nothing. Each control is the same pod against a bucket it does write,
// which blocks.
func TestAPodThatDoesNotWriteTheNamedBucketDoesNotBlockIt(t *testing.T) {
	ended := sessionListener
	ended.terminated = []string{"listener"}
	for _, c := range []struct {
		name            string
		pods            []fakePod
		bucket, control string
	}{
		{"listeners with no GitHub route", []fakePod{sessionListener, dispatchListener}, bus.CIStateBucket, bus.KnownSessionBucket},
		{"envoy-dispatch", []fakePod{dispatchApp}, bus.KnownSessionBucket, bus.CIStateBucket},
		{"a terminated listener", []fakePod{ended, hubTailnet}, bus.InterestBucket, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := &world{pods: podsJSON(t, c.pods...)}
			if code, stdout, stderr := recreateIn(t, w, c.bucket, "envoy"); code != 0 {
				t.Fatalf("recreate of %s beside %s = %d, want 0\n%s%s", c.bucket, c.name, code, stdout, stderr)
			}
			if c.control == "" {
				return
			}
			if code, stdout, stderr := recreateIn(t, w, c.control, "envoy"); code != 1 || !strings.Contains(stderr, "which writes "+c.control) {
				t.Fatalf("control: recreate of %s beside %s = %d, want 1\n%s%s", c.control, c.name, code, stdout, stderr)
			}
		})
	}
}

// Against the pods of the dispatch cluster as listed on 2026-11-23, a recreate of the known-session
// bucket blocks on the three listeners alone - the webhook listener, the session listener and
// Dispatch's listener - and not on envoy-dispatch, the hub's servers, its tailnet hosts or the
// Actions runner of a namespace not named.
func TestTheDispatchClustersPodsBlockTheKnownSessionBucketOnItsListenersAlone(t *testing.T) {
	w := &world{podsIn: map[string]string{
		"envoy":       podsJSON(t, webhookListener, sessionListener, hubServer, hubTailnet),
		"dispatch":    podsJSON(t, dispatchListener, dispatchApp),
		"arc-systems": podsJSON(t, actionsRunner),
	}}
	code, stdout, stderr := recreateIn(t, w, bus.KnownSessionBucket, "envoy", "dispatch")
	var blocked []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "  blocked: kubernetes prod: pod ") {
			blocked = append(blocked, strings.Fields(line)[4])
		}
	}
	want := []string{"envoy/" + webhookListener.name, "envoy/" + sessionListener.name, "dispatch/" + dispatchListener.name}
	if code != 1 || !slices.Equal(blocked, want) {
		t.Fatalf("recreate of %s on the cluster's pods = %d blocked on %v, want 1 blocked on %v\n%s%s", bus.KnownSessionBucket, code, blocked, want, stdout, stderr)
	}
}

// The writer map names every copied bucket: the buckets every listener writes and the CI state
// bucket, which a listener with the GitHub route and envoy-dispatch write, are bus.CopiedBuckets,
// so a copied bucket added to the bus without a writer here fails. A listener container named
// nothing a listener is named, found by its ENVOY_MACHINE_ID alone, writes them.
func TestTheWriterMapNamesEveryCopiedBucket(t *testing.T) {
	mapped := append(slices.Clone(listenerBuckets), bus.CIStateBucket)
	if got, want := slices.Sorted(slices.Values(mapped)), slices.Sorted(slices.Values(bus.CopiedBuckets())); !slices.Equal(got, want) {
		t.Fatalf("the writer map names %v, bus.CopiedBuckets %v", got, want)
	}
	var renamed podContainer
	if err := json.Unmarshal([]byte(`{"name":"app","env":[{"name":"ENVOY_MACHINE_ID","value":"x"},{"name":"ENVOY_WEBHOOKS","value":"slack, github"}]}`), &renamed); err != nil {
		t.Fatalf("decode the container: %v", err)
	}
	if got := renamed.writes(); !slices.Equal(got, mapped) {
		t.Fatalf("a listener container named app writes %v, want %v", got, mapped)
	}
	if got := (podContainer{Name: "advertiser"}).writes(); len(got) != 0 {
		t.Fatalf("a container that is no writer writes %v, want none", got)
	}
}

// --kube-context needs each --namespace the proof reads, and --namespace needs its context: each
// alone is a usage error, before anything connects or runs.
func TestAClusterIsNamedWithItsNamespaces(t *testing.T) {
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"--kube-context", "prod"}, "--kube-context needs each --namespace"},
		{[]string{"--host", "box-1", "--namespace", "envoy"}, "--namespace names a namespace of a --kube-context"},
	} {
		w := &world{}
		code, _, stderr := runCommand(t, w, nil, append([]string{"check", "--nats", "nats://127.0.0.1:1"}, c.args...)...)
		if code != 2 || !strings.Contains(stderr, c.says) || len(w.calls) != 0 {
			t.Errorf("check %v = %d, want the usage error %q and no call (%v)\n%s", c.args, code, c.says, w.calls, stderr)
		}
	}
}

// BEST EFFORT - INVERT WHEN THE SERVER-SIDE HOLD LEAVES NO WRITER A CONNECTION. A
// pod deleted with --force is gone from the API while its containers may still run on its node,
// and the proof reads the API alone (GKE Autopilot refuses the node's runtime list), so recreate
// goes ahead. The hold refuses that writer's connection whatever the API lists. The same pod while
// the API lists it refuses, so the pass is not vacuous.
func TestBestEffortProofMissesAForceDeletedPodUntilTheServerSideHoldLands(t *testing.T) {
	listed := &world{podsIn: map[string]string{"envoy": podsJSON(t, sessionListener)}}
	if code, _, stderr := recreateIn(t, listed, bus.SessionBucket, "envoy"); code != 1 || !strings.Contains(stderr, sessionListener.name) {
		t.Fatalf("control: recreate while the API lists the listener = %d, want refused naming it:\n%s", code, stderr)
	}
	deleted := &world{podsIn: map[string]string{"envoy": noPods}}
	if code, stdout, stderr := recreateIn(t, deleted, bus.SessionBucket, "envoy"); code != 0 || !strings.Contains(stdout, "created "+bus.SessionBucket) {
		t.Fatalf("recreate once the API forgets the force-deleted pod = %d, want today's best-effort outcome, the bucket recreated:\n%s%s", code, stdout, stderr)
	}
}
