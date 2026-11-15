package supervise

import (
	"fmt"
	"time"
)

// ReleaseTask is the project release's ask to one role of an issue pod whose workspace holds
// unpushed work, whose shim refused the read, or that has a role in a turn (dispatch://INFRA-3583
// R6). The daemon (internal/daemon's project release) builds it once per release and issue and
// sends it on the role's own Envoy topic, as an update round's ask goes (record.UpdateAsk, with
// round set to ask), so it can arrive in the middle of a turn and tells the role to act now.
//
// ask is the ask's id, the round the role names in its answer: the `legion` tool's
// ready_for_update takes any round string, and the daemon hands an answer that names the running
// release's ask to the release rather than to an update round. issue is the issue key, whose
// branch `legion/<issue>` is where the role pushes. deletesAt is the release's bound: the moment
// the daemon cuts the turn and deletes the pod whatever the role did. The daemon pushes nothing,
// so work not pushed by then goes with the pod, and the text says so.
//
// The ask is no task: the daemon sends it once, and the daemon's next boot sends it again to no
// one. The role's next task waits for the start that comes after that boot.
//
// Another agent reads it, so it follows ASD-STE100: one instruction per sentence, active voice,
// and no word that has a second reading.
func ReleaseTask(ask, issue string, deletesAt time.Time) string {
	at := deletesAt.UTC().Format(time.RFC3339)
	branch := "legion/" + issue
	return fmt.Sprintf(`The daemon releases every pod of this project. It deletes the pod that runs this issue's roles at %s (UTC), and the disk goes with the pod. The disk holds your workspace. The daemon pushes nothing for you. Work that you do not push before %s is lost with the pod.

The ask is %s. If every asked role of this pod answers and no role is in a turn, the daemon deletes the pod before that moment. A turn in progress does not delay that moment.

This message can arrive while you are in a turn. Do these three steps now:

1. Push every commit that you want to keep to the branch %s.
2. Call the `+"`legion`"+` tool with op: "ready_for_update" and round: "%s".
3. End your turn.

If your `+"`legion`"+` tool has no "ready_for_update" operation, do step 1 now. Then end your turn. The daemon deletes the pod at %s whatever you do.

The daemon starts your process again in a new pod, in this same session, after the daemon starts again. The daemon holds your next task until then. Do not start new work now.`, at, at, ask, branch, ask, at)
}
