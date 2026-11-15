package grapherase

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
)

// ErasedRows is one table the erase emptied of a node's rows.
type ErasedRows struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

// ErasedVersion is one document version whose markdown held the erased text, named as a reader
// addresses it. It carries no markdown: the record of an erase holds no erased text.
type ErasedVersion struct {
	Document string `json:"document"`
	Issue    string `json:"issue,omitempty"`
	Project  string `json:"project,omitempty"`
	Version  int    `json:"version"`
}

// ErasedReview is one approval whose content_sha256 covered the erased bytes. It cannot be
// rewritten - artifact_review_content_sha256_update raises on any statement that moves a stored
// digest - so the erase ends it, and names it by its document and its version.
type ErasedReview struct {
	ID       string `json:"id"`
	Document string `json:"document"`
	Issue    string `json:"issue,omitempty"`
	Version  int    `json:"version"`
	State    string `json:"state"`
}

// ErasedDocument is one document whose live state the erase replaces, named as a reader addresses
// it, with what replacing it costs. It carries no text.
type ErasedDocument struct {
	Document string `json:"document"`
	Issue    string `json:"issue,omitempty"`
	Project  string `json:"project,omitempty"`
	// Held is whether its live text held the erased text, which then reads as the marker. A
	// document whose live text held none - a version of it held the text, and an edit took it
	// out since - has its history replaced alone, since the items that edit deleted can still
	// hold the bytes there.
	Held bool `json:"live_text_held"`
	// Left is whether its live text holds the erased bytes where its markdown spells them
	// otherwise than the erased text is written - an escaped `*` or `_`, for one - so no marker
	// can stand where they are. Such a document refuses the erase (Unredacted).
	Left bool `json:"live_bytes_left"`
	// Updates is how many stored updates its history held, all of which one seed replaces;
	// AnchorMarks and MarginRecords are what goes with them (docs.ErasedLiveText).
	Updates       int64 `json:"updates"`
	AnchorMarks   int   `json:"anchor_marks"`
	MarginRecords int   `json:"margin_records"`
	live          *docs.ErasedLiveText
}

// Operator is who ran the erase: the database role the credential is, and the account and host
// the command ran from. Only a person holding the deployment's database credential runs it, so
// this is the whole of who that was.
type Operator struct {
	DatabaseUser string `json:"database_user"`
	SystemUser   string `json:"system_user,omitempty"`
	Host         string `json:"host,omitempty"`
}

// Erasure is one run of `envoy-dispatch erase`: the node it addressed, everything that would
// go with it, and - once Applied - everything that did.
type Erasure struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Supertag string `json:"supertag"`
	// Tuples is how many tuples state something out of the node, Inbound how many state
	// something whose far end it is.
	Tuples  int64 `json:"tuples"`
	Inbound int64 `json:"inbound_tuples"`
	// Rows are the projection rows of the node and the rows that cannot exist without them.
	Rows []ErasedRows `json:"rows"`
	// RedactedRows are the rows the run rewrote where it may take no row away: a block of a
	// document that quoted the erased text, whose row projects a block node of its own, the
	// append-only event log, the one stored update each replaced history is seeded with, and the
	// search vectors written again from the latest version left: a document's search row and its
	// issue's own vector where the document is the issue's spec. Each that held the bytes holds
	// the marker where they were.
	RedactedRows []ErasedRows `json:"redacted"`
	// Versions and Reviews are the document versions whose markdown holds the erased text and
	// the approvals whose digest covered those bytes.
	Versions []ErasedVersion `json:"versions"`
	Reviews  []ErasedReview  `json:"reviews"`
	// Documents are the documents whose live state the erase replaces, and Recounted the
	// projects whose term statistics it counted again.
	Documents []ErasedDocument `json:"documents"`
	Recounted []string         `json:"recounted"`
	// Blockers are the rows of other things that point at this node's projection and are not
	// part of it: a reply to an erased comment, an ask handed back through it. Each refuses the
	// run, because deleting them would remove somebody else's words and rewriting them would
	// state something nobody said.
	Blockers []string `json:"-"`
	// Applied is whether this run removed anything. A run with no --confirm reads everything
	// above and rolls back, which is how the command refuses until it has listed what it would
	// remove.
	Applied bool      `json:"-"`
	RanBy   Operator  `json:"-"`
	RanAt   time.Time `json:"-"`
	// Swept is how many display trees the sweep after the commit deleted (sweepTrees): each is
	// one a read that began before the commit stored back inside the window it waits out.
	Swept int64 `json:"-"`
	// sums are the keys of the display trees the run deleted (treeSums). They are held here and
	// nowhere else - no row, no record, no log - since each is the digest of an erased text, and
	// they go with the process once the sweep has read them.
	sums [][]byte
}

// Unredacted names every document whose live text holds the erased bytes where its markdown
// spells them otherwise (ErasedDocument.Left). The erase refuses while one is listed: the seed of
// its history would hold those bytes, and a run that reported success over them would not be
// an erase. An edit that takes them out of the document is what lets the erase run.
func (e *Erasure) Unredacted() []string {
	var documents []string
	for _, document := range e.Documents {
		if document.Left {
			documents = append(documents, document.Document)
		}
	}
	return documents
}

// removed adds one table's deleted rows to the report, keeping one line per table.
func (e *Erasure) removed(table string, rows int64) {
	e.Rows = countedRows(e.Rows, table, rows)
}

// redacted adds one table's rewritten rows to the report, counted as removed counts a deleted
// one, so a run says what it rewrote as well as what it emptied.
func (e *Erasure) redacted(table string, rows int64) {
	e.RedactedRows = countedRows(e.RedactedRows, table, rows)
}

// countedRows adds one table's rows to a count of them, keeping one line per table.
func countedRows(counted []ErasedRows, table string, rows int64) []ErasedRows {
	if rows == 0 {
		return counted
	}
	for i := range counted {
		if counted[i].Table == table {
			counted[i].Rows += rows
			return counted
		}
	}
	return append(counted, ErasedRows{Table: table, Rows: rows})
}

// Write prints what the run read and what it did, the versions, the reviews and the documents
// whose live state it replaces named one per line, so an operator reads every one before
// confirming. A document's line says what replacing its history costs, which is what the run
// cannot give back: the edits before it, the anchors and margin records a seed does not carry,
// and the keystrokes of an editor open on it that were not yet stored.
func (e *Erasure) Write(w io.Writer) {
	verb := "would remove"
	if e.Applied {
		verb = "removed"
	}
	fmt.Fprintf(w, "erase %s:%s (supertag %s), %s by %s at %s\n",
		e.Kind, e.ID, e.Supertag, verb, e.RanBy.DatabaseUser, e.RanAt.Format(time.RFC3339))
	fmt.Fprintf(w, "  tuples: %d out of the node, %d into it\n", e.Tuples, e.Inbound)
	if len(e.Rows) == 0 {
		fmt.Fprintln(w, "  rows: none")
	}
	for _, rows := range e.Rows {
		fmt.Fprintf(w, "  rows: %s %d\n", rows.Table, rows.Rows)
	}
	for _, rows := range e.RedactedRows {
		fmt.Fprintf(w, "  redacted: %s %d\n", rows.Table, rows.Rows)
	}
	fmt.Fprintf(w, "  versions holding the erased text: %d\n", len(e.Versions))
	for _, version := range e.Versions {
		fmt.Fprintf(w, "    %s version %d%s\n", version.Document, version.Version, versionOwner(version))
	}
	fmt.Fprintf(w, "  reviews over those bytes: %d\n", len(e.Reviews))
	for _, review := range e.Reviews {
		fmt.Fprintf(w, "    %s version %d %s (review %s)%s\n",
			review.Document, review.Version, review.State, review.ID, reviewIssue(review))
	}
	fmt.Fprintf(w, "  live documents whose history is replaced: %d\n", len(e.Documents))
	for _, document := range e.Documents {
		if document.Left {
			fmt.Fprintf(w, "    %s%s: not redacted: its live text holds the erased bytes where its markdown spells "+
				"them otherwise than the erased text is written - an escaped * or _, for one - so no marker can stand "+
				"where they are; the erase refuses until an edit takes them out of the document\n",
				document.Document, owner(document.Issue, document.Project))
			continue
		}
		text := "its live text holds none of the erased text"
		if document.Held {
			text = "its live text is redacted, the marker standing where the erased text was"
		}
		fmt.Fprintf(w, "    %s%s: %s; its %d stored update(s) are replaced by one; %d anchor mark(s) and "+
			"%d margin record(s) go with them; an editor open on it reloads and loses what it has not stored\n",
			document.Document, owner(document.Issue, document.Project), text, document.Updates,
			document.AnchorMarks, document.MarginRecords)
	}
	if len(e.Recounted) > 0 {
		fmt.Fprintf(w, "  term statistics counted again: %s\n", strings.Join(e.Recounted, ", "))
	}
	if e.Applied {
		fmt.Fprintf(w, "  text_trees swept after the commit: %d\n", e.Swept)
	}
	for _, blocker := range e.Blockers {
		fmt.Fprintf(w, "  blocked: %s\n", blocker)
	}
}

// versionOwner names the issue or the project a version's document belongs to, for the line
// that lists it.
func versionOwner(version ErasedVersion) string {
	return owner(version.Issue, version.Project)
}

// owner is " of " the issue a document belongs to, or else its project, for a line that names
// the document.
func owner(issue, project string) string {
	if issue != "" {
		return " of " + issue
	}
	if project != "" {
		return " of " + project
	}
	return ""
}

func reviewIssue(review ErasedReview) string {
	if review.Issue == "" {
		return ""
	}
	return " of " + review.Issue
}
