package seedcorpus

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// replayCopiedIssue replays one captured issue through the API as recorderSession, the agent
// that wrote it: the issue with its spec, each section of its decisions.md as a `decision` block of
// the spec's `## Decisions` part (decisionBlocks), and each captured ask as an asks row in the
// state the capture holds it in (replayAsk). An ask whose block the spec holds is the row that
// block settles into; any other is asked again through its own route.
func (s *seeder) replayCopiedIssue(ctx context.Context, capture copiedCapture) error {
	spec := withDecisions(capture.Documents.Spec.Markdown, decisionBlocks(capture.Documents.Decisions.Markdown))
	var issue model.Issue
	if err := s.api.call(ctx, asSession(recorderSession), http.MethodPost, "/api/v1/issues", map[string]any{
		"project": s.project, "title": capture.Title, "spec": spec, "force": true,
	}, &issue, http.StatusCreated); err != nil {
		return fmt.Errorf("replay %s: %w", capture.file, err)
	}
	name := strings.TrimSuffix(capture.file, ".json")
	s.corpus.set(name+".issue", issue.Key)
	s.corpus.set(name+".spec", issue.PrimaryArtifactID)
	blockAsks, err := s.blockAsks(ctx, issue.Key, capture.blockIDs())
	if err != nil {
		return fmt.Errorf("replay %s: %w", capture.file, err)
	}
	for index, ask := range capture.Asks {
		id, err := s.replayAsk(ctx, issue, capture.Documents.Spec.Markdown, ask, blockAsks)
		if err != nil {
			return fmt.Errorf("replay ask %d (%s) of %s: %w", index, ask.ID, capture.file, err)
		}
		s.corpus.set(fmt.Sprintf("%s.ask.%s", name, ask.ID), id)
	}
	return nil
}

// decisionBlocks writes each section of a decisions.md as one `decision` block, in document order:
// each `##` heading outside `## Replaced` and each `###` heading under it, as the decisions step
// cuts one (docs' decisionSection), its title the block's first paragraph in bold and every line
// after the heading up to the next heading of its level or above the rest of its words, word for
// word. A section under `## Replaced` is written `replaced="true"`. Nothing of a section's words
// is read for an attribute: the uncertainty, the asks it cites and what it shaped stay its text,
// so no part of the decisions step's own conversion is written again here.
func decisionBlocks(markdown string) []string {
	var blocks []string
	var title string
	var body []string
	replaced, open, number := false, false, 0
	flush := func() {
		if !open {
			return
		}
		number++
		attributes := fmt.Sprintf("#decision-%d", number)
		if replaced {
			attributes += ` replaced="true"`
		}
		words := strings.TrimSpace(strings.Join(body, "\n"))
		block := ":::decision{" + attributes + "}\n**" + title + "**\n"
		if words != "" {
			block += "\n" + words + "\n"
		}
		blocks = append(blocks, block+":::\n")
		open, body = false, nil
	}
	for _, line := range strings.Split(markdown, "\n") {
		level, heading := headingOf(line)
		switch {
		case level == 2 && heading == "Replaced":
			flush()
			replaced = true
		case level == 2 || (level == 3 && replaced):
			flush()
			if level == 2 {
				replaced = false
			}
			title, open = heading, true
		case level == 1:
			flush()
		case open:
			body = append(body, line)
		}
	}
	flush()
	return blocks
}

// headingOf is a line's ATX heading level and its words, or level 0 for a line that is no heading.
func headingOf(line string) (int, string) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, ""
	}
	return level, strings.TrimSpace(line[level:])
}

// withDecisions puts blocks at the end of the spec's `## Decisions` part, after whatever the part
// holds already, and opens that part at the spec's end where it has none.
func withDecisions(spec string, blocks []string) string {
	joined := strings.Join(blocks, "\n")
	const part = "\n## Decisions\n"
	at := strings.Index(spec, part)
	if at < 0 {
		return strings.TrimRight(spec, "\n") + "\n" + part + "\n" + joined
	}
	end := len(spec)
	if next := strings.Index(spec[at+len(part):], "\n## "); next >= 0 {
		end = at + len(part) + next + 1
	}
	return strings.TrimRight(spec[:end], "\n") + "\n\n" + joined + "\n" + spec[end:]
}

// blockIDs are the ids of the ask blocks the capture's spec holds.
func (c copiedCapture) blockIDs() []string {
	var ids []string
	for _, ask := range c.Asks {
		if id := ask.heldBlock(c.Documents.Spec.Markdown); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// heldBlock is the id of the ask's block where the spec still holds it, and "" where the ask was
// asked from no block or the spec no longer holds its block: the capture names the block each ask
// was asked from, and a later version of the spec dropped most of them.
func (a capturedAsk) heldBlock(spec string) string {
	if a.BlockID == nil {
		return ""
	}
	if strings.Contains(spec, ":::ask{#"+*a.BlockID+" ") || strings.Contains(spec, ":::ask{#"+*a.BlockID+"}") {
		return *a.BlockID
	}
	return ""
}

// replayAsk makes the asks row of one captured ask and leaves it in the captured state: the row
// its spec block settled into, or a question asked through the issue's ask route, or an approval
// asked through the spec's approval route; then answered as the capture's answerer answered it,
// resolved, or left open.
func (s *seeder) replayAsk(ctx context.Context, issue model.Issue, spec string, ask capturedAsk, blockAsks map[string]string) (string, error) {
	id := ""
	if block := ask.heldBlock(spec); block != "" {
		id = blockAsks[block]
	}
	switch {
	case id != "":
	case ask.Kind == "approval":
		opened, err := s.requestApproval(ctx, issue)
		if err != nil {
			return "", err
		}
		if opened == "" {
			// The spec stands approved at its current version, and an approval of a document at
			// its current version opens no ask. The captured approvals were each asked of a later
			// version than the one approved before them, so the replay writes one: a paragraph
			// naming the approval it is written for.
			if err := s.api.call(ctx, asSession(recorderSession), http.MethodPost, "/api/v1/issues/"+issue.Key+"/artifacts/spec/edits",
				map[string]any{"ops": []map[string]any{{"op": "insert", "after": "end",
					"markdown": "A version of the replay, for the approval the capture holds as ask " + ask.ID + ".\n"}}}, nil); err != nil {
				return "", fmt.Errorf("write a new version of the spec for the next approval: %w", err)
			}
			if opened, err = s.requestApproval(ctx, issue); err != nil {
				return "", err
			}
			if opened == "" {
				return "", fmt.Errorf("an approval of the spec's new version opened no ask")
			}
		}
		id = opened
	default:
		options := make([]model.AskOption, 0, len(ask.Options))
		for _, option := range ask.Options {
			options = append(options, model.AskOption{Label: option.Label, Description: option.Description})
		}
		// The capture keeps no `multiple` of its own: an answer that selected more than one option
		// was given to a multi-select ask, which the ask is asked as again.
		multiple := ask.Answer != nil && len(ask.Answer.Selected) > 1
		var opened model.Ask
		if err := s.api.call(ctx, asSession(recorderSession), http.MethodPost, "/api/v1/issues/"+issue.Key+"/asks",
			map[string]any{"question": ask.Question, "options": options, "multiple": multiple}, &opened); err != nil {
			return "", err
		}
		id = opened.ID
	}
	switch ask.State {
	case "answered":
		if ask.Answer == nil {
			return "", fmt.Errorf("the capture holds the ask answered with no answer")
		}
		body := map[string]any{"selected": ask.Answer.Selected, "expected_edited_at": nil}
		if ask.Answer.Text != "" {
			body["text"] = ask.Answer.Text
		}
		if body["selected"] == nil {
			body["selected"] = []string{}
		}
		return id, s.api.call(ctx, asPerson(ask.Answer.By), http.MethodPost, "/api/v1/asks/"+id+"/answer", body, nil)
	case "resolved":
		return id, s.api.call(ctx, asSession(recorderSession), http.MethodPost, "/api/v1/asks/"+id+"/resolve",
			map[string]any{"kind": "resolved", "reason": "resolved as the capture holds it"}, nil)
	case "open":
		return id, nil
	}
	return "", fmt.Errorf("the capture holds the ask in state %q", ask.State)
}

// requestApproval asks for an approval of the issue's spec at its current version, as the agent
// that wrote it, and answers the approval ask's id, "" where the spec stands approved at that
// version already and the route opens none.
func (s *seeder) requestApproval(ctx context.Context, issue model.Issue) (string, error) {
	var opened struct {
		Ask *model.Ask `json:"ask"`
	}
	if err := s.api.call(ctx, asSession(recorderSession), http.MethodPost,
		"/api/v1/artifacts/"+issue.PrimaryArtifactID+"/approval-requests", map[string]any{}, &opened); err != nil {
		return "", err
	}
	if opened.Ask == nil {
		return "", nil
	}
	return opened.Ask.ID, nil
}

// blockAsks waits until every ask block of the issue's spec named in ids has settled into its asks
// row, and answers each row's id by its block's id. A block ask's row is written by the settlement
// of the document that holds the block, which runs behind the write that created the issue.
func (s *seeder) blockAsks(ctx context.Context, key string, ids []string) (map[string]string, error) {
	found := map[string]string{}
	deadline := time.Now().Add(settleBound)
	for {
		var asks []model.Ask
		if err := s.api.call(ctx, asPerson(alice), http.MethodGet, "/api/v1/issues/"+key+"/asks?state=all", nil, &asks); err != nil {
			return nil, err
		}
		for _, ask := range asks {
			if ask.BlockID != nil {
				found[*ask.BlockID] = ask.ID
			}
		}
		missing := 0
		for _, id := range ids {
			if found[id] == "" {
				missing++
			}
		}
		if missing == 0 {
			return found, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%d ask blocks of %s settled into no asks row in %s", missing, key, settleBound)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(settlePoll):
		}
	}
}

// settleBound is how long the loader waits for a document's settlement to write what it owes, and
// settlePoll how often it reads whether it has.
const (
	settleBound = 30 * time.Second
	settlePoll  = 20 * time.Millisecond
)
