package daemon

// podreplace.go is the record of each replacement of an issue pod that may take a disk (R7 of
// dispatch://INFRA-3271; GPT-6.1 Sol's points 1 and 2 on #5285 at 0259f8af, which INFRA-3118's
// architect ruled fixes inside R7). The daemon writes the record before the runtime acts, keyed by
// the issue and the pod's uid (store's pod_replacements): an update round's replacement in its last
// word under the pod's launch turn (recordedDue), one a launch makes as the round's own as the
// launch's reading of the round answers it (roundPodEndsAtLaunch), and a launch's own replacement
// before its suspension (recordLaunchTakes, runtime.LaunchTakes). The record holds the read of the
// workspace made before the act. Once the act is made the record goes, in the transaction that
// queues the issue's architect's workspace-taken notice where the read found work or failed
// (finishReplacement), so the notice is queued exactly once whatever fails or stops between. A
// record still standing at a sweep is one whose call failed or whose daemon stopped: the sweep asks
// the runtime whether the pod was taken (runtime.PodTakenReader), and finishes a replacement whose
// suspension landed from the read made before it, never from a new read, and never as a keep, or
// forgets one whose pod still runs (finishReplacements).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/store/storetext"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// replacementRecord is the daemon's encoding of a replacement's record (store.PodReplacement): a
// round's (Round set: its recorded reasons, who made the replacement, and the read before it) or a
// launch's own (Claim set: the launching claim and why it replaced the pod, and the read through
// the pod's launcher). The read is what it found, or Failed, the error it failed with as the store
// keeps a far side's error (storetext.FarSideErrorEnds). The encoding of one replacement is the
// same bytes each time it is made, so a finish or a forget names the record it acts on.
type replacementRecord struct {
	Round   string      `json:"round,omitempty"`
	Reasons string      `json:"reasons,omitempty"`
	By      string      `json:"by,omitempty"`
	Claim   claim.Token `json:"claim,omitempty"`
	Why     string      `json:"why,omitempty"`
	Role    claim.Role  `json:"role,omitempty"`
	At      time.Time   `json:"at"`
	Head    string      `json:"head,omitempty"`
	Changed bool        `json:"changed,omitempty"`
	Commits []string    `json:"commits,omitempty"`
	Count   int         `json:"count,omitempty"`
	Failed  string      `json:"failed,omitempty"`
}

// failedText is the error of a read as the record keeps it, "" for a read that was made.
func failedText(err error) string {
	if err == nil {
		return ""
	}
	return storetext.FarSideErrorEnds(err.Error())
}

// roundRecordOf is the record of a round's replacement made over read, the round recording
// reasons.
func roundRecordOf(read workspaceRead, reasons string) ([]byte, error) {
	return json.Marshal(replacementRecord{Round: read.round, Reasons: reasons, By: read.by.String(), Role: read.role, At: read.at.UTC(),
		Head: read.work.Head, Changed: read.work.Changed, Commits: read.work.Commits, Count: read.work.Count, Failed: failedText(read.err)})
}

// launchRecordOf is the record of a launch's own replacement, as the runtime hands it.
func launchRecordOf(taken runtime.TakenPod) ([]byte, error) {
	return json.Marshal(replacementRecord{Claim: taken.Claim, Why: taken.Why,
		Head: taken.Work.Head, Changed: taken.Work.Changed, Commits: taken.Work.Commits, Count: taken.Work.Count, Failed: failedText(taken.Err)})
}

// work is the read the record holds, of pod.
func (r replacementRecord) work(pod string) (runtime.WorkspaceWork, error) {
	work := runtime.WorkspaceWork{Head: r.Head, Changed: r.Changed, Commits: r.Commits, Count: r.Count, PodUID: pod}
	if r.Failed != "" {
		return work, errors.New(r.Failed)
	}
	return work, nil
}

// read is a round's record as the read it holds (workspaceRead), of pod.
func (r replacementRecord) read(pod string) workspaceRead {
	by := viaProcess
	switch r.By {
	case viaIssue.String():
		by = viaIssue
	case viaLaunch.String():
		by = viaLaunch
	}
	read := workspaceRead{round: r.Round, by: by, role: r.Role, at: r.At}
	read.work, read.err = r.work(pod)
	return read
}

// taken is a launch's record as the runtime handed it (runtime.TakenPod), of issue's pod.
func (r replacementRecord) taken(issue, pod string) runtime.TakenPod {
	taken := runtime.TakenPod{Claim: r.Claim, Issue: issue, PodUID: pod, Why: r.Why}
	taken.Work, taken.Err = r.work(pod)
	return taken
}

// recordedDue is due, a replacement's last word, followed by the write of the replacement's record,
// recorded, for issue's pod: under the pod's launch turn and one statement before the suspension,
// so a suspension that lands always has its record. A record that cannot be written refuses the
// replacement, which then takes nothing.
func (s *supervision) recordedDue(issue, pod string, recorded []byte, due func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := due(ctx); err != nil {
			return err
		}
		if err := s.supervisor.store.RecordPodReplacement(ctx, s.plan.project, issue, pod, recorded); err != nil {
			return fmt.Errorf("%w: the record of the replacement cannot be written: %w", errReplacementNotDue, err)
		}
		return nil
	}
}

// finishReplacement finishes the record recorded of issue's replacement of pod, now the act is made:
// it deletes the record and, in the same transaction, queues reason, where it is not "", as the
// issue's architect's workspace-taken notice (store.Store.FinishPodReplacement). It answers false
// where the store refused, and the record then stands for a later sweep (finishReplacements); a
// record another finish took, or a later replacement of the same pod wrote over, is finished
// already, and nothing is queued for it here. attrs name the replacement on its lines.
func (s *supervision) finishReplacement(ctx context.Context, issue, pod string, recorded []byte, reason string, attrs ...any) bool {
	attrs = append([]any{"issue", issue, "pod", pod}, attrs...)
	var notice *store.IdleReleaseRow
	if reason != "" {
		row, err := record.NewOutboxRow(issue, record.Notice{Kind: record.WorkspaceTakenNotice, Role: claim.RoleArchitect, Reason: reason}, s.plan.clock.Now())
		if err != nil {
			s.log.Error("supervise: encode the issue's architect's notice of what a replacement of its pod took; its record stands, and a later sweep tells the architect",
				append(attrs, "error", err)...)
			return false
		}
		notice = &store.IdleReleaseRow{Kind: string(row.Kind), Payload: row.Payload}
	}
	finished, told, err := s.supervisor.store.FinishPodReplacement(ctx, s.plan.project, issue, pod, recorded, notice)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			s.log.Error("supervise: finish the record of a replacement of the issue's pod, and tell its architect what the replacement took; the record stands, and a later sweep finishes it",
				append(attrs, "error", err)...)
		}
		return false
	case !finished:
		s.log.Info("supervise: the record of this replacement of the issue's pod is finished already, or a later replacement of the pod wrote another", attrs...)
	case notice == nil:
	case !told:
		s.log.Info("supervise: the issue's architect is not told what a replacement of its pod took: its issue is another project's, whose rows this daemon writes none of",
			attrs...)
	default:
		s.log.Info("supervise: the issue's architect is told what a replacement of its pod took", attrs...)
	}
	return true
}

// recordLaunchTakes is the daemon's runtime.LaunchTakes: the record of a launch's own replacement
// of an issue pod, written before the replacement takes anything, where the read before it found
// work that origin does not hold or could not be made (runtime.TakenPod.Tells). A replacement over
// a read that found nothing owes nobody a notice, and is not recorded. An error fails the launch.
func (s *supervision) recordLaunchTakes(ctx context.Context, taken runtime.TakenPod) error {
	if !taken.Tells() {
		return nil
	}
	recorded, err := launchRecordOf(taken)
	if err != nil {
		return fmt.Errorf("encode the record of the replacement of %s's pod %s: %w", taken.Issue, taken.PodUID, err)
	}
	return s.supervisor.store.RecordPodReplacement(ctx, s.plan.project, taken.Issue, taken.PodUID, recorded)
}

// launchRecordGrace is how many launch bounds (runtime.Runtime.LaunchBound) a launch's record
// stands before a sweep finishes it (finishReplacements): the launch that wrote it finishes it once
// its act is made, well inside one bound, and a record older than two was left by a launch that
// stopped, or whose finish the store refused.
const launchRecordGrace = 2

// finishReplacements finishes, at the start of a sweep, every record of a replacement that a call
// or a daemon left standing (store.Store.PodReplacements), from the runtime's own record of the pod
// (runtime.PodTakenReader), and answers the issues whose round's record still stands, which the
// walk decides nothing of at this sweep: a round there must neither read a pod its replacement took
// again, nor keep it, nor end as a pod that went without its round (GPT-6.1 Sol's point 1).
// claims is the sweep's snapshot.
//
//   - A launch's own record stands for launchRecordGrace launch bounds: until then the launch that
//     wrote it may still be running, and it finishes the record itself (tellLaunchTook).
//   - A round's record that a launch made as the round's own (viaLaunch) is the walk's to finish,
//     while this daemon remembers that launch (launchReplaced, endRoundALaunchReplaced).
//   - A pod the runtime cannot read the state of leaves the record standing.
//   - A pod that is not taken — the issue's Sandbox runs it as its own — had no act made over it:
//     the record is forgotten, and the round decides again in this sweep's walk, from a new read.
//   - A pod that is taken was taken by the recorded replacement: its suspension landed, whether or
//     not the call answered so. The round ends as that replacement, from the read made before it
//     (roundReplaced), with the members that had not answered named, and the architect is told
//     what it took. A launch's record tells the architect from the launch's own read.
//
// A runtime that answers no PodTaken leaves every record standing; such a runtime replaces no pod.
func (s *supervision) finishReplacements(ctx context.Context, claims []supervise.Claim) (map[string]bool, error) {
	standing, err := s.supervisor.store.PodReplacements(ctx, s.plan.project)
	if err != nil {
		return nil, err
	}
	held := map[string]bool{}
	for _, r := range standing {
		if ctx.Err() != nil {
			return held, ctx.Err()
		}
		var rec replacementRecord
		if err := json.Unmarshal(r.Record, &rec); err != nil {
			s.log.Error("supervise: the record of a replacement of an issue's pod is one this daemon cannot read; it stands",
				"issue", r.Issue, "pod", r.PodUID, "error", err)
			continue
		}
		ofRound := rec.Round != ""
		if !ofRound && r.Age < launchRecordGrace*s.runtime.LaunchBound() {
			continue
		}
		if ofRound && rec.By == viaLaunch.String() {
			if _, _, _, replaced := s.launchReplaced(r.Issue, rec.Round); replaced {
				continue
			}
		}
		taken, read := s.podTaken(r.Issue, r.PodUID)
		switch {
		case !read:
			s.log.Info("supervise: the runtime cannot say whether a recorded replacement took the issue's pod; its record stands",
				"issue", r.Issue, "pod", r.PodUID, "round", rec.Round)
			held[r.Issue] = held[r.Issue] || ofRound
		case !taken:
			if _, err := s.supervisor.store.ForgetPodReplacement(ctx, s.plan.project, r.Issue, r.PodUID, r.Record); err != nil {
				if ctx.Err() == nil {
					s.log.Error("supervise: forget the record of a replacement whose pod still runs; it stands",
						"issue", r.Issue, "pod", r.PodUID, "round", rec.Round, "error", err)
				}
				held[r.Issue] = held[r.Issue] || ofRound
				continue
			}
			s.log.Info("supervise: a recorded replacement of the issue's pod took nothing: the pod still runs as the issue's own; its record is forgotten",
				"issue", r.Issue, "pod", r.PodUID, "round", rec.Round)
		case !ofRound:
			taken := rec.taken(r.Issue, r.PodUID)
			s.finishReplacement(ctx, r.Issue, r.PodUID, r.Record, launchTakenMessage(taken), "claim", taken.Claim, "why", taken.Why)
		default:
			told := s.noticeGroup(claimsOf(claims, r.Issue))
			ended, _, _, _ := s.launchReplaced(r.Issue, rec.Round)
			s.log.Info("supervise: a recorded replacement of the issue's pod took it, although its call did not answer so; the round ends as that replacement, from the read made before it",
				"issue", r.Issue, "pod", r.PodUID, "round", rec.Round)
			if !s.roundReplaced(ctx, r.Issue, rec.Round, r.PodUID, rec.Reasons, ended, lostClaims(told, rec.Round), rec.read(r.PodUID), told, r.Record) {
				held[r.Issue] = true
			}
		}
	}
	return held, nil
}

// podTaken is the runtime's answer of whether a replacement took issue's pod (PodTakenReader), and
// read false where the runtime answers nothing of it.
func (s *supervision) podTaken(issue, pod string) (taken, read bool) {
	reader, ok := s.runtime.(runtime.PodTakenReader)
	if !ok {
		return false, false
	}
	return reader.PodTaken(issue, pod)
}
