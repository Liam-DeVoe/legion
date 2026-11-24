package sandbox

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// keptPodRows is the store's record of each pod of the rig's issue, as "<pod>/<kept_since>/<mark>"
// in the order of the pods' uids: kept_since in RFC 3339, "none" for a pod with none.
func keptPodRows(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.Pool().Query(t.Context(), `select pod_uid, kept_since, holds_earlier_work from issue_pod_disks
		where issue = $1 order by pod_uid`, testTree)
	if err != nil {
		t.Fatalf("read the record of the disks of the pods of %s: %v", testTree, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var pod string
		var kept *time.Time
		var marked bool
		if err := rows.Scan(&pod, &kept, &marked); err != nil {
			t.Fatalf("read a row of the record of the disks of the pods of %s: %v", testTree, err)
		}
		since := "none"
		if kept != nil {
			since = kept.UTC().Format(time.RFC3339)
		}
		got = append(got, pod+"/"+since+"/"+map[bool]string{true: "marked", false: "unmarked"}[marked])
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the record of the disks of the pods of %s: %v", testTree, err)
	}
	return got
}

// A role's start in a pod whose Sandbox owns the issue's kept volume records that pod with its
// volume whether or not the issue has a saved bundle (launchers.restoreWorkspace,
// store.StartKeptPod). What the roles of that pod leave unpushed stays on the volume, so the next
// pod of the same Sandbox holds it, and the store marks that next pod for it, while the first pod
// of the volume stays unmarked (store.PodHoldsEarlierWork). No release here saves any work, so a
// launcher that asked the store only where the issue has a bundle would record neither pod, and
// the next pod would read as holding nothing from before it.
func TestAKeptPodsStartRecordsThePodWithNoSavedBundle(t *testing.T) {
	st := bundleStore(t)
	g := newRig(t, nil, withOptions(keptVolumes(SandboxMicroVM)), withOptions(func(o *Options) {
		o.SavedWorkspaceBundle, o.WorkspaceBundleRestored = takeBundle(st), st.DeleteRestoredWorkspaceBundle
		o.KeptPodStart = keptPodStart(st)
	}))
	spec := workerSpec(t)
	first := g.spawn(spec)
	s := g.sandbox(first.Sandbox.Name)
	if s == nil || !ownsKeptVolume(s) {
		t.Fatalf("the issue's Sandbox = %+v, want one that owns the kept volume, or this case proves nothing", s)
	}
	if _, held, err := st.WorkspaceBundleSize(g.ctx, testTree); err != nil || held {
		t.Fatalf("the store holds a bundle for %s = %t (%v), want none, or this case proves nothing", testTree, held, err)
	}

	// The release keeps the Sandbox and its volume, and the issue's next launch brings the next pod
	// of that Sandbox, in which the role starts again.
	if err := g.r.ReleaseIssue(g.ctx, testTree, testTree, authorized); err != nil {
		t.Fatalf("ReleaseIssue: %v", err)
	}
	next := g.relaunchOnNewPod(first, &spec)
	if kept := g.sandbox(next.Sandbox.Name); kept == nil || kept.UID != s.UID {
		t.Fatalf("after the release the issue's Sandbox is %+v, want %s kept", kept, s.UID)
	}
	if _, held, err := st.WorkspaceBundleSize(g.ctx, testTree); err != nil || held {
		t.Fatalf("the store holds a bundle for %s = %t (%v), want none, or this case proves nothing", testTree, held, err)
	}

	for _, pod := range []struct {
		what string
		uid  string
		want bool
	}{
		{"the first pod of the volume", first.Sandbox.PodUID, false},
		{"the next pod of the volume", next.Sandbox.PodUID, true},
	} {
		holds, err := st.PodHoldsEarlierWork(g.ctx, testTree, pod.uid)
		if err != nil || holds != pod.want {
			t.Errorf("the store marks %s, %s, as holding work from before it = %t (%v), want %t; the store records %q",
				pod.what, pod.uid, holds, err, pod.want, keptPodRows(t, st))
		}
	}
}

// A role's start in a pod of a kept volume does not go on while the store cannot record that pod:
// a role that ran there unrecorded could leave work on the volume that the round over the next pod
// of it would name no role for. The store refuses every connection here for the whole of the first
// launch, so the record fails, whether the store's own retries of it end at once or at the bound of
// the role's start. The launch then answers the refusal (runtime.KeptPodUnrecorded), whose sentence
// names the pod and its volume's creation; the launcher was sent no Start, and no Stop either,
// since nothing was issued; and the store holds no row of the pod. Once the store answers again,
// the next launch starts the role and the row is written.
func TestAKeptPodTheStoreCannotRecordStartsNoRoleUntilItCan(t *testing.T) {
	st := bundleStore(t)
	faulted, fault := faultedBundleStore(t, st)
	g := newRig(t, nil, withOptions(keptVolumes(SandboxMicroVM)), withOptions(func(o *Options) {
		o.SavedWorkspaceBundle, o.WorkspaceBundleRestored = takeBundle(st), st.DeleteRestoredWorkspaceBundle
		o.KeptPodStart = keptPodStart(faulted)
	}))
	// The start's bound is the boot timeout here, not the minutes a kept volume's attach may take,
	// so a record the store retries while it refuses ends within seconds.
	g.r.keptVolumeBound = 0
	fault.Refuse(true)

	spec := workerSpec(t)
	g.launcher(spec.Claim)
	_, err := g.r.Spawn(g.ctx, spec)
	name := SandboxName(spec.Claim)
	s, pod := g.sandbox(name), g.pod(name)
	if s == nil || !ownsKeptVolume(s) || pod == nil {
		t.Fatalf("the issue's Sandbox = %+v with pod %+v, want one that owns the kept volume and its pod, or this case proves nothing", s, pod)
	}
	var refused runtime.KeptPodUnrecorded
	if !errors.As(err, &refused) || refused.PodUID != string(pod.UID) || !refused.KeptSince.Equal(s.CreationTimestamp.Time) {
		t.Fatalf("the launch whose pod the store could not record answered %v, want the refusal of pod %s of the kept volume made %s",
			err, pod.UID, s.CreationTimestamp)
	}
	for _, says := range []string{string(pod.UID), s.CreationTimestamp.UTC().Format(time.RFC3339), "could not be recorded for the issue's update round"} {
		if !strings.Contains(err.Error(), says) {
			t.Errorf("the refusal reads %q, which does not say %q", err, says)
		}
	}
	frames := g.sent(spec.Claim)
	if slices.ContainsFunc(frames, func(f shimwire.Frame) bool {
		switch f.(type) {
		case shimwire.LauncherStart, shimwire.LauncherStop:
			return true
		}
		return false
	}) {
		t.Fatalf("the launcher was sent %v, want no Start, and so no Stop", frameTypes(frames))
	}
	if rows := keptPodRows(t, st); len(rows) != 0 {
		t.Fatalf("after the refused launch the store records %q, want no row", rows)
	}

	fault.Refuse(false)
	spec = nextGeneration(spec, spec.Generation+1)
	loc, err := g.r.Spawn(g.ctx, spec)
	if err != nil {
		t.Fatalf("the launch once the store answers again = %v, want the role started", err)
	}
	if frames := g.sent(spec.Claim); !slices.ContainsFunc(frames, func(f shimwire.Frame) bool {
		start, ok := f.(shimwire.LauncherStart)
		return ok && start.Generation == spec.Generation
	}) {
		t.Fatalf("the launcher was sent %v, want the start of generation %d", frameTypes(frames), spec.Generation)
	}
	want := []string{loc.Sandbox.PodUID + "/" + s.CreationTimestamp.UTC().Format(time.RFC3339) + "/unmarked"}
	if rows := keptPodRows(t, st); !slices.Equal(rows, want) {
		t.Fatalf("after the launch the store records %q, want %q", rows, want)
	}
}
