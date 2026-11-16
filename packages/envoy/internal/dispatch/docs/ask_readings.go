package docs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// askReading is one block an ask block closes with (pmdoc.TypedContent): a reading, which reads
// the decision a person's answer made. ask is the ask block's id, reading the block's own, and
// kind its type, which a refusal names it by.
type askReading struct {
	ask     string
	reading string
	kind    string
}

// askReadings is every block tree's ask blocks close with, in document order.
func askReadings(tree *pmdoc.Node) []askReading {
	var found []askReading
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if node.Type == "ask" {
			found = append(found, closingReadings(node)...)
		}
		return true
	})
	return found
}

// closingReadings is the blocks ask, an ask block, closes with, in order.
func closingReadings(ask *pmdoc.Node) []askReading {
	_, admitted := pmdoc.TypedContent(ask)
	if len(admitted) == 0 {
		return nil
	}
	id, _ := ask.Attrs[pmdoc.BlockIDAttr].(string)
	readings := make([]askReading, len(admitted))
	for index, reading := range admitted {
		readingID, _ := reading.Attrs[pmdoc.BlockIDAttr].(string)
		readings[index] = askReading{ask: id, reading: readingID, kind: reading.Type}
	}
	return readings
}

// askAnswer is what the store holds of the ask one ask block opens: its `dispatch://` address,
// which a refusal names ("" for an ask no address names, one on an agent conversation's
// document), and whether a person answered it.
type askAnswer struct {
	address  string
	answered bool
}

// askAnswers reads the ask each of blockIDs, ask blocks of artifactID, opens, as the store holds
// it now, by block id; a block that opens no ask yet has none. It is the one reader of whether an
// ask holds an answer that a reading inside it depends on, and it reads the ask's node: its newest
// answer_state value (graphstore.TupleAnswerState), which asks.Settle appends, and which an ask
// node holding none - an ask a process of the release before wrote, before the boot step states
// it - reads as open. The asks row names the ask and its document's address alone.
func askAnswers(ctx context.Context, tx pgx.Tx, artifactID string, blockIDs []string) (map[string]askAnswer, error) {
	rows, err := tx.Query(ctx, `
		select k.block_id, k.id::text, coalesce(answer.to_id, $3), k.issue_key, a.project_key, coalesce(a.slug, '')
		from asks k join artifacts a on a.id = k.block_artifact_id
		left join lateral (
			select t.to_id, t.asserted from graph_tuples t
			where t.from_kind = $4 and t.from_id = k.id::text and t.kind = $5 and t.to_kind = $6
			order by t.seq desc
			limit 1
		) answer on answer.asserted
		where k.block_artifact_id = $1 and k.block_id = any($2::text[])
	`, artifactID, blockIDs, asks.StateOpen, graphstore.KindAsk, graphstore.TupleAnswerState, graphstore.KindValue)
	if err != nil {
		return nil, fmt.Errorf("read the answers of the asks of document %s: %w", artifactID, err)
	}
	defer rows.Close()
	answers := make(map[string]askAnswer, len(blockIDs))
	for rows.Next() {
		var blockID, id, state, slug string
		var issueKey, project *string
		if err := rows.Scan(&blockID, &id, &state, &issueKey, &project, &slug); err != nil {
			return nil, fmt.Errorf("scan the answer of an ask of document %s: %w", artifactID, err)
		}
		ref := text.Ref{ID: id, Document: slug}
		if issueKey != nil {
			ref.IssueKey = *issueKey
		} else if project != nil {
			ref.Project = *project
		}
		answers[blockID] = askAnswer{address: refs.AskRef(ref), answered: state == "answered"}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the answers of the asks of document %s: %w", artifactID, err)
	}
	return answers, nil
}

// readingWithoutAnswer is why reading may not stand in its ask, which holds no answer yet, or
// nil where the ask holds one. A reading reads a decision, and a person's ask is one only once
// they answer it, so the refusal names the reading and the ask and says what is missing. An ask
// block that opens no ask yet - one this very write adds - is named by its block id.
func readingWithoutAnswer(reading askReading, answers map[string]askAnswer) error {
	answer, opened := answers[reading.ask]
	if answer.answered {
		return nil
	}
	ask := fmt.Sprintf("the ask of ask block %q, which no ask row holds yet", reading.ask)
	if opened && answer.address != "" {
		ask = "the ask " + answer.address
	}
	return fmt.Errorf("%s %q stands in %s, and that ask has no answer yet: a %s reads a decision, and an ask is one only once a person answers it, so write the %s after the answer", reading.kind, reading.reading, ask, reading.kind, reading.kind)
}

// refuseReadingsWithoutAnswers refuses tree, the tree an agent's or API write to artifactID
// leaves, when a reading it introduces stands in an ask that holds no answer
// (readingWithoutAnswer), naming the first such reading in document order (ErrInvalidAskBlock).
// A reading is introduced when prior, the tree before the write, holds no reading under its id in
// the same ask block; a nil prior (a new document, or one outside the schema) holds none. So an
// edit elsewhere in the document never meets a reading another writer left. A browser's write
// never comes here: its editor refuses such a reading, and settlement flags one it meets anyway
// (flagReadingsWithoutAnswers).
func refuseReadingsWithoutAnswers(ctx context.Context, tx pgx.Tx, artifactID string, tree, prior *pmdoc.Node) error {
	readings := askReadings(tree)
	if len(readings) == 0 {
		return nil
	}
	held := map[askReading]struct{}{}
	for _, reading := range askReadings(prior) {
		held[reading] = struct{}{}
	}
	var introduced []askReading
	var asks []string
	for _, reading := range readings {
		if _, kept := held[reading]; kept {
			continue
		}
		introduced = append(introduced, reading)
		asks = append(asks, reading.ask)
	}
	if len(introduced) == 0 {
		return nil
	}
	answers, err := askAnswers(ctx, tx, artifactID, asks)
	if err != nil {
		return err
	}
	for _, reading := range introduced {
		if reason := readingWithoutAnswer(reading, answers); reason != nil {
			return &ErrInvalidAskBlock{Reason: reason}
		}
	}
	return nil
}

// flagReadingsWithoutAnswers is blocks, the readable ask blocks settlement collected from a
// document of artifactID, less each one that closes with a reading while its ask holds no answer
// (readingWithoutAnswer), which joins invalid with that reason: settlement flags it `invalid` as
// it flags any ask block the strict read refuses, and leaves its row as it was. A browser's write
// is never refused, so this is where one that left such a reading meets the rule a markdown
// write is held to (refuseReadingsWithoutAnswers).
func flagReadingsWithoutAnswers(ctx context.Context, tx pgx.Tx, artifactID string, blocks []askBlock, invalid []invalidAskBlock) ([]askBlock, []invalidAskBlock, error) {
	closing := map[string][]askReading{}
	var asks []string
	for _, block := range blocks {
		readings := closingReadings(block.node)
		if len(readings) == 0 {
			continue
		}
		closing[block.id] = readings
		asks = append(asks, block.id)
	}
	if len(asks) == 0 {
		return blocks, invalid, nil
	}
	answers, err := askAnswers(ctx, tx, artifactID, asks)
	if err != nil {
		return nil, nil, err
	}
	readable := make([]askBlock, 0, len(blocks))
	for _, block := range blocks {
		var reason error
		for _, reading := range closing[block.id] {
			if reason = readingWithoutAnswer(reading, answers); reason != nil {
				break
			}
		}
		if reason != nil {
			invalid = append(invalid, invalidAskBlock{node: block.node, id: block.id, reason: reason})
			continue
		}
		readable = append(readable, block)
	}
	return readable, invalid, nil
}
