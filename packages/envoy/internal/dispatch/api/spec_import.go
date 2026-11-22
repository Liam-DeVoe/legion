package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/specstore"
)

// The import of an approved level (D9): the level is the spec of its issue, and its notes a second
// document of that issue. The import reads the level at the version the person's newest approval of
// the spec names, and the notes at the version that approval's request names, else the newest one
// written by the time of the approval, and writes them into the approved spec of the issue's
// repository, which must be a spec source of its project, with that approval as provenance
// (specstore.Import). Each statement it cannot resolve is kept as a hold, and the holds of the level
// that are open and name no repair issue get one: a child issue of the level's issue, assigned to
// that issue's assignee, whose spec lists each hold. The child is opened through the issue route's
// own handler, the one writer of an issue, after the import commits, and named on each hold in a
// write of its own, so an import run again opens it where an earlier run stopped before it.

// importLevelInput is the body of POST /api/v1/issues/{key}/spec-import-level.
type importLevelInput struct {
	Notes string       `json:"notes" required:"true"`
	Actor *model.Actor `json:"actor"`
}

// levelApproval is the person's newest approval of a level's spec: its review row, the version it
// approved, its approval request, its person and its time.
type levelApproval struct {
	review  string
	version int
	ask     *string
	person  model.Actor
	at      time.Time
}

// POST /api/v1/issues/{key}/spec-import-level
func (s *server) importSpecLevel(w http.ResponseWriter, r *http.Request) {
	var input importLevelInput
	if err := decodeJSON(r, &input); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	actor, ok := s.requireActor(w, r, input.Actor)
	if !ok {
		return
	}
	key := r.PathValue("key")
	imported, issue, repo, err := s.importLevel(r.Context(), key, strings.TrimSpace(input.Notes), actor)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	answer := model.SpecLevelImport{Issue: key, Repo: repo, Written: []string{}, Held: []model.SpecImportHold{}}
	for _, written := range imported.Written {
		answer.Written = append(answer.Written, written.ID)
	}
	pool, err := s.readyPool()
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	holds, err := levelHolds(r.Context(), pool, issue, repo)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	var unrepaired []specstore.Hold
	for _, hold := range holds {
		provenance, err := s.specProvenanceRead(r.Context(), pool, hold.Provenance)
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		answer.Held = append(answer.Held, model.SpecImportHold{ID: hold.ID, Text: hold.Text, Lacks: hold.Lacks, Provenance: provenance})
		if hold.RepairIssue == "" {
			unrepaired = append(unrepaired, hold)
		} else if answer.RepairIssue == nil {
			answer.RepairIssue = new(hold.RepairIssue)
		}
	}
	if len(unrepaired) > 0 {
		repair := ""
		if answer.RepairIssue != nil {
			repair = *answer.RepairIssue
		} else if repair, err = s.openRepairIssue(r, issue, unrepaired, actor); err != nil {
			s.writeHandlerError(w, err)
			return
		}
		if err := s.nameRepairIssue(r.Context(), issue.Project, repo, unrepaired, repair, actor); err != nil {
			s.writeHandlerError(w, err)
			return
		}
		answer.RepairIssue = &repair
	}
	WriteJSON(w, http.StatusOK, answer)
}

// importLevel imports the level of the issue key in one transaction (specstore.Import) and answers
// what it wrote, the issue and its repository.
func (s *server) importLevel(ctx context.Context, key, notesName string, actor model.Actor) (specstore.Imported, model.Issue, string, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	defer tx.Rollback(ctx)
	issue, err := s.loadIssue(ctx, tx, key)
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	repo, held, err := issueSpecSource(ctx, tx, issue)
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	if !held {
		return specstore.Imported{}, model.Issue{}, "", errorf(http.StatusConflict, "SPEC_SOURCE_REQUIRED",
			"the repository of %s is no spec source of %s, so it holds no approved spec to import the level into; name it one first (PUT /api/v1/projects/%s/spec-sources/{owner}/{repo})", key, issue.Project, issue.Project)
	}
	spec, err := artifactMetadata(ctx, tx, issue.PrimaryArtifactID)
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	approval, err := newestPersonApproval(ctx, tx, spec.ID)
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	if approval == nil {
		return specstore.Imported{}, model.Issue{}, "", errorf(http.StatusConflict, "LEVEL_NOT_APPROVED",
			"no person approved the spec of %s, so no version of the level is approved to import", key)
	}
	notes, notesVersion, err := levelNotes(ctx, tx, key, notesName, approval)
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	levelText, err := storedVersionText(ctx, tx, spec.ID, approval.version)
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	notesText, err := storedVersionText(ctx, tx, notes.ID, notesVersion)
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	level, err := specstore.ParseLevel(levelText, notesText)
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	provenance := specstore.Provenance{Act: specstore.ActImport, Issue: key, Ask: approval.ask, Review: &approval.review, Person: approval.person}
	if provenance.Spec, err = specDocumentOf(ctx, tx, spec, approval.version); err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	if provenance.Notes, err = specDocumentOf(ctx, tx, notes, notesVersion); err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	imported, err := specstore.Import(ctx, tx, specstore.ImportInput{
		Project: issue.Project, Repo: repo, Issue: key, Level: level, Provenance: provenance, Who: actor,
	})
	if err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return specstore.Imported{}, model.Issue{}, "", err
	}
	return imported, issue, repo, nil
}

// newestPersonApproval is the person's newest approval of the document artifactID, nil where none
// approved it.
func newestPersonApproval(ctx context.Context, q queryer, artifactID string) (*levelApproval, error) {
	var approval levelApproval
	var person []byte
	err := q.QueryRow(ctx, `
		select id::text, version, ask_id::text, actor, created_at from artifact_reviews
		where artifact_id = $1::uuid and state = 'approved' and kind = $2
		order by `+docs.ReviewOrder+` limit 1
	`, artifactID, docs.ReviewKindPerson).Scan(&approval.review, &approval.version, &approval.ask, &person, &approval.at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the approval of %s: %w", artifactID, err)
	}
	if err := json.Unmarshal(person, &approval.person); err != nil {
		return nil, fmt.Errorf("decode the person of the approval of %s: %w", artifactID, err)
	}
	return &approval, nil
}

// levelNotes is the notes document of the level issue key, by its name or slug, and the version of
// it the approval names: the one its approval request names for it, else the newest written by the
// time of the approval.
func levelNotes(ctx context.Context, q queryer, key, name string, approval *levelApproval) (model.Artifact, int, error) {
	if name == "" {
		return model.Artifact{}, 0, errorf(http.StatusBadRequest, "INVALID_INPUT", "notes names the level's notes document by its name or slug")
	}
	var id string
	err := q.QueryRow(ctx, `
		select id::text from artifacts
		where issue_key = $1 and kind = 'doc' and (name = $2 or slug = $2) and retired_at is null
		order by created_at limit 1
	`, key, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Artifact{}, 0, errorf(http.StatusNotFound, "NOT_FOUND", "%s holds no document %q", key, name)
	}
	if err != nil {
		return model.Artifact{}, 0, err
	}
	notes, err := artifactMetadata(ctx, q, id)
	if err != nil {
		return model.Artifact{}, 0, err
	}
	if approval.ask != nil {
		var raw []byte
		if err := q.QueryRow(ctx, `select approval from asks where id = $1::uuid`, *approval.ask).Scan(&raw); err != nil {
			return model.Artifact{}, 0, fmt.Errorf("read the approval request %s: %w", *approval.ask, err)
		}
		var request model.AskApproval
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &request); err != nil {
				return model.Artifact{}, 0, fmt.Errorf("decode the approval request %s: %w", *approval.ask, err)
			}
		}
		if named := request.Document(notes.ID); named != nil {
			return notes, named.Version, nil
		}
	}
	var version *int
	if err := q.QueryRow(ctx, `select max(number) from artifact_versions where artifact_id = $1::uuid and created_at <= $2`,
		notes.ID, approval.at).Scan(&version); err != nil {
		return model.Artifact{}, 0, fmt.Errorf("read the version of %s the approval read: %w", name, err)
	}
	if version == nil {
		return model.Artifact{}, 0, errorf(http.StatusConflict, "LEVEL_NOT_APPROVED", "%s held no version of %s when the person approved its spec", key, name)
	}
	return notes, *version, nil
}

// storedVersionText is the stored text of version of the document artifactID.
func storedVersionText(ctx context.Context, q queryer, artifactID string, version int) (string, error) {
	var markdown string
	if err := q.QueryRow(ctx, `select markdown from artifact_versions where artifact_id = $1::uuid and number = $2`, artifactID, version).Scan(&markdown); err != nil {
		return "", fmt.Errorf("read version %d of %s: %w", version, artifactID, err)
	}
	return markdown, nil
}

// levelHolds is each open hold of the repository that the import of issue's level kept.
func levelHolds(ctx context.Context, q queryer, issue model.Issue, repo string) ([]specstore.Hold, error) {
	holds, err := specstore.Holds(ctx, q, issue.Project, repo)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(holds, func(hold specstore.Hold) bool { return hold.Provenance.Issue != issue.Key }), nil
}

// openRepairIssue opens the child issue of the level issue that repairs holds, assigned to the
// level's assignee, through the issue route's own handler, as the request r's caller, and answers
// its key.
func (s *server) openRepairIssue(r *http.Request, issue model.Issue, holds []specstore.Hold, actor model.Actor) (string, error) {
	var spec strings.Builder
	spec.WriteString("The import of the approved level of " + issue.Key + " could not resolve these statements, so the approved spec does not hold them yet. " +
		"For each one, write a change request that supplies what it lacks, and request its approval. The approval writes the statement and clears its hold.\n\n")
	for _, hold := range holds {
		spec.WriteString("- `" + hold.ID + "` lacks " + strings.Join(hold.Lacks, ", ") + ". Its approved text: " + strings.ReplaceAll(hold.Text, "\n", " ") + "\n")
	}
	body := map[string]any{
		"project": issue.Project, "title": "Repair the held statements of the level " + issue.Key,
		"parent": issue.Key, "spec": spec.String(), "force": true, "actor": actor,
	}
	if issue.Assignee != nil {
		body["assignee"] = *issue.Assignee
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	request := r.Clone(r.Context())
	request.Method, request.URL = http.MethodPost, &url.URL{Path: "/api/v1/issues"}
	request.Body, request.ContentLength = io.NopCloser(bytes.NewReader(data)), int64(len(data))
	request.Header.Set("Content-Type", "application/json")
	answer := &capturedResponse{header: http.Header{}}
	s.createIssue(answer, request)
	if answer.status != http.StatusCreated {
		return "", errorf(http.StatusBadGateway, "REPAIR_ISSUE_NOT_OPENED",
			"the import kept %d holds and could not open their repair issue: %d %s; run the import again", len(holds), answer.status, strings.TrimSpace(answer.body.String()))
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(answer.body.Bytes(), &created); err != nil || created.Key == "" {
		return "", fmt.Errorf("read the repair issue the create answered %q: %v", answer.body.String(), err)
	}
	return created.Key, nil
}

// nameRepairIssue names issue the repair of each of holds, on the hold's node.
func (s *server) nameRepairIssue(ctx context.Context, project, repo string, holds []specstore.Hold, issue string, actor model.Actor) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := graphstore.Write(ctx, tx, nil, specstore.RepairIssueFacts(project, repo, holds, issue, actor, time.Now().UTC())); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// capturedResponse is the answer of a handler this server calls itself: its status, headers and
// body.
type capturedResponse struct {
	status int
	header http.Header
	body   bytes.Buffer
}

func (c *capturedResponse) Header() http.Header { return c.header }

func (c *capturedResponse) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *capturedResponse) Write(data []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(data)
}
