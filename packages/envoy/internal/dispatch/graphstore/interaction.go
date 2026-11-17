package graphstore

import (
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// A run of the command bar (api/going.go) is an interaction node of this store
// (dispatch://INFRA-1950/spec#b-r-interaction), addressed `dispatch://interaction/<uuid>` by the
// run's own uuid, and `going_requests` and `going_events` are its projections: the row of the
// first projects the node and its made_by, and each row of the second one frame value of it.
// These are the facts of one run in the one shape every writer of a run states them in: the
// going POST's record, and the backfill's writer for one run an earlier build recorded with no
// node (graphcensus.WriteRun).

// The attributes of an interaction beside its words (AttributeWords), as the tuple that states
// each value spells it and as migration 20261116123354 declares them on the interaction type: the
// moment the run is over, and each frame of its stream. Its project is the base attribute
// project, which no attribute node declares.
const (
	AttributeEndsAt = "ends_at"
	AttributeFrame  = "frame"
)

// Interaction is one run of the command bar as the store holds it: ID, the run's own uuid in the
// lowercase form Postgres writes it; Author, the whole credential the POST proved, recorded
// (model.Actor.Recorded), which is the node's made_by and which going_requests.author projects;
// the project the run was asked under, the empty string for every project, which states no
// project; the words of the question; At, when it was asked; and EndsAt, when its window closes.
type Interaction struct {
	ID      string
	Author  model.Actor
	Project string
	Words   string
	At      time.Time
	EndsAt  time.Time
}

// InteractionFacts is what the store holds of one run: its interaction node, then the author's
// own nodes, and its tuples. The node is live and reserved for its author whoever that is, since
// a run is read by the credential that asked it and by nobody else, and a run whose author names
// nobody - a credential that proves no author (ProvesAuthor) - is read by nobody, as the stream
// reads it (api.streamGoingAnswer). It carries its words and the moment it ends, each also stated
// as a value tuple, and the run's project where it names one. Where AuthorOf names somebody of the
// author, the run also states its authored_by tuple, with that author's own person or session node
// and the aliases the server proved of them (AuthorFacts).
//
// ends_at is written in UTC as RFC 3339 with its fraction of a second, a timestamp attribute's
// text, so the node's attribute and its value tuple read the same instant the run's row holds.
// Every fact is made by the author, at the moment the run was asked, and caused by no event: a
// run appends none.
func InteractionFacts(run Interaction) ([]Node, []Tuple) {
	endsAt := run.EndsAt.UTC().Format(time.RFC3339Nano)
	authors, authored, _ := AuthorFacts(KindInteraction, run.ID, run.Author, run.At, NoEvent)
	nodes := make([]Node, 0, 1+len(authors))
	nodes = append(nodes, Node{
		Kind: KindInteraction, ID: run.ID, Supertag: KindInteraction,
		Visibility: VisibilityAuthor, State: StateLive, Project: run.Project,
		Attributes: map[string]any{AttributeWords: run.Words, AttributeEndsAt: endsAt},
		MadeAt:     run.At, MadeBy: run.Author, SourceEvent: NoEvent,
	})
	nodes = append(nodes, authors...)
	tuples := make([]Tuple, 0, 2+len(authored))
	for _, attribute := range [...]struct{ kind, value string }{{AttributeWords, run.Words}, {AttributeEndsAt, endsAt}} {
		tuples = append(tuples, Tuple{
			FromKind: KindInteraction, FromID: run.ID, Kind: attribute.kind, ToKind: KindValue, ToID: attribute.value,
			At: run.At, Who: run.Author, SourceEvent: NoEvent, Asserted: true,
		})
	}
	return nodes, append(tuples, authored...)
}

// FrameWriter is who every frame value tuple is stated by: the server answering the run - the
// answer, a reader that ends the run at its deadline, or the sweep - which acts for no person, as
// LensKindWriter does. The run's author asked the question; no frame is their writing.
var FrameWriter = model.Actor{Kind: "system", ID: "going"}

// FrameValueSQL is the value of one frame tuple as SQL over the frame's sequence, name and data,
// each an SQL expression: the JSON object {"sequence": <integer>, "name": <text>, "data": <the
// frame's payload>}, written as Postgres writes jsonb as text, so its keys stand in jsonb's own
// order ("data", "name", "sequence") and its spacing is jsonb's. A frame's value is computed in
// SQL rather than in Go so that the frame path, which reads the sequence it takes in the same
// statement, and a writer that states a frame from a going_events row an earlier build wrote
// (graphcensus.WriteRun) write the same text for the same frame, byte for byte. The text is the
// value tuple's far end, which no repeat key or read index bounds; a reader orders a run's frames
// by `(to_id::jsonb ->> 'sequence')::integer`.
func FrameValueSQL(sequence, name, data string) string {
	return `(jsonb_build_object('sequence', ` + sequence + `, 'name', ` + name + `, 'data', ` + data + `)::text)`
}

// FrameTuple is one frame of the run whose id is run, stated as a value tuple of its interaction
// node: value is the frame's text (FrameValueSQL), and at the moment its going_events row is
// written, which that row's created_at holds. It is stated by FrameWriter and caused by no event.
func FrameTuple(run, value string, at time.Time) Tuple {
	return Tuple{
		FromKind: KindInteraction, FromID: run, Kind: AttributeFrame, ToKind: KindValue, ToID: value,
		At: at, Who: FrameWriter, SourceEvent: NoEvent, Asserted: true,
	}
}
