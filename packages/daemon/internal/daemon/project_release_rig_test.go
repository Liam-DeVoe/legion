package daemon

// The project release (dispatch://INFRA-3583 R6) run on the idle rig: the whole daemon on the
// Sandbox runtime over the rig's Kubernetes API, real Postgres and NATS, a real Envoy listener, and
// a scripted role process in each pod, which answers the release's read of its workspace and its
// ask as the rig's plan says (idleRigPlan). The move between two clusters is
// project_move_test.go's, in this same rig.

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// idleRigProjectReleaseWait is how long a rig test waits for a project release to finish: its
// bound, then the stops, the deletes and the pods going in the rig's Kubernetes API.
const idleRigProjectReleaseWait = 3 * time.Minute

// startRelease starts the project release with bound over the operator's route and answers the
// record the daemon answered it with, 202.
func (r *idleWakeRig) startRelease(bound string) api.ProjectReleaseResponse {
	r.t.Helper()
	status, body := r.api.request(http.MethodPost, api.ProjectReleaseRoute, api.ProjectReleaseRequest{Bound: bound}, true)
	if status != http.StatusAccepted {
		r.t.Fatalf("POST %s = %d, want 202; body %s", api.ProjectReleaseRoute, status, body)
	}
	var started api.ProjectReleaseResponse
	if err := json.Unmarshal(body, &started); err != nil {
		r.t.Fatalf("decode the release's record %s: %v", body, err)
	}
	return started
}

// readRelease is the release's GET as the daemon served it, decoded and raw.
func (r *idleWakeRig) readRelease() (api.ProjectReleaseResponse, []byte) {
	r.t.Helper()
	status, body := r.api.request(http.MethodGet, api.ProjectReleaseRoute, nil, true)
	if status != http.StatusOK {
		r.t.Fatalf("GET %s = %d, want 200; body %s", api.ProjectReleaseRoute, status, body)
	}
	var release api.ProjectReleaseResponse
	if err := json.Unmarshal(body, &release); err != nil {
		r.t.Fatalf("decode the release %s: %v", body, err)
	}
	return release, body
}

// awaitReleaseFinished reads the release until it finished, and answers it as its GET served it.
func (r *idleWakeRig) awaitReleaseFinished() (api.ProjectReleaseResponse, []byte) {
	r.t.Helper()
	var release api.ProjectReleaseResponse
	var raw []byte
	idleRigEventually(r.t, idleRigProjectReleaseWait, "the project release finished", func() bool {
		release, raw = r.readRelease()
		return release.FinishedAt != nil
	})
	return release, raw
}

// stateRelease is `projectRelease` of the daemon's state body as the route served it, the body
// `legion state --json` prints (cmd/legion's runState), nil when the body carries none.
func (r *idleWakeRig) stateRelease() any {
	r.t.Helper()
	status, body := r.api.request(http.MethodGet, "/legion/v1/state", nil, false)
	if status != http.StatusOK {
		r.t.Fatalf("GET /legion/v1/state = %d; body %s", status, body)
	}
	var state map[string]any
	if err := json.Unmarshal(body, &state); err != nil {
		r.t.Fatalf("decode the state %s: %v", body, err)
	}
	return state["projectRelease"]
}

// releaseLine is the release's line of the Sandbox name.
func releaseLine(t *testing.T, release api.ProjectReleaseResponse, name string) api.ReleasedSandbox {
	t.Helper()
	for _, line := range release.Result.Sandboxes {
		if line.Name == name {
			return line
		}
	}
	t.Fatalf("the release names no Sandbox %s; it names %+v", name, release.Result.Sandboxes)
	return api.ReleasedSandbox{}
}

// releaseAsks is every message one role of an issue, or the controller (key idleRigControllerPod),
// was given that names a project release's round.
func (r *idleWakeRig) releaseAsks(key string, role claim.Role) []idleRigMessage {
	var asks []idleRigMessage
	for _, message := range r.agents.role(key, string(role)).Messages {
		if idleRigReleaseAsk.MatchString(message.Text) {
			asks = append(asks, message)
		}
	}
	return asks
}

// dispatch://INFRA-3583 R6: the release takes each pod of the project by what its claims and its
// workspace say. A pod whose workspace holds nothing unpushed is read, asked nothing and deleted
// with nothing unpushed. A pod whose workspace holds an unpushed commit is asked once, and deleted
// after its role answered, with the work lost and its answer's time named. A pod no role process
// runs in is deleted with no read. A pod a claim of which an operator suspended is left, with its
// disk. Each claim the release stopped is stored suspended by `project_release`, for the next boot
// to start, and the record names each pod and each such claim with its session.
func TestProjectReleaseTakesEachPodOfTheProject(t *testing.T) {
	rig := newIdleWakeRig(t, "clean", "unpushed", "unread", "held")
	clean, unpushed, unread, held := rig.issue("clean"), rig.issue("unpushed"), rig.issue("unread"), rig.issue("held")
	rig.agents.setPlan(unpushed.key, string(unpushed.role), idleRigPlan{TakeTurn: true,
		Unpushed: &idleRigUnpushed{Commits: 1, WorkingCopyChanged: true}, AnswerRelease: true})
	rig.launchAll()
	// The pod no role process runs in: its one claim is stopped, and its Sandbox stays, which
	// only the tree's cleanup deletes.
	if status, body := rig.api.request(http.MethodPost, "/legion/v1/operator/claims/"+string(rig.token(unread))+"/stop", nil, true); status != http.StatusOK {
		t.Fatalf("stop %s = %d; body %s", rig.token(unread), status, body)
	}
	if status, body := rig.api.request(http.MethodPost, "/legion/v1/operator/claims/"+string(rig.token(held))+"/suspend", nil, true); status/100 != 2 {
		t.Fatalf("suspend %s = %d; body %s", rig.token(held), status, body)
	}
	idleRigEventually(t, idleRigPodWait, "the stopped claim retired and the operator's suspension stored", func() bool {
		return rig.api.claim(rig.token(unread)).State == string(supervise.StateRetired) &&
			rig.api.claim(rig.token(held)).State == string(supervise.StateSuspended)
	})

	started := rig.startRelease("45s")
	release, _ := rig.awaitReleaseFinished()
	if release.ID != started.ID {
		t.Fatalf("the release finished as %d, want the one started, %d", release.ID, started.ID)
	}

	t.Run("the clean pod is deleted with nothing unpushed and asked nothing", func(t *testing.T) {
		line := releaseLine(t, release, clean.pod)
		if line.Outcome != api.ReleaseDeletedClean || line.Commits == nil || *line.Commits != 0 || len(line.Asked) != 0 {
			t.Fatalf("the clean pod's line is %+v, want deleted_clean, no commit and no ask", line)
		}
		if asks := rig.releaseAsks(clean.key, clean.role); len(asks) != 0 {
			t.Fatalf("the clean pod's role was asked %d times, want never", len(asks))
		}
	})

	t.Run("the pod with unpushed work is asked once and deleted after its answer with the work lost", func(t *testing.T) {
		line := releaseLine(t, release, unpushed.pod)
		if line.Outcome != api.ReleaseDeletedLost || line.Commits == nil || *line.Commits != 1 ||
			line.WorkingCopyChanged == nil || !*line.WorkingCopyChanged {
			t.Fatalf("the unpushed pod's line is %+v, want deleted_lost with 1 commit and a changed working copy", line)
		}
		asks := rig.releaseAsks(unpushed.key, unpushed.role)
		if len(asks) != 1 {
			t.Fatalf("the unpushed pod's role was asked %d times, want once; its messages: %+v", len(asks),
				rig.agents.role(unpushed.key, string(unpushed.role)).Messages)
		}
		for _, want := range []string{"release:", "legion/" + unpushed.key, "ready_for_update", "is lost with the pod", release.BoundAt.UTC().Format(time.RFC3339)} {
			if !strings.Contains(asks[0].Text, want) {
				t.Fatalf("the ask %q does not say %q", asks[0].Text, want)
			}
		}
		if len(line.Asked) != 1 || line.Asked[0].Role != unpushed.role || line.Asked[0].AnsweredAt == nil || line.Asked[0].Bound {
			t.Fatalf("the unpushed pod's asked roles are %+v, want its implementer answered before the bound; the role processes said:\n%s",
				line.Asked, idleRigProcessLines(rig.pods.log()))
		}
		if line.Asked[0].AnsweredAt.After(release.BoundAt) {
			t.Fatalf("the answer at %s came after the bound %s", line.Asked[0].AnsweredAt, release.BoundAt)
		}
	})

	t.Run("the pod no role process runs in is deleted with no read", func(t *testing.T) {
		if line := releaseLine(t, release, unread.pod); line.Outcome != api.ReleaseDeletedUnread || line.Commits != nil || line.Refusal != "" {
			t.Fatalf("the unread pod's line is %+v, want deleted_unread with no read", line)
		}
	})

	t.Run("the pod an operator suspended a claim of is left with its disk", func(t *testing.T) {
		if line := releaseLine(t, release, held.pod); line.Outcome != api.ReleaseLeft || !strings.Contains(line.Reason, "operator") {
			t.Fatalf("the held pod's line is %+v, want left for the operator's suspension", line)
		}
		if rig.kube.sandbox(held.pod) == nil {
			t.Fatal("the left pod's Sandbox is gone")
		}
		if asks := rig.releaseAsks(held.key, held.role); len(asks) != 0 {
			t.Fatalf("the left pod's role was asked %d times, want never", len(asks))
		}
	})

	t.Run("each deleted pod's Sandbox is gone", func(t *testing.T) {
		for _, issue := range []idleRigIssue{clean, unpushed, unread} {
			if rig.kube.sandbox(issue.pod) != nil {
				t.Errorf("the Sandbox %s of %s is still there", issue.pod, issue.key)
			}
		}
	})

	t.Run("each stopped claim is stored project_release and named with its session", func(t *testing.T) {
		if len(release.Result.Claims) != 2 {
			t.Fatalf("the release names the claims %+v, want the two it stopped", release.Result.Claims)
		}
		for i, issue := range []idleRigIssue{clean, unpushed} {
			line, stored := release.Result.Claims[i], rig.api.claim(rig.token(issue))
			if line.Token != rig.token(issue) || line.Issue != issue.key || line.Role != issue.role || line.Session == "" ||
				line.Session != stored.Session || line.Ended != string(supervise.SuspendedByProjectRelease) || !line.BootStarts {
				t.Errorf("the claim line %+v, want %s's implementer on session %q, ended project_release, which the boot starts", line, issue.key, stored.Session)
			}
			if stored.State != string(supervise.StateSuspended) || stored.SuspendedBy != string(supervise.SuspendedByProjectRelease) || stored.UpdateRound != "" {
				t.Errorf("the claim of %s is stored %s by %q in round %q, want suspended by project_release in no round",
					issue.key, stored.State, stored.SuspendedBy, stored.UpdateRound)
			}
		}
		if stored := rig.api.claim(rig.token(held)); stored.SuspendedBy != string(supervise.SuspendedByOperator) {
			t.Errorf("the left pod's claim is held by %q, want the operator's", stored.SuspendedBy)
		}
	})

	t.Run("a second POST answers the same release and starts nothing", func(t *testing.T) {
		status, body := rig.api.request(http.MethodPost, api.ProjectReleaseRoute, api.ProjectReleaseRequest{DryRun: true}, true)
		var again api.ProjectReleaseResponse
		if err := json.Unmarshal(body, &again); status != http.StatusOK || err != nil || again.ID != release.ID || again.DryRun {
			t.Fatalf("a second POST = %d %s, want 200 with release %d", status, body, release.ID)
		}
	})
}

// dispatch://INFRA-3583 R7, "legion state --json shows the release and its result": the state
// body carries the release while it runs, with no finish, and once it ended carries the release
// with its result, the same result its own GET answers. A later GET, after the daemon's restart
// on the same database, says a boot ended it, and so does the state.
func TestProjectReleaseShowsInTheStateWithItsResult(t *testing.T) {
	rig := newIdleWakeRig(t, "waiting")
	waiting := rig.issue("waiting")
	// A role that does not answer holds its pod to the bound, which is what keeps the release
	// running while the state is read.
	rig.agents.setPlan(waiting.key, string(waiting.role), idleRigPlan{TakeTurn: true, Unpushed: &idleRigUnpushed{Commits: 2}})
	rig.launchAll()
	if served := rig.stateRelease(); served != nil {
		t.Fatalf("the state before any release carries %v, want no projectRelease", served)
	}

	started := rig.startRelease("10s")
	t.Run("while it runs", func(t *testing.T) {
		served, ok := rig.stateRelease().(map[string]any)
		if !ok {
			t.Fatal("the state of a running release carries no projectRelease")
		}
		if served["id"] != float64(started.ID) || served["finishedAt"] != nil {
			t.Fatalf("the state carries the release %v, want %d running and unfinished", served, started.ID)
		}
		if time.Now().After(started.BoundAt) {
			t.Fatalf("the state was read at %s, after the bound %s: this case read no running release", time.Now().UTC(), started.BoundAt)
		}
	})

	release, raw := rig.awaitReleaseFinished()
	t.Run("after it ends, with the result its GET answers", func(t *testing.T) {
		var read any
		if err := json.Unmarshal(raw, &read); err != nil {
			t.Fatal(err)
		}
		served := rig.stateRelease()
		if !reflect.DeepEqual(served, read) {
			t.Fatalf("the state carries the release\n%v\nwant the release its GET answers\n%v", served, read)
		}
		line := releaseLine(t, release, waiting.pod)
		if line.Outcome != api.ReleaseDeletedLost || line.Commits == nil || *line.Commits != 2 || len(line.Asked) != 1 || !line.Asked[0].Bound {
			t.Fatalf("the release's line is %+v, want deleted_lost with 2 commits, its role asked and the bound passed", line)
		}
	})

	rig.restart()
	t.Run("a later GET after a restart says a boot ended it", func(t *testing.T) {
		ended, _ := rig.readRelease()
		if ended.ID != release.ID || ended.EndedBy != store.ProjectReleaseEndedByBoot || ended.EndedAt == nil || ended.FinishedAt == nil {
			t.Fatalf("the release after the restart reads %+v, want release %d ended by a boot", ended, release.ID)
		}
		served, ok := rig.stateRelease().(map[string]any)
		if !ok || served["endedBy"] != store.ProjectReleaseEndedByBoot {
			t.Fatalf("the state after the restart carries %v, want the release ended by a boot", served)
		}
	})
}

// idleRigProcessLines is every line a rig role process wrote of its own, out of the launchers'
// log, which is where a scripted answer that did not reach the daemon says why.
func idleRigProcessLines(log string) string {
	var said []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "idle rig role process") {
			said = append(said, line)
		}
	}
	return strings.Join(said, "\n")
}
