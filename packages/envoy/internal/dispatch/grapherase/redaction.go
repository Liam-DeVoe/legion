package grapherase

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// placeholders is `$1 … $n` for one redaction: the marker first and one for each erased text
// after it, which is the order redactionArgs binds them in.
func placeholders(texts []string) []string {
	bound := make([]string, len(texts)+1)
	for i := range bound {
		bound[i] = "$" + strconv.Itoa(i+1)
	}
	return bound
}

// redactionArgs are those parameters' values.
func redactionArgs(marker string, texts []string) []any {
	args := make([]any, 0, len(texts)+1)
	args = append(args, marker)
	for _, text := range texts {
		args = append(args, text)
	}
	return args
}

// jsonStrings is each of those parameters as a jsonb rendered to text holds it: the value's own
// json escaping, spelled by the database rather than by this package, so what a statement looks
// for in a payload is byte for byte what that payload holds. to_jsonb wraps a string in quotes,
// which the substring cuts back off.
func jsonStrings(bound []string) []string {
	escaped := make([]string, len(bound))
	for i, value := range bound {
		rendered := "to_jsonb(" + value + "::text)::text"
		escaped[i] = "substring(" + rendered + " from 2 for length(" + rendered + ") - 2)"
	}
	return escaped
}

// redaction is one column with every erased text replaced by the marker, as a statement spells
// it: one replace() around the last, so a row holding two of those texts is rewritten by one
// pass and counted once. values is placeholders, or jsonStrings of them for a column a
// statement reads as json.
func redaction(column string, values []string) string {
	rewritten := column
	for _, text := range values[1:] {
		rewritten = "replace(" + rewritten + ", " + text + ", " + values[0] + ")"
	}
	return rewritten
}

// blockTextHolds is the test redactBlocks rewrites a document_block_texts row named held by, and
// excerptHolds the one it rewrites a document_blocks row's excerpt by: the column differs from
// itself with every erased text replaced by the marker. bound is placeholders of those texts.
// quotedDocuments reads by the same two, so the documents whose owners the erase takes for its
// blocks are the ones these statements write.
func blockTextHolds(bound []string) string {
	return "held.text <> " + redaction("held.text", bound)
}

// excerptHolds is blockTextHolds' test over a document_blocks row's excerpt.
func excerptHolds(bound []string) string {
	return "held.excerpt <> " + redaction("held.excerpt", bound)
}

// redact rewrites the stores that hold the erased text and that the erase may take no row out
// of: the blocks of the documents that quoted it, whose rows project block nodes of their own,
// and the append-only event log. It runs after the deletes, so a row this run has already
// removed is not rewritten first and counted as both. quoted are the documents whose blocks the
// run read holding the text (erasedRead.quoted).
func (e *Erasure) redact(ctx context.Context, tx pgx.Tx, texts, quoted []string) error {
	if len(texts) == 0 {
		return nil
	}
	if err := e.redactBlocks(ctx, tx, texts, quoted); err != nil {
		return err
	}
	return e.redactEvents(ctx, tx, texts)
}

// redactBlocks rewrites every block that held the erased text: the text itself, and the excerpt
// of the document_blocks row that text was cut for. Those rows are rewritten and not deleted
// because each projects a block node of its own - a paragraph of a document that quoted the
// node, not the node this run addressed - and a live node no row projects is a difference the
// store census reads (c-census).
//
// An excerpt is cut from the rewritten text the way the indexer cuts it (refs.IndexBlocks), so
// a reader of the excerpt and a reader of the text are told the same thing, and an excerpt that
// held only the first part of a long erased text is rewritten too. A block that rewrite does
// not reach - one whose text document_block_texts does not hold - has its excerpt rewritten by
// the statement after it.
//
// Both statements write the blocks of quoted alone, the documents whose owners the run took
// because the read under its locks found their blocks holding the text by these same tests
// (quotedDocuments), so neither writes a block under an owner the run does not hold.
//
// text_sha256 stands as it is. It is the digest of the bytes the version stated, which is what
// the indexer compares a later version's block against: moved to the digest of the redacted
// text, a version restating the erased bytes would read as a change and write them back in.
func (e *Erasure) redactBlocks(ctx context.Context, tx pgx.Tx, texts, quoted []string) error {
	bound := placeholders(texts)
	args := append(redactionArgs(e.marker(), texts), quoted)
	documents := "$" + strconv.Itoa(len(args)) + "::uuid[]"
	rewritten := redaction("held.text", bound)
	var blockTexts, recut int64
	if err := tx.QueryRow(ctx, `
		with redacted as (
			update document_block_texts held set text = `+rewritten+`
			where `+blockTextHolds(bound)+` and held.artifact_id = any(`+documents+`)
			returning held.artifact_id, held.block_id, held.version, held.text
		), recut as (
			update document_blocks block set excerpt = left(redacted.text, `+blockExcerpt+`)
			from redacted
			where redacted.artifact_id = block.artifact_id and redacted.block_id = block.block_id
			  and redacted.version = block.changed_version
			  and block.excerpt <> left(redacted.text, `+blockExcerpt+`)
			returning 1
		)
		select (select count(*) from redacted), (select count(*) from recut)`,
		args...).Scan(&blockTexts, &recut); err != nil {
		return fmt.Errorf("redact the block texts holding the text of %s:%s: %w", e.Kind, e.ID, err)
	}
	tag, err := tx.Exec(ctx, `update document_blocks held set excerpt = `+redaction("held.excerpt", bound)+
		` where `+excerptHolds(bound)+` and held.artifact_id = any(`+documents+`)`, args...)
	if err != nil {
		return fmt.Errorf("redact the block excerpts holding the text of %s:%s: %w", e.Kind, e.ID, err)
	}
	e.redacted("document_block_texts", blockTexts)
	e.redacted("document_blocks", recut+tag.RowsAffected())
	return nil
}

// redactEvents rewrites the payload of every event that held the erased text. The event log is
// append-only - R5 redacts there rather than deleting - so the statement moves the payload and
// nothing else: the row stands with its id, its time and its actor, because what the log says
// happened and who did it is the record, and the bytes inside it are all the erase takes. A
// text a payload holds is json escaped, so each is looked for as that payload spells it, and
// the whole of the payload is searched, a text written into a key going with one written into
// a value.
func (e *Erasure) redactEvents(ctx context.Context, tx pgx.Tx, texts []string) error {
	rewritten := redaction("payload::text", jsonStrings(placeholders(texts)))
	tag, err := tx.Exec(ctx, `update events set payload = (`+rewritten+`)::jsonb
		where payload::text <> `+rewritten, redactionArgs(e.marker(), texts)...)
	if err != nil {
		return fmt.Errorf("redact the event payloads holding the text of %s:%s: %w", e.Kind, e.ID, err)
	}
	e.redacted("events", tag.RowsAffected())
	return nil
}
