package refs

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
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

// topLevelCandidates keeps, of candidates, each block that no block of blocks encloses: a
// top-level block of the version. A block inside a list item, a quote, a footnote definition or a
// typed block starts after its container's marker or line, and its later lines carry the
// container's prefix, which its text read alone loses (pmdoc.FragmentReadsAs), so such a block
// takes the version whose sum moved. blocks are the version's rendered blocks in document order,
// each parent before its children, and candidates are in that order too. The renderer writes a
// blank line between two top-level blocks, so a block that starts at or before the end of the
// last top-level block before it is inside that block: an empty block at the end of its container
// starts where the container ends. It filters candidates in place.
func topLevelCandidates(blocks []model.ArtifactBlock, candidates []respellCandidate) []respellCandidate {
	kept := candidates[:0]
	end, next := -1, 0
	for _, candidate := range candidates {
		for ; next < len(blocks) && blocks[next].From < candidate.block.From; next++ {
			if blocks[next].From > end {
				end = blocks[next].To
			}
		}
		if candidate.block.From > end {
			kept = append(kept, candidate)
		}
	}
	return kept
}

// respelledBlocks keeps, of candidates, which are top-level blocks of the version
// (topLevelCandidates), each block that holds the same tree as the text recorded for it at its
// storedAt (document_block_texts), spelled by another renderer version: that text stood in no
// container there, and reads, alone, as its text now (pmdoc.FragmentReadsAs). A block that stood
// inside a list item, a quote, a footnote definition or a typed block at storedAt was recorded
// after that container's marker or line, with its prefix on each later line, which its text read
// alone loses. So a block is kept only when its recorded text occurs in storedAt's stored markdown
// and every occurrence starts a line (startsLinesOnly): a nested block starts after its
// container's prefix or marker (`  `, `> `, `- `, `[^f]: `). A text that does not occur there, and
// a version that holds no markdown, keep nothing.
//
// The row of each block it keeps takes the new text's sha256 and excerpt and keeps its changed
// version and time, and it returns the ids it kept; version is the version IndexBlocks indexes,
// which a failed write names. One query reads the recorded text of every candidate, one the
// stored markdown of each distinct version the candidates whose text reads alone name, and one
// statement updates the blocks it kept; none runs when it has nothing to do.
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
	readsAlone := candidates[:0]
	var numbers []int
	markdowns := make(map[int]string)
	for _, candidate := range candidates {
		if body, held := recorded[candidate.block.ID]; held && pmdoc.FragmentReadsAs(body, candidate.body) {
			readsAlone = append(readsAlone, candidate)
			if _, named := markdowns[candidate.storedAt]; !named {
				markdowns[candidate.storedAt] = ""
				numbers = append(numbers, candidate.storedAt)
			}
		}
	}
	if len(readsAlone) == 0 {
		return nil, nil
	}
	rows, err = tx.Query(ctx, `
		select number, markdown from artifact_versions
		where artifact_id = $1 and number = any($2::integer[]) and markdown is not null
	`, artifactID, numbers)
	if err != nil {
		return nil, fmt.Errorf("read the stored markdown of the versions of %s its blocks were recorded at: %w", artifactID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var number int
		var markdown string
		if err := rows.Scan(&number, &markdown); err != nil {
			return nil, fmt.Errorf("scan a stored version of %s: %w", artifactID, err)
		}
		markdowns[number] = markdown
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the stored markdown of the versions of %s its blocks were recorded at: %w", artifactID, err)
	}
	rows.Close()
	kept := make(map[string]bool, len(readsAlone))
	var keptIDs, keptSums, keptExcerpts []string
	for _, candidate := range readsAlone {
		if startsLinesOnly(markdowns[candidate.storedAt], recorded[candidate.block.ID]) {
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

// startsLinesOnly reports whether body, a block's recorded text, occurs in markdown, the stored
// markdown of the version it was recorded at, and every occurrence of it starts a line: at
// markdown's start or right after a line feed. An empty body answers false.
func startsLinesOnly(markdown, body string) bool {
	if body == "" {
		return false
	}
	found := false
	for from := 0; ; {
		at := strings.Index(markdown[from:], body)
		if at < 0 {
			return found
		}
		at += from
		if at > 0 && markdown[at-1] != '\n' {
			return false
		}
		found, from = true, at+1
	}
}
