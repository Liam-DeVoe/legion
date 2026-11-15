package reviewer

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/internal/dispatch/modelpace"
)

// Bounds of one model call where the caller's Model sets none.
const (
	// DefaultTimeout bounds one call, its wait for the pacer included: the default of
	// DISPATCH_REVIEWER_TIMEOUT.
	DefaultTimeout = 120 * time.Second
	// DefaultOutputTokens bounds one reply: a review's findings and a rewrite of a text of
	// about 25,000 characters. A caller that rewrites longer texts sets Model.OutputTokens.
	DefaultOutputTokens = 8192
	// charsPerToken is how many characters a token is taken to hold when a call is charged to
	// the pacer before it is made.
	charsPerToken = 4
)

// The purposes of a model call, as Call.Purpose names them.
const (
	PurposeReview   = "review"
	PurposeDetector = "detector"
	PurposeRewrite  = "rewrite"
	PurposeDefine   = "define"
)

// Model is the model the component calls and how. The caller chooses it: the server builds it
// from a model group's settings (DISPATCH_REVIEWER_MODEL_* or DISPATCH_REWRITE_DEFINE_MODEL_*,
// pointed at the stand-in under DISPATCH_TEST_MODEL_URL), and the harness from any candidate.
type Model struct {
	// Model is the model and its endpoint.
	Model agentloop.Model
	// Pace is the caller's share of the model's deployment on the process's one pacer
	// (modelpace); nil paces nothing.
	Pace *modelpace.Caller
	// Timeout bounds one call, its wait for the pacer included; zero is DefaultTimeout.
	Timeout time.Duration
	// OutputTokens bounds one reply; zero is DefaultOutputTokens.
	OutputTokens int
	// Temperature is the sampling temperature of every call; nil leaves the provider's default.
	Temperature *float64
}

// deployment is the deployment the model's requests name: an Azure deployment where the endpoint
// names one apart from the model, else the model id, which is a Foundry chat deployment's name.
func (m Model) deployment() string {
	return cmp.Or(m.Model.Endpoint.Deployment, m.Model.ID)
}

// Call is one model call the component made: what it was for, the deployment its request named,
// the model id the provider says served it ("" where no reply came), what it used and cost, and
// the fault it ended on, if any.
type Call struct {
	Purpose    string          `json:"purpose"`
	Deployment string          `json:"deployment"`
	Served     string          `json:"served"`
	Usage      agentloop.Usage `json:"usage"`
	Fault      agentloop.Fault `json:"fault,omitempty"`
}

// ModelError is a model call that gave no usable answer: the pacer could not serve it in time,
// the run could not be made, it ended on a fault (a provider error, the time bound, a refusal, a
// reply cut at its length), or its reply was not the object its schema names. The server posts a
// held text as written on any of them and counts it (R5).
type ModelError struct {
	// Purpose is the call's purpose (Call.Purpose).
	Purpose string
	// Fault is the run's fault, and FaultMalformed for a reply this package could not read;
	// empty where Err ended the call.
	Fault agentloop.Fault
	// Detail says what happened, for a log.
	Detail string
	// Err is the error that ended the call before an answer: the pacer's, the run's or the
	// context's.
	Err error
}

func (e *ModelError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("reviewer: the %s call failed: %v", e.Purpose, e.Err)
	}
	return fmt.Sprintf("reviewer: the %s call ended on %s: %s", e.Purpose, e.Fault, cmp.Or(e.Detail, "no detail"))
}

func (e *ModelError) Unwrap() error { return e.Err }

// call makes one model call for purpose: system as the system prompt, input as the one user
// message, and a reply that must be one object of schema, which it decodes into out. It waits for
// the pacer first, charged with the prompt's estimated tokens and the whole reply's bound. Every
// failure is a *ModelError, and the Call is filled as far as the call went.
func (m Model) call(ctx context.Context, purpose, system, input string, schema json.RawMessage, out any) (Call, error) {
	call := Call{Purpose: purpose, Deployment: m.deployment()}
	timeout := cmp.Or(m.Timeout, DefaultTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output := cmp.Or(m.OutputTokens, DefaultOutputTokens)
	if m.Pace != nil {
		tokens := (utf8.RuneCountInString(system)+utf8.RuneCountInString(input))/charsPerToken + output
		if err := m.Pace.Wait(ctx, tokens); err != nil {
			return call, &ModelError{Purpose: purpose, Err: fmt.Errorf("pace the call: %w", err)}
		}
	}
	// The run's own bound is the time the call has left after the pacer's wait.
	left := timeout
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	if left <= 0 {
		return call, &ModelError{Purpose: purpose, Err: fmt.Errorf("pace the call: %w", context.DeadlineExceeded)}
	}
	result, err := agentloop.Run(ctx, agentloop.Request{
		Model:       m.Model,
		System:      system,
		Input:       []agentloop.Message{{Role: agentloop.User, Blocks: []agentloop.Block{{Kind: agentloop.TextBlock, Text: input}}}},
		NoCache:     true,
		Bounds:      agentloop.Bounds{Time: left, Calls: 1, OutputTokens: output},
		Final:       schema,
		Temperature: m.Temperature,
	})
	call.Usage, call.Fault = result.Usage, result.Fault
	if n := len(result.Steps); n > 0 {
		call.Served = result.Steps[n-1].Model
	}
	switch {
	case err != nil:
		return call, &ModelError{Purpose: purpose, Err: err}
	case result.Fault != "":
		return call, &ModelError{Purpose: purpose, Fault: result.Fault, Detail: result.Detail}
	}
	if err := json.Unmarshal(result.Value, out); err != nil {
		call.Fault = agentloop.FaultMalformed
		return call, &ModelError{Purpose: purpose, Fault: agentloop.FaultMalformed, Detail: err.Error()}
	}
	return call, nil
}

// malformed is a *ModelError for a reply that fits its schema and still cannot be used, and marks
// the call with it.
func malformed(call *Call, detail string) error {
	call.Fault = agentloop.FaultMalformed
	return &ModelError{Purpose: call.Purpose, Fault: agentloop.FaultMalformed, Detail: detail}
}
