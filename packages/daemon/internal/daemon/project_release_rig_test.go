package daemon

// The project release (dispatch://INFRA-3583 R6) run on the idle rig: the whole daemon on the
// Sandbox runtime over the rig's Kubernetes API, real Postgres and NATS, a real Envoy listener, and
// a scripted role process in each pod, which answers the release's read of its workspace and its
// ask as the rig's plan says (idleRigPlan). The move between two clusters is
// project_move_test.go's, in this same rig.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
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
				line.Session != stored.Session || line.Ended != api.ReleasedClaimEnd(supervise.SuspendedByProjectRelease) || !line.BootStarts {
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
		if !ok || served["endedBy"] != string(store.ProjectReleaseEndedByBoot) {
			t.Fatalf("the state after the restart carries %v, want the release ended by a boot", served)
		}
	})
}

// releaseLineNow is the release's line of the Sandbox name as the record holds it now, and whether
// it holds one yet: the release writes its lines once it has listed the project's Sandboxes.
func releaseLineNow(release api.ProjectReleaseResponse, name string) (api.ReleasedSandbox, bool) {
	for _, line := range release.Result.Sandboxes {
		if line.Name == name {
			return line, true
		}
	}
	return api.ReleasedSandbox{}, false
}

// releaseClaimLine is the release's line of the claim token.
func releaseClaimLine(t *testing.T, release api.ProjectReleaseResponse, token claim.Token) api.ReleasedClaim {
	t.Helper()
	for _, line := range release.Result.Claims {
		if line.Token == token {
			return line
		}
	}
	t.Fatalf("the release names no claim %s; it names %+v", token, release.Result.Claims)
	return api.ReleasedClaim{}
}

// dispatch://INFRA-3583 R6: the release deletes the controller's pod, cutting a turn at the bound
// at the latest. The controller's stop is asked once every issue pod has ended, and a controller
// in a turn then is stopped at the turn's end, which can come later than the stop timeout after
// the stop and still before the bound. The release waits for that turn rather than leave the
// controller's pod, deletes the pod once the stop is stored, and names the controller's claim
// stored suspended by `project_release`, which the next boot starts.
func TestProjectReleaseWaitsForTheControllersTurnToTheBound(t *testing.T) {
	rig := newIdleRigWith(t, idleRigOptions{
		configure:      func(cfg *config.Config) { cfg.ControllerLaunch = config.ControllerLaunchDaemon },
		controllerPlan: &idleRigPlan{StayInTurn: true},
	}, idleRigSeed{name: "clean", roles: []claim.Role{claim.RoleImplementer}})
	clean := rig.issue("clean")
	rig.launchAll()
	controller := claim.ControllerToken(rig.project)
	idleRigEventually(t, idleRigPodWait, "the daemon's controller in the turn its start message began", func() bool {
		c, ok := rig.claimsByToken()[controller]
		return ok && c.State == string(supervise.StateWorking)
	})

	rig.startRelease("2m")
	idleRigEventually(t, idleRigProjectReleaseWait, "the clean pod ended", func() bool {
		release, _ := rig.readRelease()
		line, listed := releaseLineNow(release, clean.pod)
		return listed && line.Outcome != api.ReleaseWaiting
	})
	// The controller's stop was asked as the clean pod ended, and its turn holds the stop. The
	// turn runs on past the stop timeout, well before the bound.
	time.Sleep(rig.cfg.WorkerStopTimeout + 5*time.Second)
	if release, _ := rig.readRelease(); release.FinishedAt != nil {
		t.Fatalf("the release finished while the controller's turn ran before the bound, with the lines %+v", release.Result.Sandboxes)
	}
	if c := rig.claimsByToken()[controller]; c.State != string(supervise.StateWorking) {
		t.Fatalf("the controller stands %s before its turn ended, want working", c.State)
	}
	rig.agents.setPlan(idleRigControllerPod, string(claim.RoleController), idleRigPlan{TakeTurn: true})

	release, _ := rig.awaitReleaseFinished()
	if line := releaseLine(t, release, clean.pod); line.Outcome != api.ReleaseDeletedClean {
		t.Errorf("the clean pod's line is %+v, want deleted_clean", line)
	}
	var line api.ReleasedSandbox
	for _, l := range release.Result.Sandboxes {
		if l.Kind == runtime.ProjectSandboxController {
			line = l
		}
	}
	if line.Outcome != api.ReleaseDeleted {
		t.Fatalf("the controller's line is %+v, want deleted; the release names %+v", line, release.Result.Sandboxes)
	}
	if rig.kube.sandbox(line.Name) != nil {
		t.Errorf("the controller's Sandbox %s is still there", line.Name)
	}
	if release.FinishedAt.After(release.BoundAt) {
		t.Errorf("the release finished at %s, after the bound %s: the controller's turn was cut, not waited for", release.FinishedAt, release.BoundAt)
	}
	if ended := releaseClaimLine(t, release, controller); ended.Ended != api.ReleasedClaimEnd(supervise.SuspendedByProjectRelease) || !ended.BootStarts {
		t.Errorf("the controller's claim line is %+v, want it ended project_release, which the next boot starts", ended)
	}
	if stored := rig.claimsByToken()[controller]; stored.State != string(supervise.StateSuspended) ||
		stored.SuspendedBy != string(supervise.SuspendedByProjectRelease) {
		t.Errorf("the controller is stored %s by %q, want suspended by project_release", stored.State, stored.SuspendedBy)
	}
}

// refuseReleaseStops has the store refuse every write of token's claim as the project release's
// suspension, as a database that does not take that write refuses it, for the rest of the test:
// with a serialization failure (SQLSTATE 40001), which the claim's machine owes as it owes any
// write the database did not take (supervise's writeOwed), its claim suspended in memory.
func (r *idleWakeRig) refuseReleaseStops(token claim.Token) {
	r.t.Helper()
	for _, statement := range []string{
		`create function idle_rig_refuse_release_stop() returns trigger language plpgsql as $$
			begin raise exception 'the store refused the write' using errcode = 'serialization_failure'; end $$`,
		`create trigger idle_rig_refuse_release_stop before insert or update on claims for each row
			when (new.token = '` + string(token) + `' and new.suspended_by = '` + string(supervise.SuspendedByProjectRelease) + `')
			execute function idle_rig_refuse_release_stop()`,
	} {
		if _, err := r.pool.Exec(context.Background(), statement); err != nil {
			r.t.Fatalf("make the store refuse the release's stop of %s: %v", token, err)
		}
	}
}

// dispatch://INFRA-3583 R8 and its plan's decision 8: the release deletes a pod only once each of
// its claims is stored suspended. A stop whose write the database refuses leaves the claim
// suspended in its machine's memory and its row as it was, which the next boot would read as a
// role still running in a pod that is gone. The release reads the row, so it leaves the pod with
// its disk, its line naming how the store holds the claim, and holds the claim for an operator.
func TestProjectReleaseLeavesAPodWhoseStopTheStoreDidNotTake(t *testing.T) {
	rig := newIdleWakeRig(t, "refused")
	refused := rig.issue("refused")
	rig.launchAll()
	token := rig.token(refused)
	rig.refuseReleaseStops(token)

	rig.startRelease("10s")
	release, _ := rig.awaitReleaseFinished()
	line := releaseLine(t, release, refused.pod)
	if line.Outcome != api.ReleaseLeft || !strings.Contains(line.Reason, "the store holds it") {
		t.Fatalf("the pod's line is %+v, want it left for its claim the store does not hold suspended", line)
	}
	if rig.kube.sandbox(refused.pod) == nil {
		t.Fatal("the pod whose claim the store does not hold suspended was deleted")
	}
	if stored := rig.api.claim(token); stored.State != string(supervise.StateSuspended) || stored.SuspendedBy != string(supervise.SuspendedByOperator) {
		t.Errorf("the claim is stored %s by %q, want suspended by an operator", stored.State, stored.SuspendedBy)
	}
	if ended := releaseClaimLine(t, release, token); ended.Ended != api.ReleasedClaimEnd(supervise.SuspendedByOperator) || ended.BootStarts {
		t.Errorf("the claim line is %+v, want it ended operator, which the next boot does not start", ended)
	}
}

// dispatch://INFRA-3583 R6, "the release leaves no round open on any claim": a pod the release
// leaves keeps none of its claims in an update round, the claim an operator suspended before the
// release included, which is no claim the release stops. That pod is the one a pod update round
// waits on past its bound, since the operator's suspension holds it (podupdate.go's
// heldByOperator), and a round left on its claims would replace it once the operator resumes them.
// Each claim of the pod stays held by an operator, so no boot starts it.
func TestProjectReleaseTakesALeftPodsClaimsOutOfTheirUpdateRound(t *testing.T) {
	rig := newIdleRigWith(t, idleRigOptions{orphanSweep: 2 * time.Second},
		idleRigSeed{name: "held", roles: []claim.Role{claim.RoleImplementer, claim.RolePlanner}})
	held := rig.issue("held")
	rig.launchAll()
	implementer, planner := rig.roleToken(held, claim.RoleImplementer), rig.roleToken(held, claim.RolePlanner)
	if !rig.kube.madeByAnOlderDaemon(held.pod, moveOlderImage) {
		t.Fatalf("the pod of %s is not in the cluster to stand for an older daemon's", held.key)
	}
	idleRigEventually(t, idleRigPodWait, "the update round open over both claims of the older daemon's pod", func() bool {
		claims := rig.claimsByToken()
		return claims[implementer].UpdateRound != "" && claims[planner].UpdateRound != ""
	})
	if status, body := rig.api.request(http.MethodPost, "/legion/v1/operator/claims/"+string(planner)+"/suspend", nil, true); status/100 != 2 {
		t.Fatalf("suspend %s = %d; body %s", planner, status, body)
	}
	idleRigEventually(t, idleRigPodWait, "the operator's suspension of the planner stored, in its round", func() bool {
		c := rig.claimsByToken()[planner]
		return c.State == string(supervise.StateSuspended) && c.SuspendedBy == string(supervise.SuspendedByOperator) && c.UpdateRound != ""
	})

	rig.startRelease("10s")
	release, _ := rig.awaitReleaseFinished()
	if line := releaseLine(t, release, held.pod); line.Outcome != api.ReleaseLeft || !strings.Contains(line.Reason, "operator") {
		t.Fatalf("the pod's line is %+v, want it left for the operator's suspension", line)
	}
	if rig.kube.sandbox(held.pod) == nil {
		t.Fatal("the left pod's Sandbox is gone")
	}
	claims := rig.claimsByToken()
	for _, token := range []claim.Token{implementer, planner} {
		c := claims[token]
		if c.UpdateRound != "" || c.UpdateHead != "" {
			t.Errorf("the left pod's claim %s is stored in round %q with head %q, want in no round", token, c.UpdateRound, c.UpdateHead)
		}
		if c.State != string(supervise.StateSuspended) || c.SuspendedBy != string(supervise.SuspendedByOperator) {
			t.Errorf("the left pod's claim %s is stored %s by %q, want suspended by an operator", token, c.State, c.SuspendedBy)
		}
	}
}

// putSandbox puts into the cluster a Sandbox the running daemon has not listed, labelled with
// labels: one whose create the daemon sent just before a project release's mark, and that landed
// after the release listed the project. It is Suspended, as the runtime creates every Sandbox
// before it patches it Running, so it has no pod, and every watcher hears of it.
func (a *issueSandboxAPI) putSandbox(name string, labels map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.uids++
	a.putLocked("sandboxes", name, map[string]any{
		"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox",
		"metadata": map[string]any{"name": name, "namespace": sandboxNamespace, "labels": labels,
			"uid": fmt.Sprintf("%suid-sandboxes-%d", a.prefix, a.uids), "generation": float64(1),
			"creationTimestamp": time.Now().UTC().Format(time.RFC3339)},
		"spec": map[string]any{"operatingMode": "Suspended"},
	})
}

// dispatch://INFRA-3583 R6, "exit 0 means no Sandbox of the project was left": a create the
// daemon sent just before the release's mark can land after the release listed the project. Once
// its issue pods ended, the release lists the project again from the API server, gives each
// Sandbox its first list did not hold a line, and takes it: an issue's pod in which no role
// process runs is deleted unread, a repository cache is deleted, and a Sandbox of a kind the
// release does not know is left, so the command does not exit 0 while it stands.
func TestProjectReleaseTakesTheSandboxesItsFirstListDidNotHold(t *testing.T) {
	rig := newIdleWakeRig(t, "waiting")
	waiting := rig.issue("waiting")
	// A role that does not answer holds its pod to the bound, so the release lists the project
	// again only after the Sandboxes below landed.
	rig.agents.setPlan(waiting.key, string(waiting.role), idleRigPlan{TakeTurn: true, Unpushed: &idleRigUnpushed{Commits: 1}})
	rig.launchAll()
	project := rig.kube.label(waiting.pod, "legion.dev/project")
	lateIssue := rig.cfg.Project + "-99"
	lateToken, err := claim.NewToken(rig.project, lateIssue, claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	late, cache, stray := sandbox.SandboxName(lateToken), "legion-"+project+"-cache-late", "legion-"+project+"-stray"

	rig.startRelease("20s")
	idleRigEventually(t, idleRigRecordWait, "the release's first list of the project", func() bool {
		release, _ := rig.readRelease()
		_, listed := releaseLineNow(release, waiting.pod)
		return listed
	})
	rig.kube.putSandbox(late, map[string]string{"legion.dev/project": project, "legion.dev/issue": lateIssue, "legion.dev/tree": lateIssue})
	rig.kube.putSandbox(cache, map[string]string{"legion.dev/project": project, "legion.dev/cache": "true"})
	rig.kube.putSandbox(stray, map[string]string{"legion.dev/project": project})
	if release, _ := rig.readRelease(); release.FinishedAt != nil {
		t.Fatalf("the release finished before the Sandboxes landed, with the lines %+v: this case reads no second list", release.Result.Sandboxes)
	}

	release, _ := rig.awaitReleaseFinished()
	if line := releaseLine(t, release, waiting.pod); line.Outcome != api.ReleaseDeletedLost {
		t.Errorf("the first list's pod's line is %+v, want deleted_lost", line)
	}
	if line := releaseLine(t, release, late); line.Kind != runtime.ProjectSandboxIssue || line.Issue != lateIssue || line.Outcome != api.ReleaseDeletedUnread {
		t.Errorf("the late issue pod's line is %+v, want %s's pod deleted_unread", line, lateIssue)
	}
	if line := releaseLine(t, release, cache); line.Kind != runtime.ProjectSandboxCache || line.Outcome != api.ReleaseDeleted {
		t.Errorf("the late cache's line is %+v, want a cache deleted", line)
	}
	if line := releaseLine(t, release, stray); line.Kind != runtime.ProjectSandboxUnknown || line.Outcome != api.ReleaseLeft || line.Reason != releaseUnknownKind {
		t.Errorf("the late Sandbox of no known kind has the line %+v, want it left: %s", line, releaseUnknownKind)
	}
	for name, stays := range map[string]bool{late: false, cache: false, stray: true} {
		if standing := rig.kube.sandbox(name) != nil; standing != stays {
			t.Errorf("the late Sandbox %s stands %t after the release, want %t", name, standing, stays)
		}
	}
}

// dispatch://INFRA-3583 R6, "name each workspace it read with lost work": a pod whose first read
// counted unpushed commits, and whose asked role's process ended before the last read, has no role
// process to read it at the end. Its line keeps the first read's count and flag and says that no
// role process ran at the last read, as deleted_read_refused: deleted_unread is a pod no read
// ever reached.
func TestProjectReleaseKeepsTheCountOfAPodWhoseRoleEndedBeforeItsLastRead(t *testing.T) {
	rig := newIdleWakeRig(t, "ended")
	ended := rig.issue("ended")
	rig.agents.setPlan(ended.key, string(ended.role), idleRigPlan{TakeTurn: true, Unpushed: &idleRigUnpushed{Commits: 2, WorkingCopyChanged: true}})
	rig.launchAll()

	rig.startRelease("15s")
	idleRigEventually(t, idleRigRecordWait, "the release's ask to the pod's role", func() bool {
		return len(rig.releaseAsks(ended.key, ended.role)) == 1
	})
	rig.stopRole(ended)
	release, _ := rig.awaitReleaseFinished()
	line := releaseLine(t, release, ended.pod)
	if line.Outcome != api.ReleaseDeletedReadRefused || line.Commits == nil || *line.Commits != 2 ||
		line.WorkingCopyChanged == nil || !*line.WorkingCopyChanged || line.Reason != releaseNoRoleAtLastRead {
		t.Fatalf("the pod's line is %s, want deleted_read_refused with the first read's 2 commits and changed working copy, and the reason %q",
			lineJSON(line), releaseNoRoleAtLastRead)
	}
	if len(line.Asked) != 1 || line.Asked[0].Role != ended.role || !line.Asked[0].Bound {
		t.Errorf("the pod's asked roles are %+v, want its implementer, which did not answer by the bound", line.Asked)
	}
	if rig.kube.sandbox(ended.pod) != nil {
		t.Error("the pod's Sandbox is still there")
	}
}

// refuseAsks has the store refuse every update round's ask written to the outbox
// (record.UpdateAsk), the project release's among them, for the rest of the test.
func (r *idleWakeRig) refuseAsks() {
	r.t.Helper()
	for _, statement := range []string{
		`create function idle_rig_refuse_ask() returns trigger language plpgsql as $$
			begin raise exception 'the store refused the ask'; end $$`,
		`create trigger idle_rig_refuse_ask before insert on outbox for each row
			when (new.kind = '` + string(record.OutboxKindUpdateAsk) + `') execute function idle_rig_refuse_ask()`,
	} {
		if _, err := r.pool.Exec(context.Background(), statement); err != nil {
			r.t.Fatalf("make the store refuse the outbox's asks: %v", err)
		}
	}
}

// dispatch://INFRA-3583 R6: an ask the release could not write reached no role, so the release
// reports no role asked and waits for no answer. The pod's line says the ask could not be written
// and why, names no asked role and no bound passed, and the pod goes straight to its last read and
// its delete, well before the bound.
func TestProjectReleaseReportsAnAskItCouldNotWrite(t *testing.T) {
	rig := newIdleWakeRig(t, "unasked")
	unasked := rig.issue("unasked")
	rig.agents.setPlan(unasked.key, string(unasked.role), idleRigPlan{TakeTurn: true, Unpushed: &idleRigUnpushed{Commits: 1}})
	rig.launchAll()
	rig.refuseAsks()

	rig.startRelease("90s")
	release, _ := rig.awaitReleaseFinished()
	line := releaseLine(t, release, unasked.pod)
	if !strings.HasPrefix(line.Reason, releaseAskUnwritten+": ") || !strings.Contains(line.Reason, "the store refused the ask") {
		t.Errorf("the pod's line gives the reason %q, want that its ask could not be written, with the store's refusal", line.Reason)
	}
	if len(line.Asked) != 0 {
		t.Errorf("the pod's line names the asked roles %+v, want none: no ask was written", line.Asked)
	}
	if line.Outcome != api.ReleaseDeletedLost || line.Commits == nil || *line.Commits != 1 {
		t.Errorf("the pod's line is %s, want deleted_lost with its 1 commit", lineJSON(line))
	}
	if release.FinishedAt.After(release.BoundAt) {
		t.Errorf("the release finished at %s, after the bound %s: the pod waited for answers nobody was asked for", release.FinishedAt, release.BoundAt)
	}
	if asks := rig.releaseAsks(unasked.key, unasked.role); len(asks) != 0 {
		t.Errorf("the pod's role was given %d asks, want none", len(asks))
	}
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
