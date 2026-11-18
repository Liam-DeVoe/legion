package refs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// respellCandidate is a block the document's index holds whose text's sum a version moved, and
// whose stored changed version, storedAt, another renderer version stored or none stamped
// (pmdoc.OtherRenderer): the block, with its text at this version, that text's sha256 and its
// excerpt.
type respellCandidate struct {
	movedBlock
	storedAt int
}

// respelledBlocks keeps, of candidates, each block that holds the same tree as the text recorded
// for it at its storedAt (document_block_texts), spelled by another renderer version: that text
// reads, alone, as its text now (pmdoc.FragmentReadsAs). The row of each block it keeps takes the
// new text's sha256 and excerpt and keeps its changed version and time, and it returns the ids it
// kept; version is the version IndexBlocks indexes, which a failed write names. One query reads
// the recorded text of every candidate and one statement updates the blocks it kept; neither runs
// when it has nothing to do.
func respelledBlocks(ctx context.Context, tx pgx.Tx, artifactID string, version int, candidates []respellCandidate) (map[string]bool, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	ids := make([]string, len(candidates))
	versions := make([]int, len(candidates))
	for index, candidate := range candidates {
		ids[index], versions[index] = candidate.block.ID, candidate.storedAt
	}
	rows, err := tx.Query(ctx, `
		select t.block_id, t.text
		from unnest($2::text[], $3::integer[]) as c (id, version)
		join document_block_texts t on t.artifact_id = $1 and t.block_id = c.id and t.version = c.version
		where t.text is not null
	`, artifactID, ids, versions)
	if err != nil {
		return nil, fmt.Errorf("read the recorded texts of the blocks of %s: %w", artifactID, err)
	}
	defer rows.Close()
	recorded := make(map[string]string, len(ids))
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, fmt.Errorf("scan a recorded block text of %s: %w", artifactID, err)
		}
		recorded[id] = body
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the recorded texts of the blocks of %s: %w", artifactID, err)
	}
	rows.Close()
	kept := make(map[string]bool, len(recorded))
	var keptIDs, keptSums, keptExcerpts []string
	for _, candidate := range candidates {
		if body, held := recorded[candidate.block.ID]; held && pmdoc.FragmentReadsAs(body, candidate.body) {
			kept[candidate.block.ID] = true
			keptIDs, keptSums = append(keptIDs, candidate.block.ID), append(keptSums, candidate.sum)
			keptExcerpts = append(keptExcerpts, candidate.excerpt)
		}
	}
	if len(keptIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			update document_blocks d set text_sha256 = r.sum, excerpt = r.excerpt
			from unnest($2::text[], $3::text[], $4::text[]) as r (id, sum, excerpt)
			where d.artifact_id = $1 and d.block_id = r.id
		`, artifactID, keptIDs, keptSums, keptExcerpts); err != nil {
			return nil, fmt.Errorf("record the blocks version %d of %s holds respelled: %w", version, artifactID, err)
		}
	}
	return kept, nil
}
