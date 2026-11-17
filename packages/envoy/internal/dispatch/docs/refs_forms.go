package docs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// RefsFormsLease names the lease one pass of the forms drain runs under
// (RunRefsFormsReconciliation). A lease of its own rather than BlockIndexLease: a period one
// process has served is skipped by the others (duty.Exclusive), and during a rolling update the
// process that wins the block index's period may be one of the release before this one, which
// knows nothing of refs_forms_owed and would drain none of it.
const RefsFormsLease = "refs-forms"

// RefsFormsTick is what one pass of the forms drain did (ReconcileRefsForms).
type RefsFormsTick struct {
	// Texts is how many owed comments, asks and issue messages it re-indexed, and Documents how
	// many owed documents it indexed itself, each row settled in the transaction that indexed it.
	Texts     int
	Documents int
	// Gone is how many rows it settled for a document that is gone or holds no markdown version,
	// which owes no text.
	Gone int
	// LiveAhead is how many owed documents it left owed because their live text is ahead of
	// their latest version and differs from it (indexOwedDocument), and Unindexed how many it
	// left owed because its own index did not run: another process indexed the document first,
	// or a write moved the document's latest version or the row's enqueued_at meanwhile.
	LiveAhead int
	Unindexed int
	// OwedTexts is what refs_forms_owed holds after the pass.
	OwedTexts int
	// Full reports a listed batch that was full, or documents the pass left for the next one, so
	// more may be waiting.
	Full bool
}

// RunRefsFormsReconciliation drains refs_forms_owed (refs.RefsFormsOwed, migrations
// 20261117025900-20261117025906): every comment, ask, issue message and document an older build
// indexed without the reference forms this build reads - an issue's tab addresses and a
// document's dashboard URLs - which that build's index deleted the edges of. A row stays owed
// until a build that reads those forms has indexed that text itself.
//
// A duty of its own, under its own lease (RefsFormsLease), rather than a batch of the block-index
// pass (RunBlockIndexReconciliation), which drains refs_grammar_owed: during a rolling update a
// period the older process served there would drain nothing of this queue. One pass at start,
// then every blockIndexStep while a batch is full and every BlockIndexReconcileInterval once none
// is, until ctx ends, as the block-index pass is paced (duty.RunLeasedAfterEachPass).
func (s *Service) RunRefsFormsReconciliation(ctx context.Context, leases duty.Leases) {
	ctx = store.WithTransactionTracking(ctx)
	// logged is the last pass this loop logged: a pass that drained nothing and leaves the same
	// counts (a document whose live text stays ahead, left owed every pass) logs nothing.
	var logged RefsFormsTick
	duty.RunLeasedAfterEachPass(ctx, leases, RefsFormsLease, BlockIndexReconcileInterval, func(ctx context.Context, _ time.Duration) time.Duration {
		return s.refsFormsTick(ctx, &logged)
	})
}

// refsFormsTick runs one pass, logs what it did unless it drained nothing and left the counts
// *logged holds, and returns how long to wait before the next pass.
func (s *Service) refsFormsTick(ctx context.Context, logged *RefsFormsTick) time.Duration {
	tick, err := s.ReconcileRefsForms(ctx)
	if ctx.Err() != nil {
		return BlockIndexReconcileInterval
	}
	if err != nil {
		slog.Error("dispatch: refs forms reconciliation", "error", err)
	}
	if tick.Texts > 0 || tick.Documents > 0 || tick.Gone > 0 || tick.LiveAhead != logged.LiveAhead ||
		tick.Unindexed != logged.Unindexed || tick.OwedTexts != logged.OwedTexts {
		slog.Info("dispatch: refs forms index", "texts", tick.Texts, "documents", tick.Documents,
			"gone", tick.Gone, "live_ahead", tick.LiveAhead, "unindexed", tick.Unindexed,
			"owed_texts", tick.OwedTexts)
		*logged = tick
	}
	if tick.Full {
		return blockIndexStep
	}
	return BlockIndexReconcileInterval
}

// ReconcileRefsForms runs one pass: it lists the oldest blockIndexItemBatch rows of
// refs_forms_owed (refs.ListOwed) and drains each in a transaction of its own. A comment, an ask
// or an issue message goes through refs.ReindexOwed, which re-indexes its text with this build's
// grammar and settles its row in the same transaction. A document is indexed here, at its latest
// version, by the block index's own writer (drainFormsDocument), at most blockIndexDocumentBatch
// of them a pass, the bound the block index reads documents under; the rest wait for the next
// pass, which comes after blockIndexStep. A row it cannot drain for a reason of its own is logged
// and left owed, so one never holds back the rest of its batch.
func (s *Service) ReconcileRefsForms(ctx context.Context) (RefsFormsTick, error) {
	var tick RefsFormsTick
	owed, err := refs.ListOwed(ctx, s.store.Pool, refs.RefsFormsOwed, blockIndexItemBatch)
	if err != nil {
		return tick, err
	}
	tick.Full = len(owed) == blockIndexItemBatch
	documents := 0
	for _, source := range owed {
		switch source.Kind {
		case "comment", "ask", "message":
			indexed, err := refs.ReindexOwed(ctx, s.store.Pool, source, s.serverURL)
			switch {
			case ctx.Err() != nil:
				return tick, ctx.Err()
			case err != nil:
				slog.Error("dispatch: refs forms index skipped text it could not index",
					"kind", source.Kind, "id", source.ID, "error", err)
			case indexed:
				tick.Texts++
			}
		case "artifact":
			if documents == blockIndexDocumentBatch {
				tick.Full = true
				continue
			}
			documents++
			drained, err := s.drainFormsDocument(ctx, source)
			switch {
			case ctx.Err() != nil:
				return tick, ctx.Err()
			case errors.Is(err, ErrDocNewerContent):
				// Another build wrote an element this build does not know, during a release; a
				// build that reads it indexes the document, which stays owed until then.
				slog.Warn("dispatch: refs forms index skipped a document holding an element this build cannot read",
					"artifact", source.ID, "error", err)
			case err != nil:
				slog.Error("dispatch: refs forms index skipped a document it could not index",
					"artifact", source.ID, "error", err)
			default:
				drained.count(&tick)
			}
		default:
			slog.Error("dispatch: refs forms index skipped a row of a kind it does not drain",
				"kind", source.Kind, "id", source.ID)
		}
	}
	if err := s.store.Pool.QueryRow(ctx, `select count(*) from refs_forms_owed`).Scan(&tick.OwedTexts); err != nil {
		return tick, fmt.Errorf("count what is owed an index with the reference forms: %w", err)
	}
	return tick, nil
}

// formsDocumentDrain is what drainFormsDocument did with one document's row.
type formsDocumentDrain int

const (
	// formsDocumentIndexed: this drain indexed the document and settled its row.
	formsDocumentIndexed formsDocumentDrain = iota
	// formsDocumentGone: the document is gone or holds no markdown version, and its row was
	// settled.
	formsDocumentGone
	// formsDocumentLiveAhead: the document's live text is ahead of its latest version, and its
	// row is left owed.
	formsDocumentLiveAhead
	// formsDocumentUnindexed: this drain's index did not run, and the row is left owed.
	formsDocumentUnindexed
)

// count adds what one document's drain did to tick.
func (drained formsDocumentDrain) count(tick *RefsFormsTick) {
	switch drained {
	case formsDocumentIndexed:
		tick.Documents++
	case formsDocumentGone:
		tick.Gone++
	case formsDocumentLiveAhead:
		tick.LiveAhead++
	case formsDocumentUnindexed:
		tick.Unindexed++
	}
}

// drainFormsDocument drains one `artifact` row of refs_forms_owed by indexing the document itself,
// at its latest version, through the index the block index writes a document with
// (indexOwedDocument, indexRenderedVersion): its own mentions, the mentions its blocks hold and
// the excerpt each shows, read with this build's grammar.
//
// The row is settled by this drain only through its own index of the document, in that index's
// transaction (indexOwedDocument's settle), and only while it holds the enqueued_at it was listed
// with. It is not settled by owing the document a block index, as refs_grammar_owed's drain settles
// its own (drainOwedDocument): any build's block-index pass may take a document owed one, and a
// build that predates these forms would index it without them and leave nothing owed. So the
// document is owed a block index here (oweDocumentIndex), which indexOwedDocument requires of what
// it indexes, and indexed in the same pass. Whoever indexes it between the two leaves
// indexOwedDocument finding it no longer owed: an older build's block index leaves the row owed
// until this build's own index of the document commits, in a later pass, and a process of this
// build settles the row with its own index (refs.replaceRefs). A document whose live text is ahead
// of its latest version is left owed as the block index leaves it, for the settlement that writes
// its next version; a version an older build writes then owes the row again (migration
// 20261117025904), and one this build writes indexes the document and settles it.
//
// A document that is gone, or holds no markdown version, owes no text and is owed no block index:
// its row is settled while it holds the enqueued_at it was listed with. An older build's removal
// of a document deletes its edges, which owes this queue the document (migration
// 20261117025906), and settles only refs_grammar_owed.
func (s *Service) drainFormsDocument(ctx context.Context, source refs.Owed) (formsDocumentDrain, error) {
	document, err := s.latestDocumentVersion(ctx, source.ID)
	if err != nil {
		return formsDocumentUnindexed, err
	}
	if document.version == 0 {
		settled, err := refs.SettleOwed(ctx, s.store.Pool, source)
		if err != nil || !settled {
			return formsDocumentUnindexed, err
		}
		return formsDocumentGone, nil
	}
	if err := oweDocumentIndex(ctx, s.store.Pool, source.ID); err != nil {
		return formsDocumentUnindexed, err
	}
	indexed, ahead, err := s.indexOwedDocument(ctx, document, &source)
	switch {
	case err != nil:
		return formsDocumentUnindexed, err
	case ahead:
		return formsDocumentLiveAhead, nil
	case indexed:
		return formsDocumentIndexed, nil
	}
	return formsDocumentUnindexed, nil
}

// latestDocumentVersion is document artifactID as indexOwedDocument takes it: its latest markdown
// version, 0 for a document that holds none or is gone, which has no text to index.
// indexOwedDocument reads that version again under the document's owner row, so a version written
// after this read leaves the document unindexed and its row owed.
func (s *Service) latestDocumentVersion(ctx context.Context, artifactID string) (owedDocument, error) {
	document := owedDocument{id: artifactID}
	if err := s.store.Pool.QueryRow(ctx, `
		select coalesce(max(number), 0) from artifact_versions
		where artifact_id = $1::uuid and markdown is not null
	`, artifactID).Scan(&document.version); err != nil {
		return owedDocument{}, fmt.Errorf("read the latest version of %s: %w", artifactID, err)
	}
	return document, nil
}
