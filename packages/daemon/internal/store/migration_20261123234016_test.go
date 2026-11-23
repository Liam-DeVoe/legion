package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// bundlePodsVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const bundlePodsVersion = 20261123234016

// 20261123234016 adds workspace_bundle_pods, the pods that took an idle release's work, and
// changes no table that the release before reads. A bundle the release before saved is still the
// bundle after the migration, the release before's own statements on it still run, and they
// leave the new table as it was, since that release names it nowhere. The first take of this
// release that sends the bundle records its pod. The table's key is the issue and the pod, and it
// refuses a row with no pod or with no release. The negative control is the table itself, which
// is not there before the migration.
func TestThePodsThatTookAReleasesWorkArriveWith20261123234016(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, bundlePodsVersion-1)
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
	const pods = `select issue || '/' || pod_uid || '/' || release_id from workspace_bundle_pods order by issue, pod_uid`
	if err := exec(pods); err == nil || !strings.Contains(err.Error(), "workspace_bundle_pods") {
		t.Fatalf("before the migration a read of the pods that took a release's work = %v, want the table to be missing", err)
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the pods that took a release's work: %v", err)
	}
	if got := rowsAsText(t, store, pods); len(got) != 0 {
		t.Fatalf("after the migration the table holds %v, want it empty", got)
	}
	// The release before's take, its own statement (TakeWorkspaceBundleForRestore before this
	// release), spends the attempt of a bundle saved before the migration and records no pod.
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
	if got := rowsAsText(t, store, pods); len(got) != 0 {
		t.Fatalf("after the release before's take the table holds %v, want it empty: that release names it nowhere", got)
	}
	if err := exec(`delete from workspace_bundles where issue = $1`, "INFRA-945"); err != nil {
		t.Fatalf("the release before's delete of the bundle: %v", err)
	}

	// This release's take of the next release's bundle records the pod it sends it to.
	if err := store.PutWorkspaceBundle(ctx, "INFRA-945", 12, runtime.WorkspaceBundle{Bundle: []byte("PACK the next release's work"),
		Head: "0a8f0aee63b13682fefe975ba0cc51da6f2ba9e9", At: at.Add(time.Hour), PodUID: "pod-of-the-release-before"}); err != nil {
		t.Fatalf("save a bundle after the migration: %v", err)
	}
	if _, take, err := store.TakeWorkspaceBundleForRestore(ctx, bundleProject, "INFRA-945", "pod-of-this-release", at.Add(time.Hour+time.Minute)); err != nil ||
		take != runtime.WorkspaceBundleSend {
		t.Fatalf("this release's take answered %v (%v), want the bundle sent", take, err)
	}
	if got, want := rowsAsText(t, store, pods), []string{"INFRA-945/pod-of-this-release/12"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after this release's take the table holds %v, want %v", got, want)
	}

	// One row per issue and pod: the same pod of another issue is a row of its own, the same pair
	// again is refused by the key, and a row with no pod or no release is refused.
	const insert = `insert into workspace_bundle_pods (issue, pod_uid, release_id, at) values ($1, $2, $3, now())`
	if err := exec(insert, "OTHER-8", "pod-of-this-release", 21); err != nil {
		t.Fatalf("the same pod of another issue: %v", err)
	}
	for _, refused := range []struct {
		what    string
		issue   string
		pod     string
		release int64
	}{
		{"the same issue and pod again", "INFRA-945", "pod-of-this-release", 13},
		{"a row with no pod", "INFRA-945", "", 13},
		{"a row with no release", "INFRA-945", "pod-of-a-later-release", 0},
	} {
		if err := exec(insert, refused.issue, refused.pod, refused.release); err == nil {
			t.Fatalf("%s was written, want it refused", refused.what)
		}
	}
	if got, want := rowsAsText(t, store, pods), []string{"INFRA-945/pod-of-this-release/12", "OTHER-8/pod-of-this-release/21"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the table holds %v, want %v", got, want)
	}
}
