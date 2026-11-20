package api

// going_decision.go holds what a run of the command bar decides before it answers and offers
// once it has: the actions a caller may be offered, the one decision call and the decision frame
// it becomes, with the line on top it gives, and the proposal frame the answer's arguments
// become. The run itself, which calls these in order, is startGoingRun's.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/sjawhar/envoy/going"
	"github.com/sjawhar/envoy/internal/ciphertoken"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The line on top of the bar's list (R2), as the action the decision model Jev chose gives it:
// Ask for no action, File as a new issue for the create of an issue, and Do for any other action
// of the catalogue (goingLineOnTopOf).
const (
	goingLineAsk  = "ask"
	goingLineDo   = "do"
	goingLineFile = "file"
)

// goingLineOnTopOf is the line on top action gives.
func goingLineOnTopOf(action string) string {
	switch action {
	case going.ActionNone:
		return goingLineAsk
	case goingIssueCreateTool:
		return goingLineFile
	}
	return goingLineDo
}

// goingItemFields are the catalogue arguments that name an item the search finds, by the kind
// the search lists it under: the bar prompts for one of these as an item it searches for rather
// than as text.
var goingItemFields = map[string]string{
	"issue":        "issue",
	"parent":       "issue",
	"ask":          "ask",
	"reply_to_ask": "ask",
	"comment":      "comment",
	"reply_to":     "comment",
	"artifact":     "document",
	"in_reply_to":  "message",
}

// The kinds of argument the bar can prompt for (R3): a text, a number, one of an enumeration's
// values, or an item the search finds.
const (
	goingPromptText   = "text"
	goingPromptNumber = "number"
	goingPromptEnum   = "enum"
	goingPromptItem   = "item"
)

// goingCredential is what decides which rows of the catalogue a caller may be offered: whether
// the credential is a browser's cookie, which a bearer-only row refuses, the scopes it holds,
// the session it proved (session), which fills a row addressing the caller's own session, and
// whether it is presented with a proof bound to each request (a broker-issued access token under
// DPoP), which is no credential the command bar acts with: a run acts with a person's own
// credential (going_grant.go), and a proof is single use and bound to one URL.
type goingCredential struct {
	cookie     bool
	credential *model.Credential
	proofBound bool
}

// goingCredentialOf is the asking request's credential as goingCredential reads it.
func goingCredentialOf(r *http.Request, actor model.Actor, cookie bool) goingCredential {
	_, proofBound := ciphertoken.Presented(r)
	return goingCredential{cookie: cookie, credential: actor.Credential, proofBound: proofBound}
}

// session is the session the credential proved, "" for none or no credential.
func (c goingCredential) session() string {
	if c.credential == nil {
		return ""
	}
	return c.credential.Session
}

// runnableBy reports whether the route table lets this caller call the row: a bearer-only row
// takes no cookie, and every scope the row needs is one the credential holds. It reads the table's
// own columns, so a row the table opens or closes later is offered or not without a name here.
func (route GoingRoute) runnableBy(caller goingCredential) bool {
	if route.auth == authBearer && caller.cookie {
		return false
	}
	for _, scope := range route.scopes {
		if !caller.credential.Holds(scope) {
			return false
		}
	}
	return true
}

// runnableBy is the catalogue as one caller may run it: each action keeps the rows
// the caller may call, and an action left with none is not offered at all, so a person is never
// offered a handoff or a reply to a delivery, which only a session's bearer may write. A caller
// whose token needs a proof per request is offered no action at all: the bar acts as a person's
// own credential, and a broker-issued token is not one.
func (c GoingCatalogue) runnableBy(caller goingCredential) []GoingAction {
	runnable := make([]GoingAction, 0, len(c.actions))
	if caller.proofBound {
		return runnable
	}
	for _, action := range c.actions {
		routes := make([]GoingRoute, 0, len(action.Routes))
		for _, route := range action.Routes {
			if route.runnableBy(caller) {
				routes = append(routes, route)
			}
		}
		if len(routes) == 0 {
			continue
		}
		action.Routes = routes
		runnable = append(runnable, action)
	}
	return runnable
}

// goingRunStart is what a pause's answer is started with beyond its request row: the person's
// typed words, the context chip, the actions its caller may be offered, the session its caller's
// credential proved, the read tools its caller may call (goingReadsFor), and the server's own URL,
// against which the words' dashboard addresses are read (goingNamedBy).
type goingRunStart struct {
	words     string
	chip      *goingContext
	actions   []GoingAction
	session   string
	reads     []string
	serverURL string
}

// action is the runnable action of that name, and whether the caller may be offered one.
func (start goingRunStart) action(name string) (GoingAction, bool) {
	for _, action := range start.actions {
		if action.Action == name {
			return action, true
		}
	}
	return GoingAction{}, false
}

// goingDecision is the decision frame's data (contracts GoingDecisionEvent), which the run's
// record keeps as it was sent: the action the decision model chose, going.ActionNone among the
// choices, the line on top that action gives (goingLineOnTopOf), and what the words are about.
type goingDecision struct {
	Action  string `json:"action"`
	Line    string `json:"line" enum:"ask,do,file"`
	Context string `json:"context" enum:"page,named,none"`
	// offered are the actions the answer is told to write arguments for: the action chosen, or
	// none where the decision chose none.
	offered []string
}

// goingDecide makes the run's one decision call, the action asked among the actions the caller
// may run, and answers the decision it came to. It answers false - no decision, and so no
// decision frame and no proposal - with no decider configured, and for a call that could not be
// made or answered nothing usable: the answer runs as it would with none, and the bar's line on
// top reads Ask.
func (s *server) goingDecide(ctx context.Context, words string, start goingRunStart) (goingDecision, bool) {
	decider := s.deps.Decider
	if decider == nil {
		return goingDecision{}, false
	}
	known := make(map[string]bool, len(start.actions)+1)
	for _, action := range start.actions {
		known[action.Action] = true
	}
	known[going.ActionNone] = true
	var page going.PageContext
	if start.chip != nil {
		page = going.PageContext{Path: start.chip.Path, Project: start.chip.Project, Issue: start.chip.Issue, Block: start.chip.Block}
	}
	result, err := decider.Decide(ctx, going.DecisionQuestions(GoingDecisionActions(start.actions)), going.DecisionState(words, page))
	switch {
	case err != nil:
		slog.Warn("dispatch going: the decision call could not be made, so the run answers with no decision", "error", err)
		return goingDecision{}, false
	case result.Fault != "":
		slog.Warn("dispatch going: the decision call gave no answer, so the run answers with no decision",
			"fault", result.Fault, "detail", result.Detail)
		return goingDecision{}, false
	}
	action, answered := result.Answers[going.QuestionAction]
	if !answered || !known[action.Choice] {
		slog.Warn("dispatch going: the decision named no action it was offered, so the run answers with no decision")
		return goingDecision{}, false
	}
	decision := goingDecision{Action: action.Choice, Line: goingLineOnTopOf(action.Choice), Context: going.ContextNone}
	if about, answered := result.Answers[going.QuestionContext]; answered &&
		slices.Contains([]string{going.ContextPage, going.ContextNamed, going.ContextNone}, about.Choice) {
		decision.Context = about.Choice
	}
	if action.Choice != going.ActionNone {
		decision.offered = []string{action.Choice}
	}
	return decision, true
}

// goingNamedHit is the fast tier of R2: words that are an issue's key, or the very key, title or
// name of a hit the keyword search finds for them in project, whatever the case, are answered by
// that hit, and no model is asked of them. It answers whether the words are such words, with the
// hit they name where the search found one: words of a key's shape are a key whether or not an
// issue holds it. The search is the keyword statement alone (searchParameters.keywordsOnly), so
// it asks no embedding of the words either.
func (s *server) goingNamedHit(ctx context.Context, words, project string) (*going.Hit, bool, error) {
	key := issueKeyPattern.MatchString(strings.ToUpper(words))
	found, err := s.runSearch(ctx, searchParameters{text: words, project: project, limit: searchDefaultLimit, keywordsOnly: true})
	if err != nil {
		return nil, key, err
	}
	for _, result := range found.Results {
		if goingNamesHit(result, words) {
			return &going.Hit{Kind: result.Kind, ID: result.ID, Title: goingHitTitle(result), Href: result.Href}, true, nil
		}
	}
	return nil, key, nil
}

// goingNamesHit is whether words are result's own name, whatever the case: its issue's key or
// title, or its document's name, as the bar reads its hits.
func goingNamesHit(result model.SearchResult, words string) bool {
	owner := result.Owner
	if owner.Kind == "issue" {
		return strings.EqualFold(owner.Key, words) || strings.EqualFold(owner.Title, words)
	}
	return strings.EqualFold(owner.Name, words)
}

// GoingDecisionActions is actions as the decision model chooses among them: each action's name
// and the catalogue's own description of it, in the catalogue's order. The server's decision call
// and the bake-off (cmd/going-bakeoff) both put their action question through it, so the
// bake-off measures what the server asks.
func GoingDecisionActions(actions []GoingAction) []going.Action {
	choices := make([]going.Action, 0, len(actions))
	for _, action := range actions {
		choices = append(choices, going.Action{Name: action.Action, Description: action.Description})
	}
	return choices
}

// offer is what the answer call is told of this decision: each offered action with its own
// argument schema, the catalogue's.
func (d goingDecision) offer(start goingRunStart) going.Offer {
	var offer going.Offer
	for _, name := range d.offered {
		action, runnable := start.action(name)
		if !runnable {
			continue
		}
		// A slice of ToolField always marshals.
		schema, _ := json.Marshal(action.Request)
		offer.Actions = append(offer.Actions, going.OfferedAction{Name: name, Schema: schema})
	}
	return offer
}

// goingProposal is the proposal frame's data (contracts GoingProposalEvent), which the run's
// record keeps as it was sent.
type goingProposal struct {
	Offers []goingOffer `json:"offers"`
}

// goingOffer is one offered action: one invocation for each item it changes, and Offered false
// where none survived the checks. Withheld says why each invocation the answer wrote that a person
// would otherwise have been offered was not: one the bar can never send as written
// (goingUnsendable), or one whose pick would do what no control of the bar says
// (goingUploadWithheld).
type goingOffer struct {
	Action      string            `json:"action"`
	Invocations []goingInvocation `json:"invocations"`
	Offered     bool              `json:"offered"`
	Withheld    []string          `json:"withheld,omitempty"`
}

// goingInvocation is one run of an action: the arguments the answer and the page filled, each
// checked against the action's own schema, the one argument the bar still has to ask for,
// whether it may run at once, on submit, rather than only when the person picks it
// (goingAutomatic), and the argument naming the item it acts on, as the row it reaches binds
// that item into its path (goingTargetArgument).
type goingInvocation struct {
	Arguments      map[string]any        `json:"arguments"`
	Missing        *goingMissingArgument `json:"missing"`
	Automatic      bool                  `json:"automatic"`
	TargetArgument string                `json:"target_argument"`
}

// goingMissingArgument is the one argument an invocation still needs, of a kind the bar prompts
// for: a text, a number, one of Choices, or an item of the search kind Item.
type goingMissingArgument struct {
	Field   string   `json:"field"`
	Kind    string   `json:"kind" enum:"text,number,enum,item"`
	Choices []string `json:"choices,omitempty"`
	Item    string   `json:"item,omitempty"`
}

// goingPropose is the proposal of a decision once the answer has written: for each action the
// decision offered, the answer's arguments for it, each element held to that
// action's own schema, a target the words left out filled from the chip where the decision read
// the words as about the page, and every invocation that still needs more than one argument, or
// one the bar cannot prompt for, dropped. An action the answer wrote arguments for that the
// decision did not offer - one outside the catalogue among them - is no part of the proposal and
// so never runs. With no structured reply (final nil), or no arguments for an
// action, the action is offered on what the page and one prompted argument fill. Each invocation
// is marked automatic by what the words name and what holds reads of the store now
// (goingAutomatic); a holds that reads nothing leaves a hand-over and an upload's name to run at
// once only where the words name it. An invocation the bar can never send as written - a picture
// file, a dispatch:// reference placing it, and the rest goingUnsendable names - is withheld and
// the offer says why, in the words a pick of it would be refused with. An upload under the name of
// a document its owner holds, as holds reads it, is withheld and the offer says why, unless it
// carries force, the answer wrote the name that document is stored under - never its slug - and
// the person's own words write that name (goingUploadWithheld).
func goingPropose(decision goingDecision, final *going.Final, start goingRunStart, holds goingHolds) goingProposal {
	written := map[string]json.RawMessage{}
	if final != nil && final.Arguments != "" {
		if err := json.Unmarshal([]byte(final.Arguments), &written); err != nil {
			// Text that is no object of lists offers nothing written: each action is offered
			// on what the page fills, as with no structured reply.
			written = map[string]json.RawMessage{}
		}
	}
	var chip *goingContext
	if decision.Context == going.ContextPage {
		chip = start.chip
	}
	named := goingNamedBy(start.words, start.serverURL)
	proposal := goingProposal{Offers: []goingOffer{}}
	for _, name := range decision.offered {
		action, runnable := start.action(name)
		if !runnable {
			continue
		}
		var elements []json.RawMessage
		if raw, wrote := written[name]; wrote {
			if err := json.Unmarshal(raw, &elements); err != nil {
				elements = nil
			}
		}
		calls := make([]map[string]any, 0, max(len(elements), 1))
		for _, element := range elements {
			var arguments map[string]any
			if err := json.Unmarshal(element, &arguments); err != nil || arguments == nil {
				continue
			}
			calls = append(calls, arguments)
		}
		if len(elements) == 0 {
			calls = append(calls, map[string]any{})
		}
		offer := goingOffer{Action: name, Invocations: []goingInvocation{}}
		for _, arguments := range calls {
			invocation, kept := goingInvocationOf(action, arguments, chip, start.session)
			if !kept {
				continue
			}
			if why := goingUnsendable(name, invocation.Arguments); why != "" {
				offer.Withheld = append(offer.Withheld, why)
				continue
			}
			if withheld := goingUploadWithheld(name, invocation.Arguments, start.session, start.words, holds.document); withheld != "" {
				offer.Withheld = append(offer.Withheld, withheld)
				continue
			}
			invocation.Automatic = goingAutomatic(name, invocation.Arguments, chip, start.session, named, holds)
			offer.Invocations = append(offer.Invocations, invocation)
		}
		offer.Offered = len(offer.Invocations) > 0
		proposal.Offers = append(proposal.Offers, offer)
	}
	return proposal
}

// goingInvocationOf holds one element of the answer's arguments to its action's schema and fills
// what it can: every argument it names must be one the action takes and hold what that argument
// holds, or the element is dropped; then each argument still missing that the chip names is
// filled from the chip; then what is left missing - a required argument, or what the closest row
// of the action needs in its path - is at most one argument the bar can prompt for, or the
// element is dropped.
func goingInvocationOf(action GoingAction, arguments map[string]any, chip *goingContext, session string) (goingInvocation, bool) {
	fields := make(map[string]contracts.ToolField, len(action.Request))
	for _, field := range action.Request {
		fields[field.Name] = field
	}
	for name, value := range arguments {
		field, takes := fields[name]
		if !takes || checkGoingValue(field, value) != "" {
			return goingInvocation{}, false
		}
	}
	route, missing, routed := goingClosestRoute(action, arguments, session)
	if !routed {
		return goingInvocation{}, false
	}
	if chip != nil {
		for _, name := range missing {
			if value := goingChipValue(chip, name); value != "" {
				arguments[name] = value
			}
		}
		route, missing, routed = goingClosestRoute(action, arguments, session)
		if !routed {
			return goingInvocation{}, false
		}
	}
	invocation := goingInvocation{Arguments: arguments, TargetArgument: goingTargetArgument(route)}
	switch len(missing) {
	case 0:
		if checkGoingArguments(action.Request, arguments) != "" {
			return goingInvocation{}, false
		}
		return invocation, true
	case 1:
		prompt, promptable := goingPromptFor(fields, missing[0])
		if !promptable {
			return goingInvocation{}, false
		}
		invocation.Missing = &prompt
		return invocation, true
	}
	return goingInvocation{}, false
}

// goingClosestRoute is the row of the action a call with these arguments comes closest to
// reaching, and what the call still needs: every required argument it does not name, and what
// that row needs in its path besides, goingCallerSession standing for the caller's own session
// where the credential proved none. It answers false where no row of the action takes the call at
// all, whatever is filled: a guard such as a claim's release picks no row the caller may call.
func goingClosestRoute(action GoingAction, arguments map[string]any, session string) (GoingRoute, []string, bool) {
	var required []string
	for _, field := range action.Request {
		if _, named := arguments[field.Name]; field.Required && !named {
			required = append(required, field.Name)
		}
	}
	var closest GoingRoute
	var closestNeeds []string
	routed := false
	for _, route := range action.Routes {
		if !route.takes(arguments) {
			continue
		}
		needs := slices.Clone(required)
		for _, argument := range route.path {
			if argument == goingCallerSession {
				if session == "" && !slices.Contains(needs, goingCallerSession) {
					needs = append(needs, goingCallerSession)
				}
				continue
			}
			if text, isText := arguments[argument].(string); (!isText || text == "") && !slices.Contains(needs, argument) {
				needs = append(needs, argument)
			}
		}
		if !routed || len(needs) < len(closestNeeds) {
			closest, closestNeeds, routed = route, needs, true
		}
	}
	slices.Sort(closestNeeds)
	return closest, closestNeeds, routed
}

// goingTargetPlaceholders are a row's path placeholders that name the item a call acts on, the
// item's own before the issue or project it sits under: a document edit's slug names the
// document, and its key only the issue or project the document belongs to.
var goingTargetPlaceholders = []string{"id", "session_id", "slug", "key"}

// goingTargetArgument is the argument naming the item a call through route acts on: the one the
// row binds to the first of goingTargetPlaceholders its path holds, and "" for a row whose path
// names none - a write that makes its item, whose line names that item once it is made - or names
// only the caller's own session.
func goingTargetArgument(route GoingRoute) string {
	for _, placeholder := range goingTargetPlaceholders {
		if argument := route.path[placeholder]; argument != "" && argument != goingCallerSession {
			return argument
		}
	}
	return ""
}

// goingChipValue is the value the context chip holds for one argument: the issue the page names
// fills an issue argument, and its project a project argument. Nothing else is read off a page.
func goingChipValue(chip *goingContext, argument string) string {
	switch argument {
	case "issue":
		return chip.Issue
	case "project":
		return chip.Project
	}
	return ""
}

// goingPromptFor is how the bar asks for one missing argument, and false for one it cannot ask
// for: a boolean, a list or an object. A session the caller's own credential did not prove is
// asked for by the name the person knows it by, which the write resolves.
func goingPromptFor(fields map[string]contracts.ToolField, name string) (goingMissingArgument, bool) {
	if name == goingCallerSession {
		return goingMissingArgument{Field: name, Kind: goingPromptText}, true
	}
	field, takes := fields[name]
	if !takes {
		return goingMissingArgument{}, false
	}
	switch field.Kind {
	case contracts.ToolFieldString:
		if kind, item := goingItemFields[name]; item {
			return goingMissingArgument{Field: name, Kind: goingPromptItem, Item: kind}, true
		}
		return goingMissingArgument{Field: name, Kind: goingPromptText}, true
	case contracts.ToolFieldNumber:
		return goingMissingArgument{Field: name, Kind: goingPromptNumber}, true
	case contracts.ToolFieldEnum:
		return goingMissingArgument{Field: name, Kind: goingPromptEnum, Choices: field.Values}, true
	}
	return goingMissingArgument{}, false
}
