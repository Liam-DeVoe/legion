package api

import (
	"context"
	"fmt"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// attachAskStates sets Ask, in place, on every ask hit of a search page: the ask's state, its
// answer and its resolution, read off its node (askNodeAnswerColumn, askNodeAnswer.applyTo), as
// every read of an answer reads them (R3). A resolved ask's answer is the last one its
// ask.answered events recorded (loadAskAnswersByAsk), since a resolved ask holds no answer in the
// shape every ask read gives it, and one that document settlement retracted while it was answered
// keeps its answer nowhere else; those events are read only when the page holds a resolved ask.
// One query reads every ask hit's row and node, and a page with no ask hit reads nothing. An ask
// removed after the search ran has no row left and keeps Ask nil.
func (s *server) attachAskStates(ctx context.Context, q queryer, results []model.SearchResult, reader *displayTreeReader) error {
	indexByID := map[string]int{}
	var askIDs []string
	for index := range results {
		if results[index].Kind == "ask" {
			indexByID[results[index].ID] = index
			askIDs = append(askIDs, results[index].ID)
		}
	}
	if len(askIDs) == 0 {
		return nil
	}

	rows, err := q.Query(ctx, `select `+docs.AskColumns+`, `+askNodeAnswerColumn+` from asks a where a.id = any($1::uuid[])`, askIDs)
	if err != nil {
		return fmt.Errorf("load search ask states: %w", err)
	}
	defer rows.Close()
	var resolvedIDs []string
	for rows.Next() {
		var nodeAnswer []byte
		ask, err := docs.ScanAsk(rows, &nodeAnswer)
		if err != nil {
			return fmt.Errorf("scan search ask state: %w", err)
		}
		node, err := readAskNodeAnswer(nodeAnswer)
		if err != nil {
			return fmt.Errorf("read search ask %s: %w", ask.ID, err)
		}
		node.applyTo(&ask)
		if ask.State == "resolved" {
			resolvedIDs = append(resolvedIDs, ask.ID)
		}
		results[indexByID[ask.ID]].Ask = &model.SearchAsk{
			State:      ask.State,
			Answer:     newAskAnswerWithTree(ask.Answer, reader),
			Resolution: ask.Resolution,
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load search ask states: %w", err)
	}

	if len(resolvedIDs) > 0 {
		answers, err := s.loadAskAnswersByAsk(ctx, q, resolvedIDs)
		if err != nil {
			return fmt.Errorf("load search ask answers: %w", err)
		}
		for _, id := range resolvedIDs {
			if history := answers[id]; len(history) > 0 {
				results[indexByID[id]].Ask.Answer = newAskAnswerWithTree(&history[len(history)-1], reader)
			}
		}
	}
	return nil
}
