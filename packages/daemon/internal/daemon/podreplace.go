package daemon

// podreplace.go is the record of each replacement of an issue pod that may take a disk (R7 of
// #5285; GPT-6.1 Sol's points 1 and 2 on #5285 at 0259f8af, which the spec's
// architect ruled fixes inside R7). The daemon writes the record before the runtime acts, under an
// identity of the act's own and never the pod's, with what the act takes and its target (store's
// pod_replacements; Sol's points 3 and 4 at fa9deb04): an update round's replacement before the
// call whose last word the runtime asks under the pod's launch turn (recordRoundReplacement,
// recordedDue), one a launch makes as the round's own as the launch's reading of the round answers
// it (roundPodEndsAtLaunch), and a launch's own replacement before its suspension
// (recordLaunchTakes, runtime.LaunchTakes). The record holds the read of the
// workspace made before the act. Once the act is made the record goes, in the transaction that
// queues the issue's architect's workspace-taken notice where the read found work or failed
// (finishReplacement), so the notice is queued exactly once whatever fails or stops between, and
// the finish takes every record of the act's target with it, so one act is told once. A record
// still standing at a sweep is one whose call failed, whose daemon stopped, or whose notice the
// store refused: the sweep has
// the runtime read whether the act was made, under the pod's launch turn and from the cluster's API
// (runtime.ActReader), and finishes a replacement whose suspension landed from the read made
// before it, never from a new read, and never as a keep, or forgets one whose target is still in
// use: a pod that still runs as the issue's own, or a Sandbox of the volume that runs a live pod on
// it (finishReplacements).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/store/storetext"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// replacementRecord is the daemon's encoding of a replacement's record (store.PodReplacement's
// Record): a round's (Round set: its recorded reasons, who made the replacement, and the read
// before it) or a launch's own (Claim set: the launching claim and why it replaced the pod, and the
// read through the pod's launcher). The read is what it found, or Failed, the error it failed with
// as the store keeps a far side's error (storetext.FarSideErrorEnds).
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
	// WithPod is a launch's act that takes the volume and, with it, the live pod of its Sandbox
	// (runtime.TakenPod.WithPod).
	WithPod bool `json:"withPod,omitempty"`
}

// failedText is the error of a read as the record keeps it, "" for a read that was made.
func failedText(err error) string {
	if err == nil {
		return ""
	}
	return storetext.FarSideErrorEnds(err.Error())
}

// roundRecordOf is the record of round's replacement of issue's pod made over read, the round
// recording reasons, under an operation identity of its own (GPT-6.1 Sol's point 3 on #5285 at
// fa9deb04): each replacement a round asks for is an act of its own, whatever pod it names. A
// round's replacement is the suspension of the issue's Sandbox over the pod, so it takes the pod,
// and the pod's uid is its target (GPT-6.1 Sol's point 4).
func roundRecordOf(issue, pod string, read workspaceRead, reasons string) (store.PodReplacement, error) {
	encoded, err := json.Marshal(replacementRecord{Round: read.round, Reasons: reasons, By: read.by.String(), Role: read.role, At: read.at.UTC(),
		Head: read.work.Head, Changed: read.work.Changed, Commits: read.work.Commits, Count: read.work.Count, Failed: failedText(read.err)})
	if err != nil {
		return store.PodReplacement{}, err
	}
	return store.PodReplacement{Operation: uuid.NewString(), Issue: issue, PodUID: pod, Act: runtime.PodAct(pod), Record: encoded}, nil
}

// launchRecordOf is the record of a launch's own replacement, as the runtime hands it, under the
// operation identity the runtime gave it, with what the act takes and its target.
func launchRecordOf(taken runtime.TakenPod) (store.PodReplacement, error) {
	encoded, err := json.Marshal(replacementRecord{Claim: taken.Claim, Why: taken.Why, WithPod: taken.WithPod,
		Head: taken.Work.Head, Changed: taken.Work.Changed, Commits: taken.Work.Commits, Count: taken.Work.Count, Failed: failedText(taken.Err)})
	if err != nil {
		return store.PodReplacement{}, err
	}
	return store.PodReplacement{Operation: taken.Operation, Issue: taken.Issue, PodUID: taken.PodUID, Act: taken.Act, Record: encoded}, nil
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

// taken is a launch's record, recorded, as the runtime handed it (runtime.TakenPod).
func (r replacementRecord) taken(recorded store.PodReplacement) runtime.TakenPod {
	taken := runtime.TakenPod{Operation: recorded.Operation, Claim: r.Claim, Issue: recorded.Issue, PodUID: recorded.PodUID, Why: r.Why,
		Act: recorded.Act, WithPod: r.WithPod}
	taken.Work, taken.Err = r.work(recorded.PodUID)
	return taken
}

// recordRoundReplacement writes the record of round's replacement of issue's pod, made over read,
// the round recording reasons (roundRecordOf), before the runtime is asked to act: through a
// process (replacePodForRound) or by issue (replaceIssuePodForRound) before the call, and at a
// launch (roundPodEndsAtLaunch) before the answer that has the launch suspend the pod. The record
// is written before the last word, so the last word is the last decision before the suspension
// (GPT-6.1 Sol's point 1 on #5285 at fa9deb04), and a pod the runtime finds gone at the call is a
// replacement whose record stands, and whose notice goes out (the Legion reviewer's minor point
// 3). A record that cannot be encoded or written is logged, and nothing is replaced: a reading
// nobody recorded may not delete a disk. It answers the record, and false where none is written.
func (s *supervision) recordRoundReplacement(ctx context.Context, issue, round, pod, reasons string, read workspaceRead) (store.PodReplacement, bool) {
	recorded, err := roundRecordOf(issue, pod, read, reasons)
	if err == nil {
		err = s.supervisor.store.RecordPodReplacement(ctx, s.plan.project, recorded)
	}
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("supervise: write the record of this update round's replacement; nothing is replaced, and the round stays open",
				"issue", issue, "round", round, "pod", pod, "replacer", read.by.String(), "error", err)
		}
		return store.PodReplacement{}, false
	}
	return recorded, true
}

// recordedDue is due, a replacement's last word, for the replacement whose record, recorded, the
// caller wrote before it called the runtime (recordRoundReplacement). The runtime asks it under the
// pod's launch turn, one statement before the suspension. A last word that refuses takes the
// record away again (forgetReplacement): the replacement takes nothing, and the next sweep decides
// the round afresh.
func (s *supervision) recordedDue(recorded store.PodReplacement, due func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		err := due(ctx)
		if err != nil {
			s.forgetReplacement(ctx, recorded)
		}
		return err
	}
}

// forgetReplacement takes away the record recorded of a replacement the runtime did not make
// (store.Store.ForgetPodReplacement): its last word refused it, or the call found the round's pod
// replaced by a launch as the round's own replacement, whose record the walk finishes. A store
// that refuses leaves it for a later sweep, which reads the act (finishReplacements).
func (s *supervision) forgetReplacement(ctx context.Context, recorded store.PodReplacement) {
	if err := s.supervisor.store.ForgetPodReplacement(ctx, s.plan.project, recorded.Issue, recorded.Operation); err != nil && ctx.Err() == nil {
		s.log.Error("supervise: forget the record of a replacement that took nothing; it stands, and a later sweep reads whether its act was made",
			"issue", recorded.Issue, "pod", recorded.PodUID, "operation", recorded.Operation, "error", err)
	}
}

// finishReplacement finishes the record of operation, a replacement of issue's pod pod, now the
// act is made: it deletes the record, with every other record of an act on the same target, and
// of an act on withPod, where it is not "", a pod the act took with its volume (tookWithPod), and,
// in the same transaction, queues reason, where it is not "", as the issue's architect's
// workspace-taken notice (store.Store.FinishPodReplacement). It answers false where the store
// refused, and the records then stand for a later sweep (finishReplacements); a record that the
// finish of another act on its target took, or another finish of the same act, is finished
// already, and nothing is queued for it here (the Legion reviewer's Important 1 on #5285 at
// 7da847c0). attrs name the replacement on its lines.
func (s *supervision) finishReplacement(ctx context.Context, operation, issue, pod, withPod, reason string, attrs ...any) bool {
	attrs = append([]any{"issue", issue, "pod", pod, "operation", operation}, attrs...)
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
	finished, told, err := s.supervisor.store.FinishPodReplacement(ctx, s.plan.project, issue, operation, withPod, notice)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			s.log.Error("supervise: finish the record of a replacement of the issue's pod, and tell its architect what the replacement took; the record stands, and a later sweep finishes it",
				append(attrs, "error", err)...)
		}
		return false
	case !finished:
		s.log.Info("supervise: the record of this replacement of the issue's pod is finished already, with the record of another act on the same target or by another finish of it; nothing is queued for it",
			attrs...)
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
// of an issue pod, under the operation identity the runtime gave it, written before the
// replacement takes anything, where the read before it found work that origin does not hold or
// could not be made (runtime.TakenPod.Tells). A replacement over a read that found nothing owes
// nobody a notice, and is not recorded. An error fails the launch.
func (s *supervision) recordLaunchTakes(ctx context.Context, taken runtime.TakenPod) error {
	if !taken.Tells() {
		return nil
	}
	recorded, err := launchRecordOf(taken)
	if err != nil {
		return fmt.Errorf("encode the record of the replacement of %s's pod %s: %w", taken.Issue, taken.PodUID, err)
	}
	return s.supervisor.store.RecordPodReplacement(ctx, s.plan.project, recorded)
}

// tellLaunchTook is the daemon's runtime.LaunchTook, its word of a launch's own replacement of an
// issue pod (sandbox.Options.LaunchTook), which the runtime hands it once the act is made: the pod
// gone, and the Sandbox and its kept volume gone where the act takes the volume (GPT-6.1 Sol's
// point 4 on #5285 at fa9deb04). Where the read through a launcher of the pod before the
// replacement found work that no branch or tag on origin holds, or no read could be made
// (runtime.TakenPod.Tells), the record the launch wrote before the act (recordLaunchTakes) is
// finished with the issue's architect's notice of what it took, in one transaction
// (finishReplacement): each commit and the working-copy change by name, or that no read could be
// made and why (launchTakenMessage). A read that found nothing tells nobody. The record is the
// launch's one write of it: a record a sweep finished already is not written again
// (the Legion reviewer's points B and minor 7), and a store that refuses the finish leaves the
// record for a later sweep (finishReplacements), so the launch, which is done, loses nothing of it.
func (s *supervision) tellLaunchTook(ctx context.Context, taken runtime.TakenPod) {
	attrs := []any{"claim", taken.Claim, "why", taken.Why}
	if !taken.Tells() {
		s.log.Info("supervise: a launch replaced its issue's pod, and the read of the workspace before it found nothing that no branch or tag on origin holds",
			append([]any{"issue", taken.Issue, "pod", taken.PodUID}, attrs...)...)
		return
	}
	s.finishReplacement(ctx, taken.Operation, taken.Issue, taken.PodUID, tookWithPod(taken), launchTakenMessage(taken), attrs...)
}

// tookWithPod is the pod a launch's act took with its volume, "" for an act that took no pod
// beside its target (runtime.TakenPod.WithPod): the records of acts on that pod are over with the
// launch's (the Legion reviewer's Important 1 on #5285 at acda4d1e).
func tookWithPod(taken runtime.TakenPod) string {
	if !taken.WithPod {
		return ""
	}
	return taken.PodUID
}

// finishReplacements finishes, at the start of a sweep, every record of a replacement that a call
// or a daemon left standing (store.Store.PodReplacements), from the runtime's read of the act
// (runtime.ActReader), and answers the issues whose round's record still stands, which the walk
// decides nothing of at this sweep: a round there must neither read a pod its replacement took
// again, nor keep it, nor end as a pod that went without its round (GPT-6.1 Sol's point 1 at
// 0259f8af). claims is the sweep's snapshot.
//
// The runtime reads each act under the pod's launch turn, from the cluster's API (GPT-6.1 Sol's
// point 2 on #5285 at fa9deb04, the Legion reviewer's point A). Every act a record names is made
// under that turn: a launch's own replacement (recordLaunchTakes, then tellLaunchTook), a launch's
// replacement as the round's own (roundPodEndsAtLaunch), and a round's replacement by the
// runtime's call, whose last word comes under it (recordedDue). So a turn another holds is an act
// that may be under way, and its record stands for a later sweep; a turn that is free is one under
// which no act of the record's runs, and what the runtime reads then is how the act ended. A record
// is forgotten under that turn, in a short write that waits for no turn.
//
//   - A round's record that a launch made as the round's own (viaLaunch) is the walk's to finish,
//     while this daemon remembers that launch (launchReplaced, endRoundALaunchReplaced).
//   - A turn another holds, a read that fails, and an act the runtime cannot say was made leave
//     the record standing.
//   - An act not made — its target still in use: the issue's Sandbox runs the pod as its own, or
//     the Sandbox of the volume runs a live pod of its own on it — is forgotten, and the round
//     decides again in this sweep's walk, from a new read.
//   - An act made was made by the recorded replacement: its suspension landed, whether or not the
//     call answered so. The round ends as that replacement, from the read made before it
//     (roundReplaced), with the members that had not answered named, and the architect is told
//     what it took. A launch's record tells the architect from the launch's own read.
//
// The sweep reads the records newest first, and finishes each target once (the Legion reviewer's
// Important 3 on #5285 at acda4d1e). A target is taken once, so the finish of its newest record
// takes the target's other records with it, and the notice is the newest record's: the last read
// recorded before the target was taken. An older record of a target this sweep has decided is not
// finished again: where its round is the one that ended, or it is a launch's, it went with the
// newer record; where the newer record's finish was refused, it stands with it, and holds its
// round; where the walk ends the target's round as a launch's replacement, it is the walk's too.
// An older record of another round, whose record a launch's act finished, ends that round, which
// queues no notice, since the record is gone.
//
// A runtime that reads no act leaves every record standing; such a runtime replaces no pod.
func (s *supervision) finishReplacements(ctx context.Context, claims []supervise.Claim) (map[string]bool, error) {
	standing, err := s.supervisor.store.PodReplacements(ctx, s.plan.project)
	if err != nil {
		return nil, err
	}
	held := map[string]bool{}
	decided := map[actKey]targetDecided{}
	for i := len(standing) - 1; i >= 0; i-- {
		r := standing[i]
		if ctx.Err() != nil {
			return held, ctx.Err()
		}
		var rec replacementRecord
		if err := json.Unmarshal(r.Record, &rec); err != nil {
			s.log.Error("supervise: the record of a replacement of an issue's pod is one this daemon cannot read; it stands",
				"issue", r.Issue, "pod", r.PodUID, "operation", r.Operation, "error", err)
			continue
		}
		ofRound := rec.Round != ""
		attrs := []any{"issue", r.Issue, "pod", r.PodUID, "operation", r.Operation, "round", rec.Round}
		keys := actKeysOf(r, rec)
		if was, ok := decidedOf(decided, keys); ok {
			switch {
			case was.walk || was.finished && (!ofRound || rec.Round == was.round):
				s.log.Info("supervise: a newer record of the same target was decided at this sweep; this record of a replacement of the issue's pod goes with it", attrs...)
				continue
			case !was.finished:
				s.log.Info("supervise: a newer record of the same target stands at this sweep; this record of a replacement of the issue's pod stands with it", attrs...)
				held[r.Issue] = held[r.Issue] || ofRound
				continue
			}
		}
		if ofRound && rec.By == viaLaunch.String() {
			if _, replaced := s.launchReplaced(r.Issue, rec.Round); replaced {
				decide(decided, keys, targetDecided{walk: true})
				continue
			}
		}
		state, err := s.readAct(ctx, r)
		switch {
		case errors.Is(err, runtime.ErrTurnBusy):
			s.log.Info("supervise: an act on the issue's pod holds its launch turn; the record of a replacement of the pod stands for a later sweep", attrs...)
			held[r.Issue] = held[r.Issue] || ofRound
		case err != nil:
			if ctx.Err() == nil {
				s.log.Error("supervise: read whether a recorded replacement took the issue's pod, or forget the record of one that took nothing; its record stands",
					append(attrs, "error", err)...)
			}
			held[r.Issue] = held[r.Issue] || ofRound
		case state == runtime.ActUncertain:
			s.log.Info("supervise: the runtime cannot say whether a recorded replacement took the issue's pod; its record stands", attrs...)
			held[r.Issue] = held[r.Issue] || ofRound
		case state == runtime.ActNotMade:
			s.log.Info("supervise: a recorded replacement of the issue's pod took nothing: its target is still in use, the pod running as the issue's own or the Sandbox of the volume running a pod on it; its record is forgotten", attrs...)
		case !ofRound:
			taken := rec.taken(r)
			finished := s.finishReplacement(ctx, r.Operation, r.Issue, r.PodUID, tookWithPod(taken), launchTakenMessage(taken), "claim", taken.Claim, "why", taken.Why)
			decide(decided, keys, targetDecided{finished: finished})
		default:
			told := s.noticeGroup(claimsOf(claims, r.Issue))
			s.log.Info("supervise: a recorded replacement of the issue's pod took it, and its round is not over yet: its call did not answer so, its daemon stopped, or the store refused the architect's workspace-taken notice; the round ends as that replacement, from the read made before it",
				attrs...)
			finished := s.roundReplaced(ctx, r.Issue, rec.Round, r.PodUID, rec.Reasons, roundEnd{}, lostClaims(told, rec.Round), rec.read(r.PodUID), told, r)
			if !finished {
				held[r.Issue] = true
			}
			decide(decided, keys, targetDecided{round: rec.Round, finished: finished})
		}
	}
	return held, nil
}

// actKey is one target of an issue's acts: what an act takes and the uid of what it takes.
type actKey struct {
	issue string
	act   runtime.RecordedAct
}

// targetDecided is what a sweep decided of a target from its newest record (finishReplacements):
// the round that record ended, "" for a launch's own record; whether its finish committed; and
// whether the walk ends the round as a launch's replacement (walk).
type targetDecided struct {
	round    string
	finished bool
	walk     bool
}

// actKeysOf is every target the act r records takes: its own, and the pod a launch's act took with
// its volume (replacementRecord.WithPod).
func actKeysOf(r store.PodReplacement, rec replacementRecord) []actKey {
	keys := []actKey{{issue: r.Issue, act: r.Act}}
	if rec.WithPod && r.PodUID != "" {
		keys = append(keys, actKey{issue: r.Issue, act: runtime.PodAct(r.PodUID)})
	}
	return keys
}

// decidedOf is what the sweep decided of the first of keys it decided, and whether it decided any.
func decidedOf(decided map[actKey]targetDecided, keys []actKey) (targetDecided, bool) {
	for _, key := range keys {
		if was, ok := decided[key]; ok {
			return was, true
		}
	}
	return targetDecided{}, false
}

// decide records was as what the sweep decided of each of keys.
func decide(decided map[actKey]targetDecided, keys []actKey, was targetDecided) {
	for _, key := range keys {
		decided[key] = was
	}
}

// readAct is the runtime's read of the act that r records (runtime.ActReader), under the pod's
// launch turn, where the record is forgotten in the same turn if the act was not made
// (store.Store.ForgetPodReplacement). A runtime that reads no act answers runtime.ActUncertain.
func (s *supervision) readAct(ctx context.Context, r store.PodReplacement) (runtime.ActState, error) {
	reader, ok := s.runtime.(runtime.ActReader)
	if !ok {
		return runtime.ActUncertain, nil
	}
	state := runtime.ActUncertain
	err := reader.ReadAct(ctx, r.Issue, r.Act, func(ctx context.Context, read runtime.ActState) error {
		state = read
		if read != runtime.ActNotMade {
			return nil
		}
		return s.supervisor.store.ForgetPodReplacement(ctx, s.plan.project, r.Issue, r.Operation)
	})
	return state, err
}
