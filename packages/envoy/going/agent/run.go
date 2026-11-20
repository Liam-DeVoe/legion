package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/going/agentwire"
)

// The byte limits of one model call of a run (section 5 of the background-runs design): the
// response stream at most 8 MiB and the reply it builds at most 512 KiB, past which the call
// fails and the run says why.
const (
	runStreamLimit = 8 << 20
	runReplyLimit  = 512 << 10
)

// The reasons a run ends failed with, beside the bounds of its level (levels.go) and the reasons
// the server names.
const (
	// ReasonNoModel is a run of an agent whose server names no chat model.
	ReasonNoModel = "NO_MODEL: this server names no chat model (DISPATCH_ANSWERER), so its agent cannot work the run"
	// codeModelFailed, codeModelRefused, codeModelTooLarge and codeModelCallTime are a model call
	// that failed, that the model refused, whose stream or reply passed its limit, and that took
	// past modelCallTime.
	codeModelFailed   = "MODEL_FAILED"
	codeModelRefused  = "MODEL_REFUSED"
	codeModelTooLarge = "MODEL_REPLY_TOO_LARGE"
	codeModelCallTime = "MODEL_CALL_TIME"
	// codeAgentFailed is a run the agent's own code failed on: a step it could not save, a
	// history it could not read, a callback of its loop that panicked.
	codeAgentFailed = "AGENT_FAILED"
)

// The frames a run's steps show its person's bar.
const (
	// frameTool is one tool call, as it ended: toolFrame.
	frameTool = "tool"
	// frameReply is one model reply: replyFrame.
	frameReply = "reply"
)

// toolFrame is the data of a tool frame: the step, the tool, its one-line note and whether it
// was refused.
type toolFrame struct {
	Step  int    `json:"step"`
	Tool  string `json:"tool"`
	Note  string `json:"note"`
	Error bool   `json:"error"`
}

// replyFrame is the data of a reply frame: the step, the reply's prose and the tools it called.
type replyFrame struct {
	Step  int      `json:"step"`
	Text  string   `json:"text"`
	Tools []string `json:"tools"`
}

// errStepTaken ends agentloop.Run once its one model call of a step finished and was saved: each
// step starts the loop again from the saved steps.
var errStepTaken = errors.New("the step's model call finished and was saved")

// worker is one claimed run of the agent's: the run, its level, what it has spent, its history,
// and its own context, which a stop, a lease the run no longer holds, the watchdog and the
// agent's end each cancel.
type worker struct {
	agent *agent
	run   *agentwire.ClaimedRun
	name  string
	level Level

	// used is what the run has spent, its earlier turns' and this one's; turn is this turn's
	// alone, which end adds to the run's budget.
	used, turn agentwire.Budget
	// history is the run's steps as the claim answered them, then each step saved since, in the
	// order of their numbers; next is the number the next step takes.
	history []agentwire.SavedStep
	next    int

	ctx    context.Context
	cancel context.CancelFunc
	// release frees the run's slot of the agent's, once whichever calls it first: the run's
	// goroutine as it returns, or the watchdog as it gives the run up.
	release func()

	// mu guards stepped, the steps a model call in flight saved, which it adds to history at its
	// end.
	mu      sync.Mutex
	stepped []agentwire.SavedStep

	// grantEnded is the reason a call's grant ended with, which stops the run; lost is set once
	// the run is no longer held under its lease, after which the worker writes nothing more of it;
	// proposed is set by a step whose model proposed a plan.
	grantEnded atomic.Pointer[string]
	lost       atomic.Bool
	proposed   atomic.Bool
}

// newWorker is the worker of run, its context ctx's, its slot freed by release.
func newWorker(ctx context.Context, a *agent, run *agentwire.ClaimedRun, release func()) *worker {
	w := &worker{agent: a, run: run, name: run.Level, history: run.History, release: release}
	w.ctx, w.cancel = context.WithCancel(ctx)
	w.used = run.BudgetUsed
	models := 0
	for _, step := range run.History {
		if step.Kind == agentwire.StepModel {
			models++
		}
		w.next = step.Step
	}
	// A turn cut short by the agent's death adds nothing to budget_used, so the run's own saved
	// model steps count its calls too.
	w.used.Calls = max(w.used.Calls, models)
	w.next++
	return w
}

// lose stops working on the run: it is no longer held under its lease, so nothing more of it is
// written.
func (w *worker) lose() {
	w.lost.Store(true)
	w.cancel()
}

// endGrant stops the run for reason, the grant it acts with having ended.
func (w *worker) endGrant(reason string) {
	w.grantEnded.CompareAndSwap(nil, &reason)
	w.cancel()
}

// work runs the run's turn: a step at a time from its saved steps, until it answers, proposes a
// plan, spends a bound of its level, fails, or stops being the agent's to work.
func (w *worker) work() {
	level, known := Levels[w.run.Level]
	switch {
	case w.agent.config.Model.ID == "":
		w.end(agentwire.StateFailed, ReasonNoModel, nil)
		return
	case !known:
		w.end(agentwire.StateFailed, fmt.Sprintf("%s: the run's level %q is none of fast, standard and deep", codeAgentFailed, w.run.Level), nil)
		return
	}
	w.level = level
	if done, state, reason := w.settled(); done {
		w.end(state, reason, nil)
		return
	}
	retry := time.Second
	for w.ctx.Err() == nil {
		if reason := w.level.spent(w.name, w.used); reason != "" {
			w.end(agentwire.StateStopped, reason, []string{agentwire.OfferContinue})
			return
		}
		if wholeBytes(lastWhole(w.history)) > historyLimit {
			w.end(agentwire.StateFailed, ReasonHistoryTooLarge, nil)
			return
		}
		outcome := w.step()
		switch {
		case w.lost.Load():
			return
		case w.grantEnded.Load() != nil:
			w.end(agentwire.StateStopped, *w.grantEnded.Load(), nil)
			return
		case w.ctx.Err() != nil:
			return
		case outcome.retry:
			timer := time.NewTimer(retry)
			select {
			case <-w.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			retry = min(2*retry, 30*time.Second)
			continue
		case outcome.state != "":
			w.end(outcome.state, outcome.reason, outcome.offers)
			return
		}
		retry = time.Second
	}
}

// settled reports whether the run's saved steps already end its turn: a last step that answered
// with no tool call, or that proposed a plan, which waits for its person.
func (w *worker) settled() (bool, string, string) {
	if len(w.history) == 0 {
		return false, "", ""
	}
	last := w.history[len(w.history)-1]
	if len(last.Body) == 0 {
		return false, "", ""
	}
	switch last.Kind {
	case agentwire.StepModel:
		var body modelBody
		if json.Unmarshal(last.Body, &body) == nil && len(body.Calls) == 0 {
			return true, agentwire.StateDone, ""
		}
	case agentwire.StepTool:
		var body toolBody
		if json.Unmarshal(last.Body, &body) == nil && body.Call.Tool == toolProposePlan && !body.Error {
			return true, agentwire.StateWaiting, ""
		}
	}
	return false, "", ""
}

// stepOutcome is how a step ended: retry to make it again after a wait, or the state, reason and
// offers to end the run's turn with; neither for a step that saved its model call and goes on.
type stepOutcome struct {
	retry  bool
	state  string
	reason string
	offers []string
}

// step makes one step: one agentloop.Run of one model call, from the run's saved steps, with the
// tools it calls; the model call is saved (OnStep), each tool call as it ends (toolRun). The
// watchdog gives up the run when the step runs past its model call's time and watchdogGrace.
func (w *worker) step() stepOutcome {
	messages, err := conversation(w.run, lastWhole(w.history))
	if err != nil {
		return stepOutcome{state: agentwire.StateFailed, reason: fmt.Sprintf("%s: %v", codeAgentFailed, err)}
	}
	base := w.next
	var finish string
	var calls []string
	w.proposed.Store(false)
	prompt := len(systemPrompt)
	for _, message := range messages {
		for _, block := range message.Blocks {
			prompt += len(block.Text) + len(block.Input)
			for _, part := range block.Content {
				prompt += len(part.Text)
			}
		}
	}
	request := w.level.request(w.agent.config.Model, w.used)
	request.Model = w.agent.pacer.paced(request.Model, func() int { return prompt/charsPerToken + w.level.OutputTokens })
	request.System, request.Input, request.Tools = systemPrompt, messages, w.tools(base)
	request.OnStep = func(step agentloop.Step) error {
		finish = step.Finish
		calls = w.saveReply(base, step)
		return errStepTaken
	}
	if w.agent.config.Hooks {
		request.OnText = hookOnText
	}
	stepCtx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	finished := w.agent.watch(w, request.Bounds.Time+watchdogGrace)
	started := time.Now()
	result, err := agentloop.Run(stepCtx, request)
	working := time.Since(started)
	if !finished() {
		// The watchdog gave the run up: its goroutine was counted stuck, and it writes nothing.
		w.lose()
		return stepOutcome{}
	}
	w.turn.WorkingMS += working.Milliseconds()
	w.used.WorkingMS += working.Milliseconds()
	w.turn.Tokens += result.Usage.Output
	w.used.Tokens += result.Usage.Output
	if len(result.Steps) > 0 || result.Fault != "" {
		w.turn.Calls++
		w.used.Calls++
	}
	w.mu.Lock()
	w.history = append(w.history, w.stepped...)
	if len(w.stepped) > 0 {
		w.next = w.stepped[len(w.stepped)-1].Step + 1
	}
	w.stepped = nil
	w.mu.Unlock()
	switch {
	case w.lost.Load() || w.grantEnded.Load() != nil || w.ctx.Err() != nil:
		return stepOutcome{}
	case errors.Is(err, errStepTaken):
		switch {
		case finish == "length":
			return stepOutcome{state: agentwire.StateStopped, reason: w.level.cut(w.name), offers: []string{agentwire.OfferContinue}}
		case w.proposed.Load():
			return stepOutcome{state: agentwire.StateWaiting}
		case len(calls) == 0:
			return stepOutcome{state: agentwire.StateDone}
		}
		return stepOutcome{}
	case err != nil:
		return stepOutcome{state: agentwire.StateFailed, reason: fmt.Sprintf("%s: %v", codeAgentFailed, err)}
	}
	return w.faulted(result)
}

// faulted is how a step whose model call saved nothing ends the run's turn: at its level's time,
// or at the model call's own; a model call that failed, that the model refused or that passed its
// size limits fails the run; a model the quota or a 429 held back is tried again.
func (w *worker) faulted(result agentloop.Result) stepOutcome {
	switch result.Fault {
	case agentloop.FaultTime:
		if reason := w.level.spent(w.name, w.used); reason != "" {
			return stepOutcome{state: agentwire.StateStopped, reason: reason, offers: []string{agentwire.OfferContinue}}
		}
		return stepOutcome{state: agentwire.StateFailed, reason: fmt.Sprintf("%s: a model call of this run took past %s", codeModelCallTime, modelCallTime)}
	case agentloop.FaultTooLarge:
		return stepOutcome{state: agentwire.StateFailed, reason: fmt.Sprintf("%s: %s", codeModelTooLarge, result.Detail)}
	case agentloop.FaultRefusal:
		return stepOutcome{state: agentwire.StateFailed, reason: codeModelRefused + ": the chat model refused to answer"}
	case agentloop.FaultLength:
		return stepOutcome{state: agentwire.StateStopped, reason: w.level.cut(w.name), offers: []string{agentwire.OfferContinue}}
	}
	if result.Status == http.StatusTooManyRequests || strings.Contains(result.Detail, errQuotaPastDeadline.Error()) {
		return stepOutcome{retry: true}
	}
	detail := "the chat model answered no reply"
	if result.Status != 0 {
		detail = fmt.Sprintf("the chat model answered %d", result.Status)
	}
	return stepOutcome{state: agentwire.StateFailed, reason: fmt.Sprintf("%s: %s", codeModelFailed, detail)}
}

// saveReply saves step, the model call of the step numbered base, and answers the tools it
// called.
func (w *worker) saveReply(base int, step agentloop.Step) []string {
	var body modelBody
	var tools []string
	for _, block := range step.Blocks {
		switch block.Kind {
		case agentloop.TextBlock:
			body.Text += block.Text
		case agentloop.ToolCallBlock:
			input := block.Input
			if !json.Valid(input) {
				input = json.RawMessage(`{}`)
			}
			body.Calls = append(body.Calls, toolCall{ID: block.ToolCallID, Tool: block.Name, Arguments: input})
			tools = append(tools, block.Name)
		}
	}
	body.Output = step.Usage.Output
	line := note(body.Text)
	if len(tools) > 0 {
		line = note(fmt.Sprintf("%s [calls %s]", body.Text, strings.Join(tools, ", ")))
	}
	frame, _ := json.Marshal(replyFrame{Step: base, Text: body.Text, Tools: append([]string{}, tools...)})
	w.save(agentwire.StepRequest{Step: base, Kind: agentwire.StepModel, Note: line, Frames: []agentwire.Frame{{Name: frameReply, Data: frame}}}, body)
	return tools
}

// toolRun is the agentloop Run of tool in the step whose model call is numbered base: it makes the
// call do makes as the step numbered after base by the tool call's index, and saves it as that
// step, with what the model is told of its result.
func (w *worker) toolRun(base int, tool string, do func(ctx context.Context, step int, input json.RawMessage) (string, bool)) func(context.Context, agentloop.ToolCall) (string, bool) {
	return func(ctx context.Context, call agentloop.ToolCall) (string, bool) {
		step := base + call.Index
		result, refused := do(ctx, step, call.Input)
		if w.lost.Load() {
			return result, refused
		}
		input := call.Input
		if !json.Valid(input) {
			input = json.RawMessage(`{}`)
		}
		line := note(fmt.Sprintf("%s %s: %s", tool, input, result))
		frame, _ := json.Marshal(toolFrame{Step: step, Tool: tool, Note: line, Error: refused})
		w.save(agentwire.StepRequest{Step: step, Kind: agentwire.StepTool, Note: line, Frames: []agentwire.Frame{{Name: frameTool, Data: frame}}},
			toolBody{Call: toolCall{ID: call.ID, Tool: tool, Arguments: input}, Result: result, Error: refused})
		return result, refused
	}
}

// save saves one step of the run with body, and keeps it for the history of the next step. A
// step the server refuses as no longer held loses the run; any other failure fails the step's
// call to the server, which the run's next step makes again.
func (w *worker) save(request agentwire.StepRequest, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		slog.Error("agent: encode a step", "run", w.run.ID, "step", request.Step, "error", err)
		return
	}
	request.Run, request.LeaseToken, request.Body = w.run.ID, w.run.LeaseToken, encoded
	if _, err := w.agent.client.Step(w.ctx, request); err != nil {
		var refused *agentwire.Error
		if errors.As(err, &refused) && refused.Code == agentwire.CodeStaleLease {
			w.lose()
			return
		}
		if w.ctx.Err() == nil {
			slog.Warn("agent: save a step", "run", w.run.ID, "step", request.Step, "error", err)
		}
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stepped = append(w.stepped, agentwire.SavedStep{Step: request.Step, Kind: request.Kind, Body: encoded, Note: request.Note})
	// The model step is saved after the tool steps it asked for, so the history keeps the
	// numbers' order.
	for i := len(w.stepped) - 1; i > 0 && w.stepped[i].Step < w.stepped[i-1].Step; i-- {
		w.stepped[i], w.stepped[i-1] = w.stepped[i-1], w.stepped[i]
	}
}

// end ends the run's turn in state, with reason and offers, adding what the turn spent. A run
// that is no longer held, or that was stopped, is ended by nobody: its new owner, or the stop,
// ended it.
func (w *worker) end(state, reason string, offers []string) {
	if w.lost.Load() {
		return
	}
	// The end is the run's last word, made even as the agent stops, within its own bound.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(w.ctx), 10*time.Second)
	defer cancel()
	request := agentwire.EndRequest{Run: w.run.ID, LeaseToken: w.run.LeaseToken, State: state, Reason: reason, Used: w.turn, Offers: offers}
	if err := w.agent.client.End(ctx, request); err != nil {
		var refused *agentwire.Error
		if !errors.As(err, &refused) || refused.Code != agentwire.CodeStaleLease {
			slog.Warn("agent: end a run", "run", w.run.ID, "state", state, "error", err)
		}
	}
}
