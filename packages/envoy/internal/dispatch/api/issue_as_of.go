package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/asof"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// The issue read and the issue listing at a cut (task t6 of INFRA-4620's plan). Both run their
// present statements under the cut, which the store binds to the shadows. These are the parts of
// the two answers that the shadows do not give, and that each answer leaves out (leaveOut), or
// reads another way, at a cut.

// refuseListingFilterAtCut answers 400 AS_OF_NOT_SERVED, naming the parameter, for a listing at a
// cut that sends a filter the records at the cut cannot evaluate (decision 5): route_status reads
// the route roster now, and pinned reads one person's pins now; neither keeps a history. It
// reports whether it answered.
func refuseListingFilterAtCut(w http.ResponseWriter, r *http.Request) bool {
	if _, atCut := asof.CutFrom(r.Context()); !atCut {
		return false
	}
	query := r.URL.Query()
	for _, filter := range []struct{ parameter, source string }{
		{"route_status", "the route roster"},
		{"pinned", "the pins of a person"},
	} {
		if query.Has(filter.parameter) {
			writeError(w, contracts.DispatchAsOfNotServed, http.StatusBadRequest, fmt.Sprintf(
				"%s: %s keeps no history, so a listing at a past time cannot filter by %s; send the request with no %s",
				filter.parameter, filter.source, filter.parameter, filter.parameter))
			return true
		}
	}
	return false
}

// issueAtTheCut completes the issue read at the cut ctx carries. The read does not read the route
// roster, which keeps no past state: an issue with a route leaves out the route's reach, and an
// issue a session holds leaves out the holder's liveness, where the present read reads the roster
// (getIssue); the fields are null without a route and without a session holder at any time. It
// leaves out design_gates (no event records them) and referenced_by_count (the reference graph
// keeps no history), which readIssue does not read at a cut, and the members of an explicit
// component attachment, which the present component model splits (an attachment of another mode
// holds no member). Progress follows decision 9 (tasksAtTheCut). The documents leave out the
// documents of an approval request whose newest event at the cut holds none (reason B), and the
// open asks what the records at the cut do not give of them (leaveOutPastAskFields).
func (s *server) issueAtTheCut(ctx context.Context, read *issueDetail) error {
	leaveOut(ctx, "/design_gates", "/referenced_by_count")
	if read.Route != nil {
		leaveOut(ctx, "/route_status", "/route_holder", "/route_status_reason")
	}
	if issueClaimSession(read.Claim) != "" {
		leaveOut(ctx, "/holder_state", "/last_seen", "/takeable_at")
	}
	if read.Components.Mode == "explicit" {
		leaveOut(ctx, "/components/ids", "/components/unknown")
	}
	tasks, err := s.tasksAtTheCut(ctx, []string{read.Key})
	if err != nil {
		return err
	}
	if counted, ok := tasks[read.Key]; ok {
		read.Progress.Tasks = counted
	}
	asks := make([]model.Ask, len(read.OpenAsks))
	for index := range read.OpenAsks {
		asks[index] = read.OpenAsks[index].Ask
	}
	for index, artifact := range read.Artifacts {
		if artifact.Approval != nil && namesApprovalWithNoDocuments(asks, artifact.ID) {
			leaveOut(ctx, "/artifacts/"+strconv.Itoa(index)+"/approval/documents")
		}
	}
	return s.leaveOutPastAskFields(ctx, asks, func(index int) string { return "/open_asks/" + strconv.Itoa(index) })
}

// namesApprovalWithNoDocuments reports whether one of asks is an approval request on the document
// artifactID whose state at the cut names no document: a request written before approval requests
// held their documents, which a migration filled with no event (reason B). The document's approval
// then cannot say which documents the request covered.
func namesApprovalWithNoDocuments(asks []model.Ask, artifactID string) bool {
	for _, ask := range asks {
		if ask.Approval != nil && len(ask.Approval.Documents) == 0 && ask.Approval.ArtifactID == artifactID {
			return true
		}
	}
	return false
}

// issueSummariesAtTheCut completes the listing at the cut ctx carries, each issue at the place
// that prefix and its index name in the answer ("/" for an unpaged answer, "/issues/" for a
// paged one). The listing does not read the route roster: an issue with a route leaves out the
// route's reach, which is null at any time for an issue with none. An issue with an explicit
// component attachment leaves out its members, which the present component model splits.
// Progress follows decision 9 (tasksAtTheCut).
func (s *server) issueSummariesAtTheCut(ctx context.Context, issues []model.IssueSummary, prefix string) error {
	keys := make([]string, len(issues))
	for index := range issues {
		keys[index] = issues[index].Key
	}
	tasks, err := s.tasksAtTheCut(ctx, keys)
	if err != nil {
		return err
	}
	for index := range issues {
		at := prefix + strconv.Itoa(index)
		if counted, ok := tasks[issues[index].Key]; ok {
			issues[index].Progress.Tasks = counted
		}
		if issues[index].Route != nil {
			leaveOut(ctx, at+"/route_status", at+"/route_holder", at+"/route_status_reason")
		}
		if issues[index].Components.Mode == "explicit" {
			leaveOut(ctx, at+"/components/ids", at+"/components/unknown")
		}
	}
	return nil
}

// tasksAtTheCutStatement reads, of the issues whose keys $1 holds, each issue whose primary
// document's newest version at the cut is not the version its stored task count names
// (tasks_version, which the issues shadow gives as the row holds it now), with that version's
// markdown: the newest version that holds markdown, or the empty text. It is the
// reconciliation's own reading of a drifted count (docs.RecordTaskProgressMarkdown,
// latestPrimaryVersion), read through the shadows of artifacts and artifact_versions, so it reads
// the versions at the cut.
const tasksAtTheCutStatement = `
		select i.key,
		       coalesce((
		         select v.markdown from artifacts a join artifact_versions v on v.artifact_id = a.id
		         where a.issue_key = i.key and a.is_primary and v.markdown is not null
		         order by v.number desc limit 1
		       ), '')
		from issues i
		where i.key = any($1::text[]) and i.tasks_version is distinct from (
			select coalesce(max(v.number), 0)
			from artifacts a join artifact_versions v on v.artifact_id = a.id
			where a.issue_key = i.key and a.is_primary
		)`

// tasksAtTheCut is progress.tasks at the cut ctx carries for each of keys whose primary
// document's newest version at the cut is not the version its stored count names (decision 9):
// the task items of that version's markdown, counted as a version write counts them
// (pmdoc.CountTasksMarkdown), nil for none. An issue it does not name keeps the stored count,
// which counts the version at the cut. Markdown the reader cannot read counts as no task, as the
// reconciliation counts it.
func (s *server) tasksAtTheCut(ctx context.Context, keys []string) (map[string]*model.ProgressCount, error) {
	tasks := map[string]*model.ProgressCount{}
	if len(keys) == 0 {
		return tasks, nil
	}
	rows, err := s.deps.Store.Pool.Query(ctx, tasksAtTheCutStatement, keys)
	if err != nil {
		return nil, fmt.Errorf("read the primary documents' versions at the cut: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, markdown string
		if err := rows.Scan(&key, &markdown); err != nil {
			return nil, err
		}
		counted, err := pmdoc.CountTasksMarkdown(markdown)
		if err != nil {
			slog.Warn("dispatch: the issue read at a cut cannot read the spec's version at the cut; counting no task",
				"issue", key, "error", err)
			counted = pmdoc.TaskProgress{}
		}
		tasks[key] = nil
		if counted.Total > 0 {
			tasks[key] = &model.ProgressCount{Done: counted.Done, Total: counted.Total}
		}
	}
	return tasks, rows.Err()
}

// leaveOutPastAskFields names what a past-time answer leaves out of each of asks, the ask at the
// place at(index) names in the answer (decision 5):
//   - referenced_by_count, which the reference graph gives now (reason G);
//   - revision, each option's id and the answer's id, the ask's parts as the present tables hold
//     them, for an ask that changed after the cut (reason P, asof.AsksChangedAfterStatement);
//   - waiting_on_person where the records at the cut give no person: no one handed the ask on,
//     its issue held nobody, and its project's default assignee is unknown at the cut, which the
//     projects shadow gives as null (reason D); the read then gives no person;
//   - approval.documents of an approval request whose state at the cut names no document, written
//     before requests held their documents (reason B).
func (s *server) leaveOutPastAskFields(ctx context.Context, asks []model.Ask, at func(index int) string) error {
	cut, atCut := asof.CutFrom(ctx)
	if !atCut || len(asks) == 0 {
		return nil
	}
	ids := make([]string, len(asks))
	for index := range asks {
		ids[index] = asks[index].ID
	}
	rows, err := s.deps.Store.Pool.Query(ctx, asof.AsksChangedAfterStatement, ids, cut.Time())
	if err != nil {
		return fmt.Errorf("read which asks changed after the cut: %w", err)
	}
	changed := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		changed[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read which asks changed after the cut: %w", err)
	}
	for index, ask := range asks {
		place := at(index)
		leaveOut(ctx, place+"/referenced_by_count")
		if changed[ask.ID] {
			leaveOut(ctx, place+"/revision")
			for option := range ask.Options {
				leaveOut(ctx, place+"/options/"+strconv.Itoa(option)+"/id")
			}
			if ask.Answer != nil {
				leaveOut(ctx, place+"/answer/id")
			}
		}
		if ask.WaitingOnPerson == "" {
			leaveOut(ctx, place+"/waiting_on_person")
		}
		if ask.Approval != nil && len(ask.Approval.Documents) == 0 {
			leaveOut(ctx, place+"/approval/documents")
		}
	}
	return nil
}
