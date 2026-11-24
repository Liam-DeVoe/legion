package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// releasedAgo leaves the finish of the issue's last release of a pod no claim owns minutes minutes
// ago (unowned_release_fences): the rule releases the issue again only once a claim of it was
// written after that finish, and a test that backdates its claims' writes, so that the pod reads
// quiet, backdates the release before them.
func (r *idleRig) releasedAgo(t *testing.T, minutes int) {
	t.Helper()
	tag, err := r.pool.Exec(context.Background(), `update unowned_release_fences
		set released_at = now() - make_interval(mins => $3) where project = $1 and issue = $2`,
		"legion", r.issue.Key, minutes)
	if err != nil {
		t.Fatalf("backdate the release of %s: %v", r.issue.Key, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("backdated %d releases of %s, want its one", tag.RowsAffected(), r.issue.Key)
	}
}

// The release of a pod no claim owns keeps the Sandbox of a kept volume, Suspended, for the issue's
// next pod (sandbox.Runtime.ReleaseIssue), and it changes no claim, so the issue stands in the
// candidate answer of every later pass. Once that release has deleted the Sandbox's Secrets, it
// records the delete on the Sandbox (legion.dev/secrets-released). Such a Sandbox runs no pod and
// owns no Secret, so there is nothing of it to release: the runtime answers it absent
// (sandbox.Runtime.IssueSandbox), and the pass holds the issue as unowned_sandbox_gone and writes
// no release row for it, where a release row a minute would suspend a Sandbox already Suspended
// for ever. The answer is the real runtime's, read over the test's API. A kept volume's Sandbox
// that is Running, and a Suspended Sandbox with an ephemeral volume, are each answered present and
// released, as every Sandbox was before.
//
// A kept volume's Sandbox that is Suspended and carries no such record is released once: a launch
// that failed after it wrote the Secrets leaves it so, still owning them (suspendFailedLaunch),
// and so does a release that stopped after its deletes and before its record. That release
// deletes the Secrets, each one already gone counted as deleted, keeps the Sandbox with its uid,
// and records the delete, and the next pass holds the issue and writes no row.
func TestAnUnownedReleaseLeavesAKeptSuspendedSandboxAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		sandbox func(name, tree, issue string) map[string]any
		// secrets is whether the Sandbox still owns its seven Secrets.
		secrets bool
		// release is whether the pass releases the pod. keeps is whether that release's teardown
		// keeps the Sandbox, Suspended, deletes its Secrets and records the delete, so the next
		// pass holds it.
		release, keeps bool
	}{
		{"a kept volume's Sandbox, Suspended, that runs no pod and whose Secrets a release deleted", func(name, tree, issue string) map[string]any {
			object := keptIssueSandbox(name, "sandbox-uid", tree, issue, "Suspended")
			object["metadata"].(map[string]any)["annotations"] = map[string]any{secretsReleased: "sandbox-uid"}
			return object
		}, false, false, false},
		{"a kept volume's Sandbox, Suspended, that runs no pod and still owns its Secrets, as a failed launch leaves it", func(name, tree, issue string) map[string]any {
			return keptIssueSandbox(name, "sandbox-uid", tree, issue, "Suspended")
		}, true, true, true},
		{"a kept volume's Sandbox, Suspended, whose release stopped after its Secret deletes and before its record", func(name, tree, issue string) map[string]any {
			return keptIssueSandbox(name, "sandbox-uid", tree, issue, "Suspended")
		}, false, true, true},
		{"a kept volume's Sandbox, Running", func(name, tree, issue string) map[string]any {
			return keptIssueSandbox(name, "sandbox-uid", tree, issue, "Running")
		}, false, true, false},
		{"a Sandbox with an ephemeral volume, Suspended", func(name, tree, issue string) map[string]any {
			object := issueSandbox(name, "sandbox-uid", tree, issue)
			object["spec"].(map[string]any)["operatingMode"] = "Suspended"
			return object
		}, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newIdleRig(t)
			rig.stoppedImplementer(t, false)
			name := sandbox.SandboxName(rig.token)
			api := newIssueSandboxAPI(tc.sandbox(name, rig.issue.Tree, rig.issue.Key))
			if tc.secrets {
				for _, secret := range issueSandboxSecrets(name) {
					api.objects["secrets"][secret] = ownedSecret(secret, name, "sandbox-uid")
				}
			}
			rt := issueSandboxRuntime(t, api, rig.st)
			rig.w.idleRuntime = rt
			rig.sup.deps.Runtime = &sandboxTeardowns{releaseRuntime: rig.rt, sandboxes: rt}

			// held fails the test unless a pass over the issue holds it as unowned_sandbox_gone and
			// writes no row.
			held := func(t *testing.T, why string) {
				t.Helper()
				var hold workflow.IdleHold
				var marked bool
				queued := rig.queuedBy(t, func() { hold, marked = rig.w.releaseUnownedPod(ctx, rig.issue.Key) })
				if hold != workflow.IdleHoldUnownedSandboxGone || marked {
					t.Fatalf("the pass = %s, released %t; want the pod held as %s: %s", hold, marked, workflow.IdleHoldUnownedSandboxGone, why)
				}
				if len(queued) != 0 {
					t.Fatalf("queued rows = %v, want none: %s", queued, why)
				}
				if rig.marked(t) {
					t.Fatalf("a release was marked for a kept volume's Sandbox that runs no pod: %s", why)
				}
			}
			if !tc.release {
				held(t, "the Sandbox runs no pod and owns no Secret")
				if records := rig.records(t); len(records.Releases) != 0 || len(records.Holds) != 0 {
					t.Fatalf("records = %+v / %+v, want neither", records.Releases, records.Holds)
				}
				return
			}
			var hold workflow.IdleHold
			var marked bool
			queued := rig.queuedBy(t, func() { hold, marked = rig.w.releaseUnownedPod(ctx, rig.issue.Key) })
			if !marked || len(queued) != 1 || queued[0] != "issue_release" {
				t.Fatalf("the pass = %s, released %t, queued %v; want the release's teardown queued", hold, marked, queued)
			}
			if !tc.keeps {
				return
			}

			rig.drain(t)
			if rig.marked(t) {
				t.Fatal("the release left its mark behind, so its teardown never finished")
			}
			if got := api.mode(name); got != "Suspended" {
				t.Fatalf("the kept volume's Sandbox after the release is %s, want it kept, Suspended", got)
			}
			if got := api.uid(name); got != "sandbox-uid" {
				t.Fatalf("the kept volume's Sandbox after the release has uid %q, want sandbox-uid kept", got)
			}
			if left := heldSecrets(api, name); len(left) != 0 {
				t.Fatalf("the release left the Secrets %v, want each Secret of the Sandbox deleted", left)
			}
			if record := sandboxAnnotation(api, name, secretsReleased); record != "sandbox-uid" {
				t.Fatalf("the Sandbox's %s after the release is %v, want sandbox-uid: the release records that it deleted the Secrets", secretsReleased, record)
			}
			api.requireServed(t)

			testwait.WithinPolling(t, 10*time.Second, 10*time.Millisecond,
				"the runtime to answer the released kept Sandbox absent once the release recorded the deleted Secrets",
				func() bool { _, present := rt.IssueSandbox(rig.issue.Key); return !present })
			held(t, "the release has deleted the Sandbox's Secrets and recorded it")
		})
	}
}

// issueSandboxSecrets are the seven Secrets an issue's Sandbox owns, named as the sandbox runtime
// names them: the provisioning Secret `<sandbox>-boot` (secretName) and the Secret
// `<sandbox>-<role>-boot` of each of the issue's six roles (roleSecretName).
func issueSandboxSecrets(sandbox string) []string {
	secrets := []string{sandbox + "-boot"}
	for _, role := range claim.Roles {
		secrets = append(secrets, sandbox+"-"+string(role)+"-boot")
	}
	return secrets
}

// ownedSecret is the Secret name as the API holds it, owned by the Sandbox sandbox of uid, as the
// sandbox runtime writes an issue Sandbox's Secrets.
func ownedSecret(name, sandbox, uid string) map[string]any {
	return map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": name, "namespace": sandboxNamespace, "uid": "uid-" + name, "resourceVersion": "1",
			"ownerReferences": []any{map[string]any{"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox", "name": sandbox, "uid": uid, "controller": true}}},
		"data": map[string]any{},
	}
}

// secretsReleased is the annotation by which the sandbox runtime's release records, on a kept
// Sandbox, that it deleted the Sandbox's Secrets (runtime/sandbox's secretsReleasedAnnotation).
const secretsReleased = "legion.dev/secrets-released"

// A Sandbox that a release kept carries the release's record that its Secrets are deleted, so the
// release of a pod no claim owns leaves it alone. A launch into it removes the record before it
// writes the first Secret. A launch that then fails after it wrote its Secrets
// (suspendFailedLaunch) leaves the Sandbox Suspended, owning them, with no record. The next pass
// releases it once: one release row, whose teardown deletes the Secrets and only then writes the
// record, and the pass after it writes no row. A release whose record patch is refused after its
// deletes stops there, and its row's next attempt releases the Sandbox again: it finds no Secret
// left, counts each delete as done, writes the record and keeps the uid. Both cases run the real
// Sandbox runtime over the test's API.
func TestAReleasedKeptSandboxWhoseLaunchFailedIsReleasedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// overKeptSandbox is the idle rig whose issue's kept Sandbox the test API holds, Running, with
	// its seven Secrets, behind a recorder of every write, and the real runtime of a deployment
	// whose microVM pods keep their volumes, with the issue's teardowns made by that runtime.
	overKeptSandbox := func(t *testing.T) (*idleRig, *apiWrites, *sandbox.Runtime, string) {
		t.Helper()
		rig := newIdleRig(t)
		rig.stoppedImplementer(t, false)
		name := sandbox.SandboxName(rig.token)
		api := newIssueSandboxAPI(keptIssueSandbox(name, "sandbox-uid", rig.issue.Tree, rig.issue.Key, "Running"))
		for _, secret := range issueSandboxSecrets(name) {
			api.objects["secrets"][secret] = ownedSecret(secret, name, "sandbox-uid")
		}
		writes := &apiWrites{api: api}
		server := httptest.NewServer(writes)
		t.Cleanup(server.Close)
		// Cleanups run last first, so the runtime stops before the server closes: a server closed
		// while the runtime's watches are open waits for them.
		running, stop := context.WithCancel(ctx)
		t.Cleanup(stop)
		opts := issueSandboxOptions(t, rig.st, quietLogger())
		opts.Sandbox = sandbox.SandboxConfig{Type: sandbox.SandboxMicroVM,
			MicroVM: &sandbox.MicroVM{ComputeClass: "example-microvm", StorageClass: "example-microvm-rwo", KeepVolume: true}}
		opts.BootTimeout, opts.TerminationGrace = 300*time.Millisecond, 100*time.Millisecond
		rt, err := sandbox.New(running, sandboxRESTConfig(server.URL), opts)
		if err != nil {
			t.Fatal(err)
		}
		rig.w.idleRuntime = rt
		rig.sup.deps.Runtime = &sandboxTeardowns{releaseRuntime: rig.rt, sandboxes: rt}
		return rig, writes, rt, name
	}
	// releasesOnce fails the test unless a pass over the issue releases its pod with one row.
	releasesOnce := func(t *testing.T, rig *idleRig, why string) {
		t.Helper()
		var marked bool
		queued := rig.queuedBy(t, func() { _, marked = rig.w.releaseUnownedPod(ctx, rig.issue.Key) })
		if !marked || !slices.Equal(queued, []string{"issue_release"}) {
			t.Fatalf("the pass released %t and queued %v, want the release's one teardown row: %s", marked, queued, why)
		}
	}
	// answers waits until the runtime answers the issue's Sandbox present, or absent.
	answers := func(t *testing.T, rig *idleRig, rt *sandbox.Runtime, present bool) {
		t.Helper()
		testwait.WithinPolling(t, 10*time.Second, 10*time.Millisecond, fmt.Sprintf("the runtime to answer the issue's Sandbox present %t", present),
			func() bool { _, got := rt.IssueSandbox(rig.issue.Key); return got == present })
	}
	// holds fails the test unless, once the runtime has seen the record, a pass over the issue
	// writes no row.
	holds := func(t *testing.T, rig *idleRig, rt *sandbox.Runtime) {
		t.Helper()
		answers(t, rig, rt, false)
		var hold workflow.IdleHold
		var marked bool
		queued := rig.queuedBy(t, func() { hold, marked = rig.w.releaseUnownedPod(ctx, rig.issue.Key) })
		if hold != workflow.IdleHoldUnownedSandboxGone || marked || len(queued) != 0 {
			t.Fatalf("the pass after the release = %s, released %t, queued %v; want the pod held as %s and no row",
				hold, marked, queued, workflow.IdleHoldUnownedSandboxGone)
		}
	}
	// recordedAfterDeletes fails the test unless writes delete each of the seven Secrets of the
	// Sandbox name, each delete answered status, and then write the release's record, answered OK,
	// once and after the last delete.
	recordedAfterDeletes := func(t *testing.T, writes []apiWrite, name string, status int) {
		t.Helper()
		secrets := issueSandboxSecrets(name)
		deleted, record := 0, -1
		for i, w := range writes {
			switch {
			case w.method == http.MethodDelete && w.resource == "secrets" && slices.Contains(secrets, w.name):
				if record >= 0 {
					t.Fatalf("write %d deletes the Secret %s after write %d wrote the record: %v", i, w.name, record, writes)
				}
				if w.status != status {
					t.Fatalf("the delete of the Secret %s answered %d, want %d", w.name, w.status, status)
				}
				deleted++
			case w.setsRecord(name):
				if record >= 0 || w.status != http.StatusOK {
					t.Fatalf("write %d writes the record (answered %d) after write %d did: %v", i, w.status, record, writes)
				}
				record = i
			}
		}
		if deleted != len(secrets) || record < 0 {
			t.Fatalf("the teardown deleted %d of the %d Secrets and wrote the record at write %d, want every delete and then the record: %v",
				deleted, len(secrets), record, writes)
		}
	}
	// keptReleased fails the test unless the API holds the Sandbox name Suspended, with its uid
	// and the release's record, and none of its Secrets.
	keptReleased := func(t *testing.T, api *issueSandboxAPI, name string) {
		t.Helper()
		if mode, uid := api.mode(name), api.uid(name); mode != "Suspended" || uid != "sandbox-uid" {
			t.Fatalf("the kept Sandbox after the release is %s with uid %q, want it Suspended with sandbox-uid", mode, uid)
		}
		if record := sandboxAnnotation(api, name, secretsReleased); record != "sandbox-uid" {
			t.Fatalf("the Sandbox's %s after the release is %v, want sandbox-uid", secretsReleased, record)
		}
		if left := heldSecrets(api, name); len(left) != 0 {
			t.Fatalf("the release left the Secrets %v, want each Secret of the Sandbox deleted", left)
		}
	}

	t.Run("a launch that failed after it wrote its Secrets", func(t *testing.T) {
		rig, writes, rt, name := overKeptSandbox(t)
		releasesOnce(t, rig, "the kept Sandbox runs and owns its Secrets")
		from := writes.count()
		rig.drain(t)
		if rig.marked(t) {
			t.Fatal("the release left its mark behind, so its teardown never finished")
		}
		recordedAfterDeletes(t, writes.since(from), name, http.StatusOK)
		keptReleased(t, writes.api, name)
		holds(t, rig, rt)

		// The issue's next launch keeps the Sandbox. The test API runs no controller, so the
		// launch fails at its wait for the new pod, once it has written its Secrets and its
		// Running patch, and it sets the Sandbox Suspended again.
		prompt := filepath.Join(t.TempDir(), "role.md")
		if err := os.WriteFile(prompt, []byte("You are the role.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		spec, err := issueLaunchSpecs{prompt: prompt}.SpawnSpec(ctx, supervise.Claim{})
		if err != nil {
			t.Fatal(err)
		}
		spec.Claim, spec.Project, spec.Tree, spec.Issue, spec.Role = rig.token, "legion", rig.issue.Tree, rig.issue.Key, claim.RoleImplementer
		spec.Generation, spec.BootToken = 2, "boot-"+string(rig.token)+"-g2"
		from = writes.count()
		if _, err := rt.Spawn(ctx, spec); err == nil || !strings.Contains(err.Error(), "wait for its new pod") {
			t.Fatalf("the launch = %v, want it to fail at its wait for the new pod", err)
		}
		launch := writes.since(from)
		removed := slices.IndexFunc(launch, func(w apiWrite) bool { return w.removesRecord(name) })
		written := slices.IndexFunc(launch, func(w apiWrite) bool { return w.resource == "secrets" })
		if removed < 0 || written < 0 || removed > written {
			t.Fatalf("the launch removed the record at write %d and wrote the first Secret at write %d of %v; want the record removed first",
				removed, written, launch)
		}
		if mode := writes.api.mode(name); mode != "Suspended" {
			t.Fatalf("the Sandbox after the failed launch is %s, want it Suspended again", mode)
		}
		if record := sandboxAnnotation(writes.api, name, secretsReleased); record != nil {
			t.Fatalf("the Sandbox after the launch carries the record %v, want none", record)
		}
		if held := heldSecrets(writes.api, name); len(held) == 0 {
			t.Fatal("the failed launch left none of the Sandbox's Secrets, want the ones it wrote")
		}

		// A real launch writes its claim (the generation it starts, launching, then failed) after
		// the release finished, and the pod is released again only once that write has gone
		// quiet. The test runs the runtime's launch alone, so it leaves the two times as that
		// would: the release an hour ago, the claim's failure half an hour ago.
		rig.releasedAgo(t, 60)
		rig.stoppedImplementer(t, false)
		answers(t, rig, rt, true)
		releasesOnce(t, rig, "the failed launch left the Sandbox owning its Secrets")
		from = writes.count()
		rig.drain(t)
		if rig.marked(t) {
			t.Fatal("the second release left its mark behind, so its teardown never finished")
		}
		recordedAfterDeletes(t, writes.since(from), name, http.StatusOK)
		keptReleased(t, writes.api, name)
		holds(t, rig, rt)
		writes.api.requireServed(t)
	})

	t.Run("a release that stopped after its Secret deletes and before its record", func(t *testing.T) {
		rig, writes, rt, name := overKeptSandbox(t)
		writes.refuseRecords(1)
		releasesOnce(t, rig, "the kept Sandbox runs and owns its Secrets")
		// The runner's clock stands still, so the row that the refusal returns to the outbox
		// waits out its backoff rather than running again in this drain.
		frozen := time.Now().Add(time.Minute)
		rig.runner.now = func() time.Time { return frozen }
		from := writes.count()
		rig.drain(t)
		if !rig.marked(t) {
			t.Fatal("the release whose record was refused finished, want its row to run again")
		}
		stopped := writes.since(from)
		if refused := slices.IndexFunc(stopped, func(w apiWrite) bool { return w.setsRecord(name) && w.status == http.StatusForbidden }); refused < 0 {
			t.Fatalf("the release wrote %v, want its record refused", stopped)
		}
		if mode, uid := writes.api.mode(name), writes.api.uid(name); mode != "Suspended" || uid != "sandbox-uid" {
			t.Fatalf("the Sandbox after the stopped release is %s with uid %q, want it Suspended with sandbox-uid", mode, uid)
		}
		if record := sandboxAnnotation(writes.api, name, secretsReleased); record != nil {
			t.Fatalf("the stopped release left the record %v, want none", record)
		}
		if left := heldSecrets(writes.api, name); len(left) != 0 {
			t.Fatalf("the stopped release left the Secrets %v, want each one deleted before the record", left)
		}

		rig.runner.now = func() time.Time { return time.Now().Add(time.Hour) }
		from = writes.count()
		rig.drain(t)
		if rig.marked(t) {
			t.Fatal("the release's next attempt left its mark behind, so its teardown never finished")
		}
		recordedAfterDeletes(t, writes.since(from), name, http.StatusNotFound)
		keptReleased(t, writes.api, name)
		holds(t, rig, rt)
		writes.api.requireServed(t)
	})
}

// apiWrites is the test API behind a recorder of every request that writes, in the order the API
// answered them, with the status of each answer. While refused is above zero, it refuses that
// many patches that write the release's record (secretsReleased), as a release that stops after
// its deletes and before its record.
type apiWrites struct {
	api     *issueSandboxAPI
	mu      sync.Mutex
	writes  []apiWrite
	refused int
}

// apiWrite is one write request: its method, its resource and object name (none for a create),
// its body and the status of its answer.
type apiWrite struct {
	method, resource, name, body string
	status                       int
}

// setsRecord is whether w is a patch of the Sandbox name that writes the release's record.
func (w apiWrite) setsRecord(name string) bool {
	return w.method == http.MethodPatch && w.resource == "sandboxes" && w.name == name && strings.Contains(w.body, `"`+secretsReleased+`":"`)
}

// removesRecord is whether w is a patch of the Sandbox name that removes the release's record.
func (w apiWrite) removesRecord(name string) bool {
	return w.method == http.MethodPatch && w.resource == "sandboxes" && w.name == name && strings.Contains(w.body, `"`+secretsReleased+`":null`)
}

func (a *apiWrites) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.api.ServeHTTP(rw, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	resource, name, _ := kubePath(r.URL.Path)
	write := apiWrite{method: r.Method, resource: resource, name: name, body: string(body)}
	a.mu.Lock()
	refuse := a.refused > 0 && write.setsRecord(name)
	if refuse {
		a.refused--
	}
	a.mu.Unlock()
	if refuse {
		write.status = http.StatusForbidden
		kubeStatus(rw, http.StatusForbidden, metav1.StatusReasonForbidden)
	} else {
		answered := &answerStatus{ResponseWriter: rw, status: http.StatusOK}
		a.api.ServeHTTP(answered, r)
		write.status = answered.status
	}
	a.mu.Lock()
	a.writes = append(a.writes, write)
	a.mu.Unlock()
}

// refuseRecords makes the API refuse the next n patches that write the release's record.
func (a *apiWrites) refuseRecords(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refused = n
}

// count is how many writes the API has answered.
func (a *apiWrites) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.writes)
}

// since is every write the API answered after the first from.
func (a *apiWrites) since(from int) []apiWrite {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.writes[from:])
}

// answerStatus is a response writer that keeps the status it was answered with.
type answerStatus struct {
	http.ResponseWriter
	status int
}

func (a *answerStatus) WriteHeader(code int) {
	a.status = code
	a.ResponseWriter.WriteHeader(code)
}

// sandboxAnnotation is the annotation key of the Sandbox name as the API holds it, nil where it
// carries none.
func sandboxAnnotation(api *issueSandboxAPI, name, key string) any {
	meta, _ := api.sandbox(name)["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	return annotations[key]
}

// heldSecrets are the Secrets of the issue Sandbox name (issueSandboxSecrets) that the API holds.
func heldSecrets(api *issueSandboxAPI, name string) []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	var held []string
	for _, secret := range issueSandboxSecrets(name) {
		if _, ok := api.objects["secrets"][secret]; ok {
			held = append(held, secret)
		}
	}
	return held
}
