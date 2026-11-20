package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// On an issue that Legion runs, a version of the issue's spec (its primary document) or of its
// Verification document is written only by a person, by an architect of the issue, or by an
// architect of an issue above it: the session of an architect row of issue_role_holders on the
// issue's parent chain (dispatch://INFRA-4764/spec, R1 and R2). Every route that writes such a
// version calls refuseSpecWriter inside its transaction, after the owner's lock and before its
// first document call and its first write, so a write it refuses with 403
// DOCUMENT_WRITER_NOT_ARCHITECT stores nothing and appends no event; the source guard
// (spec_writer_route_guard_test.go) holds every route of the table to that. The check sits in the
// routes, where the writer is known, and not in the document service: many versions are written
// later, by a settlement, which has no request writer.
//
// The check passes a person at once: a request whose credential holds the person scope, a person
// at a browser among them, read before the handler's transaction opens (specWriterOf), and
// Dispatch's own system writer (parentStatusFixWriter). For every other writer it reads one
// statement over the chain of the write: whether Legion runs it, by the legion-label walk that
// every "Legion runs" answer reads (issueChainSQL, legionOnChainSQL); whether the project is marked
// (project_role_holder_marks, which the first role-holder publication Dispatch takes for an issue
// of the project writes, issue_role_holders.go); the architect sessions on the chain, nearest
// first; the role the writer's session holds on the issue; and the project's proven_sessions
// setting. An issue that Legion does not run, and an issue of a project that no publication marked
// yet, pass: nothing changes for a project until the daemon publishes its first role holders. An
// architect's session passes. For a close, a reopen and a write that takes an issue out of Legion,
// the session that the Legion daemon of the issue's project writes as (legionDaemonSessionOf)
// passes too. While the project's proven_sessions is on, each of those sessions passes only on a
// credential that proves it (model.Actor.Proven): a broker token of that session. Every other
// writer is refused, and the server logs one line for each refusal.
//
// A create of an issue under a parent, and a move of an issue under a parent, are judged on the row
// as the write leaves it: Legion runs it when its own labels or its new parent's chain carry the
// label (legionManagedStatusOfRow), and its architects are those of the parent's chain. A create
// under no parent is not checked: the controller files a root with the label legion when a
// person's answer asks Legion to run it.

// specWriterArchitectRole is the role whose sessions write the spec and the Verification document
// of an issue Legion runs: the first role of legionagents.Roles.
const specWriterArchitectRole = "architect"

// specWriter is who writes, as the check judges it: actor is the actor the write records, and person
// is that the check passes the write at once, because the request's credential holds the person
// scope or because the writer is Dispatch's own system writer.
type specWriter struct {
	actor  model.Actor
	person bool
}

// specWriterOf is the writer of r, recorded as actor. It resolves the request's credential
// (callerHoldsScope), which reads the store, so a handler calls it before it opens its
// transaction: the pool refuses a second connection inside one (store.ErrNestedAcquire).
func (s *server) specWriterOf(r *http.Request, actor model.Actor) specWriter {
	return specWriter{actor: actor, person: s.callerHoldsScope(r, model.ScopePerson)}
}

// specWriter is the check's writer of an issue write (writeIssue, a status change's plan): a person
// at a browser or on a credential that holds the person scope, read from the credential the writer
// carries, with no second read of the pool. Dispatch's system writer passes as Dispatch's other own
// writes do, so the check never stops the boot fix of parent and child statuses.
func (writer issueWriter) specWriter() specWriter {
	return specWriter{
		actor:  writer.actor,
		person: writer.human || writer.system || writer.credential.Holds(model.ScopePerson),
	}
}

// specWriteAct is what a checked write does. It words the refusal, and it decides whether the
// session of the Legion daemon passes.
type specWriteAct int

const (
	// specWriteDocument writes a version of the document: an upload, an edit, a named version, the
	// resolution, the retraction or the edit of a block ask, the accept or the reject of a
	// suggestion.
	specWriteDocument specWriteAct = iota
	// specWriteCreate creates an issue, with its spec, under a parent.
	specWriteCreate
	// specWriteMove moves an issue under a parent.
	specWriteMove
	// specWriteClose closes the issue, which writes the blocks of its open asks withdrawn.
	specWriteClose
	// specWriteReopen reopens the issue, which writes the blocks of its withdrawn asks open again.
	specWriteReopen
	// specWriteOutOfLegion writes the labels or the parent of the issue so that Legion no longer
	// runs it, after which the check no longer holds its documents.
	specWriteOutOfLegion
)

// daemonPasses reports whether the session of the Legion daemon of the issue's project passes the
// check for act: a close, a reopen and a write that takes the issue out of Legion, which the daemon
// makes as it runs an issue.
func (act specWriteAct) daemonPasses() bool {
	return act == specWriteClose || act == specWriteReopen || act == specWriteOutOfLegion
}

// specWrite is one write the check judges.
type specWrite struct {
	act specWriteAct
	// issue is the issue the write is on: the issue whose document it writes, or the issue it
	// moves, closes, reopens or takes out of Legion. It is empty for a create, whose issue has no
	// key yet.
	issue string
	// document is the name the refusal gives the document: spec.md or verification.md. A create, a
	// move, a close, a reopen and a write that takes the issue out of Legion write no text of their
	// own, and the refusal names the issue's spec.md for them.
	document string
	// under is the parent of a create or of a move, and labels are the labels the write leaves on
	// the row: the check reads the row as the write leaves it. under is empty for every other
	// write, which the check reads on the issue's own chain as it stands.
	under  string
	labels []string
}

// chainIssue is the first issue of the write's chain, the one its architects are read from and
// above: the parent of a create or of a move, and else the issue itself.
func (write specWrite) chainIssue() string {
	if write.under != "" {
		return write.under
	}
	return write.issue
}

// specWriterReadSQL is the check's one statement over the chain from the row start selects
// (issueChainSQL, which carries each row's key): whether Legion runs that row (legionOnChainSQL);
// the subject, the first issue of the chain, which is the issue itself or, for a row that start
// gives no key, its parent; whether project_role_holder_marks holds the subject's project; that
// project's proven_sessions and its key; the role the session $1 holds on the subject, empty for
// none; and the session and the issue of each architect row of issue_role_holders on the chain,
// nearest first.
func specWriterReadSQL(start string) string {
	return `
	with recursive ` + issueChainSQL(start, "key") + `,
	subject as (
		select c.key from chain c where c.key is not null order by c.depth limit 1
	),
	project as (
		select i.project_key as key from issues i join subject s on s.key = i.key
	),
	architects as (
		select h.session, c.key, c.depth
		from chain c join issue_role_holders h on h.issue_key = c.key and h.role = '` + specWriterArchitectRole + `'
	)
	select ` + legionOnChainSQL + `,
		exists (select 1 from project_role_holder_marks m join project p on p.key = m.project_key),
		coalesce((select pr.proven_sessions from projects pr join project p on p.key = pr.key), false),
		coalesce((select p.key from project p), ''),
		coalesce((select h.role from issue_role_holders h join subject s on s.key = h.issue_key where h.session = $1), ''),
		coalesce((select array_agg(a.session order by a.depth) from architects a), '{}'),
		coalesce((select array_agg(a.key order by a.depth) from architects a), '{}')`
}

var (
	// specWriterOfIssueSQL is the check's statement for a write on the issue $2, over its chain as
	// it stands.
	specWriterOfIssueSQL = specWriterReadSQL(`select i.key, i.parent_key, i.labels, 0 as depth from issues i where i.key = $2`)
	// specWriterOfRowSQL is the check's statement for a create or a move under the parent $2, over
	// the row as the write leaves it, $3 its labels: the walk of legionManagedStatusOfRowSQL. The
	// row's own key is not carried, so its architects are read from the parent's chain alone.
	specWriterOfRowSQL = specWriterReadSQL(`select null::text as key, $2::text as parent_key, $3::text[] as labels, 0 as depth`)
)

// specWriterRead is what the check's one statement reads over the chain of a write.
type specWriterRead struct {
	legion, marked, provenSessions bool
	project, role                  string
	// architects are the architect sessions on the chain, nearest first, and architectIssues the
	// issue of each, in the same order.
	architects, architectIssues []string
}

// specWriterRefusal is a write the check refuses: the write, the writer's session, what the check
// read, and whether the writer's session would pass but the request's credential does not prove
// it while the project's proven_sessions is on.
type specWriterRefusal struct {
	write    specWrite
	session  string
	read     specWriterRead
	unproven bool
}

// refuseSpecWriter is the check of who writes a Legion-run issue's spec or Verification document,
// which every route that writes one calls (the file's comment above). It answers nil for a write it
// passes, and for a write it refuses it logs one line and answers 403
// DOCUMENT_WRITER_NOT_ARCHITECT. q is the caller's transaction, after the owner's lock.
func refuseSpecWriter(ctx context.Context, q queryer, writer specWriter, write specWrite) error {
	refusal, err := judgeSpecWriter(ctx, q, writer, write)
	if err != nil || refusal == nil {
		return err
	}
	issue := write.issue
	if issue == "" {
		issue = write.under
	}
	slog.Info("dispatch: refused a document write by a session that is not an architect of the issue",
		"issue", issue, "document", write.document, "session", refusal.session,
		"reason", refusal.reason(), "architects", refusal.read.architects)
	return errorf(http.StatusForbidden, "DOCUMENT_WRITER_NOT_ARCHITECT", "%s", refusal.message())
}

// judgeSpecWriter is the check's decision on write, with no log line: nil when it passes, and else
// the refusal. It reads one statement through q, and none for a person.
func judgeSpecWriter(ctx context.Context, q queryer, writer specWriter, write specWrite) (*specWriterRefusal, error) {
	if writer.person {
		return nil, nil
	}
	session := ""
	if writer.actor.Kind == "session" {
		session = writer.actor.ID
	}
	statement, args := specWriterOfIssueSQL, []any{session, write.issue}
	if write.under != "" {
		statement, args = specWriterOfRowSQL, []any{session, write.under, write.labels}
	}
	var read specWriterRead
	if err := q.QueryRow(ctx, statement, args...).Scan(
		&read.legion, &read.marked, &read.provenSessions, &read.project, &read.role, &read.architects, &read.architectIssues,
	); err != nil {
		return nil, fmt.Errorf("read who may write the documents of %s: %w", write.chainIssue(), err)
	}
	if !read.legion || !read.marked {
		return nil, nil
	}
	passes := session != "" && (slices.Contains(read.architects, session) ||
		(write.act.daemonPasses() && session == legionDaemonSessionOf(write.issue)))
	if passes && (!read.provenSessions || writer.actor.Proven) {
		return nil, nil
	}
	return &specWriterRefusal{write: write, session: writer.actor.ID, read: read, unproven: passes}, nil
}

// reason is why the check refused, as its log line gives it.
func (refusal *specWriterRefusal) reason() string {
	if refusal.unproven {
		return "the project records proven sessions, and the request's credential does not prove the session"
	}
	return "the session is not an architect of the issue or of an issue above it"
}

// message is the one wording of the check's refusal, in plain short sentences: what the session
// cannot do, on which issue and to which document, who can, the role the session holds on the
// issue, and each architect session Dispatch records on the chain with its issue. The proof
// refusal names the project and says that the request's credential does not prove the session.
func (refusal *specWriterRefusal) message() string {
	write, session, read := refusal.write, refusal.session, refusal.read
	chain := write.chainIssue()
	var act, why string
	switch write.act {
	case specWriteCreate:
		act = "create an issue under " + chain
		why = "Legion runs the new issue, and the create writes its " + write.document + "."
	case specWriteMove:
		act = "move " + write.issue + " under " + chain
		why = "Legion runs " + write.issue + " under " + chain + ", and the move changes who can write its " + write.document + "."
	case specWriteClose:
		act = "close " + write.issue
		why = "Legion runs " + write.issue + ", and the close writes the asks in its " + write.document + "."
	case specWriteReopen:
		act = "reopen " + write.issue
		why = "Legion runs " + write.issue + ", and the reopen writes the asks in its " + write.document + "."
	case specWriteOutOfLegion:
		act = "take " + write.issue + " out of Legion"
		why = "Legion runs " + write.issue + ". When Legion stops running it, any session can write its " + write.document + "."
	default:
		act = "write " + write.document + " of " + write.issue
		why = "Legion runs " + write.issue + "."
	}
	var message strings.Builder
	fmt.Fprintf(&message, "Session %s cannot %s.", session, act)
	if refusal.unproven {
		fmt.Fprintf(&message, " Project %s records proven sessions, so it takes this write only on a credential that proves the writer's session."+
			" This request's credential does not prove session %s.", read.project, session)
		return message.String()
	}
	who := "a person or an architect of " + chain + " or of an issue above it"
	if write.act.daemonPasses() {
		who = "a person, an architect of " + chain + " or of an issue above it, or the Legion daemon of its project"
	}
	fmt.Fprintf(&message, " %s Only %s can %s.", why, who, act)
	if read.role != "" {
		fmt.Fprintf(&message, " Session %s is the %s of %s.", session, read.role, chain)
	} else {
		fmt.Fprintf(&message, " Session %s holds no role on %s.", session, chain)
	}
	if len(read.architects) == 0 {
		fmt.Fprintf(&message, " Dispatch records no architect on %s or above it.", chain)
	} else {
		named := make([]string, len(read.architects))
		for index, architect := range read.architects {
			named[index] = "session " + architect + " on " + read.architectIssues[index]
		}
		fmt.Fprintf(&message, " Dispatch records these architects on %s and above it: %s.", chain, strings.Join(named, ", "))
	}
	message.WriteString(" Propose the change with a suggestion or a comment.")
	return message.String()
}

// specWriterDocumentName is the name the check gives a stored document, and whether the check
// holds the document at all: spec.md for an issue's spec (isSpecDocument), and verification.md for
// its Verification document (documentPlays, so an older row whose role is still null is held by
// its slug). The check holds no other document, and no document of a project or of a session.
func specWriterDocumentName(artifact model.Artifact) (string, bool) {
	switch {
	case artifact.IssueKey == nil:
		return "", false
	case isSpecDocument(artifact):
		return "spec.md", true
	case documentPlays(artifact, model.ArtifactRoleVerification):
		return "verification.md", true
	}
	return "", false
}

// refuseSpecWriterOfDocument is the check of a write of the stored document artifact, which it
// passes for a document the check does not hold.
func refuseSpecWriterOfDocument(ctx context.Context, q queryer, writer specWriter, artifact model.Artifact) error {
	document, held := specWriterDocumentName(artifact)
	if !held || writer.person {
		return nil
	}
	return refuseSpecWriter(ctx, q, writer, specWrite{issue: *artifact.IssueKey, document: document})
}

// refuseSpecWriterOfDocuments is the check of a write of the stored documents ids, read through q
// in their order. The first document the check holds decides, and the refusal names it: the
// documents one write changes together are documents of one owner (a block ask's document and
// each copy of the ask), so one check of the issue decides them all.
func refuseSpecWriterOfDocuments(ctx context.Context, q queryer, writer specWriter, ids ...string) error {
	if writer.person {
		return nil
	}
	for _, id := range ids {
		artifact, err := loadArtifactRowByID(ctx, q, id)
		if err != nil {
			return err
		}
		if _, held := specWriterDocumentName(artifact); held {
			return refuseSpecWriterOfDocument(ctx, q, writer, artifact)
		}
	}
	return nil
}

// refuseSpecWriterOfAsk is the check of a write that settles a block ask's closed state into its
// document and into each copy of the ask (writeClosedBlockState): the block's document first, then
// the copies (docs.API.CopiesOf). ctx is joined to the caller's transaction tx, which holds the
// ask's owner lock. An ask with no block writes no document.
func (s *server) refuseSpecWriterOfAsk(ctx context.Context, tx pgx.Tx, writer specWriter, ask model.Ask) error {
	if writer.person || ask.BlockID == nil {
		return nil
	}
	block, err := blockArtifactOf(ask)
	if err != nil {
		return err
	}
	copies, err := s.deps.Docs.CopiesOf(ctx, ask)
	if err != nil {
		return err
	}
	return refuseSpecWriterOfDocuments(ctx, tx, writer, append([]string{block}, copies...)...)
}

// refuseSpecWriterOfIssueWrite is the check of one write of an issue: a PATCH's (writeIssue) or a
// step of a status change, judged on the step as its writer read it under the owner locks. A move
// under a parent is checked on the new parent's chain, with the labels the write leaves. A close
// and a reopen are checked on the issue's own chain, with the daemon's pass. A write of the labels
// or the parent after which Legion no longer runs the issue - Legion runs it before the write
// (legionManagedStatus) and not as the write leaves it (legionManagedStatusOfRow) - is checked on
// the issue's chain as it stands before the write, with the daemon's pass. A labels write that
// keeps the label legion on the chain, and a labels_add, which cannot remove it, take nothing out.
func refuseSpecWriterOfIssueWrite(ctx context.Context, tx pgx.Tx, writer specWriter, step issueWriteStep) error {
	if writer.person {
		return nil
	}
	labels := step.labelsBefore
	if step.labelsAfter != nil {
		labels = *step.labelsAfter
	}
	if step.changesParent() && step.parentAfter != nil {
		if err := refuseSpecWriter(ctx, tx, writer, specWrite{
			act: specWriteMove, issue: step.key, document: "spec.md", under: *step.parentAfter, labels: labels,
		}); err != nil {
			return err
		}
	}
	var act specWriteAct
	switch {
	case step.statusAfter != nil && *step.statusAfter == "done" && step.statusBefore != "done":
		act = specWriteClose
	case step.statusAfter != nil && *step.statusAfter != "done" && step.statusBefore == "done":
		act = specWriteReopen
	case step.changesParent() || step.labelsAfter != nil:
		parent := step.parentBefore
		if step.reparent {
			parent = step.parentAfter
		}
		before, err := legionManagedStatus(ctx, tx, step.key)
		if err != nil || !before {
			return err
		}
		after, err := legionManagedStatusOfRow(ctx, tx, step.key, parent, labels)
		if err != nil || after {
			return err
		}
		act = specWriteOutOfLegion
	default:
		return nil
	}
	return refuseSpecWriter(ctx, tx, writer, specWrite{act: act, issue: step.key, document: "spec.md"})
}

// spareSettlementOfARefusedClose reports whether the check refuses writer's close of the issue key,
// read once on the pool before the close's transaction opens (writeIssue), so that a refused close
// does not settle the issue's documents first (settleIssueBeforeClose). It logs nothing and answers
// no refusal: the check under the close's owner locks decides, after the write's other refusals. A
// read that fails spares nothing.
func (s *server) spareSettlementOfARefusedClose(ctx context.Context, writer issueWriter, key string) bool {
	pool, err := s.readyPool()
	if err != nil {
		return false
	}
	refusal, err := judgeSpecWriter(ctx, pool, writer.specWriter(), specWrite{act: specWriteClose, issue: key, document: "spec.md"})
	return err == nil && refusal != nil
}
