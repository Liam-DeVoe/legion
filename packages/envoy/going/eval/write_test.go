package eval

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// TestTheSeededStoreHoldsTheSeed writes the committed seed into an empty database and reads it
// back as Dispatch stores it: each issue's fields, its close and its claim at the seed's own times,
// its spec, each blocks and relates link, each ask with its answer and its opening event, each
// comment, message and file, and the project counters a server's next write takes. A second write
// leaves the store as it is, and a database holding another project is refused.
func TestTheSeededStoreHoldsTheSeed(t *testing.T) {
	ctx := context.Background()
	seed, err := LoadSeed(Files)
	if err != nil {
		t.Fatal(err)
	}
	database := storetest.Open(t)
	written, err := WriteSeed(ctx, database, seed)
	if err != nil || !written {
		t.Fatalf("WriteSeed on an empty database = %v, %v; want the seed written", written, err)
	}

	for _, want := range seed.Issues {
		var title, status, route, claimedBy string
		var labels []string
		var priority *int16
		var parent, assignee *string
		var created time.Time
		var closed, claimed *time.Time
		var specVersions int
		if err := database.Pool.QueryRow(ctx, `
			select i.title, i.status, i.labels, i.priority, i.parent_key, i.assignee, coalesce(i.route, ''),
			       i.created_at, i.closed_at, coalesce(i.claimed_by->>'id', ''), i.claimed_at,
			       (select count(*) from artifact_versions v join artifacts a on a.id = v.artifact_id
			         where a.issue_key = i.key and a.is_primary)
			from issues i where i.key = $1
		`, want.Key).Scan(&title, &status, &labels, &priority, &parent, &assignee, &route, &created, &closed,
			&claimedBy, &claimed, &specVersions); err != nil {
			t.Fatalf("read issue %s: %v", want.Key, err)
		}
		if title != want.Title || status != want.Status || !slices.Equal(labels, orEmpty(want.Labels)) {
			t.Errorf("issue %s holds %q %s %v, want %q %s %v", want.Key, title, status, labels, want.Title, want.Status, want.Labels)
		}
		if (priority == nil) != (want.Priority == nil) || (priority != nil && int(*priority) != *want.Priority) {
			t.Errorf("issue %s holds the priority %v, want %v", want.Key, priority, want.Priority)
		}
		if value(parent) != want.Parent || value(assignee) != want.Assignee || route != sessionRoute(want.Route) {
			t.Errorf("issue %s holds the parent %q, assignee %q and route %q, want %q, %q and %q",
				want.Key, value(parent), value(assignee), route, want.Parent, want.Assignee, sessionRoute(want.Route))
		}
		if !created.Equal(want.CreatedAt) || !sameTime(closed, want.ClosedAt) {
			t.Errorf("issue %s was created at %s and closed at %v, want %s and %v", want.Key, created, closed, want.CreatedAt, want.ClosedAt)
		}
		var wantClaim string
		var wantClaimed *time.Time
		if want.Claim != nil {
			wantClaim, wantClaimed = want.Claim.Session, &want.Claim.At
		}
		if claimedBy != wantClaim || !sameTime(claimed, wantClaimed) {
			t.Errorf("issue %s is claimed by %q at %v, want %q at %v", want.Key, claimedBy, claimed, wantClaim, wantClaimed)
		}
		if wantVersions := min(len(want.Spec), 1); specVersions != wantVersions {
			t.Errorf("the spec of issue %s holds %d versions, want %d", want.Key, specVersions, wantVersions)
		}
	}

	for _, link := range seed.Links {
		var held bool
		query := `select exists (select 1 from issue_links where issue_key = $2 and kind = 'blocked_by' and target_key = $1)`
		if link.Kind == LinkRelates {
			query = `select exists (select 1 from refs r join comments c on c.id::text = r.from_id
			          where r.from_kind = 'comment' and c.issue_key = $1 and r.to_kind = 'issue' and r.to_id = $2)`
		}
		if err := database.Pool.QueryRow(ctx, query, link.From, link.To).Scan(&held); err != nil {
			t.Fatalf("read the link from %s to %s: %v", link.From, link.To, err)
		}
		if !held {
			t.Errorf("the store holds no %s link from %s to %s", link.Kind, link.From, link.To)
		}
	}

	for _, ask := range seed.Asks {
		var state, author string
		var answer []byte
		var created time.Time
		var openedSeq *int
		var ownerSeq int
		if err := database.Pool.QueryRow(ctx, `
			select a.state, a.author->>'id', a.answer, a.created_at,
			       (select e.seq from events e where e.type = 'ask.opened' and e.payload->>'id' = a.id::text
			         and e.issue_key is not distinct from a.issue_key and e.artifact_id is not distinct from a.artifact_id),
			       coalesce((select last_seq from issues where key = a.issue_key), (select last_seq from artifacts where id = a.artifact_id))
			from asks a where a.id = $1
		`, ItemID("ask", ask.ID)).Scan(&state, &author, &answer, &created, &openedSeq, &ownerSeq); err != nil {
			t.Fatalf("read ask %s: %v", ask.ID, err)
		}
		if state != ask.State || author != ask.AskedBy || !created.Equal(ask.CreatedAt) {
			t.Errorf("ask %s is %s by %s at %s, want %s by %s at %s", ask.ID, state, author, created, ask.State, ask.AskedBy, ask.CreatedAt)
		}
		if ask.Answer != nil {
			var stored struct {
				User     string    `json:"user"`
				Selected []string  `json:"selected"`
				Text     string    `json:"text"`
				At       time.Time `json:"at"`
			}
			if err := json.Unmarshal(answer, &stored); err != nil || stored.User != ask.Answer.By ||
				!slices.Equal(stored.Selected, ask.Answer.Selected) || stored.Text != ask.Answer.Text || !stored.At.Equal(ask.Answer.At) {
				t.Errorf("ask %s holds the answer %s, want %+v", ask.ID, answer, *ask.Answer)
			}
		}
		if openedSeq == nil || *openedSeq > ownerSeq {
			t.Errorf("ask %s has the opening event %v under an owner whose last sequence is %d, want one at or below it", ask.ID, openedSeq, ownerSeq)
		}
	}

	for _, comment := range seed.Comments {
		var body, author string
		var created time.Time
		if err := database.Pool.QueryRow(ctx, `select body, author->>'id', created_at from comments where id = $1`,
			ItemID("comment", comment.ID)).Scan(&body, &author, &created); err != nil {
			t.Fatalf("read comment %s: %v", comment.ID, err)
		}
		if body != comment.Body || author != comment.Author || !created.Equal(comment.CreatedAt) {
			t.Errorf("comment %s holds %q by %s at %s, want %q by %s at %s", comment.ID, body, author, created, comment.Body, comment.Author, comment.CreatedAt)
		}
	}
	for _, message := range seed.Messages {
		var issue, author string
		var created time.Time
		if err := database.Pool.QueryRow(ctx, `select issue_key, author->>'id', created_at from messages where id = $1`,
			ItemID("message", message.ID)).Scan(&issue, &author, &created); err != nil {
			t.Fatalf("read message %s: %v", message.ID, err)
		}
		if issue != message.Issue || author != message.Author || !created.Equal(message.CreatedAt) {
			t.Errorf("message %s is on %s by %s at %s, want %s by %s at %s", message.ID, issue, author, created, message.Issue, message.Author, message.CreatedAt)
		}
	}
	for _, artifact := range seed.Artifacts {
		var stored string
		if err := database.Pool.QueryRow(ctx, `
			select coalesce(convert_from(v.content, 'UTF8'), v.markdown) from artifact_versions v
			join artifacts a on a.id = v.artifact_id where a.slug = $1 and v.number = 1
		`, artifact.Slug).Scan(&stored); err != nil {
			t.Fatalf("read %s/%s: %v", artifact.Owner, artifact.Slug, err)
		}
		if artifact.Kind == ArtifactFile && stored != artifact.Body {
			t.Errorf("the file %s holds %q, want %q", artifact.Slug, stored, artifact.Body)
		}
		if first := strings.SplitN(artifact.Body, "\n", 2)[0]; artifact.Kind == ArtifactDocument && !strings.Contains(stored, strings.TrimPrefix(first, "# ")) {
			t.Errorf("the document %s holds %q, which lacks its first line %q", artifact.Slug, stored, first)
		}
	}

	var unnumbered int
	if err := database.Pool.QueryRow(ctx, `
		select count(*) from projects p where p.next_number <> (select max(number) + 1 from issues where project_key = p.key)
	`).Scan(&unnumbered); err != nil || unnumbered != 0 {
		t.Errorf("%d projects number their next issue at another number than one past their last (%v)", unnumbered, err)
	}

	again, err := WriteSeed(ctx, database, seed)
	if err != nil || again {
		t.Errorf("WriteSeed on the seeded database = %v, %v; want nothing written and no error", again, err)
	}
	var issues int
	if err := database.Pool.QueryRow(ctx, `select count(*) from issues`).Scan(&issues); err != nil || issues != len(seed.Issues) {
		t.Errorf("the store holds %d issues after a second write, want %d (%v)", issues, len(seed.Issues), err)
	}

	other := storetest.Open(t)
	for _, statement := range []string{
		`insert into people (email) values ('x@example.com')`,
		`insert into projects (key, name, default_assignee) values ('ELSE', 'Something else', 'x@example.com')`,
	} {
		if _, err := other.Pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if written, err := WriteSeed(ctx, other, seed); written || err == nil || !strings.Contains(err.Error(), "ELSE") {
		t.Errorf("WriteSeed on a database holding project ELSE = %v, %v; want a refusal naming ELSE", written, err)
	}
}

func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

func sameTime(got, want *time.Time) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.Equal(*want)
}
