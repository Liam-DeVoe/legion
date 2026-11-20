package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/going"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// A going run's record is one run of the command bar: what the person typed and where, and the
// parts of an answered pause the record keeps - the hits, the decision, the proposal - beside the
// run's own columns (migration 20261119201453) and its steps, events, grant, landings and plans.
// It is a row of its own (going_runs, migration 20261112173940) because the going request it
// shares an id with is swept an hour after it is asked (sweepGoingRequests).
//
// The run writes the row when it starts (recordGoingRun) and fills it as it goes (fillGoingRun),
// so a run that is abandoned keeps what it stored and reads as unfinished. What a run changed is
// its landings (going_landings), which its undo reads (going_undo.go). GET /api/v1/me/going-runs
// reads the caller's own runs alone.

// goingRunsKept is how long a run's record is kept: a row older than this is read by nobody, even
// before sweepGoingRuns deletes it, and History lists a conversation until its newest run is older
// than this (goingConversationListed).
const goingRunsKept = 7 * 24 * time.Hour

// goingRunColumns is the going_runs select list scanGoingRun reads, in scan order.
const goingRunColumns = `r.id::text, r.author_login, r.words, r.context, r.hits, r.decision,
	r.proposal, r.issues, r.structured_output, r.created_at, r.finished_at`

// goingContext is the item the bar's context chip names when the person paused: the page's
// path, and the project, issue and document block that page shows, each empty where it shows
// none.
type goingContext struct {
	Path    string `json:"path"`
	Project string `json:"project"`
	Issue   string `json:"issue"`
	Block   string `json:"block"`
}

// goingRunHit is one hit the run found, as the stream's item frame names it.
type goingRunHit struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Title string `json:"title"`
	Href  string `json:"href"`
}

// goingWriteOutcome is how one request of a write or of an undo went: ok, and the HTTP status the
// route it ran through answered, with the refusal's code and message where it refused (both empty
// when ok).
type goingWriteOutcome struct {
	OK      bool   `json:"ok"`
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// goingWriteRequest is one request a write sent, by method, path and the hex SHA-256 of its body
// (goingSend.digest), how it went, and the id of the item it made where it made one
// (goingMadeID). A request is the same request as another only where all three match
// (goingSameSend).
type goingWriteRequest struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	BodySHA256 string            `json:"body_sha256,omitempty"`
	Outcome    goingWriteOutcome `json:"outcome"`
	Made       string            `json:"made,omitempty"`
}

// goingSameSend reports whether the recorded request is the request spec sends: the same method,
// path and body digest. Every request this build records names its digest, so one that names none
// is no request's.
func goingSameSend(recorded goingWriteRequest, spec goingSend) bool {
	return recorded.Method == spec.method && recorded.Path == spec.path &&
		recorded.BodySHA256 != "" && recorded.BodySHA256 == spec.digest()
}

// goingDelivery is one delivery of a message as a send to an agent answers it: its state, as the
// route answered it (model.DeliveryStatePending, ...Sent, ...Queued or ...Failed - queued for a
// session the listener knows and that is not running, which gets the message when it runs
// again), and the error a failed one gave, null for none.
type goingDelivery struct {
	State string  `json:"state"`
	Error *string `json:"error"`
}

// goingRun is one run's record as GET /api/v1/me/going-runs answers it: every part stored, null
// where the run has not written it, finished whether it ended (finished_at is set), and issues
// every issue key a hit or a landed request of the run touched.
type goingRun struct {
	ID               string         `json:"id"`
	AuthorLogin      string         `json:"author_login"`
	Words            string         `json:"words"`
	Context          *goingContext  `json:"context"`
	Hits             []goingRunHit  `json:"hits"`
	Decision         *goingDecision `json:"decision"`
	Proposal         *goingProposal `json:"proposal"`
	Issues           []string       `json:"issues"`
	StructuredOutput *string        `json:"structured_output"`
	Finished         bool           `json:"finished"`
	CreatedAt        time.Time      `json:"created_at"`
	FinishedAt       *time.Time     `json:"finished_at"`
}

// goingRunsPage is a page of the caller's runs, newest first, paged as GET /api/v1/me/answers is.
type goingRunsPage struct {
	Rows   []goingRun `json:"rows"`
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

// goingRunParts is what fillGoingRun writes onto a run: each part left nil keeps what the row
// holds, so the run writes each part as it has it. Finished sets finished_at, once: a second
// finish keeps the first moment.
type goingRunParts struct {
	Hits             []going.Hit
	Decision         *goingDecision
	Proposal         *goingProposal
	StructuredOutput *string
	Finished         bool
}

// goingRunFilter is what GET /api/v1/me/going-runs narrows a person's runs to: one issue key the
// run touched, and a case-insensitive substring of its words. The zero value narrows nothing.
type goingRunFilter struct {
	Issue string
	Q     string
}

// goingRunWindow is the condition that keeps a row past goingRunsKept out of every read and
// write here, $n being goingRunsKept in seconds.
func goingRunWindow(n int) string {
	return fmt.Sprintf("r.created_at > now() - make_interval(secs => $%d)", n)
}

// goingConversationListed is the condition that holds of the run r while History lists its
// conversation: the conversation's newest run is younger than goingRunsKept, $n being
// goingRunsKept in seconds. A run stored before conversations names none, and is its own.
func goingConversationListed(n int) string {
	return fmt.Sprintf(`greatest(r.created_at, (select max(c.created_at) from going_runs c where c.conversation_id = r.conversation_id))
	  > now() - make_interval(secs => $%d)`, n)
}

// recordGoingRun writes a run's row when the run starts, under its going request's id, keyed on
// the asking person's canonical login. Every other part is written as the run reaches it.
func (s *server) recordGoingRun(ctx context.Context, id, authorLogin, words string, chip *goingContext) error {
	var stored any
	if chip != nil {
		encoded, err := json.Marshal(chip)
		if err != nil {
			return err
		}
		stored = string(encoded)
	}
	_, err := s.deps.Store.Pool.Exec(ctx, `
		insert into going_runs (id, author_login, words, context) values ($1::uuid, $2, $3, $4::jsonb)
	`, id, authorLogin, words, stored)
	return err
}

// fillGoingRun writes the parts the run has reached onto its row, adding the key of every issue
// among the hits to the run's issues. A run the record does not hold is pgx.ErrNoRows.
func (s *server) fillGoingRun(ctx context.Context, id string, parts goingRunParts) error {
	var hits any
	var hitIssues []string
	if parts.Hits != nil {
		rows := make([]goingRunHit, 0, len(parts.Hits))
		for _, hit := range parts.Hits {
			rows = append(rows, goingRunHit{Kind: hit.Kind, ID: hit.ID, Title: hit.Title, Href: hit.Href})
			if hit.Kind == "issue" {
				hitIssues = append(hitIssues, hit.ID)
			}
		}
		encoded, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		hits = string(encoded)
	}
	decision, err := nullableJSON(parts.Decision)
	if err != nil {
		return err
	}
	proposal, err := nullableJSON(parts.Proposal)
	if err != nil {
		return err
	}
	tag, err := s.deps.Store.Pool.Exec(ctx, `
		update going_runs set
		  hits = coalesce($2::jsonb, hits),
		  decision = coalesce($3::jsonb, decision),
		  proposal = coalesce($4::jsonb, proposal),
		  structured_output = coalesce($5::text, structured_output),
		  finished_at = case when $6::boolean then coalesce(finished_at, now()) else finished_at end,
		  issues = array(select distinct key from unnest(issues || $7::text[]) key order by key)
		where id = $1::uuid
	`, id, hits, decision, proposal, parts.StructuredOutput, parts.Finished, hitIssues)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// readGoingRun is the author's one run by id: pgx.ErrNoRows for a run of anyone else's, one the
// record does not hold, and one older than goingRunsKept, which are one answer to whoever asks.
func (s *server) readGoingRun(ctx context.Context, id, authorLogin string) (goingRun, error) {
	return scanGoingRun(s.deps.Store.Pool.QueryRow(ctx, `
		select `+goingRunColumns+` from going_runs r
		where r.id = $1::uuid and r.author_login = $2 and `+goingRunWindow(3),
		id, authorLogin, goingRunsKept.Seconds()))
}

// listGoingRuns is one page of the author's runs inside goingRunsKept, newest first, narrowed by
// filter, with the count of every run the filter keeps.
func (s *server) listGoingRuns(ctx context.Context, authorLogin string, filter goingRunFilter, limit, offset int) ([]goingRun, int, error) {
	rows, err := s.deps.Store.Pool.Query(ctx, `
		select `+goingRunColumns+`, count(*) over () as total
		from going_runs r
		where r.author_login = $1 and `+goingRunWindow(2)+`
		  and ($3::text = '' or r.issues @> array[$3::text])
		  and ($4::text = '' or strpos(lower(r.words), lower($4::text)) > 0)
		order by r.created_at desc, r.id desc
		limit $5 offset $6
	`, authorLogin, goingRunsKept.Seconds(), filter.Issue, filter.Q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	runs := []goingRun{}
	total := 0
	for rows.Next() {
		var count int64
		run, err := scanGoingRun(rows, &count)
		if err != nil {
			return nil, 0, err
		}
		total = int(count)
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

// listMyGoingRuns answers GET /api/v1/me/going-runs: the caller's own runs, newest first, keyed
// on the person actingPerson names. It takes no login: no caller reads another person's runs.
func (s *server) listMyGoingRuns(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.actingPerson(w, r)
	if !ok {
		return
	}
	limit, offset, err := parseMyAnswersPage(r)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	query := r.URL.Query()
	filter := goingRunFilter{Issue: strings.TrimSpace(query.Get("issue")), Q: strings.TrimSpace(query.Get("q"))}
	runs, total, err := s.listGoingRuns(r.Context(), caller.ID, filter, limit, offset)
	if err != nil {
		s.writeHandlerError(w, fmt.Errorf("list my going runs: %w", err))
		return
	}
	WriteJSON(w, http.StatusOK, goingRunsPage{Rows: runs, Total: total, Limit: limit, Offset: offset})
}

// scanGoingRun decodes one goingRunColumns row, and into extra any columns the select adds after
// them.
func scanGoingRun(row pgx.Row, extra ...any) (goingRun, error) {
	var run goingRun
	var chip, hits []byte
	var decision, proposal []byte
	targets := append([]any{
		&run.ID, &run.AuthorLogin, &run.Words, &chip, &hits, &decision, &proposal,
		&run.Issues, &run.StructuredOutput, &run.CreatedAt, &run.FinishedAt,
	}, extra...)
	if err := row.Scan(targets...); err != nil {
		return goingRun{}, err
	}
	if chip != nil {
		run.Context = &goingContext{}
		if err := json.Unmarshal(chip, run.Context); err != nil {
			return goingRun{}, fmt.Errorf("decode going run context: %w", err)
		}
	}
	if hits != nil {
		if err := json.Unmarshal(hits, &run.Hits); err != nil {
			return goingRun{}, fmt.Errorf("decode going run hits: %w", err)
		}
	}
	if decision != nil {
		run.Decision = &goingDecision{}
		if err := json.Unmarshal(decision, run.Decision); err != nil {
			return goingRun{}, fmt.Errorf("decode going run decision: %w", err)
		}
	}
	if proposal != nil {
		run.Proposal = &goingProposal{}
		if err := json.Unmarshal(proposal, run.Proposal); err != nil {
			return goingRun{}, fmt.Errorf("decode going run proposal: %w", err)
		}
	}
	run.Finished = run.FinishedAt != nil
	return run, nil
}

// nullableJSON is part as a jsonb parameter: SQL null where the run has not reached it (nil), so a
// coalesce keeps what the row holds.
func nullableJSON[T any](part *T) (any, error) {
	if part == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(part)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

// sweepGoingRuns deletes every run older than goingRunsKept. The reads above already leave such
// a run out, so the sweep only keeps the table from growing.
func sweepGoingRuns(ctx context.Context, pool *store.Pool) error {
	_, err := pool.Exec(ctx, `
		delete from going_runs where created_at < now() - make_interval(secs => $1)
	`, goingRunsKept.Seconds())
	return err
}
