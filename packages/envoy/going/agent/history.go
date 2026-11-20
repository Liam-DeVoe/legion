package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/going/agentwire"
)

// historyLimit is the most the whole steps of a run's history may hold: a run whose last
// agentwire's 8 whole steps pass it stops with ReasonHistoryTooLarge, as the server fails one at
// its claim.
const historyLimit = 2 << 20

// ReasonHistoryTooLarge is the stop reason of a run whose last 8 steps pass historyLimit, so no
// model call can be given them.
const ReasonHistoryTooLarge = "HISTORY_TOO_LARGE: the last 8 steps of this run pass 2 MiB, which no model call can be given"

// noteLimit is the most bytes of a step's note: the one line a run's history gives in place of
// an older step's body.
const noteLimit = 500

// modelBody is the body of a saved model step: the reply's prose and the tool calls it asked
// for, and the output tokens the call wrote.
type modelBody struct {
	Text   string     `json:"text"`
	Calls  []toolCall `json:"calls"`
	Output int        `json:"output_tokens"`
}

// toolCall is one tool call a model step asked for: the provider's id for it, the tool and its
// arguments as the model wrote them.
type toolCall struct {
	ID        string          `json:"id"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolBody is the body of a saved tool step: the call, what the model was told of its result,
// and whether that is a refusal; and for a row of the run's plan that the agent ran (runRows), the
// row.
type toolBody struct {
	Call   toolCall           `json:"call"`
	Result string             `json:"result"`
	Error  bool               `json:"error"`
	Row    *agentwire.PlanRow `json:"row,omitempty"`
}

// note is text cut to noteLimit bytes, at a rune's start, on one line.
func note(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= noteLimit {
		return text
	}
	cut := noteLimit
	for cut > 0 && !utf8RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// utf8RuneStart reports whether b starts a rune rather than continuing one.
func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// wholeBytes is the bytes of the whole steps of history, which historyLimit bounds.
func wholeBytes(history []agentwire.SavedStep) int {
	whole := 0
	for _, step := range history {
		whole += len(step.Body)
	}
	return whole
}

// lastWhole is the last agentwire's 8 steps of history whole and each older one as its note,
// as the server answers a claim: the history the next step starts from.
func lastWhole(history []agentwire.SavedStep) []agentwire.SavedStep {
	const whole = 8
	out := make([]agentwire.SavedStep, len(history))
	for i, step := range history {
		if i < len(history)-whole {
			step.Body = nil
		}
		out[i] = step
	}
	return out
}

// conversation is what the next step of run gives the model, from history: one message of the
// person's words, with the turns of the conversation before this run, the page they asked from
// and the notes of the run's older steps; then each whole step, a model step as the reply it was
// and its tool steps as that reply's results. The person's answer to a plan, Auto-review's and
// each row of the plan that ran are told the model as the person's next message. A tool call whose
// result was not saved is answered as unsaved, so each call has its result, as the providers
// require; a tool step whose model step is only a note is told as a note.
func conversation(run *agentwire.ClaimedRun, history []agentwire.SavedStep) ([]agentloop.Message, error) {
	var opening strings.Builder
	if len(run.Turns) > 0 {
		opening.WriteString("Earlier in this conversation, oldest first:\n")
		for _, turn := range run.Turns {
			fmt.Fprintf(&opening, "- The person asked: %s\n", turn.Words)
			for _, line := range turn.Notes {
				fmt.Fprintf(&opening, "  - %s\n", line)
			}
		}
		opening.WriteString("\n")
	}
	fmt.Fprintf(&opening, "The person asks now:\n%s\n", run.Words)
	if len(run.Context) > 0 && string(run.Context) != "null" {
		fmt.Fprintf(&opening, "\nThe page the person asks from, %s:\n%s\n", storedDataNote, run.Context)
	}
	var older []string
	var steps []agentloop.Message
	// asked is the tool calls of the last model step, in its order, and pending those of them no
	// tool step has answered yet.
	var asked []toolCall
	pending := map[string]bool{}
	results := func() *agentloop.Message {
		if steps[len(steps)-1].Role != agentloop.ToolRole {
			steps = append(steps, agentloop.Message{Role: agentloop.ToolRole})
		}
		return &steps[len(steps)-1]
	}
	closeCalls := func() {
		for _, call := range asked {
			if pending[call.ID] {
				message := results()
				message.Blocks = append(message.Blocks, toolResult(call, "this call's result was not saved; call the tool again if you need it", true))
			}
		}
		asked = nil
		clear(pending)
	}
	// told adds text to the person's message that follows the steps so far.
	told := func(text string) {
		if len(steps) == 0 || steps[len(steps)-1].Role != agentloop.User {
			steps = append(steps, agentloop.Message{Role: agentloop.User})
		}
		message := &steps[len(steps)-1]
		message.Blocks = append(message.Blocks, agentloop.Block{Kind: agentloop.TextBlock, Text: text})
	}
	for _, step := range history {
		if len(step.Body) == 0 {
			older = append(older, fmt.Sprintf("step %d (%s): %s", step.Step, step.Kind, step.Note))
			continue
		}
		switch step.Kind {
		case agentwire.StepModel:
			closeCalls()
			var body modelBody
			if err := json.Unmarshal(step.Body, &body); err != nil {
				return nil, fmt.Errorf("read step %d of run %s: %w", step.Step, run.ID, err)
			}
			reply := agentloop.Message{Role: agentloop.Assistant}
			if body.Text != "" {
				reply.Blocks = append(reply.Blocks, agentloop.Block{Kind: agentloop.TextBlock, Text: body.Text})
			}
			for _, call := range body.Calls {
				reply.Blocks = append(reply.Blocks, agentloop.Block{Kind: agentloop.ToolCallBlock, ToolCallID: call.ID, Name: call.Tool, Input: call.Arguments})
				asked = append(asked, call)
				pending[call.ID] = true
			}
			if len(reply.Blocks) == 0 {
				reply.Blocks = []agentloop.Block{{Kind: agentloop.TextBlock, Text: "(no reply)"}}
			}
			steps = append(steps, reply)
		case agentwire.StepTool:
			var body toolBody
			if err := json.Unmarshal(step.Body, &body); err != nil {
				return nil, fmt.Errorf("read step %d of run %s: %w", step.Step, run.ID, err)
			}
			if body.Row != nil {
				closeCalls()
				told(fmt.Sprintf("Change %d of plan %d, %s %s: %s", body.Row.Row, body.Row.Plan, body.Call.Tool, body.Call.Arguments, body.Result))
				continue
			}
			if !pending[body.Call.ID] {
				older = append(older, fmt.Sprintf("step %d (tool %s): %s", step.Step, body.Call.Tool, step.Note))
				continue
			}
			message := results()
			message.Blocks = append(message.Blocks, toolResult(body.Call, body.Result, body.Error))
			delete(pending, body.Call.ID)
		case agentwire.StepPerson:
			closeCalls()
			var body agentwire.PersonStep
			if err := json.Unmarshal(step.Body, &body); err != nil {
				return nil, fmt.Errorf("read step %d of run %s: %w", step.Step, run.ID, err)
			}
			told(personText(body))
		case agentwire.StepReview:
			closeCalls()
			var body reviewBody
			if err := json.Unmarshal(step.Body, &body); err != nil {
				return nil, fmt.Errorf("read step %d of run %s: %w", step.Step, run.ID, err)
			}
			told(fmt.Sprintf("Auto-review read plan %d against the person's words, which ask for changes %v; the changes it let through run without waiting, and every other change waits for the person's approval.", body.Plan, body.Asked))
		default:
			return nil, fmt.Errorf("step %d of run %s is of kind %q, which this build does not read", step.Step, run.ID, step.Kind)
		}
	}
	closeCalls()
	if len(older) > 0 {
		fmt.Fprintf(&opening, "\nThe earlier steps of this run, as one-line notes:\n- %s\n", strings.Join(older, "\n- "))
	}
	messages := []agentloop.Message{{Role: agentloop.User, Blocks: []agentloop.Block{{Kind: agentloop.TextBlock, Text: opening.String()}}}}
	return append(messages, steps...), nil
}

// toolResult is the block that answers call with result.
func toolResult(call toolCall, result string, isError bool) agentloop.Block {
	return agentloop.Block{
		Kind: agentloop.ToolResultBlock, ToolCallID: call.ID, Name: call.Tool, IsError: isError,
		Content: []agentloop.Block{{Kind: agentloop.TextBlock, Text: result}},
	}
}

// personText is what the model is told of the person's answer to a plan: the changes they
// approved and those they left out, which run as the steps after it say; or the words they asked
// another plan with, beside the rows of the plan those words revise.
func personText(step agentwire.PersonStep) string {
	var text strings.Builder
	if step.Revise != "" {
		fmt.Fprintf(&text, "The person read plan %d and asks for another plan in its place, which you propose with propose_plan. They say:\n%s\n\nThe changes of plan %d, none of which ran:\n", step.Plan, step.Revise, step.Plan)
		for _, row := range step.Rows {
			fmt.Fprintf(&text, "- %d: %s %s\n", row.Row, row.Tool, row.Arguments)
		}
		return text.String()
	}
	fmt.Fprintf(&text, "The person approved plan %d", step.Plan)
	if len(step.Unticked) > 0 {
		fmt.Fprintf(&text, ", leaving out changes %v, which do not run", step.Unticked)
	}
	text.WriteString(". Each approved change ran as follows; say in a few words what was done, and propose nothing more unless the person asks.")
	return text.String()
}
