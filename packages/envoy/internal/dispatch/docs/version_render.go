package docs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// One renderer answers every read of a document's version. A version the writer rendered from the
// tree holds a version_renders row, written in the transaction that writes the version's tree
// (QueueVersionRender), and renders again from the tree's tuples as of the row's time; every other
// version - each one written before the tree was rendered from at all among them - answers the
// markdown it stores, byte for byte, and says so. A version whose markdown is null holds no bytes,
// and says that rather than answering an empty document. The boundary is the row and not a clock:
// no tuple, whatever it is dated, moves a version that holds no row off its stored bytes.

// RenderSource is what a Rendering's bytes are, as the version routes name it in their
// Dispatch-Render-Source header.
type RenderSource string

const (
	// RenderedFromTree is a render of the tree's tuples as of a time.
	RenderedFromTree RenderSource = "tree"
	// StoredBytes is a version's stored markdown, byte for byte.
	StoredBytes RenderSource = "stored"
	// NoBytes is a version that stores no markdown: its Markdown is empty and is no document.
	NoBytes RenderSource = "none"
)

// Rendering is a document's markdown as one read answers it, and what those bytes are.
type Rendering struct {
	Markdown string
	Source   RenderSource
}

// RenderAt renders the document artifactID from the tree's tuples as of at, in the text order its
// author wrote: the head where at is now, and a version as of the time
// its version_renders row records (RenderVersion). It reads the document's members, each block
// whose newest in_document dated at or before at names the document (refs.TreeMembers), and of
// each its newest text value, state, text_parent and text_rank dated at or before at, with every
// retraction dated before it applied (refs.TreeFactsOf at a render's cut, refs.TreeCut's Dated). It
// leaves out a member whose state then is gone, and one that holds no text value, no text_parent or
// no text_rank then. It prints the document's text children, the members whose text_parent names
// the document's node, in increasing text_rank, compared byte for byte (a tie, which no pass
// writes, by the lower address), each by its text value: its whole slice of the markdown the
// version was written from, the bytes of its own text children included, so every member prints
// once, and two members of one text each print at their own place. It reads no part_of tuple and
// no rank value, so a node's parent and its place among its siblings change no byte of it. The
// text children's renderings are joined by one blank line and the last line ends; a document that
// holds no text child renders as no bytes at all.
//
// who is the reader the render answers; the render leaves out no node for a reader, so every
// reader reads one rendering.
func RenderAt(ctx context.Context, q Queryer, who model.Actor, artifactID string, at time.Time) (Rendering, error) {
	var refKey string
	if err := q.QueryRow(ctx, `select ref_key from artifacts where id = $1`, artifactID).Scan(&refKey); err != nil {
		return Rendering{}, fmt.Errorf("read the document %s to render: %w", artifactID, err)
	}
	cut := refs.TreeCut{At: at, Dated: true}
	members, err := refs.TreeMembers(ctx, q, refKey, cut)
	if err != nil {
		return Rendering{}, err
	}
	facts, err := refs.TreeFactsOf(ctx, q, members, cut)
	if err != nil {
		return Rendering{}, fmt.Errorf("read the blocks of %s to render: %w", refKey, err)
	}
	type textChild struct{ rank, address, text string }
	children := make([]textChild, 0, len(members))
	for _, member := range members {
		held := facts[member]
		text, written := held.Values[graphstore.AttributeText]
		rank, ranked := held.Values[refs.AttributeTextRank]
		if !written || !ranked || held.Values[graphstore.AttributeState] == graphstore.StateGone {
			continue
		}
		if slices.ContainsFunc(held.Relations, func(relation refs.TreeRelation) bool {
			return relation.Kind == refs.TupleTextParent && relation.To.Kind == graphstore.KindArtifact && relation.To.ID == refKey
		}) {
			children = append(children, textChild{rank: rank, address: member.ID, text: text})
		}
	}
	if len(children) == 0 {
		return Rendering{Source: RenderedFromTree}, nil
	}
	slices.SortFunc(children, func(a, b textChild) int {
		return cmp.Or(strings.Compare(a.rank, b.rank), strings.Compare(a.address, b.address))
	})
	length := 2*len(children) - 1
	for _, child := range children {
		length += len(child.text)
	}
	var rendered strings.Builder
	rendered.Grow(length)
	for index, child := range children {
		if index > 0 {
			rendered.WriteString("\n\n")
		}
		rendered.WriteString(child.text)
	}
	rendered.WriteByte('\n')
	return Rendering{Markdown: rendered.String(), Source: RenderedFromTree}, nil
}

// versionRenderStatement is what a read of version $2 of document $1 needs: the time its
// version_renders row renders it as of, null where it holds none, and only then its stored
// markdown and whether that is null, so a version that renders from the tree reads no stored byte.
const versionRenderStatement = `
	select r.at, r.at is null and v.markdown is null, case when r.at is null then v.markdown end
	from artifact_versions v
	left join version_renders r on r.artifact_id = v.artifact_id and r.number = v.number
	where v.artifact_id = $1 and v.number = $2`

// RenderVersion is version number of the document artifactID as every read of it answers it: with
// a version_renders row, RenderAt as of the row's time; without one, the version's stored markdown
// byte for byte as StoredBytes, and NoBytes, with an empty Markdown, where that markdown is null. A
// version the document does not hold is pgx.ErrNoRows.
func RenderVersion(ctx context.Context, q Queryer, who model.Actor, artifactID string, number int) (Rendering, error) {
	var at *time.Time
	var empty bool
	var markdown *string
	if err := q.QueryRow(ctx, versionRenderStatement, artifactID, number).Scan(&at, &empty, &markdown); err != nil {
		return Rendering{}, fmt.Errorf("read version %d of %s: %w", number, artifactID, err)
	}
	switch {
	case at != nil:
		return RenderAt(ctx, q, who, artifactID, *at)
	case empty:
		return Rendering{Source: NoBytes}, nil
	}
	return Rendering{Markdown: *markdown, Source: StoredBytes}, nil
}

// errVersionRenderCut refuses a version_renders row of a record read at a render's cut, which
// names no seq the record read.
var errVersionRenderCut = errors.New("docs: a version's record is read at a cut of the tuple log's seq, not at a render's time")

// QueueVersionRender queues on b, the write phase of the transaction that writes version number of
// the document artifactID's tree, that version's version_renders row: the digest of record, the
// record of the document's query the version was written with, which artifact_version_text_sha256
// then answers for the version and an approval of it pins; the time the version renders as of,
// the time of the record's cut, which is no earlier than any tuple the transaction dated before it
// read the cut (refs.HeadCut); and the highest graph_tuples.seq the record read. The row commits
// with the version or not at all. It writes a table of its own and no tuple of the store.
func QueueVersionRender(b *store.WriteBatch, artifactID string, number int, record QueryRecord) error {
	if record.Cut.Dated || record.Cut.At.IsZero() {
		return errVersionRenderCut
	}
	b.Queue("write the version_renders row of version "+fmt.Sprint(number), `
		insert into version_renders (artifact_id, number, record_sha256, at, read_seq) values ($1, $2, $3, $4, $5)`,
		artifactID, number, record.Digest, record.Cut.At, record.Cut.ReadSeq)
	return nil
}

// ForgetVersionRenders deletes, in tx, the version_renders row of each version a statement of
// tx deletes without deleting its document - version numbers[i] of the document artifacts[i] -
// and answers how many it deleted. A delete of a version row runs it beside that delete, in the
// same transaction (grapherase's erase), so no row is left naming a version that is gone; a
// delete of the document takes the rows by their foreign key. The census of the rendered versions
// reads a row whose version is gone as a difference (graphcensus.CompareVersionRenders).
func ForgetVersionRenders(ctx context.Context, tx pgx.Tx, artifacts []string, numbers []int32) (int64, error) {
	tag, err := tx.Exec(ctx, `
		delete from version_renders where exists (
			select 1 from unnest($1::uuid[], $2::int[]) as doomed (artifact, number)
			where doomed.artifact = version_renders.artifact_id and doomed.number = version_renders.number)`,
		artifacts, numbers)
	if err != nil {
		return 0, fmt.Errorf("delete the version_renders rows of %d versions: %w", len(artifacts), err)
	}
	return tag.RowsAffected(), nil
}

// versionRenderAt renders the document artifactID as of at for the census of the rendered
// versions: RenderAt's markdown, as the server reads the store.
func versionRenderAt(ctx context.Context, q graphstore.Queryer, artifactID string, at time.Time) (string, error) {
	rendering, err := RenderAt(ctx, q, model.Actor{}, artifactID, at)
	return rendering.Markdown, err
}

// ReadVersionRenderCensus reads, through q, what the census of `census --store` prints of the
// versions version_renders holds (graphcensus.VersionRenderCensus): each one's stored markdown
// held to a fresh render of the tree as of the time its row records, which only docs renders.
func ReadVersionRenderCensus(ctx context.Context, q graphstore.Queryer) (graphcensus.VersionRenderCensus, error) {
	return graphcensus.CompareVersionRenders(ctx, q, versionRenderAt)
}
