package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sjawhar/legion/daemon/internal/api"
)

// newProjectCommand is the operator's hand on the daemon's project as a whole: one command,
// release, the project release of dispatch://INFRA-3583 (internal/api/project_release.go).
func newProjectCommand(stdout, stderr io.Writer) *cobra.Command {
	return group(&cobra.Command{Use: "project", Short: "the operator's hand on the daemon's project: release every pod of it, asking each role with unpushed work to push first, for a move to another cluster"},
		newProjectReleaseCommand(stdout, stderr),
	)
}

// newProjectReleaseCommand is `legion project release`: the three flags every operator command
// takes, and the release's own --bound, --wait, --dry-run and --json. Its --wait is its own rather
// than withWait's, since the call it bounds waits out the release's bound (waitUntil).
func newProjectReleaseCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "release every pod of the project, asking each role with unpushed work to push first, for a move to another cluster",
		Args:  noArguments,
	}
	c := newOperatorCall(cmd, "file holding the operator bearer the daemon's operator_token_file names (required)", stdout, stderr)
	bound := c.flags.Duration("bound", 0, "how long the release waits for the roles it asks before it deletes every pod still waiting (default the daemon's pod_update_grace_minutes)")
	wait := c.flags.Duration("wait", 0, "how long the whole command may take before it ends, saying the release goes on (default the release's bound plus 30m)")
	dryRun := c.flags.Bool("dry-run", false, "print what a release would do now: read each pod's workspace, and ask, stop and save nothing")
	asJSON := c.flags.Bool("json", false, "print the release as the daemon served it, one JSON object")
	cmd.RunE = runs(func(ctx context.Context, _ []string) int {
		return runProjectRelease(ctx, c, *bound, *wait, *dryRun, *asJSON)
	})
	return cmd
}

// projectReleasePoll is how often `legion project release` reads the release while it runs. A
// variable so that this package's tests read a release that finishes without waiting the poll
// out.
var projectReleasePoll = 5 * time.Second

// projectReleaseWaitBeyondBound is how much longer than the release's bound the command waits for
// it by default: the bound ends every pod's wait, and the stops, the deletes and the holds of a
// left pod's claims come after it.
const projectReleaseWaitBeyondBound = 30 * time.Minute

// releaseGoesOn ends every sentence this command's wait makes about a release the daemon runs:
// the release is the daemon's, which no bound of this command cancels, and the command run again
// reads it.
const releaseGoesOn = "the release goes on in the daemon. Run this command again to read it"

// runProjectRelease releases every pod of the project (R6): it asks the daemon to start the
// release, which a daemon that already ran one in this process answers with that release, reads
// the release every projectReleasePoll until it finished or --wait ends, and prints it — one line
// for each Sandbox of the project and one for each claim the release stopped. --dry-run asks for
// the outcome a release would give now, and stops nothing; a daemon that already ran a release in
// this process answers it with that release, which the command then reads and prints as a release.
//
// It exits 0 once the release finished and left no Sandbox of the project, and for a dry run's
// answer. It exits 1 when the release names a pod it left or stopped short, when the daemon
// refuses, and when --wait ends first, saying that the release goes on in the daemon. A pod
// deleted with work lost changes no exit: the line names that workspace. A usage error is 2.
func runProjectRelease(ctx context.Context, c *operatorCall, bound, wait time.Duration, dryRun, asJSON bool) int {
	if code, ok := c.check("operator-token-file"); !ok {
		return code
	}
	if bound < 0 || wait < 0 {
		return refuse(c.cmd, "--bound and --wait must not be negative")
	}
	op, ok := c.connect()
	if !ok {
		return 1
	}
	began := time.Now()
	// The call is bounded from its start. Until the daemon names the release's bound, the bound
	// the command was given stands in for it, and a bound the daemon chooses moves the end once
	// the daemon has named it (waitUntil).
	c.until = waitUntil(began, wait, bound)
	request := api.ProjectReleaseRequest{DryRun: dryRun}
	if bound > 0 {
		request.Bound = bound.String()
	}
	release, answer, cut, ok := releaseRequest(ctx, c, op, http.MethodPost, request)
	// The daemon has answered nothing yet, so which sentence ends the wait follows --dry-run.
	if cut {
		ended := releaseGoesOn
		if dryRun {
			ended = "a dry run records and starts nothing"
		}
		fmt.Fprintf(c.stderr, "%s: %s %s%s was not answered before the command's wait of %s ended; %s\n",
			c.name(), http.MethodPost, op.base, api.ProjectReleaseRoute, c.until.Sub(began).Round(time.Millisecond), ended)
		return 1
	}
	if !ok {
		return 1
	}
	// What is printed, and how the command exits, follow the answer and not the flag: a daemon
	// that already ran a release in this process answers a dry run with that release, which may
	// still run or have left a pod.
	if release.DryRun {
		return printProjectRelease(c, release, answer, asJSON)
	}
	if dryRun {
		fmt.Fprintf(c.stderr, "%s: the daemon already ran release %d in this process, so it answered the dry run with that release; it is read as the release it is\n",
			c.name(), release.ID)
	}
	if wait == 0 {
		c.until = waitUntil(began, 0, release.BoundAt.Sub(release.StartedAt))
	}
	// Once the daemon has answered with a release, the end of the wait prints that release as the
	// command last read it, and says it goes on, whether the wait ends between two reads or
	// inside one: a read the wait cuts loses nothing the command already holds.
	waitEndedFirst := func() int {
		printProjectRelease(c, release, answer, asJSON)
		fmt.Fprintf(c.stderr, "%s: the command's wait of %s ended before release %d finished; %s\n",
			c.name(), c.until.Sub(began).Round(time.Millisecond), release.ID, releaseGoesOn)
		return 1
	}
	for release.FinishedAt == nil && release.EndedAt == nil {
		left := time.Until(c.until)
		if left <= 0 {
			return waitEndedFirst()
		}
		select {
		case <-ctx.Done():
			return 1
		case <-time.After(min(projectReleasePoll, left)):
		}
		if time.Now().After(c.until) {
			continue
		}
		read, readAnswer, cut, ok := releaseRequest(ctx, c, op, http.MethodGet, nil)
		if cut {
			return waitEndedFirst()
		}
		if !ok {
			return 1
		}
		release, answer = read, readAnswer
	}
	return printProjectRelease(c, release, answer, asJSON)
}

// releaseRequest makes one request of the release's route under the command's bound
// (operatorCall.requestCut) and answers the release the daemon served, decoded, with its body as
// served. A refusal and an answer that cannot be read are each printed and answer false. A request
// the bound ended answers false with cut true and is not printed: the command says what that end
// means, which depends on what the daemon has already answered.
func releaseRequest(ctx context.Context, c *operatorCall, op operator, method string, body any) (release api.ProjectReleaseResponse, answer []byte, cut, ok bool) {
	_, answer, cut, ok = c.requestCut(ctx, op, method, api.ProjectReleaseRoute, body)
	if !ok {
		return api.ProjectReleaseResponse{}, nil, cut, false
	}
	if err := decodeAnswer(answer, &release); err != nil {
		fmt.Fprintf(c.stderr, "%s: read the answer %s%s served: %v\n", c.name(), op.base, api.ProjectReleaseRoute, err)
		return api.ProjectReleaseResponse{}, nil, false, false
	}
	return release, answer, false, true
}

// waitUntil is when the command's call ends: --wait from its start when one was given, and
// otherwise the release's bound plus projectReleaseWaitBeyondBound.
func waitUntil(began time.Time, wait, bound time.Duration) time.Time {
	if wait > 0 {
		return began.Add(wait)
	}
	return began.Add(bound + projectReleaseWaitBeyondBound)
}

// printProjectRelease prints release, as the daemon served it under --json and as its lines
// otherwise, and answers the command's exit: 0 for a dry run, and for a release that finished
// leaving no Sandbox of the project; 1 for one that left a Sandbox or stopped short. A release a
// boot ended before it finished is one that stopped short. The exit of a release that has not
// finished is its caller's.
func printProjectRelease(c *operatorCall, release api.ProjectReleaseResponse, answer []byte, asJSON bool) int {
	if asJSON {
		if err := writeAnswer(c.stdout, answer); err != nil {
			return 1
		}
	} else {
		for _, line := range release.Result.Sandboxes {
			fmt.Fprintln(c.stdout, releasedSandboxLine(line, release.DryRun))
		}
		for _, line := range release.Result.Claims {
			fmt.Fprintln(c.stdout, releasedClaimLine(line, release.DryRun))
		}
	}
	if release.DryRun || release.FinishedAt == nil && release.EndedAt == nil {
		return 0
	}
	code := 0
	if release.Result.Failure != "" {
		fmt.Fprintf(c.stderr, "%s: release %d stopped short: %s\n", c.name(), release.ID, release.Result.Failure)
		code = 1
	}
	if release.FinishedAt == nil {
		fmt.Fprintf(c.stderr, "%s: release %d was ended by a %s before it finished; read the Sandboxes left in the cluster before the project moves\n",
			c.name(), release.ID, orNone(string(release.EndedBy)))
		code = 1
	}
	var left []string
	for _, line := range release.Result.Sandboxes {
		if !line.Deleted() {
			left = append(left, line.Name)
		}
	}
	if len(left) > 0 {
		fmt.Fprintf(c.stderr, "%s: release %d left %d Sandbox(es) of the project: %s; move nothing while one stands (docs/kubernetes.md, \"Releasing a project's pods\")\n",
			c.name(), release.ID, len(left), strings.Join(left, ", "))
		code = 1
	}
	return code
}

// releasedSandboxLine is one Sandbox's line: `sandbox <name> <kind> <issue> <outcome>`, then what
// the outcome carries — the workspace's unpushed commits and whether its working copy held a
// change at the last read, or at an earlier read where no role process ran at the last one, the
// shim's refusal, why the Sandbox was left, or the other reasons an issue pod's line gives — then
// a pod that runs another image than the daemon's own, and each role the release asked, with when
// it answered or that the bound passed. On a dry run the outcome is the one a release would give
// now, and the asked roles are the ones it would ask.
func releasedSandboxLine(line api.ReleasedSandbox, dryRun bool) string {
	words := []string{"sandbox", line.Name, string(line.Kind), orNone(line.Issue), string(line.Outcome)}
	var details []string
	if line.Commits != nil {
		changed := "no change"
		if line.WorkingCopyChanged != nil && *line.WorkingCopyChanged {
			changed = "a change"
		}
		// A refused last read writes no count, so a count on a deleted_read_refused line is the one
		// an earlier read took, kept because no role process ran at the last (api.ReleasedSandbox).
		read := "the last read"
		if line.Outcome == api.ReleaseDeletedReadRefused {
			read = "an earlier read"
		}
		details = append(details, fmt.Sprintf("the workspace of %s held %d unpushed commit(s) and its working copy %s at %s", orNone(line.Issue), *line.Commits, changed, read))
	}
	if line.Refusal != "" {
		details = append(details, "the shim refused the read: "+line.Refusal)
	}
	if line.Reason != "" {
		why := line.Reason
		if line.Outcome == api.ReleaseLeft {
			why = "left because " + why
		}
		details = append(details, why)
	}
	if line.OtherImage {
		details = append(details, "its pod runs an image other than the daemon's own")
	}
	if len(line.Asked) > 0 {
		asked := make([]string, len(line.Asked))
		for i, role := range line.Asked {
			switch {
			case dryRun:
				asked[i] = string(role.Role)
			case role.AnsweredAt != nil:
				asked[i] = string(role.Role) + " answered at " + role.AnsweredAt.UTC().Format(time.RFC3339)
			case role.Bound:
				asked[i] = string(role.Role) + " did not answer by the bound"
			default:
				asked[i] = string(role.Role) + " has not answered yet"
			}
		}
		verb := "asked "
		if dryRun {
			verb = "a release would ask "
		}
		details = append(details, verb+strings.Join(asked, ", "))
	}
	if len(details) == 0 {
		return strings.Join(words, " ")
	}
	return strings.Join(words, " ") + ": " + strings.Join(details, "; ")
}

// releasedClaimLine is one stopped claim's line: `claim <token> <issue> <role> <session>`, how it
// ended, and whether the daemon's next boot starts it.
func releasedClaimLine(line api.ReleasedClaim, dryRun bool) string {
	words := []string{"claim", string(line.Token), orNone(line.Issue), string(line.Role), orNone(line.Session), orNone(string(line.Ended))}
	switch {
	case line.BootStarts && dryRun:
		words = append(words, "(the next boot would start it)")
	case line.BootStarts:
		words = append(words, "(the next boot starts it)")
	case line.Ended != "":
		words = append(words, "(the next boot does not start it)")
	}
	if line.Detail != "" {
		words = append(words, "("+line.Detail+")")
	}
	return strings.Join(words, " ")
}
