package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/rank"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// WriteSeed writes the seed into a migrated Dispatch database, in one transaction, as the rows a
// Dispatch that lived through the seed's history would hold: every time the seed fixes (an issue's
// creation and close, a claim, an ask, its answer, a comment, a message) is the row's own, which
// no route can set. The writes follow cmd/dispatch-seed's: each document, ask, comment and message
// is indexed with the server's own indexers in the same transaction, each ask has its opening
// event, and each owner's event counter and each project's next issue number are settled, so a
// server started on the database reads every item at once and rewrites none of them.
//
// The ids of the seed's asks, comments and messages are uuids made from the seed's own (ItemID).
// The sessions are no rows of Dispatch: Envoy holds them, so a caller hands Seed.Sessions to the
// Envoy its server reads.
//
// It answers false and writes nothing when the database already holds the seed's projects, which
// is a database this function seeded before, since its one transaction writes all of the seed or
// none of it. A database holding any other project is refused.
func WriteSeed(ctx context.Context, database *store.Store, seed *Seed) (bool, error) {
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	seeded, err := holdsTheSeed(ctx, tx, seed)
	if err != nil || seeded {
		return false, err
	}
	w := &seedWriter{tx: tx, seed: seed, sessions: map[string]bool{}, seqs: map[string]int{}}
	for _, session := range seed.Sessions {
		w.sessions[session.ID] = true
	}
	steps := []struct {
		name  string
		write func(context.Context) error
	}{
		{"people and projects", w.writePeopleAndProjects},
		{"issues", w.writeIssues},
		{"links", w.writeLinks},
		{"documents and files", w.writeArtifacts},
		{"asks", w.writeAsks},
		{"comments", w.writeComments},
		{"messages", w.writeMessages},
		{"counters", w.settleCounters},
	}
	for _, step := range steps {
		if err := step.write(ctx); err != nil {
			return false, fmt.Errorf("eval: write the seed's %s: %w", step.name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("eval: commit the seed: %w", err)
	}
	return true, nil
}

// ItemID is the uuid Dispatch stores the seed's item of kind ("ask", "comment" or "message") and
// seed id under: the first sixteen bytes of a sha256 of the two, stamped as a version 4 uuid, so a
// reader of the seeded store finds an item a task names by its seed id alone.
func ItemID(kind, id string) string {
	sum := sha256.Sum256([]byte("going-eval/" + kind + "/" + id))
	sum[6] = sum[6]&0x0f | 0x40
	sum[8] = sum[8]&0x3f | 0x80
	encoded := hex.EncodeToString(sum[:16])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// artifactID is the uuid of the document or file of owner (an issue's or a project's key) and
// slug, and of an issue's spec under the slug "spec".
func artifactID(owner, slug string) string {
	return ItemID("artifact", owner+"/"+slug)
}

// holdsTheSeed reports whether the database already holds every project of the seed, and refuses a
// database holding any project the seed does not.
func holdsTheSeed(ctx context.Context, tx pgx.Tx, seed *Seed) (bool, error) {
	rows, err := tx.Query(ctx, `select key from projects order by key`)
	if err != nil {
		return false, fmt.Errorf("eval: read the database's projects: %w", err)
	}
	held, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, fmt.Errorf("eval: read the database's projects: %w", err)
	}
	if len(held) == 0 {
		return false, nil
	}
	wanted := make([]string, len(seed.Projects))
	for i, project := range seed.Projects {
		wanted[i] = project.Key
	}
	slices.Sort(wanted)
	if !slices.Equal(held, wanted) {
		return false, fmt.Errorf("eval: the database holds the projects %s, not the seed's %s: seed an empty database",
			strings.Join(held, ", "), strings.Join(wanted, ", "))
	}
	return true, nil
}

// seedWriter is one run of WriteSeed: its transaction, the seed, which of the seed's names are
// sessions, and the last event sequence written under each event owner.
type seedWriter struct {
	tx       pgx.Tx
	seed     *Seed
	sessions map[string]bool
	seqs     map[string]int
}

// actor is the stored actor of one of the seed's names: a session's, or a person's.
func (w *seedWriter) actor(name string) model.Actor {
	if w.sessions[name] {
		return model.Actor{Kind: "session", ID: name}
	}
	return model.Actor{Kind: "user", ID: name}
}

// actorJSON is actor's JSON, as a jsonb column holds it.
func (w *seedWriter) actorJSON(name string) (string, error) {
	encoded, err := json.Marshal(w.actor(name))
	return string(encoded), err
}

// creator is the person every seeded row with no author of its own is made by: the seed's first
// person, who is also each project's default assignee.
func (w *seedWriter) creator() string {
	return w.seed.People[0].Login
}

// writePeopleAndProjects writes the seed's people as people who have signed in, which every
// assignee and default assignee must be, and its projects.
func (w *seedWriter) writePeopleAndProjects(ctx context.Context) error {
	if len(w.seed.People) == 0 {
		return errors.New("the seed holds no person to make its rows")
	}
	for _, person := range w.seed.People {
		if _, err := w.tx.Exec(ctx, `insert into people (email, signed_in_at) values ($1, $2) on conflict (email) do nothing`,
			person.Login, w.seed.Now); err != nil {
			return fmt.Errorf("person %s: %w", person.Login, err)
		}
	}
	for _, project := range w.seed.Projects {
		if _, err := w.tx.Exec(ctx, `
			insert into projects (key, name, default_assignee, next_number, created_at, last_seq)
			values ($1, $2, $3, 1, $4, 0)
		`, project.Key, project.Name, w.creator(), w.earliest()); err != nil {
			return fmt.Errorf("project %s: %w", project.Key, err)
		}
	}
	return nil
}

// earliest is the time of the seed's first issue, which its projects are dated at.
func (w *seedWriter) earliest() time.Time {
	first := w.seed.Now
	for _, issue := range w.seed.Issues {
		if issue.CreatedAt.Before(first) {
			first = issue.CreatedAt
		}
	}
	return first
}

// writeIssues writes every issue with its spec document, parents before children, so each row's
// parent is there when it lands. The board ranks them in the seed's order. A spec's version is
// written with the other documents (writeArtifacts).
func (w *seedWriter) writeIssues(ctx context.Context) error {
	written := map[string]bool{}
	ranked := ""
	creator, err := w.actorJSON(w.creator())
	if err != nil {
		return err
	}
	for len(written) < len(w.seed.Issues) {
		progressed := false
		for _, issue := range w.seed.Issues {
			if written[issue.Key] || (issue.Parent != "" && !written[issue.Parent]) {
				continue
			}
			project, number, err := issueNumber(issue.Key)
			if err != nil {
				return err
			}
			ranked = rank.Between(ranked, "")
			var priority *int16
			if issue.Priority != nil {
				value := int16(*issue.Priority)
				priority = &value
			}
			var claimedBy *string
			var claimedAt *time.Time
			if issue.Claim != nil {
				holder, err := w.actorJSON(issue.Claim.Session)
				if err != nil {
					return err
				}
				claimedBy, claimedAt = &holder, &issue.Claim.At
			}
			updated := issue.CreatedAt
			if issue.ClosedAt != nil {
				updated = *issue.ClosedAt
			}
			if claimedAt != nil && claimedAt.After(updated) {
				updated = *claimedAt
			}
			labels := issue.Labels
			if labels == nil {
				labels = []string{}
			}
			if _, err := w.tx.Exec(ctx, `
				insert into issues (key, project_key, number, title, status, labels, parent_key, route, created_by,
				                    created_at, updated_at, closed_at, rank, priority, assignee, claimed_by, claimed_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, $13, $14, $15, $16::jsonb, $17)
			`, issue.Key, project, number, issue.Title, issue.Status, labels, nullable(issue.Parent),
				nullable(sessionRoute(issue.Route)), creator, issue.CreatedAt, updated, issue.ClosedAt, ranked,
				priority, nullable(issue.Assignee), claimedBy, claimedAt); err != nil {
				return fmt.Errorf("issue %s: %w", issue.Key, err)
			}
			for _, holder := range issue.RoleHolders {
				if _, err := w.tx.Exec(ctx, `
					insert into issue_role_holders (issue_key, role, session, recorded_at) values ($1, $2, $3, $4)
				`, issue.Key, holder.Role, holder.Session, issue.CreatedAt); err != nil {
					return fmt.Errorf("the %s of issue %s: %w", holder.Role, issue.Key, err)
				}
			}
			// Every issue holds its spec document, as the create route makes it, with a version
			// only where the seed gives the spec a text.
			if _, err := w.tx.Exec(ctx, `
				insert into artifacts (id, issue_key, project_key, slug, name, kind, is_primary, created_by, created_at, last_seq)
				values ($1, $2, $3, 'spec', 'spec.md', 'doc', true, $4::jsonb, $5, 0)
			`, artifactID(issue.Key, "spec"), issue.Key, project, creator, issue.CreatedAt); err != nil {
				return fmt.Errorf("the spec of issue %s: %w", issue.Key, err)
			}
			written[issue.Key] = true
			progressed = true
		}
		if !progressed {
			return errors.New("the seed's parents form a cycle")
		}
	}
	return nil
}

// issueNumber is the project and the number of an issue key.
func issueNumber(key string) (string, int, error) {
	project, number, found := strings.Cut(key, "-")
	var value int
	if _, err := fmt.Sscanf(number, "%d", &value); !found || err != nil || value <= 0 {
		return "", 0, fmt.Errorf("issue key %q is no project key and number", key)
	}
	return project, value, nil
}

// sessionRoute is the stored route to a session, "" for none.
func sessionRoute(session string) string {
	if session == "" {
		return ""
	}
	return "session:" + session
}

// nullable is value, or nil for "".
func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// writeLinks writes each link between two issues: a blocks link as the blocker in the blocked
// issue's blocked_by, and a relates link, which Dispatch has no field for, as a comment on the one
// issue by the seed's first person that links the other, which the graph holds as a mention.
func (w *seedWriter) writeLinks(ctx context.Context) error {
	for _, link := range w.seed.Links {
		from, _ := w.seed.Issue(link.From)
		to, _ := w.seed.Issue(link.To)
		at := from.CreatedAt
		if to.CreatedAt.After(at) {
			at = to.CreatedAt
		}
		switch link.Kind {
		case LinkBlocks:
			if _, err := w.tx.Exec(ctx, `
				insert into issue_links (issue_key, kind, target_key, created_at) values ($1, 'blocked_by', $2, $3)
			`, link.To, link.From, at); err != nil {
				return fmt.Errorf("%s blocking %s: %w", link.From, link.To, err)
			}
		case LinkRelates:
			comment := Comment{
				ID:        "relates-" + link.From + "-" + link.To,
				Owner:     link.From,
				Author:    w.creator(),
				Body:      fmt.Sprintf("Related to dispatch://%s.", link.To),
				CreatedAt: at,
			}
			if err := w.writeComment(ctx, comment); err != nil {
				return fmt.Errorf("%s relating to %s: %w", link.From, link.To, err)
			}
		default:
			return fmt.Errorf("a link of kind %q", link.Kind)
		}
	}
	return nil
}

// owner is where an item of the seed lives: its issue, or the project document a reference
// `<project>/artifact/<slug>` names.
type owner struct {
	issue    *string
	artifact *string
}

// ownerOf reads an item's owner from the seed's reference to it.
func ownerOf(reference string) owner {
	if project, slug, found := strings.Cut(reference, "/artifact/"); found {
		id := artifactID(project, slug)
		return owner{artifact: &id}
	}
	return owner{issue: &reference}
}

// writeArtifacts writes each issue's spec text and each document and file of an issue or a
// project, every one at one version dated at its creation.
func (w *seedWriter) writeArtifacts(ctx context.Context) error {
	creator, err := w.actorJSON(w.creator())
	if err != nil {
		return err
	}
	for _, issue := range w.seed.Issues {
		if issue.Spec == "" {
			continue
		}
		if err := w.writeDocumentVersion(ctx, artifactID(issue.Key, "spec"), issue.Spec, issue.CreatedAt, true); err != nil {
			return fmt.Errorf("the spec of issue %s: %w", issue.Key, err)
		}
	}
	for _, artifact := range w.seed.Artifacts {
		id := artifactID(artifact.Owner, artifact.Slug)
		var issueKey *string
		project := artifact.Owner
		if key, _, err := issueNumber(artifact.Owner); err == nil {
			issueKey, project = &artifact.Owner, key
		}
		kind := "doc"
		if artifact.Kind == ArtifactFile {
			kind = "file"
		}
		if _, err := w.tx.Exec(ctx, `
			insert into artifacts (id, issue_key, project_key, slug, name, kind, is_primary, created_by, created_at, last_seq)
			values ($1, $2, $3, $4, $5, $6, false, $7::jsonb, $8, 0)
		`, id, issueKey, project, artifact.Slug, artifact.Name, kind, creator, artifact.CreatedAt); err != nil {
			return fmt.Errorf("%s/%s: %w", artifact.Owner, artifact.Slug, err)
		}
		if artifact.Kind == ArtifactFile {
			sum := sha256.Sum256([]byte(artifact.Body))
			if _, err := w.tx.Exec(ctx, `
				insert into artifact_versions (artifact_id, number, content, mime, size, sha256, authors, named, created_at)
				values ($1, 1, $2, $3, $4, $5, '[]', false, $6)
			`, id, []byte(artifact.Body), artifact.ContentType, len(artifact.Body), hex.EncodeToString(sum[:]), artifact.CreatedAt); err != nil {
				return fmt.Errorf("the file %s/%s: %w", artifact.Owner, artifact.Slug, err)
			}
			continue
		}
		if err := w.writeDocumentVersion(ctx, id, artifact.Body, artifact.CreatedAt, false); err != nil {
			return fmt.Errorf("the document %s/%s: %w", artifact.Owner, artifact.Slug, err)
		}
	}
	return nil
}

// writeDocumentVersion writes version 1 of document id and indexes it as a version write does:
// the markdown stored is the render of the seed's, with its blocks' ids and offsets, which is what
// the block index reads; then the blocks, their windows, the document's references and their
// excerpts, and for a spec its task progress, as cmd/dispatch-seed's writeDocument indexes one.
func (w *seedWriter) writeDocumentVersion(ctx context.Context, id, markdown string, at time.Time, spec bool) error {
	tree, err := pmdoc.Parse(markdown)
	if err != nil {
		return fmt.Errorf("parse its markdown: %w", err)
	}
	rendered, offsets, err := pmdoc.RenderWithBlockOffsets(tree)
	if err != nil {
		return fmt.Errorf("render its markdown: %w", err)
	}
	blocks := make([]model.ArtifactBlock, len(offsets))
	for index, offset := range offsets {
		blocks[index] = model.ArtifactBlock{ID: offset.ID, Type: offset.Type, From: offset.From, To: offset.To}
	}
	if _, err := w.tx.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors, named, created_at)
		values ($1, 1, $2, '[]', false, $3)
	`, id, rendered, at); err != nil {
		return fmt.Errorf("its version: %w", err)
	}
	located := text.ExtractAt(rendered, "")
	index, err := refs.ReadBlockIndex(ctx, w.tx, id)
	if err != nil {
		return fmt.Errorf("index its blocks: %w", err)
	}
	indexing, err := refs.PlanBlocks(index, id, 1, at, rendered, blocks, located, nil)
	if err != nil {
		return fmt.Errorf("index its blocks: %w", err)
	}
	if err := store.RunWrite(ctx, w.tx, func(b *store.WriteBatch) error {
		indexing.Queue(b)
		return nil
	}); err != nil {
		return fmt.Errorf("index its blocks: %w", err)
	}
	if err := refs.WriteDocumentWindows(ctx, w.tx, id, rendered, blocks); err != nil {
		return fmt.Errorf("write its windows: %w", err)
	}
	replacement, err := refs.PlanReplace(ctx, w.tx, "artifact", id, text.Refs(located), &at)
	if err != nil {
		return fmt.Errorf("index its references: %w", err)
	}
	if err := store.RunWrite(ctx, w.tx, func(b *store.WriteBatch) error {
		replacement.Queue(b)
		refs.QueueRecordExcerpts(b, id, 1, refs.MentionExcerpts(rendered, blocks, located))
		return nil
	}); err != nil {
		return fmt.Errorf("index its references: %w", err)
	}
	if spec {
		if err := docs.RecordTaskProgressMarkdown(ctx, w.tx, id, rendered); err != nil {
			return fmt.Errorf("record its task progress: %w", err)
		}
	}
	return nil
}

// writeAsks writes each ask with its options and answer, indexes its parts as the routes that
// write an ask do, and writes its opening event, which every read of an ask resolves: the ask as
// it opened, under its owner's next sequence.
func (w *seedWriter) writeAsks(ctx context.Context) error {
	for _, ask := range w.seed.Asks {
		id := ItemID("ask", ask.ID)
		where := ownerOf(ask.Owner)
		author := w.actor(ask.AskedBy)
		authorJSON, err := w.actorJSON(ask.AskedBy)
		if err != nil {
			return err
		}
		options := make([]model.AskOption, len(ask.Options))
		for index, label := range ask.Options {
			options[index] = model.AskOption{Label: label}
		}
		optionsJSON, err := json.Marshal(options)
		if err != nil {
			return err
		}
		var answer *model.AskAnswer
		var answerJSON *string
		if ask.Answer != nil {
			words := ask.Answer.Text
			answer = &model.AskAnswer{User: ask.Answer.By, Selected: ask.Answer.Selected, Text: &words, At: ask.Answer.At}
			encoded, err := json.Marshal(answer)
			if err != nil {
				return err
			}
			value := string(encoded)
			answerJSON = &value
		}
		if _, err := w.tx.Exec(ctx, `
			insert into asks (id, issue_key, artifact_id, author, question, options, multiple, urgency, state, answer, created_at, kind)
			values ($1, $2, $3, $4::jsonb, $5, $6::jsonb, false, 'med', $7, $8::jsonb, $9, 'question')
		`, id, where.issue, where.artifact, authorJSON, ask.Question, string(optionsJSON), ask.State, answerJSON,
			ask.CreatedAt); err != nil {
			return fmt.Errorf("ask %s: %w", ask.ID, err)
		}
		opening := model.Ask{
			ID: id, IssueKey: where.issue, ArtifactID: where.artifact, Author: author, Kind: "question",
			Question: ask.Question, Options: options, Urgency: "med", State: AskOpen, CreatedAt: ask.CreatedAt,
		}
		stored := opening
		stored.State, stored.Answer = ask.State, answer
		at := ask.CreatedAt
		if answer != nil {
			at = answer.At
		}
		partIndex, err := refs.ReadItemIndex(ctx, w.tx, refs.ItemAsk, id)
		if err != nil {
			return fmt.Errorf("index the parts of ask %s: %w", ask.ID, err)
		}
		parts, err := refs.PlanAskParts(partIndex, refs.NewReader(ctx, w.tx, "ask", id, ""), stored, at)
		if err != nil {
			return fmt.Errorf("index the parts of ask %s: %w", ask.ID, err)
		}
		if err := store.RunWrite(ctx, w.tx, func(b *store.WriteBatch) error {
			parts.Queue(b)
			return nil
		}); err != nil {
			return fmt.Errorf("index the parts of ask %s: %w", ask.ID, err)
		}
		if _, err := refs.WriteItemWindows(ctx, w.tx, refs.ItemAsk, id); err != nil {
			return fmt.Errorf("write the windows of ask %s: %w", ask.ID, err)
		}
		payload, err := json.Marshal(model.NewAskEventPayload(opening, model.ReferenceChanges{}))
		if err != nil {
			return err
		}
		if err := w.writeEvent(ctx, where, "ask.opened", authorJSON, string(payload), ask.CreatedAt); err != nil {
			return fmt.Errorf("the opening event of ask %s: %w", ask.ID, err)
		}
	}
	return nil
}

// writeEvent appends one published event under owner's next sequence, as a server that wrote it
// and published it would leave it.
func (w *seedWriter) writeEvent(ctx context.Context, where owner, kind, actor, payload string, at time.Time) error {
	key := "artifact:"
	if where.issue != nil {
		key = "issue:" + *where.issue
	} else {
		key += *where.artifact
	}
	w.seqs[key]++
	_, err := w.tx.Exec(ctx, `
		insert into events (issue_key, artifact_id, seq, type, actor, payload, notify, published_at, created_at)
		values ($1, $2, $3, $4, $5::jsonb, $6::jsonb, false, $7, $7)
	`, where.issue, where.artifact, w.seqs[key], kind, actor, payload, at)
	return err
}

// writeComments writes each comment of the seed (writeComment).
func (w *seedWriter) writeComments(ctx context.Context) error {
	for _, comment := range w.seed.Comments {
		if err := w.writeComment(ctx, comment); err != nil {
			return fmt.Errorf("comment %s: %w", comment.ID, err)
		}
	}
	return nil
}

// writeComment writes one comment, indexes its blocks and writes the mentions its body holds, as
// the comment route leaves them.
func (w *seedWriter) writeComment(ctx context.Context, comment Comment) error {
	id := ItemID("comment", comment.ID)
	where := ownerOf(comment.Owner)
	author, err := w.actorJSON(comment.Author)
	if err != nil {
		return err
	}
	if _, err := w.tx.Exec(ctx, `
		insert into comments (id, issue_key, artifact_id, author, body, created_at) values ($1, $2, $3, $4::jsonb, $5, $6)
	`, id, where.issue, where.artifact, author, comment.Body, comment.CreatedAt); err != nil {
		return err
	}
	return w.indexItem(ctx, refs.ItemComment, id, comment.Body, comment.CreatedAt)
}

// writeMessages writes each message of an issue's thread, to its session where it names one, and
// indexes its blocks.
func (w *seedWriter) writeMessages(ctx context.Context) error {
	for _, message := range w.seed.Messages {
		id := ItemID("message", message.ID)
		author, err := w.actorJSON(message.Author)
		if err != nil {
			return err
		}
		if _, err := w.tx.Exec(ctx, `
			insert into messages (id, issue_key, author, body, target, created_at) values ($1, $2, $3::jsonb, $4, $5, $6)
		`, id, message.Issue, author, message.Body, nullable(sessionRoute(message.To)), message.CreatedAt); err != nil {
			return fmt.Errorf("message %s: %w", message.ID, err)
		}
		if err := w.indexItem(ctx, refs.ItemMessage, id, message.Body, message.CreatedAt); err != nil {
			return fmt.Errorf("message %s: %w", message.ID, err)
		}
	}
	return nil
}

// indexItem indexes a comment's or a message's blocks with the server's indexer, writes their
// windows, and writes the item's mentions, as cmd/dispatch-seed's writers do.
func (w *seedWriter) indexItem(ctx context.Context, kind refs.ItemKind, id, body string, at time.Time) error {
	located := text.ExtractAt(body, "")
	if _, err := refs.IndexItem(ctx, w.tx, kind, id, body, at, located, nil); err != nil {
		return fmt.Errorf("index its blocks: %w", err)
	}
	if _, err := refs.WriteItemWindows(ctx, w.tx, kind, id); err != nil {
		return fmt.Errorf("write its windows: %w", err)
	}
	for _, ref := range text.Refs(located) {
		target, ok := refs.TargetOf(ref)
		if !ok {
			continue
		}
		if _, err := w.tx.Exec(ctx, `
			insert into refs (from_kind, from_id, to_kind, to_id, created_at) values ($1, $2, $3, $4, $5)
			on conflict (from_kind, from_id, to_kind, to_id) do nothing
		`, string(kind), id, target.Kind, target.ID, at); err != nil {
			return fmt.Errorf("its mention of %s %s: %w", target.Kind, target.ID, err)
		}
	}
	return nil
}

// settleCounters brings each event owner's last sequence to the highest it carries and each
// project's next issue number past its highest issue, so the first write a server makes to a
// seeded item or project takes the next free number.
func (w *seedWriter) settleCounters(ctx context.Context) error {
	for _, statement := range []string{
		`update issues i set last_seq = e.top
		   from (select issue_key, max(seq) as top from events where issue_key is not null group by issue_key) e
		  where e.issue_key = i.key`,
		`update artifacts a set last_seq = e.top
		   from (select artifact_id, max(seq) as top from events where artifact_id is not null group by artifact_id) e
		  where e.artifact_id = a.id`,
		`update projects p set next_number = i.top + 1
		   from (select project_key, max(number) as top from issues group by project_key) i
		  where i.project_key = p.key`,
	} {
		if _, err := w.tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
