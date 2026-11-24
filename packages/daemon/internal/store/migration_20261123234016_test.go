package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// podDisksVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const podDisksVersion = 20261123234016

// 20261123234016 adds issue_pod_disks, the disks of the pods of an issue that a take sent a bundle
// or in which a role of a kept volume started, and changes no table that the release before reads.
// A bundle the release before saved is still the bundle after the migration, and the release
// before's own statements on it still run. The first take of this release that sends the bundle
// records its pod, marked, and a role's start in a pod of a kept volume records that pod with its
// volume. The table's key is the issue and the pod, and it refuses a row with no pod, with no mark,
// or with neither a kept volume nor a mark, which no write makes. The negative control is the table
// itself, which is not there before the migration.
func TestTheDisksOfAnIssuesPodsArriveWith20261123234016(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, podDisksVersion-1)
	exec := func(statement string, args ...any) error {
		t.Helper()
		_, err := store.Pool().Exec(ctx, statement, args...)
		return err
	}
	if err := exec(`insert into issues (key, tree, project, title, phase, generation, status, rank, last_dispatch_seq)
		values ('INFRA-945', 'INFRA-945', 'INFRA', 'root', 'implementing', 1, 'in_progress', 'A', 1)`); err != nil {
		t.Fatalf("record INFRA-945: %v", err)
	}
	at := time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)
	// The release before saves through the statement this release's save still runs
	// (PutWorkspaceBundle), which names no new table.
	if err := store.PutWorkspaceBundle(ctx, "INFRA-945", 11, runtime.WorkspaceBundle{Bundle: []byte("PACK the released work"),
		Head: "f2f134541b30dfa5160a5d1ce5e17b76c967c661", At: at, PodUID: releasedPodUID}); err != nil {
		t.Fatalf("save a bundle before the migration: %v", err)
	}
	const disks = `select issue || '/' || pod_uid || '/' || coalesce(to_char(kept_since at time zone 'UTC', 'HH24:MI:SS'), 'none') || '/' || holds_earlier_work
		from issue_pod_disks order by issue, pod_uid`
	if err := exec(disks); err == nil || !strings.Contains(err.Error(), "issue_pod_disks") {
		t.Fatalf("before the migration a read of the disks of the pods = %v, want the table to be missing", err)
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the disks of the pods: %v", err)
	}
	if got := rowsAsText(t, store, disks); len(got) != 0 {
		t.Fatalf("after the migration the table holds %v, want it empty", got)
	}
	// The release before's take, its own statement (TakeWorkspaceBundleForRestore before this
	// release), spends the attempt of a bundle saved before the migration.
	const releaseBeforesTake = `update workspace_bundles
		set attempt_pod_uid = $2, attempt_at = now(),
			attempt_outcome = case when date_trunc('second', at) <= $3 then 'sent' else 'refused_older' end
		where issue = $1 and attempt_pod_uid is null and source_pod_uid <> $2`
	if err := exec(releaseBeforesTake, "INFRA-945", "pod-of-the-release-before", at.Add(time.Minute)); err != nil {
		t.Fatalf("the release before's take: %v", err)
	}
	const bundles = `select issue || '/' || release_id || '/' || coalesce(attempt_pod_uid, '') || '/' || coalesce(attempt_outcome, '') from workspace_bundles`
	if got, want := rowsAsText(t, store, bundles), []string{"INFRA-945/11/pod-of-the-release-before/sent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the release before's take the bundles are %v, want %v", got, want)
	}
	if err := exec(`delete from workspace_bundles where issue = $1`, "INFRA-945"); err != nil {
		t.Fatalf("the release before's delete of the bundle: %v", err)
	}

	// This release's take of the next release's bundle records the pod it sends it to, and a
	// role's start in a pod of a kept volume records that pod with its volume.
	if err := store.PutWorkspaceBundle(ctx, "INFRA-945", 12, runtime.WorkspaceBundle{Bundle: []byte("PACK the next release's work"),
		Head: "0a8f0aee63b13682fefe975ba0cc51da6f2ba9e9", At: at.Add(time.Hour), PodUID: "pod-of-the-release-before"}); err != nil {
		t.Fatalf("save a bundle after the migration: %v", err)
	}
	if _, take, err := store.TakeWorkspaceBundleForRestore(ctx, bundleProject, "INFRA-945", "pod-of-this-release", at.Add(time.Hour+time.Minute)); err != nil ||
		take != runtime.WorkspaceBundleSend {
		t.Fatalf("this release's take answered %v (%v), want the bundle sent", take, err)
	}
	if _, dropped, err := store.StartKeptPod(ctx, bundleProject, "INFRA-945", "pod-of-a-kept-volume", at.Add(2*time.Hour)); err != nil || dropped {
		t.Fatalf("this release's start in a pod of a kept volume dropped a bundle: %t (%v), want nothing dropped", dropped, err)
	}
	if got, want := rowsAsText(t, store, disks), []string{"INFRA-945/pod-of-a-kept-volume/11:30:00/false", "INFRA-945/pod-of-this-release/none/true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after this release's take and drop the table holds %v, want %v", got, want)
	}

	// One row per issue and pod: the same pod of another issue is a row of its own, the same pair
	// again is refused by the key, and a row with no pod, with no mark, or with neither a kept
	// volume nor a mark is refused. A row of a kept volume with no mark is the one this release's
	// start in a pod of a kept volume wrote above, and a marked row with no kept volume the take's.
	const insert = `insert into issue_pod_disks (issue, pod_uid, kept_since, holds_earlier_work) values ($1, $2, null, $3)`
	if err := exec(insert, "OTHER-8", "pod-of-this-release", true); err != nil {
		t.Fatalf("the same pod of another issue: %v", err)
	}
	marked, unmarked := true, false
	for _, refused := range []struct {
		what  string
		issue string
		pod   string
		mark  *bool
	}{
		{"the same issue and pod again", "INFRA-945", "pod-of-this-release", &marked},
		{"a row with no pod", "INFRA-945", "", &marked},
		{"a row with no mark", "INFRA-945", "pod-of-a-later-release", nil},
		{"a row with neither a kept volume nor a mark", "INFRA-945", "pod-of-a-later-release", &unmarked},
	} {
		if err := exec(insert, refused.issue, refused.pod, refused.mark); err == nil {
			t.Fatalf("%s was written, want it refused", refused.what)
		}
	}
	if got, want := rowsAsText(t, store, disks), []string{"INFRA-945/pod-of-a-kept-volume/11:30:00/false", "INFRA-945/pod-of-this-release/none/true",
		"OTHER-8/pod-of-this-release/none/true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the table holds %v, want %v", got, want)
	}
}
