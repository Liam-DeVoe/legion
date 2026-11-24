package answerloop

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/answer"
	"github.com/sjawhar/legion/daemon/internal/dispatchtool"
	"github.com/sjawhar/legion/daemon/internal/ghtool"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// fakeDispatchTools opens the rig's dispatch tool, recording the session and question each answer
// opens it for and how many it closed; its calls answer with output naming them.
type fakeDispatchTools struct {
	mu     sync.Mutex
	opened []string
	closed int
	calls  []dispatchtool.Call
}

func (f *fakeDispatchTools) Open(session, question string) (DispatchTool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, session+" "+question)
	return fakeDispatchTool{f}, nil
}

type fakeDispatchTool struct{ tools *fakeDispatchTools }

func (t fakeDispatchTool) Run(_ context.Context, call dispatchtool.Call) dispatchtool.Result {
	t.tools.mu.Lock()
	defer t.tools.mu.Unlock()
	t.tools.calls = append(t.tools.calls, call)
	return dispatchtool.Result{Output: "LEGION-9: a comment since you stopped says plum", Ran: true}
}

func (t fakeDispatchTool) Close() error {
	t.tools.mu.Lock()
	defer t.tools.mu.Unlock()
	t.tools.closed++
	return nil
}

// An answer whose model calls dispatch posts its reply as any answer does: the loop opens the
// answer's dispatch tool as the agent's session for the question, hands it to the model loop
// beside gh, and closes it once the answer is done. A loop with no dispatch tool, a daemon that
// found no dispatch command, hands the model loop none.
func TestAnAnswerThatCallsDispatchPostsItsReply(t *testing.T) {
	r := newRig(t)
	r.conversation("ses_tester", testerToken, 0)
	tools := &fakeDispatchTools{}
	r.cfg.DispatchTools = tools
	r.restart()
	r.answer = func(req answer.Request) (answer.Reply, error) {
		if req.Dispatch == nil || req.GH == nil {
			t.Errorf("the request carries gh %v and dispatch %v; want both", req.GH, req.Dispatch)
			return answer.Reply{}, nil
		}
		read := req.Dispatch.Run(context.Background(), dispatchtool.Call{Seq: 1, N: 1, Args: `["read","LEGION-9"]`})
		return answered(req, "Plum, from what "+read.Output), nil
	}
	r.receive(messageHandoff("ses_tester", "m-1", 1, "What does the new comment say?"))
	r.drain()
	q := r.only()
	posts := r.dispatch.posts()
	if q.State != store.QuestionPosted || len(posts) != 1 || !strings.HasPrefix(posts[0].Body["body"].(string), "Plum, from what LEGION-9: a comment since you stopped says plum\n\n"+AnswerMarker+q.ID+" -->") {
		t.Fatalf("question %s, posts %s; want the reply from the dispatch read posted", q.State, describe(posts))
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if !slices.Equal(tools.opened, []string{"ses_tester " + q.ID}) || tools.closed != 1 || len(tools.calls) != 1 {
		t.Errorf("the dispatch tool was opened %v, closed %d times, called %d times; want once as ses_tester for %s, closed, one call", tools.opened, tools.closed, len(tools.calls), q.ID)
	}

	plain := newRig(t)
	plain.conversation("ses_tester", testerToken, 0)
	plain.answer = func(req answer.Request) (answer.Reply, error) {
		if req.Dispatch != nil {
			t.Errorf("a loop with no dispatch tool handed the model loop %v", req.Dispatch)
		}
		return answered(req, "Plum."), nil
	}
	plain.receive(messageHandoff("ses_tester", "m-1", 1, "Why plum?"))
	plain.drain()
	if q := plain.only(); q.State != store.QuestionPosted {
		t.Fatalf("question = %s; want posted with gh alone", q.State)
	}
}

// A question the daemon stopped while a dispatch call ran is answered again on restart with that
// call recorded as of unknown outcome, in the dispatch tool's own words, and never looked up on
// GitHub, which holds nothing of Dispatch's: the gh call of unknown outcome beside it is looked up
// as before.
func TestARestartRecordsAnUnknownDispatchCallAndLooksNothingUp(t *testing.T) {
	r := newRig(t)
	r.conversation("ses_tester", testerToken, 0)
	r.receive(messageHandoff("ses_tester", "m-1", 1, "Comment on LEGION-12."))
	q := r.only()
	q.State, q.UpdatedAt = store.QuestionGenerating, testNow
	if err := r.st.UpdateQuestion(r.ctx, q, store.QuestionReceived); err != nil {
		t.Fatal(err)
	}
	for _, call := range []store.AnswerToolCall{
		{Seq: 1, Tool: store.ToolDispatch, Args: []string{"comment", "--issue", "LEGION-12", "--body", "Plum." + dispatchtool.Mark(q.ID, 1)}},
		{Seq: 2, Tool: store.ToolGH, Args: []string{"pr", "comment", "7", "--body", "Done."}, App: "review", Owner: "o"},
	} {
		call.Question, call.StartedAt = q.ID, testNow
		if err := r.st.StartAnswerToolCall(r.ctx, call); err != nil {
			t.Fatal(err)
		}
	}
	r.tool.found[2] = ghtool.Found{Exists: true, URL: "https://github.com/o/r/pull/7#issuecomment-5", ID: 5}
	r.restart()
	r.drain()
	if r.answerCount() != 1 {
		t.Fatalf("answers = %d; want the question answered again once", r.answerCount())
	}
	earlier := r.requests[0].Earlier
	if len(earlier) != 2 || earlier[0].Tool != store.ToolDispatch || earlier[0].Result == nil || *earlier[0].Result != dispatchtool.UnknownOutcome ||
		earlier[1].Result == nil || !strings.Contains(*earlier[1].Result, "issuecomment-5") {
		t.Fatalf("earlier calls = %+v; want the dispatch call of unknown outcome and the gh comment found on GitHub", earlier)
	}
	if call, _, _ := r.st.AnswerToolCall(r.ctx, q.ID, 1); call.Result == nil || *call.Result != dispatchtool.UnknownOutcome || call.Exit != nil {
		t.Fatalf("call 1 = %+v; want its unknown outcome recorded as its result, no exit", call)
	}
	if looked := r.tool.lookedUp(); !slices.Equal(looked, []int{2}) {
		t.Fatalf("the gh tool looked up calls %v; want the gh call alone", looked)
	}
}

// An answer that passed a bound after it ran writes is the daemon's reply naming each, the
// dispatch writes beside the gh ones: every dispatch call that ran a command but a read, and no
// read and no call the tool refused.
func TestAFaultedAnswerNamesItsDispatchWrites(t *testing.T) {
	r := newRig(t)
	r.conversation("ses_tester", testerToken, 0)
	r.answer = func(req answer.Request) (answer.Reply, error) {
		zero, one := 0, 1
		refused := "refused: `dispatch claim` is none of the commands this tool runs"
		for _, call := range []store.AnswerToolCall{
			{Seq: 1, Tool: store.ToolGH, Args: []string{"pr", "merge", "7", "-R", "acme/widgets", "--squash"}, App: "implement", Owner: "acme", Exit: &zero},
			{Seq: 2, Tool: store.ToolDispatch, Args: []string{"read", "LEGION-12"}, Exit: &zero},
			{Seq: 3, Tool: store.ToolDispatch, Args: []string{"comment", "--issue", "LEGION-12", "--body", "Merged."}, Exit: &zero},
			{Seq: 4, Tool: store.ToolDispatch, Args: []string{"claim", "--issue", "LEGION-12"}},
			{Seq: 5, Tool: store.ToolDispatch, Args: []string{"message", "--issue", "LEGION-12", "--body", "x"}, Exit: &one},
		} {
			call.Question, call.StartedAt = req.Question.ID, testNow
			if err := r.st.StartAnswerToolCall(r.ctx, call); err != nil {
				return answer.Reply{}, err
			}
			result := "done"
			if call.Seq == 4 {
				result = refused
			}
			if err := r.st.FinishAnswerToolCall(r.ctx, req.Question.ID, call.Seq, result, call.Exit, testNow); err != nil {
				return answer.Reply{}, err
			}
		}
		return answer.Reply{Kind: answer.NotAnswered, Fault: answer.ModelCalls, CostUSD: 0.25}, nil
	}
	r.receive(messageHandoff("ses_tester", "m-1", 1, "Merge PR 7 and say so on LEGION-12?"))
	r.drain()
	const reply = "I could not answer within the bound of 8 model calls. I had already run these gh writes: `gh pr merge` on acme/widgets (exit 0). " +
		"I had already run these Dispatch writes: `dispatch comment` (exit 0); `dispatch message` (exit 1). I passed your question to the Legion controller."
	if posts := r.dispatch.posts(); len(posts) != 1 || !strings.HasPrefix(posts[0].Body["body"].(string), reply+"\n\n"+AnswerMarker) {
		t.Fatalf("posts = %s; want one reply naming the merge, the comment and the message: %q", describe(posts), reply)
	}
}

// A question forwarded to the running agent names each call the loop already made with its tool,
// in its text and its payload, so a dispatch write is never read as a gh one.
func TestAForwardedQuestionNamesEachCallsTool(t *testing.T) {
	r := newRig(t)
	r.conversation("ses_tester", testerToken, 0)
	r.answer = func(req answer.Request) (answer.Reply, error) {
		zero := 0
		if err := r.st.StartAnswerToolCall(r.ctx, store.AnswerToolCall{Question: req.Question.ID, Seq: 1, Tool: store.ToolDispatch,
			Args: []string{"comment", "--issue", "LEGION-12", "--body", "Done."}, StartedAt: testNow}); err != nil {
			t.Error(err)
		}
		if err := r.st.FinishAnswerToolCall(r.ctx, req.Question.ID, 1, "comment c-9 posted", &zero, testNow); err != nil {
			t.Error(err)
		}
		r.gate.set(r.gate.started, "ses_tester", true)
		return answered(req, "Commented."), nil
	}
	r.receive(messageHandoff("ses_tester", "m-1", 1, "Please comment."))
	r.drain()
	publishes := r.listener.posts()
	if len(publishes) != 1 {
		t.Fatalf("listener posts = %s; want the forward", describe(publishes))
	}
	var payload forwardPayload
	if err := json.Unmarshal([]byte(publishes[0].Body["payload"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Calls) != 1 || payload.Calls[0].Tool != store.ToolDispatch ||
		!strings.Contains(payload.Text, "\n1. dispatch [\"comment\",\"--issue\",\"LEGION-12\",\"--body\",\"Done.\"]\ncomment c-9 posted\n") {
		t.Fatalf("forward payload = %+v; want the dispatch call named as dispatch's", payload)
	}
}
