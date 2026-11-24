package buckets

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/webhook"
)

// fakeContainer is a pod's container as the fake Kubernetes API lists it.
type fakeContainer struct {
	Name          string           `json:"name"`
	Command       []string         `json:"command,omitempty"`
	Args          []string         `json:"args,omitempty"`
	Env           []map[string]any `json:"env,omitempty"`
	EnvFrom       []map[string]any `json:"envFrom,omitempty"`
	RestartPolicy string           `json:"restartPolicy,omitempty"`
}

// fakePod is a pod as the fake Kubernetes API lists it: its labels, its containers, init
// containers and ephemeral containers; each container of any kind named in terminated has ended.
type fakePod struct {
	namespace, name string
	labels          map[string]string
	containers      []fakeContainer
	initContainers  []fakeContainer
	ephemeral       []fakeContainer
	terminated      []string
}

// podsJSON is the PodList the API answers for pods.
func podsJSON(t *testing.T, pods ...fakePod) string {
	t.Helper()
	items := []map[string]any{}
	for _, pod := range pods {
		statuses := func(containers []fakeContainer) []map[string]any {
			var statuses []map[string]any
			for _, container := range containers {
				state := map[string]any{"running": map[string]any{}}
				if slices.Contains(pod.terminated, container.Name) {
					state = map[string]any{"terminated": map[string]any{}}
				}
				statuses = append(statuses, map[string]any{"name": container.Name, "state": state})
			}
			return statuses
		}
		spec := map[string]any{"nodeName": "node-a", "containers": pod.containers}
		status := map[string]any{"containerStatuses": statuses(pod.containers)}
		if len(pod.initContainers) > 0 {
			spec["initContainers"], status["initContainerStatuses"] = pod.initContainers, statuses(pod.initContainers)
		}
		if len(pod.ephemeral) > 0 {
			spec["ephemeralContainers"], status["ephemeralContainerStatuses"] = pod.ephemeral, statuses(pod.ephemeral)
		}
		metadata := map[string]any{"name": pod.name, "namespace": pod.namespace, "uid": "uid-" + pod.name}
		if len(pod.labels) > 0 {
			metadata["labels"] = pod.labels
		}
		items = append(items, map[string]any{
			"metadata": metadata,
			"spec":     spec,
			"status":   status,
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

// The writer map names every copied bucket, and each listener pod of the dispatch cluster, as the
// API lists it, writes exactly the buckets that listener opens: bus.CopiedBuckets, the CI state
// bucket left out unless it mounts the GitHub route, as cmd/listener's listenerCopiedBuckets
// builds them. The webhook listener, whose ENVOY_WEBHOOKS names github, writes all five; the
// session listener and Dispatch's listener write four; envoy-dispatch writes the CI state bucket
// alone; together they are bus.CopiedBuckets. A listener container named nothing a listener is
// named, found by its ENVOY_MACHINE_ID alone, writes all five, and a container that is no writer
// writes none.
func TestTheWriterMapNamesEveryCopiedBucket(t *testing.T) {
	opened := func(github bool) []string {
		return slices.DeleteFunc(bus.CopiedBuckets(), func(bucket string) bool { return bucket == bus.CIStateBucket && !github })
	}
	var union []string
	for _, c := range []struct {
		pod  fakePod
		want []string
	}{
		{webhookListener, opened(true)},
		{sessionListener, opened(false)},
		{dispatchListener, opened(false)},
		{dispatchApp, []string{bus.CIStateBucket}},
	} {
		var pods podList
		if err := json.Unmarshal([]byte(podsJSON(t, c.pod)), &pods); err != nil {
			t.Fatalf("decode pod %s: %v", c.pod.name, err)
		}
		got := pods.Items[0].Spec.Containers[0].writes()
		if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(c.want))) {
			t.Errorf("pod %s/%s's container %s writes %v, want %v", c.pod.namespace, c.pod.name, c.pod.containers[0].Name, got, c.want)
		}
		union = append(union, got...)
	}
	slices.Sort(union)
	if union, want := slices.Compact(union), slices.Sorted(slices.Values(bus.CopiedBuckets())); !slices.Equal(union, want) {
		t.Errorf("the dispatch cluster's writers write %v together, want bus.CopiedBuckets %v", union, want)
	}
	var renamed podContainer
	if err := json.Unmarshal([]byte(`{"name":"app","env":[{"name":"ENVOY_MACHINE_ID","value":"x"},{"name":"ENVOY_WEBHOOKS","value":"slack, github"}]}`), &renamed); err != nil {
		t.Fatalf("decode the container: %v", err)
	}
	if got, want := renamed.writes(), opened(true); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Errorf("a listener container named app writes %v, want %v", got, want)
	}
	if got := (podContainer{Name: "advertiser"}).writes(); len(got) != 0 {
		t.Errorf("a container that is no writer writes %v, want none", got)
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

// A --namespace that is no RFC 1123 label, the only name the API gives a namespace, is a usage
// error before anything connects or runs, and the bucket is left as it was. kubectl reads a value
// starting with '-' as a flag of its own: at b25dc642e1, `recreate --kube-context fake --namespace
// --all-namespaces` beside a listener in dispatch read no namespace, listed no pod, and deleted and
// created the bucket, exit 0, as --namespace=-oname did. The control is the same recreate naming
// dispatch: the proof calls kubectl with namespace/dispatch and --namespace=dispatch, which kubectl
// never reads as a flag, and refuses on the listener.
func TestANamespaceThatIsNoLabelIsRefusedBeforeAnyCall(t *testing.T) {
	uri, js := server(t)
	config, _ := bus.CopiedBucketConfig(bus.SessionBucket, 1)
	if _, err := js.CreateKeyValue(&config); err != nil {
		t.Fatalf("create the bucket: %v", err)
	}
	before := created(t, js, bus.SessionBucket)
	pods := map[string]string{"dispatch": podsJSON(t, dispatchListener)}
	recreate := func(w *world, namespaces ...string) (int, string, string) {
		return runCommand(t, w, nil, append([]string{"recreate", "--nats", uri, "--bucket", bus.SessionBucket, "--kube-context", "fake"}, namespaces...)...)
	}
	for _, namespaces := range [][]string{
		{"--namespace", "--all-namespaces"},
		{"--namespace=-oname"},
		{"--namespace", "-A"},
		{"--namespace", "dispatch", "--namespace", "--all-namespaces"},
		{"--namespace", ""},
		{"--namespace", "Dispatch"},
		{"--namespace", "dispatch_ci"},
		{"--namespace", "envoy.dispatch"},
		{"--namespace", "envoy/dispatch"},
		{"--namespace", "dispatch-"},
		{"--namespace", strings.Repeat("a", 64)},
	} {
		t.Run(strings.Join(namespaces, " "), func(t *testing.T) {
			w := &world{podsIn: pods}
			code, stdout, stderr := recreate(w, namespaces...)
			if code != 2 || !strings.Contains(stderr, "is no namespace name: want an RFC 1123 label") || len(w.calls) != 0 {
				t.Errorf("recreate with %q = %d, want the usage error and no call (%v)\n%s%s", namespaces, code, w.calls, stdout, stderr)
			}
			if after := created(t, js, bus.SessionBucket); !after.Equal(before) {
				t.Errorf("recreate with %q changed the bucket: created %s, was %s", namespaces, after, before)
			}
		})
	}
	w := &world{podsIn: pods}
	code, stdout, stderr := recreate(w, "--namespace", "dispatch")
	if want := "pod dispatch/" + dispatchListener.name; code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("control: recreate with --namespace dispatch = %d, want 1 naming %q\n%s%s", code, want, stdout, stderr)
	}
	calls := strings.Join(w.calls, "\n")
	for _, call := range []string{" get namespace/dispatch -o name", " get pods --namespace=dispatch -o json"} {
		if !strings.Contains(calls, call) {
			t.Fatalf("control: the proof never called kubectl%s; calls:\n%s", call, calls)
		}
	}
}

// The writer map reads ENVOY_WEBHOOKS as the listener does: internal/webhook's LoadWebhookConfig,
// which mounts the GitHub route, splits it at commas and trims each entry with strings.TrimSpace.
// Each value the listener reads as naming github makes a listener container a writer of the CI
// state bucket, and each it reads as naming none leaves that bucket out. At b25dc642e1 the map
// trimmed spaces alone, and missed 'slack,github\n', 'slack,\tgithub' and the like.
func TestTheWriterMapReadsEnvoyWebhooksAsTheListenerDoes(t *testing.T) {
	for _, name := range []string{"ENVOY_GITHUB_WEBHOOK_SECRET", "ENVOY_REVIEWER_APP_ID", "ENVOY_SLACK_SIGNING_SECRET", "ENVOY_GHOSTWISPR_SIGNING_SECRET"} {
		t.Setenv(name, "set")
	}
	t.Setenv("ENVOY_GITHUB_REPOSITORY_RENAMES", "")
	github := 0
	for _, value := range []string{
		"github", "slack,github\n", "slack,\tgithub", "\tgithub\r\n", " github , slack ", "slack,\vgithub", "\u00a0github", "slack,github,",
		"", "slack", " slack , ghostwispr ", ",", "slack,\t",
	} {
		t.Setenv("ENVOY_WEBHOOKS", value)
		config, err := webhook.LoadWebhookConfig()
		if err != nil {
			t.Fatalf("the listener refuses ENVOY_WEBHOOKS %q: %v", value, err)
		}
		raw, err := json.Marshal(map[string]any{"name": "envoy-listener", "env": []map[string]string{{"name": "ENVOY_WEBHOOKS", "value": value}}})
		if err != nil {
			t.Fatalf("encode the container: %v", err)
		}
		var container podContainer
		if err := json.Unmarshal(raw, &container); err != nil {
			t.Fatalf("decode the container: %v", err)
		}
		listener, mapped := config.GitHub != nil, slices.Contains(container.writes(), bus.CIStateBucket)
		if listener != mapped {
			t.Errorf("ENVOY_WEBHOOKS %q: the listener mounts the GitHub route: %t; the writer map counts the CI state bucket: %t", value, listener, mapped)
		}
		if listener {
			github++
		}
	}
	if github != 8 {
		t.Fatalf("the listener mounts the GitHub route for %d of the values, want 8: the rows no longer test both sides", github)
	}
}

// A listener whose ENVOY_WEBHOOKS names github among white space other than spaces, or through a
// $(VAR) reference Kubernetes fills in from another variable before the listener reads it, blocks
// the CI state bucket. At b25dc642e1 each passed, and the CI state bucket was recreated beside the
// GitHub route. The control is the same listener with slack alone, which does not block it.
func TestAListenerWhoseWebhooksMayNameGitHubBlocksTheCIStateBucket(t *testing.T) {
	listener := func(vars ...map[string]any) fakePod {
		return fakePod{namespace: "envoy", name: "envoy-listener-0", containers: []fakeContainer{
			{Name: "envoy-listener", Env: append([]map[string]any{{"name": "ENVOY_MACHINE_ID", "value": "gke"}}, vars...)}}}
	}
	routes := map[string]any{"name": "ROUTES", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "routes", "key": "webhooks"}}}
	for _, c := range []struct {
		name   string
		pod    fakePod
		blocks bool
	}{
		{"a newline after github", listener(map[string]any{"name": "ENVOY_WEBHOOKS", "value": "slack,github\n"}), true},
		{"a tab before github", listener(map[string]any{"name": "ENVOY_WEBHOOKS", "value": "slack,\tgithub"}), true},
		{"a reference to a Secret's variable", listener(routes, map[string]any{"name": "ENVOY_WEBHOOKS", "value": "$(ROUTES)"}), true},
		{"control: slack alone, then a tab", listener(map[string]any{"name": "ENVOY_WEBHOOKS", "value": "slack,\t"}), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := &world{podsIn: map[string]string{"envoy": podsJSON(t, c.pod)}}
			code, stdout, stderr := recreateIn(t, w, bus.CIStateBucket, "envoy")
			if want := "runs container envoy-listener, which writes " + bus.CIStateBucket; c.blocks && (code != 1 || !strings.Contains(stderr, want)) {
				t.Errorf("recreate of %s beside %s = %d, want 1 naming %q\n%s%s", bus.CIStateBucket, c.name, code, want, stdout, stderr)
			}
			if !c.blocks && code != 0 {
				t.Errorf("recreate of %s beside %s = %d, want 0\n%s%s", bus.CIStateBucket, c.name, code, stdout, stderr)
			}
		})
	}
}

// A container whose envFrom may set a variable the writer map reads counts as a writer of each
// bucket that variable could select, since the pod shows neither the Secret's or ConfigMap's keys
// nor their values: a container named app, with no command, whose envFrom source has no prefix
// may be a listener (ENVOY_MACHINE_ID) with the GitHub route (ENVOY_WEBHOOKS), so it blocks the
// known-session bucket and the CI state bucket; a listener whose envFrom may set ENVOY_WEBHOOKS
// blocks the CI state bucket; a prefix that starts the variables' names (ENVOY_) may set them too.
// Each blocker names the variables. At b25dc642e1 the map never read envFrom, and each of these
// recreated the bucket. The controls do not block: a prefix that starts neither name (DB_), a
// listener whose env sets ENVOY_WEBHOOKS, which takes precedence over envFrom, and the app with no
// envFrom.
func TestAContainerWhoseEnvFromMaySetTheMapsVariablesBlocksWhatTheyCouldSelect(t *testing.T) {
	source := func(prefix string) []map[string]any {
		return []map[string]any{{"prefix": prefix, "secretRef": map[string]any{"name": "app-env"}}}
	}
	configMap := []map[string]any{{"configMapRef": map[string]any{"name": "listener-env"}}}
	pod := func(container fakeContainer) fakePod {
		return fakePod{namespace: "envoy", name: "pod-" + container.Name, containers: []fakeContainer{container}}
	}
	both := "(its envFrom may set ENVOY_MACHINE_ID and ENVOY_WEBHOOKS, which the pod does not show)"
	for _, c := range []struct {
		name           string
		pod            fakePod
		blocks, passes []string
		note           string
	}{
		{"an app whose envFrom has no prefix", pod(fakeContainer{Name: "app", EnvFrom: source("")}),
			[]string{bus.KnownSessionBucket, bus.CIStateBucket}, nil, both},
		{"a listener whose envFrom may set ENVOY_WEBHOOKS", pod(fakeContainer{Name: "listener", Env: env("ENVOY_MACHINE_ID", "envoy-hub"), EnvFrom: configMap}),
			[]string{bus.CIStateBucket}, nil, "(its envFrom may set ENVOY_WEBHOOKS, which the pod does not show)"},
		{"an app whose envFrom prefix is ENVOY_", pod(fakeContainer{Name: "app", EnvFrom: source("ENVOY_")}),
			[]string{bus.KnownSessionBucket}, nil, both},
		{"control: an app whose envFrom prefix is DB_", pod(fakeContainer{Name: "app", EnvFrom: source("DB_")}),
			nil, []string{bus.KnownSessionBucket, bus.CIStateBucket}, ""},
		{"control: a listener whose env sets ENVOY_WEBHOOKS", pod(fakeContainer{Name: "listener", Env: env("ENVOY_MACHINE_ID", "envoy-hub", "ENVOY_WEBHOOKS", "slack"), EnvFrom: configMap}),
			nil, []string{bus.CIStateBucket}, ""},
		{"control: an app with no envFrom", pod(fakeContainer{Name: "app"}),
			nil, []string{bus.KnownSessionBucket, bus.CIStateBucket}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := &world{podsIn: map[string]string{"envoy": podsJSON(t, c.pod)}}
			for _, bucket := range c.blocks {
				code, stdout, stderr := recreateIn(t, w, bucket, "envoy")
				if want := "runs container " + c.pod.containers[0].Name + ", which writes " + bucket + " " + c.note; code != 1 || !strings.Contains(stderr, want) {
					t.Errorf("recreate of %s beside %s = %d, want 1 naming %q\n%s%s", bucket, c.name, code, want, stdout, stderr)
				}
			}
			for _, bucket := range c.passes {
				if code, stdout, stderr := recreateIn(t, w, bucket, "envoy"); code != 0 {
					t.Errorf("recreate of %s beside %s = %d, want 0\n%s%s", bucket, c.name, code, stdout, stderr)
				}
			}
		})
	}
}

// A writer started through a launcher blocks what it may write, its blocker saying why. A container
// whose command's first word is not one of the two writer binaries - a shell (sh -c or bash -c, its
// script in the command or in args), env, tini --, dumb-init, or the image's entrypoint when the
// container sets args alone - and whose later words name envoy-listener may set ENVOY_MACHINE_ID and
// ENVOY_WEBHOOKS where the pod does not show them, so it writes every copied bucket, the CI state
// bucket among them. One whose later words name envoy-dispatch writes the CI state bucket, beside
// the listener buckets when its env makes it a listener. At 65a488fe53 the map read the command's
// first word alone, so each of these mapped to no bucket and the recreate went on; at 02facfa07d it
// read shells alone, so env, tini, dumb-init and the entrypoint still mapped to none, and a listener
// by its env whose shell named envoy-dispatch wrote the CI state bucket alone.
//
// A launcher's program can come from its environment: a shell's $PROGRAM or ${PROGRAM}, and a
// $(PROGRAM) reference the kubelet fills in from the container's env before it starts the
// container, in the command or in args. The writer map reads each such variable the pod's env sets
// with its value: [$(PROGRAM)] with PROGRAM=envoy-dispatch runs envoy-dispatch, and a shell running
// "$PROGRAM" with PROGRAM=/usr/local/bin/envoy-dispatch names it. One whose value the pod does not
// show - from a Secret, or one an envFrom source may set - may start any writer, so it writes every
// copied bucket, the blocker naming the expansion; so does a shell exporting ENVOY_MACHINE_ID, the
// variable every listener needs, for a listener its env names. At 69cde74210 each of these mapped
// to no bucket, or to envoy-dispatch's alone, and the recreate of a bucket it writes went on, the
// proof saying no pod runs a writer of it.
//
// The controls do not block a bucket they do not write: a launcher or a shell running another
// program, envoy-dispatch whose flags name the listener, which is no launcher, a launcher whose
// variable the pod's env sets to another program, a shell expanding only variables its pod does not
// set (its own, and TMPDIR), and a shell expanding its pod's own name, from a field of the pod.
// launcherRow is one pod a launcher-reading test runs: its labels (for a fieldRef that reads one),
// its container, the buckets the container writes, each bucket it blocks with the note its blocker
// ends with, and the buckets a recreate passes beside it.
type launcherRow struct {
	name      string
	labels    map[string]string
	container fakeContainer
	writes    []string
	blocks    map[string]string
	passes    []string
}

// launcherListenerNote, launcherDispatchNote and launcherUnreadNote are the notes a blocker of a
// launcher-started writer ends with (launchNote).
func launcherListenerNote(named, through string) string {
	return " (its command or args name " + named + ", run through " + through + ", which may set ENVOY_MACHINE_ID and ENVOY_WEBHOOKS where the pod does not show them)"
}

func launcherDispatchNote(named, through string) string {
	return " (its command or args name " + named + ", run through " + through + ")"
}

func launcherUnreadNote(expansion string) string {
	return ` (its command or args expand "` + expansion + `", whose value the pod does not show, so it may start any writer)`
}

// runLauncherRows runs each row as a subtest: the container writes exactly row.writes, a recreate of
// each bucket it blocks is refused with code 1, the blocker's note, and no `deleted` line, and a
// recreate of each bucket it passes exits 0.
func runLauncherRows(t *testing.T, rows []launcherRow) {
	t.Helper()
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			pod := fakePod{namespace: "envoy", name: "pod-launched", labels: c.labels, containers: []fakeContainer{c.container}}
			var pods podList
			if err := json.Unmarshal([]byte(podsJSON(t, pod)), &pods); err != nil {
				t.Fatalf("decode the pod: %v", err)
			}
			container := pods.Items[0].Spec.Containers[0]
			container.pod = &pods.Items[0]
			if got := container.writes(); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(c.writes))) {
				t.Errorf("the container writes %v, want %v", got, c.writes)
			}
			w := &world{podsIn: map[string]string{"envoy": podsJSON(t, pod)}}
			for _, bucket := range slices.Sorted(maps.Keys(c.blocks)) {
				code, stdout, stderr := recreateIn(t, w, bucket, "envoy")
				if want := "runs container app, which writes " + bucket + c.blocks[bucket] + "\n"; code != 1 || !strings.Contains(stderr, want) || strings.Contains(stdout, "deleted") {
					t.Errorf("recreate of %s beside %s = %d, want 1 naming %q and nothing deleted\n%s%s", bucket, c.name, code, want, stdout, stderr)
				}
			}
			for _, bucket := range c.passes {
				if code, stdout, stderr := recreateIn(t, w, bucket, "envoy"); code != 0 {
					t.Errorf("recreate of %s beside %s = %d, want 0\n%s%s", bucket, c.name, code, stdout, stderr)
				}
			}
		})
	}
}

func TestAWriterStartedThroughALauncherBlocksWhatItWrites(t *testing.T) {
	const script = "ENVOY_MACHINE_ID=x exec envoy-listener"
	both := func(note string) map[string]string {
		return map[string]string{bus.KnownSessionBucket: note, bus.CIStateBucket: note}
	}
	listenerBlocks := func(through string) map[string]string { return both(launcherListenerNote("envoy-listener", through)) }
	fromSecret := map[string]any{"name": "PROGRAM", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "launch", "key": "program"}}}
	neither := []string{bus.KnownSessionBucket, bus.CIStateBucket}
	runLauncherRows(t, []launcherRow{
		{name: "sh -c", container: fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c", script}},
			writes: bus.CopiedBuckets(), blocks: listenerBlocks(`"sh"`)},
		{name: "bash -c", container: fakeContainer{Name: "app", Command: []string{"bash", "-c", script}},
			writes: bus.CopiedBuckets(), blocks: listenerBlocks(`"bash"`)},
		{name: "sh -c with its script in args", container: fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c"}, Args: []string{script}},
			writes: bus.CopiedBuckets(), blocks: listenerBlocks(`"sh"`)},
		{name: "env", container: fakeContainer{Name: "app", Command: []string{"/usr/bin/env", "ENVOY_MACHINE_ID=x", "envoy-listener"}},
			writes: bus.CopiedBuckets(), blocks: listenerBlocks(`"env"`)},
		{name: "tini --", container: fakeContainer{Name: "app", Command: []string{"tini", "--", "/usr/local/bin/envoy-listener"}},
			writes: bus.CopiedBuckets(), blocks: listenerBlocks(`"tini"`)},
		{name: "dumb-init", container: fakeContainer{Name: "app", Command: []string{"/usr/bin/dumb-init", "envoy-listener"}},
			writes: bus.CopiedBuckets(), blocks: listenerBlocks(`"dumb-init"`)},
		{name: "args alone, under the image's entrypoint", container: fakeContainer{Name: "app", Args: []string{"envoy-listener"}},
			writes: bus.CopiedBuckets(), blocks: listenerBlocks("the image's entrypoint")},
		{name: "sh -c running envoy-dispatch", container: fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c", "exec /usr/local/bin/envoy-dispatch"}},
			writes: []string{bus.CIStateBucket}, blocks: map[string]string{bus.CIStateBucket: launcherDispatchNote("envoy-dispatch", `"sh"`)}, passes: []string{bus.KnownSessionBucket}},
		{name: "tini -- envoy-dispatch", container: fakeContainer{Name: "app", Command: []string{"tini", "--", "envoy-dispatch"}},
			writes: []string{bus.CIStateBucket}, blocks: map[string]string{bus.CIStateBucket: launcherDispatchNote("envoy-dispatch", `"tini"`)}, passes: []string{bus.KnownSessionBucket}},
		{name: "a listener by its env whose shell names envoy-dispatch", container: fakeContainer{Name: "app", Env: env("ENVOY_MACHINE_ID", "x"),
			Command: []string{"/bin/sh", "-c", "exec /app/start --dispatch=http://envoy-dispatch:8080"}},
			writes: bus.CopiedBuckets(), blocks: map[string]string{bus.KnownSessionBucket: "", bus.CIStateBucket: launcherDispatchNote("envoy-dispatch", `"sh"`)}},
		{name: `a shell running "$PROGRAM", its env setting PROGRAM to envoy-dispatch`, container: fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c", `exec "$PROGRAM"`},
			Env: env("PROGRAM", "/usr/local/bin/envoy-dispatch")},
			writes: []string{bus.CIStateBucket}, blocks: map[string]string{bus.CIStateBucket: launcherDispatchNote(`envoy-dispatch through "$PROGRAM", which the pod's env sets`, `"sh"`)}, passes: []string{bus.KnownSessionBucket}},
		{name: "a shell running ${PROGRAM}, its env setting PROGRAM from a Secret", container: fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c", "exec ${PROGRAM} --serve"},
			Env: []map[string]any{fromSecret}},
			writes: bus.CopiedBuckets(), blocks: both(launcherUnreadNote("${PROGRAM}"))},
		{name: "a shell exporting ENVOY_MACHINE_ID and running a listener its env names", container: fakeContainer{Name: "app",
			Command: []string{"/bin/sh", "-c", `export ENVOY_MACHINE_ID=gke ENVOY_WEBHOOKS=github; exec "$LISTENER"`}, Env: env("LISTENER", "/usr/local/bin/envoy-listener")},
			writes: bus.CopiedBuckets(), blocks: both(launcherListenerNote("ENVOY_MACHINE_ID", `"sh"`))},
		{name: "[$(PROGRAM)], its env setting PROGRAM to envoy-dispatch", container: fakeContainer{Name: "app", Command: []string{"$(PROGRAM)"}, Env: env("PROGRAM", "/usr/local/bin/envoy-dispatch")},
			writes: []string{bus.CIStateBucket}, blocks: map[string]string{bus.CIStateBucket: ""}, passes: []string{bus.KnownSessionBucket}},
		{name: "[tini, --] with args [$(PROGRAM)], its env setting PROGRAM to envoy-listener", container: fakeContainer{Name: "app", Command: []string{"tini", "--"}, Args: []string{"$(PROGRAM)"},
			Env: env("PROGRAM", "/usr/local/bin/envoy-listener")},
			writes: bus.CopiedBuckets(), blocks: listenerBlocks(`"tini"`)},
		{name: "[$(PROGRAM)], its env setting PROGRAM from a Secret", container: fakeContainer{Name: "app", Command: []string{"$(PROGRAM)"}, Env: []map[string]any{fromSecret}},
			writes: bus.CopiedBuckets(), blocks: both(launcherUnreadNote("$(PROGRAM)"))},
		{name: "args [$(APP_PROGRAM)] under the image's entrypoint, an envFrom source of prefix APP_ setting it", container: fakeContainer{Name: "app", Args: []string{"$(APP_PROGRAM)"},
			EnvFrom: []map[string]any{{"prefix": "APP_", "configMapRef": map[string]any{"name": "launch"}}}},
			writes: bus.CopiedBuckets(), blocks: both(launcherUnreadNote("$(APP_PROGRAM)"))},
		{name: "control: a launcher running another program", container: fakeContainer{Name: "app", Command: []string{"tini", "--", "nats-server", "--config", "/etc/nats/nats.conf"}},
			passes: neither},
		{name: "control: a shell whose script names neither", container: fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c", "exec nats-server --config /etc/nats/nats.conf"}},
			passes: neither},
		{name: "control: envoy-dispatch whose flags name the listener", container: fakeContainer{Name: "app", Command: []string{"/usr/local/bin/envoy-dispatch", "--listener-url=http://envoy-listener:8080"}},
			writes: []string{bus.CIStateBucket}, blocks: map[string]string{bus.CIStateBucket: ""}, passes: []string{bus.KnownSessionBucket}},
		{name: `control: a shell running "$PROGRAM", its env setting PROGRAM to another program`, container: fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c", `exec "$PROGRAM"`},
			Env: env("PROGRAM", "/usr/local/bin/nats-server")},
			passes: neither},
		{name: "control: [tini, --] with args [$(PROGRAM)], its env setting PROGRAM to another program", container: fakeContainer{Name: "app", Command: []string{"tini", "--"}, Args: []string{"$(PROGRAM)"},
			Env: env("PROGRAM", "nats-server")},
			passes: neither},
		{name: "control: a shell expanding only variables its pod does not set", container: fakeContainer{Name: "app",
			Command: []string{"/bin/sh", "-c", `err=${TMPDIR:-/tmp}/run.err; for f in /etc/nats/*.conf; do test -r "$f"; done; exec nats-server 2>"$err"`}},
			passes: neither},
		{name: "control: a shell expanding its pod's own name", container: fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c", `exec nats-server --name "$POD"`},
			Env: []map[string]any{{"name": "POD", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}}}},
			passes: neither},
	})
}

// A writer's name a launcher's shell puts together blocks what that writer writes, as a name the
// pod shows whole does. The shell joins each word with the values it expands before it runs the
// program, so the writer map reads the words as the shell does (readShell): each $NAME, ${NAME}
// and ${NAME...} becomes the value the pod's env gives NAME (a literal or a field of the pod) or the
// value the script last assigned it as a literal, $1 to $9 the words after a -c script, an unset
// name's ${NAME:-word} its word, and quotes and backslashes go, read again until nothing changes.
// So [/bin/sh, -c, 'exec "envoy-${ROLE}"'] with ROLE=dispatch, from the env or from a label, runs
// envoy-dispatch, and so does a script that builds it, PROGRAM=envoy-; PROGRAM=${PROGRAM}dispatch,
// each assignment read with the values before it. Each row runs recreate of a bucket the writer
// writes, which must refuse naming the pod. At 6928405d47 the map matched each word and each
// value alone, so each of these mapped to no bucket, and recreate deleted and created
// envoy_ci_state beside a running envoy-dispatch, the proof saying no pod runs a writer of it. An
// envFrom source makes a variable of a name one of its keys can form ([-._a-zA-Z0-9]+), so the
// kubelet's $(hostname -f), which no key forms, is a command the shell runs: at 6928405d47 a
// source of prefix host made it an unread variable, and the container a writer of every bucket.
//
// The controls: a script that assigns envoy-dispatch whole is read as one naming it, and the same
// shapes selecting another program (nats-server, envoy-hub) write nothing.
func TestAWriterNamedByTheShellsExpansionsBlocksWhatItWrites(t *testing.T) {
	shell := func(script string, positional ...string) []string {
		return append([]string{"/bin/sh", "-c", script}, positional...)
	}
	const composed = " as the shell reads them"
	dispatchBlocks := func(note string) map[string]string { return map[string]string{bus.CIStateBucket: note} }
	fromLabel := []map[string]any{{"name": "ROLE", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.labels['role']"}}}}
	dispatchOnly := []string{bus.CIStateBucket}
	sessionsOnly := []string{bus.KnownSessionBucket}
	neither := []string{bus.KnownSessionBucket, bus.CIStateBucket}
	runLauncherRows(t, []launcherRow{
		{name: `exec "envoy-${ROLE}", its env setting ROLE to dispatch`,
			container: fakeContainer{Name: "app", Command: shell(`exec "envoy-${ROLE}"`), Env: env("ROLE", "dispatch")},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch"+composed, `"sh"`)), passes: sessionsOnly},
		{name: `exec "envoy-${ROLE}", ROLE from the pod's label role=dispatch`, labels: map[string]string{"role": "dispatch"},
			container: fakeContainer{Name: "app", Command: shell(`exec "envoy-${ROLE}"`), Env: fromLabel},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch"+composed, `"sh"`)), passes: sessionsOnly},
		{name: `PROGRAM=envoy-; PROGRAM=${PROGRAM}dispatch; exec "$PROGRAM"`,
			container: fakeContainer{Name: "app", Command: shell(`PROGRAM=envoy-; PROGRAM=${PROGRAM}dispatch; exec "$PROGRAM"`)},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch"+composed, `"sh"`)), passes: sessionsOnly},
		{name: "exec /usr/local/bin/envoy-$ROLE, its env setting ROLE to dispatch",
			container: fakeContainer{Name: "app", Command: shell("exec /usr/local/bin/envoy-$ROLE"), Env: env("ROLE", "dispatch")},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch"+composed, `"sh"`)), passes: sessionsOnly},
		{name: `exec "${DIR}/envoy-${ROLE}", its env setting DIR and ROLE`,
			container: fakeContainer{Name: "app", Command: shell(`exec "${DIR}/envoy-${ROLE}"`), Env: env("DIR", "/usr/local/bin", "ROLE", "dispatch")},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch"+composed, `"sh"`)), passes: sessionsOnly},
		{name: `exec "/usr/local/bin/envoy-$1", run as sh -c ... sh dispatch`,
			container: fakeContainer{Name: "app", Command: shell(`exec "/usr/local/bin/envoy-$1"`, "sh", "dispatch")},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch"+composed, `"sh"`)), passes: sessionsOnly},
		{name: `exec /usr/local/bin/envoy-"dispatch"`,
			container: fakeContainer{Name: "app", Command: shell(`exec /usr/local/bin/envoy-"dispatch"`)},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch"+composed, `"sh"`)), passes: sessionsOnly},
		{name: "exec /usr/local/bin/envoy-${ROLE:-dispatch}, ROLE unset by the pod",
			container: fakeContainer{Name: "app", Command: shell("exec /usr/local/bin/envoy-${ROLE:-dispatch}")},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch"+composed, `"sh"`)), passes: sessionsOnly},
		{name: "env ROLE=dispatch sh -c 'exec envoy-$ROLE'",
			container: fakeContainer{Name: "app", Command: []string{"/usr/bin/env", "ROLE=dispatch", "sh", "-c", "exec envoy-$ROLE"}},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch"+composed, `"env"`)), passes: sessionsOnly},
		{name: `r='listener'; export DIR=/usr/local/bin; exec "$DIR/envoy-$r"`,
			container: fakeContainer{Name: "app", Command: shell(`r='listener'; export DIR=/usr/local/bin; exec "$DIR/envoy-$r"`)},
			writes:    bus.CopiedBuckets(), blocks: map[string]string{bus.KnownSessionBucket: launcherListenerNote("envoy-listener"+composed, `"sh"`), bus.CIStateBucket: launcherListenerNote("envoy-listener"+composed, `"sh"`)}},
		{name: "a shell running $(hostname -f) beside an envFrom source of prefix host",
			container: fakeContainer{Name: "app", Command: shell(`exec nats-server --name "$(hostname -f)"`), EnvFrom: []map[string]any{{"prefix": "host", "configMapRef": map[string]any{"name": "nats"}}}},
			passes:    neither},
		{name: "control: PROGRAM=envoy-dispatch whole",
			container: fakeContainer{Name: "app", Command: shell(`PROGRAM=envoy-dispatch; exec "$PROGRAM"`)},
			writes:    dispatchOnly, blocks: dispatchBlocks(launcherDispatchNote("envoy-dispatch", `"sh"`)), passes: sessionsOnly},
		{name: `control: PROGRAM=nats-; PROGRAM=${PROGRAM}server; exec "$PROGRAM"`,
			container: fakeContainer{Name: "app", Command: shell(`PROGRAM=nats-; PROGRAM=${PROGRAM}server; exec "$PROGRAM"`)},
			passes:    neither},
		{name: `control: exec "envoy-${ROLE}", its env setting ROLE to hub`,
			container: fakeContainer{Name: "app", Command: shell(`exec "envoy-${ROLE}"`), Env: env("ROLE", "hub")},
			passes:    neither},
		{name: `control: exec "$1", run as sh -c ... sh nats-server`,
			container: fakeContainer{Name: "app", Command: shell(`exec "$1"`, "sh", "nats-server")},
			passes:    neither},
	})
}

// The proof keeps the pod's value of a name beside every value the script gives it, and reads an
// expansion against all of them, so a script that only might reassign a name still blocks the bucket
// the pod's value writes (MUST-FIX 1), and a bash operator it does not evaluate blocks every bucket
// (MUST-FIX 2, read as unread, never 'not a writer'). Each row checks the buckets the container
// writes, with the pod's PROGRAM a dispatch writer.
func TestTheProofReadsEveryValueAPodOrScriptGivesAName(t *testing.T) {
	const dispatch = "/usr/local/bin/envoy-dispatch"
	shell := func(script string) []string { return []string{"/bin/sh", "-c", script} }
	for _, c := range []struct {
		name      string
		container fakeContainer
		writes    []string
	}{
		{"a conditional assignment keeps the pod's PROGRAM", fakeContainer{Name: "app", Env: env("PROGRAM", dispatch),
			Command: shell(`[ -n "$CI" ] || PROGRAM=nats-server; exec "$PROGRAM"`)}, []string{bus.CIStateBucket}},
		{"a prefix assignment on a command keeps the pod's PROGRAM", fakeContainer{Name: "app", Env: env("PROGRAM", dispatch),
			Command: shell(`PROGRAM=/bin/true /opt/init.sh; exec "$PROGRAM"`)}, []string{bus.CIStateBucket}},
		{"a case branch keeps the pod's PROGRAM", fakeContainer{Name: "app", Env: env("PROGRAM", dispatch),
			Command: shell(`case "$MODE" in hub) PROGRAM=/usr/local/bin/envoy-hub ;; esac; exec "$PROGRAM"`)}, []string{bus.CIStateBucket}},
		{"a default-if-unset branch keeps the pod's PROGRAM", fakeContainer{Name: "app", Env: env("PROGRAM", dispatch),
			Command: shell(`if [ -z "$PROGRAM" ]; then PROGRAM=nats-server; fi; exec "$PROGRAM"`)}, []string{bus.CIStateBucket}},
		{"a bash substring operator blocks every bucket", fakeContainer{Name: "app", Env: env("P", dispatch),
			Command: shell(`exec "${P:10}"`)}, bus.CopiedBuckets()},
		{"a bash replace operator blocks every bucket", fakeContainer{Name: "app", Env: env("P", dispatch),
			Command: []string{"/bin/bash", "-c", `exec "${P/local/opt}"`}}, bus.CopiedBuckets()},
		{"a name a script joins to a literal before the writer's name blocks", fakeContainer{Name: "app",
			Command: shell(`P=/usr/local/bin/envoy-; exec ${P}dispatch`)}, []string{bus.CIStateBucket}},
		{"a name from a command's output joined to the writer's name blocks", fakeContainer{Name: "app",
			Command: shell(`BIN="$(cat /etc/program)/envoy-"; exec "${BIN}dispatch"`)}, []string{bus.CIStateBucket}},
		{"a control whose pod PROGRAM is another program writes nothing", fakeContainer{Name: "app", Env: env("PROGRAM", "/usr/local/bin/nats-server"),
			Command: shell(`case "$MODE" in hub) PROGRAM=/usr/local/bin/envoy-hub ;; esac; exec "$PROGRAM"`)}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var pods podList
			if err := json.Unmarshal([]byte(podsJSON(t, fakePod{namespace: "envoy", name: "pod-values", containers: []fakeContainer{c.container}})), &pods); err != nil {
				t.Fatalf("decode the pod: %v", err)
			}
			container := pods.Items[0].Spec.Containers[0]
			container.pod = &pods.Items[0]
			if got := container.writes(); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(c.writes))) {
				t.Errorf("the container writes %v, want %v", got, c.writes)
			}
		})
	}
}

// A blocker's launcher is printed quoted (%q): a pod's command can hold any bytes, and whoever may
// create pods in a named namespace could otherwise put terminal control sequences on the
// operator's terminal that hide or forge the lines around it. At 69cde74210 the launcher's base
// name was printed as it is. The control is a launcher of plain letters, printed with its quotes.
func TestABlockersLauncherIsPrintedQuoted(t *testing.T) {
	for _, c := range []struct{ name, launcher, printed string }{
		{"a launcher holding a terminal control sequence", "/bin/sh\x1b[2K\rhidden", `"sh\x1b[2K\rhidden"`},
		{"control: a launcher of plain letters", "/bin/sh", `"sh"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := &world{podsIn: map[string]string{"envoy": podsJSON(t, fakePod{namespace: "envoy", name: "pod-launched", containers: []fakeContainer{
				{Name: "app", Command: []string{c.launcher, "-c", "exec envoy-listener"}}}})}}
			code, stdout, stderr := recreateIn(t, w, bus.SessionBucket, "envoy")
			if want := "run through " + c.printed + ", which may set"; code != 1 || !strings.Contains(stderr, want) || strings.ContainsRune(stderr, '\x1b') {
				t.Errorf("recreate beside a launcher %q = %d, want 1 naming it as %s and no escape byte\n%s%q", c.launcher, code, c.printed, stdout, stderr)
			}
		})
	}
}

// BEST EFFORT - INVERT WHEN THE SERVER-SIDE HOLD LEAVES NO WRITER A CONNECTION. A launcher that
// picks its program from what the pod does not show - a script file, a command's output it runs,
// a variable of the image's own environment, which the pod's env does not set - or that its
// script picks in a way the proof does not read - a variable it gives no literal value (a loop's,
// r=$(cmd)), a glob, bash's ${!REF} - names no writer the proof can read, so recreate goes ahead
// beside it. The hold refuses that writer's connection whatever the pod shows. The controls refuse,
// so the passes come from what the proof cannot read, not from a fake that hides the pod: the
// image-environment row's shell with PROGRAM set to envoy-listener in the pod's own env, and the
// r=$(cmd) row's script with r given the literal listener.
func TestBestEffortProofMissesAProgramALauncherPicksFromWhatThePodDoesNotShowUntilTheServerSideHoldLands(t *testing.T) {
	pod := func(container fakeContainer) map[string]string {
		return map[string]string{"envoy": podsJSON(t, fakePod{namespace: "envoy", name: "pod-launched", containers: []fakeContainer{container}})}
	}
	shell := func(script string) []string { return []string{"/bin/sh", "-c", script} }
	for _, control := range []fakeContainer{
		{Name: "app", Command: shell(`exec "$PROGRAM"`), Env: env("PROGRAM", "/usr/local/bin/envoy-listener")},
		{Name: "app", Command: shell("r=listener; exec /usr/local/bin/envoy-$r")},
	} {
		if code, _, stderr := recreateIn(t, &world{podsIn: pod(control)}, bus.SessionBucket, "envoy"); code != 1 || !strings.Contains(stderr, "pod-launched") {
			t.Fatalf("control: recreate beside %q = %d, want refused naming it:\n%s", control.Command, code, stderr)
		}
	}
	for _, c := range []struct {
		name      string
		container fakeContainer
	}{
		{"a script file", fakeContainer{Name: "app", Command: []string{"/bin/sh", "/opt/start.sh"}}},
		{"a command's output", fakeContainer{Name: "app", Command: shell(`exec "$(cat /etc/program)"`)}},
		{"a variable of the image's environment", fakeContainer{Name: "app", Command: shell(`exec "$PROGRAM"`)}},
		{"a script variable given a command's output", fakeContainer{Name: "app", Command: shell("r=$(cat /etc/role); exec /usr/local/bin/envoy-$r")}},
		{"a loop's variable", fakeContainer{Name: "app", Command: shell("for r in listener; do exec /usr/local/bin/envoy-$r; done")}},
		{"a glob", fakeContainer{Name: "app", Command: shell("exec /usr/local/bin/envoy-liste*")}},
		{"bash's ${!REF}", fakeContainer{Name: "app", Command: []string{"/bin/bash", "-c", `exec "${!REF}"`}, Env: env("REF", "PROGRAM", "PROGRAM", "/usr/local/bin/envoy-listener")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			hidden := &world{podsIn: pod(c.container)}
			if code, stdout, stderr := recreateIn(t, hidden, bus.SessionBucket, "envoy"); code != 0 || !strings.Contains(stdout, "created "+bus.SessionBucket) {
				t.Fatalf("recreate beside a launcher picking its program from %s = %d, want today's best-effort outcome, the bucket recreated:\n%s%s", c.name, code, stdout, stderr)
			}
		})
	}
}

// The pods this repository's components render - the clustered hub's tailnet host and its session
// listener, and Dispatch's front door - write what they run and nothing else: the session listener
// writes the listener buckets, and no tailnet or front-door container writes any, though their
// shell scripts assign and expand their own variables (socket, err), expand the image's (TMPDIR),
// commands' output ($(date -u +%s)) and the pod's own name (HOSTNAME, from metadata.name). So a
// recreate naming the namespaces they run in is not refused for them. The scripts are each
// component's golden render (gcp/testdata). A rule that took every expansion in a launcher's words
// for an unread one would block on each of those scripts: with launch's "not set by the pod" read
// as unread, the tailnet and front-door rows fail. The test fails too when it reads no container,
// or no script that expands a variable, since it would then prove nothing.
func TestTheRenderedPodsWriteOnlyWhatTheyRun(t *testing.T) {
	for file, want := range map[string]map[string][]string{
		"clustered-hub-tailnet-pod":  {},
		"dispatch-tailnet-pod":       {},
		"clustered-hub-listener-pod": {"listener": listenerBuckets},
	} {
		raw, err := os.ReadFile(filepath.Join("..", "gcp", "testdata", file+".golden"))
		if err != nil {
			t.Fatalf("read %s's render: %v", file, err)
		}
		var pod listedPod
		if err := json.Unmarshal(raw, &pod); err != nil {
			t.Fatalf("decode %s's render: %v", file, err)
		}
		read, expanding := 0, 0
		expands := regexp.MustCompile(`\$\{?[A-Za-z_]`)
		for _, container := range slices.Concat(pod.Spec.InitContainers, pod.Spec.Containers) {
			container.pod = &pod
			read++
			if expands.MatchString(strings.Join(slices.Concat(container.Command, container.Args), "\n")) {
				expanding++
			}
			if got := container.writes(); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want[container.Name]))) {
				t.Errorf("%s's container %s writes %v, want %v", file, container.Name, got, want[container.Name])
			}
		}
		if read == 0 || (len(want) == 0 && expanding == 0) {
			t.Fatalf("%s: read %d containers, %d of them a script expanding a variable: the render proves nothing", file, read, expanding)
		}
		t.Logf("%s: %d containers, %d of them a script expanding a variable", file, read, expanding)
	}
}

// A native sidecar - an init container with restartPolicy Always, which runs beside the pod's
// containers - and an ephemeral container each block a bucket they write, named with their kind.
// At b25dc642e1 the proof read a pod's containers alone, and recreated the bucket beside either.
// The control is the same sidecar once terminated, which does not block.
func TestANativeSidecarOrEphemeralListenerContainerBlocks(t *testing.T) {
	withSidecar := fakePod{namespace: "envoy", name: "app-with-sidecar", containers: []fakeContainer{{Name: "app"}},
		initContainers: []fakeContainer{{Name: "listener-sidecar", RestartPolicy: "Always", Env: env("ENVOY_MACHINE_ID", "sidecar")}}}
	debugged := fakePod{namespace: "envoy", name: "app-debugged", containers: []fakeContainer{{Name: "app"}},
		ephemeral: []fakeContainer{{Name: "debug-listener", Command: []string{"/usr/local/bin/envoy-listener"}}}}
	for _, c := range []struct {
		pod  fakePod
		want string
	}{
		{withSidecar, "pod envoy/app-with-sidecar (uid uid-app-with-sidecar, node node-a) runs init container listener-sidecar, which writes " + bus.KnownSessionBucket},
		{debugged, "pod envoy/app-debugged (uid uid-app-debugged, node node-a) runs ephemeral container debug-listener, which writes " + bus.KnownSessionBucket},
	} {
		w := &world{podsIn: map[string]string{"envoy": podsJSON(t, c.pod)}}
		if code, stdout, stderr := recreateIn(t, w, bus.KnownSessionBucket, "envoy"); code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("recreate of %s beside pod %s = %d, want 1 naming %q\n%s%s", bus.KnownSessionBucket, c.pod.name, code, c.want, stdout, stderr)
		}
	}
	ended := withSidecar
	ended.terminated = []string{"listener-sidecar"}
	w := &world{podsIn: map[string]string{"envoy": podsJSON(t, ended)}}
	if code, stdout, stderr := recreateIn(t, w, bus.KnownSessionBucket, "envoy"); code != 0 {
		t.Errorf("control: recreate of %s beside the terminated sidecar = %d, want 0\n%s%s", bus.KnownSessionBucket, code, stdout, stderr)
	}
}

// Every --namespace is read in every --kube-context, as the usage and the flag's help say: one list
// serves every context, and a namespace a context does not hold blocks, naming the context, so
// naming two clusters whose writers run in different namespaces never passes on a namespace left
// unread. The context that holds both namespaces is proven.
func TestEveryNamespaceIsReadInEveryContext(t *testing.T) {
	uri, _ := server(t)
	w := &world{namespaceErr: map[string]error{"west/dispatch": errors.New(`Error from server (NotFound): namespaces "dispatch" not found`)}}
	code, stdout, stderr := runCommand(t, w, nil, "check", "--nats", uri, "--bucket", bus.KnownSessionBucket,
		"--kube-context", "east", "--kube-context", "west", "--namespace", "envoy", "--namespace", "dispatch")
	if code != 1 || !strings.Contains(stderr, "blocked: kubernetes west: namespace dispatch: read it: Error from server (NotFound)") ||
		!strings.Contains(stdout, "proven: kubernetes east: none of the 0 pods in namespace envoy, dispatch") {
		t.Fatalf("check in two contexts, one without dispatch = %d, want 1 blocked on west's dispatch and east proven\n%s%s", code, stdout, stderr)
	}
	calls := strings.Join(w.calls, "\n")
	for _, kube := range []string{"east", "west"} {
		for _, namespace := range []string{"envoy", "dispatch"} {
			if call := "kubectl --context " + kube + " --request-timeout=45s get namespace/" + namespace + " -o name"; !strings.Contains(calls, call) {
				t.Fatalf("the proof never read %q; calls:\n%s", call, calls)
			}
		}
	}
	_, _, usage := runCommand(t, &world{}, nil, "--help")
	_, _, help := runCommand(t, &world{}, nil, "check", "--help")
	if !strings.Contains(usage, "--namespace is read in every --kube-context") || !strings.Contains(help, "read in every --kube-context") {
		t.Fatalf("the usage or the flag's help does not say every --namespace is read in every --kube-context:\n%s\n%s", usage, help)
	}
}
