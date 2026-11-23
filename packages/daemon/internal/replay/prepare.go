package replay

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/store/storetext"
)

// prepare readies fact, a decoded Dispatch event, as the daemon's Dispatch consumer readies one
// before it applies it (internal/daemon's prepareRepository and prepareArtifact), with every read of
// Dispatch answered from the replay's copy of it (the shadow) and every repository served
// (replayRepositories), so the preparation reads nothing outside. It answers the fact to apply and
// the record of its issue as the preparation read it, nil for an event of another kind or an issue
// the replay does not record, which the run's count of fenced events reads (countFence).
func (d *driver) prepare(fact intake.Fact) (intake.Fact, *record.Issue, error) {
	switch f := fact.(type) {
	case intake.DispatchIssue:
		recorded, err := d.recorded(f.Key)
		if err != nil {
			return nil, nil, err
		}
		prepared, err := d.prepareIssue(f, recorded)
		return prepared, recorded, err
	case intake.DispatchArtifact:
		// A person's approval that is not Dispatch's carry of an earlier one reads the issue's
		// detail, from which the workflow decides whether it queues a parked labelled root.
		if f.PersonApproval() {
			if iss, ok := d.shadow.issues[f.Key]; ok && iss.payload != nil {
				detail := d.shadow.detail(iss)
				f.Issue = &detail
			}
		}
		return f, nil, nil
	}
	return fact, nil, nil
}

// countFence counts fact as dropped by a sequence fence (Divergence.Fenced) where it is an issue
// event at or below the sequence its record has applied already (record.Issue.LastDispatchSeq),
// which the daemon's fences drop: the shadow numbers every event it makes past the last, so a
// count above zero is a sequence the replay got out of step.
func (d *driver) countFence(fact intake.Fact, recorded *record.Issue) {
	issue, ok := fact.(intake.DispatchIssue)
	if ok && recorded != nil && issue.Seq != 0 && issue.Seq <= recorded.LastDispatchSeq {
		d.counters.Fenced++
	}
}

// prepareIssue readies an issue event of an issue recorded as recorded (nil for none), as the
// daemon's prepareIssue does: a recorded issue's event takes the record's repository where it names
// none; an unrecorded labelled root in triage reads who set its label from its event log; an
// unrecorded issue's admission event is applied from Dispatch's view of the issue now (an Observed
// fact carrying the event it stands in for); and each reads its Dispatch parent chain where the
// waiting line may hold it under an ancestor (withAncestry), and the chains of the queued roots an
// event may have moved.
func (d *driver) prepareIssue(issue intake.DispatchIssue, recorded *record.Issue) (intake.DispatchIssue, error) {
	if recorded != nil {
		if issue.ResolvedRepoMissing {
			issue.ResolvedRepo, issue.ResolvedRepoMissing = recorded.Repo, false
		}
		return d.withAncestry(issue, recorded)
	}
	if !admissionBound(issue) {
		if issue.LabelledTriageRoot() {
			setter, err := d.shadow.labelSetter(issue.Key)
			if err != nil {
				return issue, err
			}
			issue.LabelSetter = setter
		}
		return d.withAncestry(d.prepareUnrecorded(issue), nil)
	}
	if iss, ok := d.shadow.issues[issue.Key]; ok && iss.payload != nil {
		event := issue
		issue = d.shadow.observedIssue(iss)
		issue.Seq, issue.Delivered = max(event.Seq, issue.Seq), &event
	}
	return d.withAncestry(d.prepareUnrecorded(issue), nil)
}

// prepareListed readies one issue of a boot's listing, as the daemon's prepareListed does: a
// recorded issue takes the record's repository where it names none, an unrecorded one the shadow's,
// and a labelled todo under a parent reads its Dispatch parent chain; no other root's chain is read
// again, since the listing holds every queued root's own todo.
func (d *driver) prepareListed(issue intake.DispatchIssue) (intake.DispatchIssue, error) {
	recorded, err := d.recorded(issue.Key)
	if err != nil {
		return issue, err
	}
	if recorded != nil {
		if issue.ResolvedRepoMissing {
			issue.ResolvedRepo, issue.ResolvedRepoMissing = recorded.Repo, false
		}
	} else {
		issue = d.prepareUnrecorded(issue)
	}
	if queuesUnderParent(issue, recorded) {
		issue.Ancestry = d.shadow.chain(issue.Key, issue.Parent)
	}
	return issue, nil
}

// prepareUnrecorded readies the event of an issue the workflow does not record: one that names no
// repository takes the one the shadow's payload resolves it to, as the daemon reads it from
// Dispatch's detail. Every repository is served (replayRepositories), so nothing is held for a read
// of a mapping or of GitHub.
func (d *driver) prepareUnrecorded(issue intake.DispatchIssue) intake.DispatchIssue {
	if issue.ResolvedRepoMissing {
		if iss, ok := d.shadow.issues[issue.Key]; ok {
			issue.ResolvedRepo, issue.ResolvedRepoMissing = iss.str("resolved_repo"), false
		}
	}
	return issue
}

// withAncestry is issue, recorded as recorded, with what the daemon's preparation reads up
// Dispatch's parent chains (internal/daemon's withAncestry), from the shadow: its own chain where it
// is a labelled todo under a parent, unrecorded or recorded as a root (queuesUnderParent); and, for
// an event of an issue the workflow does not record or a fresh event of a recorded issue whose parent
// is not its record's, the chain of every queued root whose recorded chain holds the issue, carried
// as Reread where it differs from the record's.
func (d *driver) withAncestry(issue intake.DispatchIssue, recorded *record.Issue) (intake.DispatchIssue, error) {
	if queuesUnderParent(issue, recorded) {
		issue.Ancestry = d.shadow.chain(issue.Key, issue.Parent)
	}
	if recorded != nil && (storetext.Text(issue.Parent) == parentOf(recorded) || issue.Seq != 0 && issue.Seq <= recorded.LastDispatchSeq) {
		return issue, nil
	}
	var roots []record.Issue
	if err := store.TransactWith(d.ctx, d.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		roots, err = d.records.QueuedRootsUnder(ctx, tx, issue.Key)
		return err
	}); err != nil {
		return issue, fmt.Errorf("read the queued roots whose chain holds %s: %w", issue.Key, err)
	}
	for _, root := range roots {
		chain := d.shadow.chain(root.Key, parentOf(&root))
		if slices.Equal(storetext.Strings(chain), root.Ancestry) {
			continue
		}
		if issue.Reread == nil {
			issue.Reread = map[string][]string{}
		}
		issue.Reread[root.Key] = chain
	}
	return issue, nil
}

// admissionBound says whether issue is an event admission would record (internal/daemon's
// admissionBound): a todo handed to Legion, or one under a parent.
func admissionBound(issue intake.DispatchIssue) bool {
	return issue.Status == "todo" && (issue.HandedOver || issue.Parent != "")
}

// queuesUnderParent says whether issue, recorded as recorded (nil for none), is a root the waiting
// line may hold under an unreleased child of an ancestor's tree (internal/daemon's
// queuesUnderParent): a labelled todo under a Dispatch parent, unrecorded or recorded as a root.
func queuesUnderParent(issue intake.DispatchIssue, recorded *record.Issue) bool {
	return issue.Status == "todo" && issue.HandedOver && issue.Parent != "" &&
		(recorded == nil || claim.IsTreeRoot(recorded.Key, recorded.Tree))
}

// parentOf is the Dispatch parent issue's record keeps, "" for none.
func parentOf(issue *record.Issue) string {
	if issue == nil || issue.Parent == nil {
		return ""
	}
	return *issue.Parent
}

// observedIssue is iss as Dispatch's detail shows it now, as an Observed fact at the sequence the
// shadow has run to (intake.ObservedIssue).
func (s *shadow) observedIssue(iss *shadowIssue) intake.DispatchIssue {
	claimed, known := iss.claimedBy()
	labels := iss.labels()
	return intake.DispatchIssue{Key: iss.key, Seq: iss.seq, Status: iss.status(), Title: iss.str("title"), Parent: iss.parent(),
		Rank: s.rank(iss), HandedOver: dispatch.CarriesLegionLabel(labels), Labels: labels, ResolvedRepo: iss.str("resolved_repo"),
		Blockers: iss.blockers(), ClaimedBy: claimed, ClaimKnown: known, Observed: true}
}

// detail is iss as Dispatch's issue detail serves it (dispatch.Issue), at the rank the replay shows
// it at.
func (s *shadow) detail(iss *shadowIssue) dispatch.Issue {
	var parent *string
	if p := iss.parent(); p != "" {
		parent = &p
	}
	claimed, known := iss.claimedBy()
	var held *dispatch.Claim
	if kind, id, ok := strings.Cut(claimed, ":"); ok {
		held = &dispatch.Claim{Kind: kind, ID: id}
	}
	labels := iss.labels()
	return dispatch.Issue{Key: iss.key, Project: iss.project, Title: iss.str("title"), Status: iss.status(), Parent: parent,
		Rank: s.rank(iss), Labels: labels, HandedOver: dispatch.CarriesLegionLabel(labels), PrimaryArtifactID: iss.str("primary_artifact_id"),
		LastSeq: iss.seq, ResolvedRepo: iss.str("resolved_repo"), BlockedBy: iss.blockers(), Claim: held, ClaimUnread: !known}
}

// labelSetter is who added the legion label to key, read from the events the replay applied of it
// by intake.LabelSetter's rule, as the daemon reads it from the issue's whole event log.
func (s *shadow) labelSetter(key string) (string, error) {
	iss, ok := s.issues[key]
	if !ok {
		return "", nil
	}
	setter, err := intake.LabelSetter(iss.log)
	if err != nil {
		return "", fmt.Errorf("read who set the label of %s from its events: %w", key, err)
	}
	return setter, nil
}
