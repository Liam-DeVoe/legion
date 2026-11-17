package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// results_settled_check.go reads every fact of a check that the results routes judge or serve
// from one state of its Verification document: the block as the block index holds it. That is
// its changed version and its digest (document_blocks) and the text whose digest that is, which
// the index recorded at that version (document_block_texts); the check's attributes (`ci_check`,
// `when`, run, negative control, surface) and its words are read out of that text. The digest is
// the sha256 of the block's whole markdown, its opening line and that line's attributes included
// (refs.BlockTextSum), so a result stored against it names the job, the `when` and the words it
// was judged under. No fact of a check comes from the live document: an edit a browser made that
// has not settled changes nothing for a write or a read until its settlement indexes it, and a
// check the index does not hold yet is no check of either route.

// settledCheckText joins each document_blocks row d to the text the block index recorded of it at
// its changed version, the text its text_sha256 is the digest of.
const settledCheckText = `left join document_block_texts t
		on t.artifact_id = d.artifact_id and t.block_id = d.block_id and t.version = d.changed_version`

// settledCheckNode is check block blockID of document artifactID as the block index holds it:
// parsed out of stored, the text the index recorded at the block's changed version, which must be
// the text whose digest is sum. Dispatch writes both in one transaction (refs.IndexBlocks), so a
// text that is missing or answers another digest is a fault of Dispatch's own records, never a
// state to read a check from.
func settledCheckNode(artifactID, blockID string, stored *string, sum string) (*pmdoc.Node, error) {
	if stored == nil || refs.BlockTextSum(*stored) != sum {
		return nil, fmt.Errorf("read check %s of document %s: the block index holds no text of it whose digest is %s", blockID, artifactID, sum)
	}
	tree, err := pmdoc.ParseRendering(*stored)
	if err != nil {
		return nil, fmt.Errorf("read check %s of document %s as the block index holds it: %w", blockID, artifactID, err)
	}
	var found *pmdoc.Node
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if id, _ := node.Attrs[pmdoc.BlockIDAttr].(string); found == nil && node.Type == "check" && id == blockID {
			found = node
		}
		return found == nil
	})
	if found == nil {
		return nil, fmt.Errorf("read check %s of document %s: the text the block index holds of it is no check block %s", blockID, artifactID, blockID)
	}
	return found, nil
}

// readSettled fills in check's attributes and surface from stored, the block index's text of it at
// check.Version, whose digest is check.SHA256 (settledCheckNode).
func (check *resolvedCheck) readSettled(stored *string) error {
	node, err := settledCheckNode(check.ArtifactID, check.BlockID, stored, check.SHA256)
	if err != nil {
		return err
	}
	block := docs.CheckBlockOf(node)
	check.Attributes, check.Surface = docs.CheckAttributesOfBlock(block), block.Surface
	return nil
}

// resolveCheck finds the check a result names among the Verification documents of the recording
// issue and of every issue above it, as the block index holds each. The value is a block id, or
// the canonical block reference (`dispatch://KEY/artifact/verification-md#b-<id>`) that names one
// document when a block id is not unique in that set: an id two documents share is 409
// CHECK_AMBIGUOUS, and an id no Verification document of the set holds — or one that is not a
// `check` block, or a check the index does not hold yet, which a browser wrote and its settlement
// has not indexed — is 400 UNKNOWN_CHECK. The check's version, digest, attributes and surface are
// one state of it (results_settled_check.go).
func (s *server) resolveCheck(ctx context.Context, q queryer, issueKey, value string) (resolvedCheck, error) {
	ancestors, err := refs.IssueAncestors(ctx, q, issueKey)
	if err != nil {
		return resolvedCheck{}, err
	}
	scope := append([]string{issueKey}, ancestors...)

	blockID, artifactFilter := value, ""
	if ref, ok := text.CanonicalBlockRef(value); ok {
		// A block of a document carries its document's fields and no Item; a block of a comment
		// or a message names that item in Item and is no check.
		if ref.Item != "" || ref.IssueKey == "" || ref.ID == "" || ref.Block == "" {
			return resolvedCheck{}, unknownCheck(value)
		}
		blockID = ref.Block
		document, held, err := s.verificationDocument(ctx, q, ref.IssueKey, ref.ID)
		if err != nil {
			return resolvedCheck{}, err
		}
		if !held {
			return resolvedCheck{}, unknownCheck(value)
		}
		artifactFilter = document.ID
	}

	rows, err := q.Query(ctx, `
		select a.issue_key, a.slug, d.artifact_id::text, d.changed_version, d.text_sha256, t.text
		from document_blocks d
		join artifacts a on a.id = d.artifact_id
		`+settledCheckText+`
		where `+aliasedVerificationRole+`
		  and a.issue_key = any($1) and d.type = 'check' and d.block_id = $2
		  and ($3 = '' or d.artifact_id = $3::uuid)
		order by a.issue_key
	`, scope, blockID, artifactFilter)
	if err != nil {
		return resolvedCheck{}, fmt.Errorf("resolve check %q of %s: %w", value, issueKey, err)
	}
	defer rows.Close()
	var found []resolvedCheck
	var texts []*string
	var references []string
	for rows.Next() {
		check := resolvedCheck{BlockID: blockID}
		var slug string
		var settled *string
		if err := rows.Scan(&check.IssueKey, &slug, &check.ArtifactID, &check.Version, &check.SHA256, &settled); err != nil {
			return resolvedCheck{}, fmt.Errorf("resolve check %q of %s: %w", value, issueKey, err)
		}
		if reference, ok := text.FormatBlockRef(text.Ref{
			Kind: "block", IssueKey: check.IssueKey, ID: slug, Block: blockID,
		}); ok {
			check.Ref = reference
			references = append(references, reference)
		}
		found, texts = append(found, check), append(texts, settled)
	}
	if err := rows.Err(); err != nil {
		return resolvedCheck{}, fmt.Errorf("resolve check %q of %s: %w", value, issueKey, err)
	}
	if len(found) == 0 {
		return resolvedCheck{}, unknownCheck(value)
	}
	if len(found) > 1 {
		issues := make([]string, len(found))
		for index, check := range found {
			issues[index] = check.IssueKey
		}
		return resolvedCheck{}, errorf(http.StatusConflict, "CHECK_AMBIGUOUS",
			"check %s is a check of the Verification document of %s: name one with its reference, %s",
			blockID, strings.Join(issues, " and "), strings.Join(references, " or "))
	}
	check := found[0]
	if err := check.readSettled(texts[0]); err != nil {
		return resolvedCheck{}, err
	}
	return check, nil
}

// refreshResolvedCheck reads again, under the locks the transaction holds, the block index's
// version and digest of a check resolved before it opened (recordCheckResult), so the row
// records the block as it stands at the commit, and its attributes with them when that block
// moved: the refusal a closed issue earns turns on `when`, the refusal of a result by another
// recorder on ci_check, run, `when` and negative control, and a CI job's row takes the check's
// surface and negative control, all read before the lock, and a settlement landing in between can
// have changed them. A digest the resolve already read is the same text, so the same attributes;
// a digest that moved is read out of its own text, the one state of the check the row is stored
// against (settledCheckNode). A block the index no longer holds is no check any more.
func refreshResolvedCheck(ctx context.Context, q queryer, check *resolvedCheck, named string) error {
	resolved := check.SHA256
	var settled *string
	err := q.QueryRow(ctx, `
		select d.changed_version, d.text_sha256, t.text
		from document_blocks d
		`+settledCheckText+`
		where d.artifact_id = $1::uuid and d.block_id = $2 and d.type = 'check'
	`, check.ArtifactID, check.BlockID).Scan(&check.Version, &check.SHA256, &settled)
	if errors.Is(err, pgx.ErrNoRows) {
		return unknownCheck(named)
	}
	if err != nil {
		return fmt.Errorf("read the check %s of document %s: %w", check.BlockID, check.ArtifactID, err)
	}
	if check.SHA256 == resolved {
		return nil
	}
	return check.readSettled(settled)
}

// checkBlocksOf is every `check` block of one Verification document as the block index holds it:
// each with its attributes and its words read out of the text the index recorded at its changed
// version, that version and its digest, its issue and its reference (results_settled_check.go).
// They come in the order of the document's newest stored version, and a block the index holds that
// that version does not, which its settlement has not indexed away yet, after them by id. A check
// a browser wrote and its settlement has not indexed is no check of the read until it is, as it is
// none of the record route (resolveCheck). The reference is built from the document's own slug,
// which is a property of the artifacts row and not of a block row.
func (s *server) checkBlocksOf(ctx context.Context, q queryer, artifact model.Artifact) ([]model.CheckBlock, error) {
	issueKey := ""
	if artifact.IssueKey != nil {
		issueKey = *artifact.IssueKey
	}
	rows, err := q.Query(ctx, `
		select d.block_id, d.changed_version, d.text_sha256, t.text
		from document_blocks d
		`+settledCheckText+`
		where d.artifact_id = $1::uuid and d.type = 'check'
	`, artifact.ID)
	if err != nil {
		return nil, fmt.Errorf("read the indexed checks of %s: %w", artifact.ID, err)
	}
	defer rows.Close()
	settled := map[string]model.CheckBlock{}
	for rows.Next() {
		var blockID, sum string
		var version int
		var settledText *string
		if err := rows.Scan(&blockID, &version, &sum, &settledText); err != nil {
			return nil, fmt.Errorf("read the indexed checks of %s: %w", artifact.ID, err)
		}
		node, err := settledCheckNode(artifact.ID, blockID, settledText, sum)
		if err != nil {
			return nil, err
		}
		check := docs.CheckBlockOf(node)
		check.ArtifactID, check.Text, check.Results = artifact.ID, blockWords(node), []model.CheckResult{}
		check.IssueKey, check.Version, check.SHA256 = issueKey, version, sum
		check.Ref, _ = text.FormatBlockRef(text.Ref{Kind: "block", IssueKey: issueKey, ID: artifact.Slug, Block: blockID})
		settled[blockID] = check
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the indexed checks of %s: %w", artifact.ID, err)
	}
	order, err := storedCheckOrder(ctx, q, artifact.ID)
	if err != nil {
		return nil, err
	}
	checks := make([]model.CheckBlock, 0, len(settled))
	for _, blockID := range order {
		if check, held := settled[blockID]; held {
			checks = append(checks, check)
			delete(settled, blockID)
		}
	}
	rest := make([]string, 0, len(settled))
	for blockID := range settled {
		rest = append(rest, blockID)
	}
	slices.Sort(rest)
	for _, blockID := range rest {
		checks = append(checks, settled[blockID])
	}
	return checks, nil
}

// storedCheckOrder is the id of each `check` block of document artifactID's newest stored
// version, in document order, the first holder of a repeated id alone, as the block index keeps
// it. A document with no stored markdown version names none.
func storedCheckOrder(ctx context.Context, q queryer, artifactID string) ([]string, error) {
	var markdown string
	err := q.QueryRow(ctx, `
		select markdown from artifact_versions
		where artifact_id = $1::uuid and markdown is not null
		order by number desc limit 1
	`, artifactID).Scan(&markdown)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the newest version of document %s: %w", artifactID, err)
	}
	tree, err := pmdoc.ParseRendering(markdown)
	if err != nil {
		return nil, fmt.Errorf("read the check blocks of the newest version of document %s: %w", artifactID, err)
	}
	var order []string
	seen := map[string]struct{}{}
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if node.Type != "check" {
			return true
		}
		if id, _ := node.Attrs[pmdoc.BlockIDAttr].(string); id != "" {
			if _, repeated := seen[id]; !repeated {
				seen[id] = struct{}{}
				order = append(order, id)
			}
		}
		return true
	})
	return order, nil
}
