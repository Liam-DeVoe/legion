package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/itemblocks"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/rank"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// parseIssuePriority decodes the tri-state `priority` field of an issue write. Absent →
// (nil, false): leave it alone. JSON null → (nil, true): clear it. An integer 0..3 → its
// value. Anything else → 400 INVALID_PRIORITY.
func parseIssuePriority(raw json.RawMessage) (priority *int, provided bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, true, nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < 0 || value > 3 {
		return nil, true, errorf(http.StatusBadRequest, "INVALID_PRIORITY", "priority must be an integer from 0 to 3 or null")
	}
	return &value, true, nil
}

// issueDuplicateError is the 409 POSSIBLE_DUPLICATE body POST /api/v1/issues answers an
// unforced create whose title overlaps an existing issue's.
type issueDuplicateError struct {
	Candidates []model.DuplicateCandidate `json:"candidates"`
	Code       string                     `json:"code"`
	Error      string                     `json:"error"`
}

// createIssueInput is the body of POST /api/v1/issues.
type createIssueInput struct {
	// Project and Title are not required of a caller: a create that names an external
	// owner/repo#n takes the project from that repository's mapping and the title from the
	// external issue (:92-127, :169-170), and only a create that names neither is refused.
	Project   string   `json:"project"`
	Title     string   `json:"title"`
	Parent    *string  `json:"parent"`
	BlockedBy []string `json:"blocked_by"`
	// Advances is the priorities the new issue advances, a list of priority ids. A create
	// carries the whole list and no revision, and is never refused for it: the issue holds no
	// relation yet, so there is no read to be answering (refuseAdvancesRevision).
	Advances   []string        `json:"advances"`
	External   string          `json:"external"`
	Force      bool            `json:"force"`
	Spec       *string         `json:"spec"`
	Labels     []string        `json:"labels"`
	Priority   json.RawMessage `json:"priority"`
	Assignee   json.RawMessage `json:"assignee"`
	Components json.RawMessage `json:"components"`
	Repo       json.RawMessage `json:"repo"`
	Actor      *model.Actor    `json:"actor"`
}

func (s *server) createIssue(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuthenticated(w, r) {
		return
	}
	var input createIssueInput
	if err := decodeJSON(r, &input); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	actor, ok := s.requireActor(w, r, input.Actor)
	if !ok {
		return
	}
	blockers, err := normalizeIssueBlockers(input.BlockedBy)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	advances, err := normalizeIssueAdvances(input.Advances)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	input.Project = strings.TrimSpace(input.Project)
	input.Title = strings.TrimSpace(input.Title)
	parentKey := ""
	if input.Parent != nil {
		parentKey = strings.TrimSpace(*input.Parent)
	}

	var externalRepo, externalNumber string
	usingDefaultProject := false
	if input.External != "" {
		var err error
		externalRepo, externalNumber, err = externalRef(input.External)
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		project, err := s.repoProject(r.Context(), externalRepo)
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		if project == "" {
			project = s.deps.DefaultProject
			if project == "" {
				writeError(w, "PROJECT_UNMAPPED", http.StatusBadRequest,
					"repository is not mapped in repository settings and DISPATCH_DEFAULT_PROJECT is not configured")
				return
			}
			usingDefaultProject = true
		}
		if input.Project != "" && input.Project != project {
			writeError(
				w,
				"EXTERNAL_PROJECT_MISMATCH",
				http.StatusBadRequest,
				fmt.Sprintf(
					"external issue project %s must match repository settings project %s",
					input.Project,
					project,
				),
			)
			return
		}
		input.Project = project
	}
	// The one write whose project its address does not name, so withinProjectScope leaves it
	// here by its class (scopeProjectInBody). It runs the moment that project is settled —
	// read from the body above, or from the repository the external ref names — and so before
	// the ref is resolved to an issue already imported: a scoped caller naming one imported
	// into another project is refused rather than answered that project's issue, which
	// respondWithExistingExternalIssue would also record a related-check outcome on. Nothing
	// is written before this point.
	// callerProject is empty for a credential that names none, which is every caller that
	// exists today; the dependency lock step and writeBlockedBy below read it again, since a
	// blocked_by target's project is no part of this address.
	callerProject, scoped := s.callerProject(r)
	if scoped && callerProject != input.Project {
		refuseWriteOutsideProjectScope(w, callerProject, input.Project)
		return
	}
	if input.External != "" {
		existingKey, err := s.resolveIssueRef(r.Context(), input.External)
		if err == nil {
			var existingProject string
			if err := s.deps.Store.Pool.QueryRow(r.Context(), `
				select project_key from issues where key = $1
			`, existingKey).Scan(&existingProject); err != nil {
				s.writeHandlerError(w, err)
				return
			}
			if existingProject != input.Project {
				writeError(
					w,
					"EXTERNAL_PROJECT_MISMATCH",
					http.StatusBadRequest,
					fmt.Sprintf(
						"external issue project %s must match repository settings project %s",
						existingProject,
						input.Project,
					),
				)
				return
			}
			s.respondWithExistingExternalIssue(w, r, existingKey, input.Project, actor)
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			s.writeHandlerError(w, err)
			return
		}
		if input.Title == "" {
			input.Title = input.External
		}
	}
	if !projectKeyPattern.MatchString(input.Project) {
		writeError(w, "INVALID_ISSUE", http.StatusBadRequest, projectKeyProblem("project", input.Project))
		return
	}
	if input.Title == "" {
		writeError(w, "INVALID_ISSUE", http.StatusBadRequest, "title is required")
		return
	}
	// Before the duplicate check, which reads the new title against every title in the project.
	if length := itemblocks.Units16(input.Title); length > contracts.IssueTitleMax {
		capExceeded(w, "title", length, contracts.IssueTitleMax)
		return
	}
	if usingDefaultProject {
		input.Labels = append(input.Labels, repoLabelPrefix+externalRepo)
	}
	labels, err := normalizeIssueLabels(input.Labels)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	priority, _, err := parseIssuePriority(input.Priority)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// A create names a person or nobody: no assignee key and a JSON null both leave assignee nil,
	// and defaultAssignee below fills it in, so a new issue always has somebody on it. Only the
	// PATCH clears an issue's assignee.
	assignee, _, err := s.parseIssueAssignee(r.Context(), input.Assignee)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	components, componentsProvided, err := parseIssueComponents(input.Components)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	repo, _, err := parseIssueRepo(input.Repo)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// The near-copy gate, run once for every create it covers whatever `force` says: a forced
	// create's record of what it went past must be the refusal an unforced create would have met,
	// and two reads would drift. The gate bounds and logs its own read (duplicateGate), and a read
	// that does not answer refuses nothing: the create goes in and is told that no near-copy check
	// was made, forced or not, a forced one also recording that it was forced with no issues named.
	//
	// A create naming a GitHub issue runs no gate at all: it imports an issue somebody else
	// already filed and refusing it would leave that issue unimported. It takes the zero
	// outcome, which records nothing anywhere, so the event cannot say a check was made that
	// never ran. Every record of the gate - the 409, the event's `forced`, `title_matches`,
	// `likely_duplicate` and `near_copy_check` - is settled here, in the one place that knows
	// whether the gate ran, and copied below rather than derived again from `force` and
	// `external` at each site.
	//
	// The gate's candidate set leaves out the new issue's whole ancestor chain, which is read
	// here, before the transaction. A parent that does not exist or sits in another project
	// yields an empty chain and still gets its own refusal inside the transaction, so no error
	// changes order.
	var outcome nearCopyOutcome
	if input.External == "" {
		ancestors := []string{}
		if parentKey != "" {
			chain, err := refs.IssueAncestors(r.Context(), s.deps.Store.Pool, parentKey)
			if err != nil {
				s.writeHandlerError(w, err)
				return
			}
			ancestors = append([]string{parentKey}, chain...)
		}
		spec := ""
		if input.Spec != nil {
			spec = *input.Spec
		}
		outcome = s.runNearCopyGate(r.Context(), input.Project, input.Title, spec, ancestors, input.Force)
		if len(outcome.Refuse) > 0 {
			WriteJSON(w, http.StatusConflict, issueDuplicateError{
				Candidates: outcome.Refuse,
				Code:       "POSSIBLE_DUPLICATE",
				Error:      fmt.Sprintf("possible duplicate of %s: %s", outcome.Refuse[0].Key, outcome.Refuse[0].Title),
			})
			return
		}
	}
	// The reviewer's hook, which reads the title, the spec and a components reason with the writing
	// check before anything is written or locked: either may call a model, and no transaction waits
	// on it.
	create := issueCreate{
		Project: input.Project, Title: input.Title, Parent: parentKey, Blockers: blockers, Advances: advances,
		Labels: labels, Priority: priority, Assignee: assignee, Components: components,
		ComponentsProvided: componentsProvided, Repo: repo, Spec: input.Spec, External: input.External,
		ExternalRepo: externalRepo, ExternalNumber: externalNumber, CallerProject: callerProject,
		Forced: outcome.Forced,
	}
	after, proceed := s.reviewWrite(w, r, actor, input, create.reviewed())
	if !proceed {
		return
	}

	tx, err := s.begin(r.Context())
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	written, err := s.writeIssue(r.Context(), tx, create, actor, 0, nil)
	if errors.Is(err, errExternalIssueImported) {
		if err := tx.Rollback(r.Context()); err != nil {
			s.writeHandlerError(w, err)
			return
		}
		existingKey, err := s.resolveIssueRef(r.Context(), input.External)
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		s.respondWithExistingExternalIssue(w, r, existingKey, input.Project, actor)
		return
	}
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	s.announceIssue(r.Context(), written)
	issue, advice, key := written.issue, written.advice, written.issue.Key
	s.reviewPosted(r.Context(), after, key)
	if advice != nil && input.Spec != nil && strings.TrimSpace(*input.Spec) != "" {
		advice.documentBlocks = documentBlocksFor(written.readBack, written.markdown)
	}
	// The related-item check runs after the write committed, whatever writeAdvice
	// answered, and waits at most relatedAllowance; its result rides the advice, or advice of its
	// own when the write-advice read failed. An answered ask it offers carries the display tree
	// of that answer where this request asked for the trees, which its reader fills before the
	// response is written.
	trees := s.readDisplayTrees(r.Context(), wantsDisplayTrees(r))
	suggestions := s.relatedCheck(r.Context(), relatedInput{
		project: input.Project,
		text:    input.Title + "\n" + written.markdown,
		title:   input.Title,
		source:  suggestionSource{kind: "issue", issueKey: key, actor: actor},
		record:  true,
		trees:   trees,
	})
	trees.resolve()
	suggestions.TitleMatches = outcome.TitleMatches
	suggestions.LikelyDuplicate = outcome.LikelyDuplicate
	suggestions.NearCopyCheck = outcome.NearCopyCheck
	WriteJSON(w, http.StatusCreated, withSuggestions(issue, advice, suggestions))
}

// errExternalIssueImported is writeIssue finding the GitHub issue its create names imported
// already, by another create that committed first: createIssue answers with that issue.
var errExternalIssueImported = errors.New("the external issue is imported already")

// issueCreate is an issue a write files, decoded and checked (createIssue): what writeIssue
// writes, and what a held issue stores, so that its post files the issue the write itself would
// have filed. Forced is what the near-copy gate recorded of a forced create (nearCopyOutcome).
type issueCreate struct {
	Project            string              `json:"project"`
	Title              string              `json:"title"`
	Parent             string              `json:"parent,omitempty"`
	Blockers           []string            `json:"blockers,omitempty"`
	Advances           []string            `json:"advances,omitempty"`
	Labels             []string            `json:"labels"`
	Priority           *int                `json:"priority,omitempty"`
	Assignee           *string             `json:"assignee,omitempty"`
	Components         *componentsInput    `json:"components,omitempty"`
	ComponentsProvided bool                `json:"components_provided,omitempty"`
	Repo               *string             `json:"repo,omitempty"`
	Spec               *string             `json:"spec,omitempty"`
	External           string              `json:"external,omitempty"`
	ExternalRepo       string              `json:"external_repo,omitempty"`
	ExternalNumber     string              `json:"external_number,omitempty"`
	CallerProject      string              `json:"caller_project,omitempty"`
	Forced             *model.ForcedCreate `json:"forced,omitempty"`
}

// issueWritten is what writeIssue wrote: the issue, its issue.created event, the advice the
// write answers with (nil for a held issue's post, which answers nobody), its spec's document,
// and the spec as the seed stored it with that rendering read back.
type issueWritten struct {
	issue      model.Issue
	event      model.Event
	advice     *writeAdvice
	artifactID string
	markdown   string
	readBack   *pmdoc.Node
}

// writeIssue files create as actor's issue in tx, the issue's rows, its spec's first version and
// its issue.created event, and commits tx. number is the issue's number: 0 takes the next of its
// project's sequence, which createIssue does, and a held issue's post passes the number its hold
// reserved (held, whose row it takes first and marks posted in the same transaction).
func (s *server) writeIssue(ctx context.Context, tx pgx.Tx, create issueCreate, actor model.Actor, number int, held *heldPost) (issueWritten, error) {
	parentKey, assignee := create.Parent, create.Assignee
	if err := held.lock(ctx, tx); err != nil {
		return issueWritten{}, err
	}
	// A create with blockers can close a wait cycle, and a create with a parent adds a link the
	// depth cap counts (refuseParentTooDeep): both take the dependency locks before the number
	// or the rank (lockIssueDependencyWrite). A new issue with neither takes none. Only the
	// blockers can name another project, since a parent is of the issue's own project.
	if len(create.Blockers) > 0 || parentKey != "" {
		if err := lockIssueDependencyWrite(ctx, tx, create.Project, create.CallerProject, create.Blockers); err != nil {
			return issueWritten{}, err
		}
	}
	var parentAssignee *string
	if parentKey != "" {
		var parentProject string
		err := tx.QueryRow(ctx, `select project_key, assignee from issues where key = $1`, parentKey).Scan(&parentProject, &parentAssignee)
		if errors.Is(err, pgx.ErrNoRows) {
			return issueWritten{}, errorf(http.StatusBadRequest, "PARENT_INPUT", "parent issue %s not found", parentKey)
		}
		if err != nil {
			return issueWritten{}, err
		}
		if parentProject != create.Project {
			return issueWritten{}, errorf(http.StatusBadRequest, "PARENT_INPUT", "parent must be in the same project")
		}
		if err := refuseParentTooDeep(ctx, tx, "", parentKey); err != nil {
			return issueWritten{}, err
		}
	}
	var projectDefaultAssignee string
	if number == 0 {
		if err := tx.QueryRow(ctx, `
			update projects
			set next_number = next_number + 1
			where key = $1
			-- default_assignee as it stands: the column names a person and refuses nothing
			-- (store/migrations/20261111210103_projects_default_assignee_not_null.up.sql), so the
			-- chain's last step always lands and this statement reads no other table for it.
			returning next_number - 1, default_assignee
		`, create.Project).Scan(&number, &projectDefaultAssignee); err != nil {
			return issueWritten{}, err
		}
	} else if err := tx.QueryRow(ctx, `
		-- A number its hold reserved (reserveIssueNumber): the project row is locked as the
		-- update above locks it, so the order of the locks below is the same.
		select default_assignee from projects where key = $1 for no key update
	`, create.Project).Scan(&projectDefaultAssignee); err != nil {
		return issueWritten{}, err
	}
	// The project's stored default is the chain's last step, and this update already holds the
	// project row: the decision moves below it so the create takes no round trip and no lock of
	// its own for it.
	if assignee == nil {
		assignee = defaultAssignee(actor, parentAssignee, projectDefaultAssignee)
	}
	// The project row is locked from here; the mapping check takes its key-share lock only now, in
	// the project-then-mapping order every mapping write follows (lockRepoProjectWrite).
	if create.Repo != nil {
		if err := checkIssueRepoMapped(ctx, tx, *create.Repo, create.Project); err != nil {
			return issueWritten{}, err
		}
	}
	key := fmt.Sprintf("%s-%d", create.Project, number)
	actorJSON, err := encodeJSON(actor)
	if err != nil {
		return issueWritten{}, err
	}
	var parent any
	if parentKey != "" {
		parent = parentKey
	}
	if err := lockProjectRankAllocation(ctx, tx, create.Project); err != nil {
		return issueWritten{}, err
	}

	lastRank, err := lastProjectRank(ctx, tx, create.Project, "")
	if err != nil {
		return issueWritten{}, err
	}
	issueRank := rank.Between(lastRank, "")

	if _, err := tx.Exec(ctx, `
		insert into issues (key, project_key, number, title, parent_key, created_by, labels, priority, rank, assignee, repo)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, key, create.Project, number, create.Title, parent, actorJSON, create.Labels, create.Priority, issueRank, assignee, create.Repo); err != nil {
		return issueWritten{}, err
	}
	if err := writeBlockedBy(ctx, tx, key, create.Project, create.CallerProject, create.Blockers); err != nil {
		return issueWritten{}, err
	}
	// The new issue holds no relation, so every id it names is stated and none is retracted.
	if len(create.Advances) > 0 {
		if err := writeIssueAdvances(ctx, tx, key, nil, create.Advances, actor, time.Now().UTC()); err != nil {
			return issueWritten{}, err
		}
	}
	if create.ComponentsProvided {
		if err := writeIssueComponents(ctx, tx, key, create.Project, *create.Components); err != nil {
			return issueWritten{}, err
		}
	}
	if create.External != "" {
		if _, err := tx.Exec(ctx, `
			insert into issue_external_links (issue_key, url, kind) values ($1, $2, 'github_issue')
		`, key, externalURL(create.ExternalRepo, create.ExternalNumber)); err != nil {
			if isUniqueViolation(err) {
				return issueWritten{}, errExternalIssueImported
			}
			return issueWritten{}, err
		}
	}

	var artifactID string
	if err := tx.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ($1, $2, 'spec', 'spec.md', 'doc', true, $3)
		returning id::text
	`, key, create.Project, actorJSON).Scan(&artifactID); err != nil {
		return issueWritten{}, err
	}
	// An issue created without a spec gets an empty primary document.
	var markdown string
	if create.Spec != nil && strings.TrimSpace(*create.Spec) != "" {
		markdown = *create.Spec
	}
	documentCtx, ledger := s.deps.Docs.Join(ctx, tx)
	defer ledger.Discard()
	markdown, err = s.deps.Docs.SeedText(documentCtx, artifactID, markdown, actor)
	if err != nil {
		return issueWritten{}, err
	}
	authors, err := encodeJSON([]model.Actor{actor})
	if err != nil {
		return issueWritten{}, err
	}
	// The seed's own rendering is what this row stores, so the row records what it was rendered
	// from (docs.Ledger.RenderStampOf): a text read of a document still standing there serves the
	// stored markdown rather than rendering the document again.
	stamp := ledger.RenderStampOf(artifactID, markdown)
	if _, err := tx.Exec(ctx, docs.RenderStampHead+`
		insert into artifact_versions (artifact_id, number, markdown, authors, doc_update_version, `+docs.RenderStampColumns+`)
		values ($1, 1, $2, $3, coalesce((select max(version) from doc_updates where artifact_id = $1), 0),
			`+docs.RenderStampValues+`, $4, $5)
	`, artifactID, markdown, authors, stamp.Token, stamp.Renderer); err != nil {
		return issueWritten{}, err
	}
	// The spec is a stored text, so its display tree is stored once this transaction commits
	// (docs.Ledger.storeVersionTrees); this route writes the version row itself.
	ledger.WroteVersionText(artifactID, markdown)
	// documentReadBack is the rendering the seed stored, read back by its own parse (docs'
	// Ledger.ReadBackOf), which the task count and the advice's blocks read in place of parsing
	// the same markdown again (recordDocumentTaskProgress, documentBlocksFor).
	documentReadBack := ledger.ReadBackOf(artifactID, markdown)
	// The version row exists now, so the count names it (LEGION-542).
	err = recordDocumentTaskProgress(ctx, tx, artifactID, documentReadBack, markdown)
	if err != nil {
		return issueWritten{}, err
	}
	specChanges, err := s.deps.Docs.IndexDocumentReferences(documentCtx, artifactID, 1, markdown)
	if err != nil {
		return issueWritten{}, err
	}
	issue, err := s.loadIssue(ctx, tx, key)
	if err != nil {
		return issueWritten{}, err
	}
	issue.LastSeq++
	// A new issue's blocked_by edges move each blocker's count, beside what its spec cites.
	blockerChanges, err := issueEdgeChanges(ctx, tx, unsetIssue, issue, false)
	if err != nil {
		return issueWritten{}, err
	}
	// A new issue changed every field it holds a value in, measured from an issue with none.
	payload, err := newIssueEventPayload(ctx, tx, issue, refs.MergeChanges(specChanges, blockerChanges), issueChangedFields(unsetIssue, issue))
	if err != nil {
		return issueWritten{}, err
	}
	payload.Forced = create.Forced
	event, err := s.appendEvent(ctx, tx, issueOwner(key).event("issue.created", actor, payload))
	if err != nil {
		return issueWritten{}, err
	}
	if err := refs.Stamp(ctx, tx, "artifact", artifactID, event.ID); err != nil {
		return issueWritten{}, err
	}
	var advice *writeAdvice
	if held == nil {
		advice = s.writeAdvice(
			ctx, tx, "POST /api/v1/issues", key, actor, "", issue.Status, false,
		)
	}
	if err := held.finish(ctx, tx); err != nil {
		return issueWritten{}, err
	}
	if err := ledger.Commit(ctx); err != nil {
		return issueWritten{}, err
	}
	return issueWritten{
		issue: issue, event: event, advice: advice, artifactID: artifactID, markdown: markdown, readBack: documentReadBack,
	}, nil
}

// announceIssue is what follows an issue's commit, a held issue's post as much as the write's
// own: its spec's settlement is queued and its event published.
func (s *server) announceIssue(ctx context.Context, written issueWritten) {
	// A seeded spec never passed through a live edit, so nothing has queued the document
	// closer that indexes its ask blocks and repairs block ids; queue it now that the
	// transaction is durable.
	s.deps.Docs.ScheduleSettlement(written.artifactID)
	s.publish(ctx, written.event)
}

// duplicateGateTimeout bounds the near-copy gate's whole read, on the context and as the read
// transaction's own statement_timeout. Every create waits on it before it is written, so it is
// held well under the second the title rule it replaces was given: past it the create goes in
// and is told that no near-copy check was made, which is what a slow read must cost rather than
// a project nobody can write to.
const duplicateGateTimeout = 500 * time.Millisecond

// nearCopyOutcome is everything the near-copy gate decides for one create, settled once by
// runNearCopyGate and then only copied: the 409 the create is refused with, what the
// issue.created event records, and what the response tells the filer. The zero value is the
// create the gate never ran for - an external create - which records nothing and says nothing,
// so no reader of the event log is told of a check that was never made.
type nearCopyOutcome struct {
	// Refuse is what a 409 POSSIBLE_DUPLICATE names, and is empty on every create that goes in:
	// a create the gate refuses is never written.
	Refuse []model.DuplicateCandidate
	// Forced is what the issue.created event stores about a create that went in with `force`, in
	// the three states dispatch://INFRA-1160/spec#b-r3 names:
	//
	//	forced past a refusal       {"past": ["INFRA-724"]}
	//	the check did not answer    {"past": []}
	//	the refusal would not have stopped it, no force, or no check  nothing
	//
	// Past carries no omitempty, so the empty list survives the write and a reader can tell the
	// second state from the third. The weekly count reads it from the event.
	Forced *model.ForcedCreate
	// TitleMatches is advice.suggestions.title_matches: the issues a forced create went past,
	// which every released client prints as a near copy it forced past. An unforced create never
	// fills it, whatever the gate found - its nudge rides in LikelyDuplicate, a field of its own,
	// because Dispatch deploys before the CLI reaches agents and a nudge carried here would tell
	// every nudged filer it forced past something it did not.
	TitleMatches []model.DuplicateCandidate
	// LikelyDuplicate is advice.suggestions.likely_duplicate: the closest open issue of an
	// unforced create the gate nudged rather than refused.
	LikelyDuplicate *model.DuplicateCandidate
	// NearCopyCheck is advice.suggestions.near_copy_check: why the create went in unchecked,
	// empty when the gate answered.
	NearCopyCheck string
}

// runNearCopyGate runs the gate for a create and settles every record of it at once. Only a
// create the gate covers reaches it: createIssue leaves the zero outcome for an external create,
// whose gate never runs.
//
// The four states are the gate's own: it did not answer, it found nothing to say, it found a
// refusal the create forced past, or it found one the create is refused with. Each sets what
// that state records and leaves the rest zero, so no site downstream reads `force` or `external`
// again and no record can disagree with another.
func (s *server) runNearCopyGate(
	ctx context.Context, project, title, spec string, ancestors []string, force bool,
) nearCopyOutcome {
	gate := s.duplicateGate(ctx, project, title, spec, ancestors)
	refusals := gate.refusals()
	switch {
	case !gate.answered():
		outcome := nearCopyOutcome{NearCopyCheck: gate.Unanswered}
		if force {
			outcome.Forced = &model.ForcedCreate{Past: []string{}}
		}
		return outcome
	case len(refusals) == 0:
		if force {
			return nearCopyOutcome{}
		}
		return nearCopyOutcome{LikelyDuplicate: gate.nudge()}
	case !force:
		return nearCopyOutcome{Refuse: refusals}
	}
	past := make([]string, 0, len(refusals))
	for _, candidate := range refusals {
		past = append(past, candidate.Key)
	}
	return nearCopyOutcome{Forced: &model.ForcedCreate{Past: past}, TitleMatches: refusals}
}

// respondWithExistingExternalIssue answers a create naming a GitHub issue Dispatch already holds
// with that issue. Nothing was created, so no related-item check is made, and the result says so
// (and the check is recorded as not made, reason already_exists).
func (s *server) respondWithExistingExternalIssue(
	w http.ResponseWriter,
	r *http.Request,
	existingKey string,
	project string,
	actor model.Actor,
) {
	adviceReadTx, err := s.begin(r.Context())
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	defer adviceReadTx.Rollback(r.Context())
	var existingStatus string
	if err := adviceReadTx.QueryRow(r.Context(), `
		select status from issues where key = $1 for no key update
	`, existingKey).Scan(&existingStatus); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	existing, err := s.loadIssue(r.Context(), adviceReadTx, existingKey)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	advice := s.writeAdvice(
		r.Context(), adviceReadTx, "POST /api/v1/issues", existingKey, actor, "", existingStatus, false,
	)
	if err := adviceReadTx.Commit(r.Context()); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	suggestions := s.relatedCheckNotRun(r.Context(), relatedInput{
		project: project,
		source:  suggestionSource{kind: "issue", issueKey: existingKey, actor: actor},
		record:  true,
	}, relatedReasonAlreadyExists, "Dispatch already holds this issue; nothing was created")
	WriteJSON(w, http.StatusOK, withSuggestions(existing, advice, suggestions))
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}
