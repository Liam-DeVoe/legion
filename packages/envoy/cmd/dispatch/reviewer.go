package main

import (
	"fmt"
	"log/slog"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/modelpace"
	"github.com/sjawhar/envoy/internal/dispatch/reviewer"
)

// newReviewer builds the reviewer of INFRA-3231's R5 (api.Reviewer), or nil where its model group
// (DISPATCH_REVIEWER_MODEL_*) is unset, which it logs: resolveWritingSettings has then refused
// every DISPATCH_REVIEWER_<KIND> but off, so nothing is reviewed and the draft route answers that
// no model is set. A set group builds the reviewer whatever the kinds say, since the draft route
// answers whenever the group is set. Its calls are charged to the group's share of its deployment
// on pacer, the process's one pacer, and point at the stand-in under DISPATCH_TEST_MODEL_URL. It
// is built from settings resolveWritingSettings has already checked, so an error here is a shape
// that check missed; it calls nothing and reads no file.
func newReviewer(cfg writingSettings, pacer *modelpace.Pacer) (*api.Reviewer, error) {
	group := cfg.ReviewerModel
	if group.Kind == "" {
		slog.Info("dispatch: the reviewer reads no text: its model group is unset", "setting", modelGroups[0].prefix+"KIND")
		return nil, nil
	}
	model, err := group.agentloopModel()
	if err != nil {
		return nil, fmt.Errorf("the reviewer's model: %w", err)
	}
	pace, err := group.pace(pacer)
	if err != nil {
		return nil, fmt.Errorf("the reviewer's model: %w", err)
	}
	rules := make([]reviewer.RuleSet, len(cfg.ReviewerRules))
	for i, rule := range cfg.ReviewerRules {
		rules[i] = reviewer.RuleSet(rule)
	}
	slog.Info("dispatch: the reviewer reads agents' texts to people", "kind", group.Kind, "deployment", group.Deployment,
		"stand_in", group.StandIn, "asks", cfg.ReviewAsks, "issues", cfg.ReviewIssues, "comments", cfg.ReviewComments,
		"messages", cfg.ReviewMessages, "documents", cfg.ReviewDocuments, "decision_blocks", cfg.ReviewDecisionBlocks,
		"rules", cfg.ReviewerRules)
	return api.NewReviewer(api.ReviewerConfig{
		Asks: cfg.ReviewAsks, Issues: cfg.ReviewIssues, Comments: cfg.ReviewComments, Messages: cfg.ReviewMessages,
		Documents: cfg.ReviewDocuments, DecisionBlocks: cfg.ReviewDecisionBlocks,
		Rules:        rules,
		Model:        reviewer.Model{Model: model, Pace: pace, Timeout: cfg.ReviewerTimeout},
		Timeout:      cfg.ReviewerTimeout,
		WriterWindow: cfg.ReviewerWriterWindow,
		Unedited:     cfg.ReviewerUnedited,
	}), nil
}
