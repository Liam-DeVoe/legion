package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/asof"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// The results of an issue's checks at a cut (task t9 of INFRA-4620's plan, decision 11). The read
// runs its present statements under the cut, which the store binds to the shadows: the
// Verification document, the results and the retractions are the rows at the cut. The checks
// come from the document's newest version at the cut, which the read parses as the present read
// parses the live text (checkBlocksAtTheCut). Where a result's commit stands, what a check
// compared its results with and what CI's run said are GitHub reads now. So the read at a cut
// makes no read of GitHub and reads no pull request head and no branch commit. It also writes
// none of what the present read writes (pull_request_heads, branch_commits and the event
// issue.pull_request_attribution_changed). It leaves those fields out (checksAtTheCut).

// refuseResultsCommitAtCut answers 400 AS_OF_NOT_SERVED, naming the parameter, for a read of the
// results at a cut that names a commit (?at=, decision 5): the read compares a result with the
// commit through GitHub now, and a read at a cut makes no GitHub read. It reports whether it
// answered.
func refuseResultsCommitAtCut(w http.ResponseWriter, r *http.Request) bool {
	if _, atCut := asof.CutFrom(r.Context()); !atCut || !r.URL.Query().Has("at") {
		return false
	}
	writeError(w, contracts.DispatchAsOfNotServed, http.StatusBadRequest,
		"at: a read of the results at a past time compares no result with a commit, "+
			"because where a commit stands is a GitHub read now; send the request with no at")
	return true
}

// checkIndexAtTheCutStatement reads what the block index recorded of each block of document $1's
// newest version at the cut, N, which the artifact_versions shadow gives: the latest row of
// document_block_texts at or before N. Its version is the version in which the text of the block
// last changed at N, which document_blocks.changed_version held then. Its text is the text whose
// digest document_blocks.text_sha256 held then (refs.BlockTextSum). A row with no text is a block
// deleted then, and is not read. As blockTextsAtTheCutStatement does, the statement reads the rows
// only where document_indexed_versions holds N: no record holds the blocks of a version that no
// block-aware build indexed (migration 0121).
const checkIndexAtTheCutStatement = `
		select t.block_id, t.version, t.text
		from (select max(v.number) as number from artifact_versions v where v.artifact_id = $1) n
		cross join lateral (
			select distinct on (b.block_id) b.block_id, b.version, b.text
			from document_block_texts b
			where b.artifact_id = $1 and b.version <= n.number
			order by b.block_id, b.version desc
		) t
		where t.text is not null
		  and exists (select 1 from document_indexed_versions i where i.artifact_id = $1 and i.version = n.number)`

// checkBlocksAtTheCut is checkBlocksOf at the cut ctx carries: every check block of the newest
// version at the cut of artifact, the Verification document, parsed as the present read parses
// the live text (checkBlocksOfRendering), with its issue and its reference. The version in which
// the words of a check last changed, and their digest, come from what the block index recorded of
// that version (checkIndexAtTheCutStatement). The text of a check's tree is the text that the
// index recorded, as the present read takes it from the live text. Where the index recorded
// nothing of a check, the answer leaves out the version and the digest (reason E), and
// checksAtTheCut leaves out whether the words of each of its results moved. One batch sends the
// statements, and nothing is read from the live document.
func (s *server) checkBlocksAtTheCut(ctx context.Context, artifact model.Artifact, trees *displayTreeReader) ([]model.CheckBlock, error) {
	var markdown string
	indexed := map[string]model.CheckBlock{}
	texts := map[string]string{}
	batch := &pgx.Batch{}
	batch.Queue(versionAtTheCutStatement, artifact.ID).QueryRow(func(row pgx.Row) error {
		var token *string
		if err := row.Scan(&markdown, &token); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	})
	batch.Queue(checkIndexAtTheCutStatement, artifact.ID).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			var block model.CheckBlock
			var text string
			if err := rows.Scan(&block.BlockID, &block.Version, &text); err != nil {
				return err
			}
			block.SHA256 = refs.BlockTextSum(text)
			indexed[block.BlockID], texts[block.BlockID] = block, text
		}
		return rows.Err()
	})
	if err := s.deps.Store.Pool.SendBatch(ctx, batch).Close(); err != nil {
		return nil, fmt.Errorf("read the checks of %s at the cut: %w", artifact.ID, err)
	}
	checks, err := checkBlocksOfRendering(artifact.ID, markdown, texts, trees)
	if err != nil {
		return nil, err
	}
	if len(checks) == 0 {
		return []model.CheckBlock{}, nil
	}
	addressChecks(checks, artifact, indexed)
	for index := range checks {
		if _, held := indexed[checks[index].BlockID]; !held {
			place := "/checks/" + strconv.Itoa(index)
			leaveOut(ctx, place+"/version", place+"/sha256")
		}
	}
	return checks, nil
}

// checksAtTheCut completes checks, the checks of a read of the results at the cut ctx carries,
// each at /checks/<index> of the answer with the results that the read loaded. A read at a cut
// makes no GitHub read, so the answer leaves out what the present read takes from GitHub (reason
// H): of each check, what it compared its results with, why it compared them with nothing, and
// whether a pass of it stands; of a check that CI proves, what CI's run said (ci), and the result
// that the present read derives from that run, which the read at a cut does not make; and of each
// result, where its commit stands and the currency that older clients print. Whether the words of
// a check moved after a result is given where the check's digest at the cut is
// (checkBlocksAtTheCut), and is left out with the digest elsewhere (reason E).
func checksAtTheCut(ctx context.Context, checks []model.CheckBlock) {
	for index := range checks {
		check := &checks[index]
		place := "/checks/" + strconv.Itoa(index)
		leaveOut(ctx, place+"/compared_with", place+"/compared_nothing", place+"/pass_stands")
		if validCICheck(*check, len(check.Results) > 0) {
			leaveOut(ctx, place+"/ci")
		}
		for result := range check.Results {
			at := place + "/results/" + strconv.Itoa(result)
			leaveOut(ctx, at+"/commit_stands", at+"/currency")
			if check.SHA256 == "" {
				leaveOut(ctx, at+"/check_words_moved")
				continue
			}
			moved := check.SHA256 != check.Results[result].CheckSHA256
			check.Results[result].CheckWordsMoved = &moved
		}
	}
}
