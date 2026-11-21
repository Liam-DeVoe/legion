package natscallout

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/natsgrant"
)

// scopeGrant is grant, entry's kind's grant (serviceGrant), held to the projects entry may reach.
//
// An all-projects entry takes that grant whole.
//
// Any other entry is held to the projects the broker's own exchange list gives its account
// (Deps.Exchange), the one list that also decides the projects of a token minted for that
// account, and to what the token exchange holds that token to: the person its exchange entry acts
// for must see each project, as projectaccess.Visible requires before a mint. So a connection and
// that account's token cannot disagree. WithinProjects writes the account's own reply prefix
// (natsgrant.InboxPrefix) in place of _INBOX.>, and keeps the GitHub events whole, since a GitHub
// event is a message of no project, so a held daemon hears every repository's events.
// Each project's answer is the one Dispatch last gave about it (heldAccess), held on its own. It
// is refused, naming why: an account no exchange entry lists, or whose entry names no project;
// Dispatch refusing the broker's own account token in the newest refresh; a project Dispatch has
// not yet answered about; a project Dispatch does not hold, or whose answer says the entry's
// person cannot see it; and a grant entry natsgrant.WithinProjects cannot hold. ParseServices
// refuses a dispatch or listener entry with "-" at start, since their grants carry KV calls.
//
// Dispatch is never asked inside a decision: a decision reads only the answers the service holds.
//
// A daemon entry held this way is narrowed to natsgrant.WithinProjects(natsgrant.Daemon(), scope),
// whose consumers are the ones a Legion daemon opens once it opens its consumers per project. So
// no deployment may list a daemon entry with "-" until the release gate for that daemon opens:
// both `packages/envoy/scripts/daemon-release-gate.sh --listeners <the commit that lands it>` and
// `daemon-release-gate.sh <that commit>` print GATE OPEN, each read once, and the stored wakes a
// legion-go-<project>-ready consumer created then would deliver hold nothing but listener_ready.
// The change that lists daemon entries with "-" waits on that gate, with those readings quoted in
// its pull request.
func scopeGrant(deps Deps, entry ServiceEntry, grant natsgrant.Permissions) (natsgrant.Permissions, error) {
	if entry.AllProjects {
		return grant, nil
	}
	if deps.Exchange == nil || deps.Access == nil {
		return natsgrant.Permissions{}, refuse(labelHeldToProjects, "held to exchange projects, which this broker cannot read: service "+entry.name())
	}
	person, projects, listed := deps.Exchange(entry.Account)
	switch {
	case !listed:
		return natsgrant.Permissions{}, refuse(labelNoExchangeEntry, "account has no exchange entry: "+entry.Account)
	case len(projects) == 0:
		return natsgrant.Permissions{}, refuse(labelNoProjects, "account's exchange entry names no project to hold it to: "+entry.Account)
	}
	if deps.held.refused() {
		// The token exchange refuses every trade that asks Dispatch while it refuses the broker's
		// token (oauth's refusedByDispatch), so every held connection is refused too, whichever
		// question of the refresh Dispatch refused.
		return natsgrant.Permissions{}, refuse(labelAccessRefused, "Dispatch refuses the broker's own account token (BROKER_PROJECT_ACCESS_TOKEN_FILE), so what the account's person may see is not known: service "+entry.name())
	}
	for _, project := range projects {
		held, known := deps.held.answer(heldKey{project: project, person: person})
		switch {
		case !known:
			// No refresh has answered about this project yet (the first is still running, or
			// Dispatch was down from the start); the client retries.
			return natsgrant.Permissions{}, refuse(labelProjectAccessUnknown, "project access not yet known: "+project)
		case held.notFound:
			return natsgrant.Permissions{}, refuse(labelProjectNotFound, "project not found: "+project)
		case !held.visible:
			// The token exchange refuses this entry's token the same way (projectaccess.Visible):
			// its person cannot see the project, so neither may the connection.
			return natsgrant.Permissions{}, refuse(labelProjectNotVisible, "project not visible to the account's person: "+project)
		}
	}
	scoped, err := natsgrant.WithinProjects(grant, natsgrant.Scope{Projects: projects, Identity: entry.Account})
	if err != nil {
		return natsgrant.Permissions{}, refuse(labelGrantNotHoldable, "grant cannot be held to its projects: service "+entry.name()+": "+err.Error())
	}
	return scoped, nil
}

// accessRefresh is how often the service reads Dispatch again, and accessFetchTimeout how long one
// question may take. accessRefresh is a var so a test can shorten it.
var accessRefresh = 20 * time.Second

const accessFetchTimeout = 10 * time.Second

// heldAccess is what the service holds of Dispatch for the service entries held to their
// exchange projects, so that no decision waits on Dispatch: the decision budget is 1.5 s
// (decideBudget), and a Dispatch call may take ten. A refresh asks, in parallel, Dispatch's answer
// about every project such an entry can name, for the person that entry's exchange entry acts
// for; run makes one at the start and one every accessRefresh after.
//
// Each project's answer is held on its own: an answer replaces the one held about its question,
// whatever the refresh's other questions did, and a question that fails (Dispatch unreachable, a
// timeout, an answer past its bound) keeps the answer held about it, logged at warn. A held answer
// is used however old it is. A question no refresh has answered yet has no answer, and a held
// connection that needs it is refused until one does. The one exception joins a whole refresh:
// Dispatch refusing the broker's own account token (a 401 or 403, projectaccess.ErrBrokerRefused)
// on any question of a refresh refuses every held connection until a later refresh is answered
// with no such refusal, since the token exchange refuses every trade that asks Dispatch then; it
// is logged at error once a refresh. A refresh no question of which was answered (Dispatch
// unreachable) shows nothing about the broker's token, so it keeps such a refusal. While Dispatch
// answers, an answer held is at most about a minute old: the 30 s (projectaccess.CacheTTL) the
// Dispatch client may have kept it, the accessRefresh to the next refresh, and the
// accessFetchTimeout its question may take.
type heldAccess struct {
	access func(ctx context.Context, project, person string) (projectaccess.Answer, error)
	asks   []heldKey
	now    func() time.Time
	log    *slog.Logger

	mu      sync.RWMutex
	answers map[heldKey]heldAnswer
	// brokerRefused is whether the newest refresh that decided it found Dispatch refusing the
	// broker's own account token, and decided when that refresh began. A refresh decides it when
	// Dispatch refused the token on one of its questions or answered one of them.
	brokerRefused bool
	decided       time.Time
}

// heldKey is one question the service asks Dispatch: whether person may see project.
type heldKey struct{ project, person string }

// heldAnswer is one question's answer: that Dispatch holds no such project, or whether the
// person may see it; and when the refresh that read it began.
type heldAnswer struct {
	notFound, visible bool
	began             time.Time
}

// newHeldAccess holds the answers about every project deps.Exchange gives an entry of services
// that is not an all-projects one, each asked for the person its exchange entry acts for; nil when
// the broker cannot read them (no exchange list, or no Dispatch client) or no entry can name a
// project.
func newHeldAccess(deps Deps, services []ServiceEntry) *heldAccess {
	if deps.Exchange == nil || deps.Access == nil {
		return nil
	}
	var asks []heldKey
	for _, entry := range services {
		if entry.AllProjects {
			continue
		}
		person, projects, _ := deps.Exchange(entry.Account)
		for _, project := range projects {
			asks = append(asks, heldKey{project: project, person: person})
		}
	}
	slices.SortFunc(asks, func(a, b heldKey) int {
		return cmp.Or(cmp.Compare(a.project, b.project), cmp.Compare(a.person, b.person))
	})
	asks = slices.Compact(asks)
	if len(asks) == 0 {
		return nil
	}
	log := deps.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &heldAccess{access: deps.Access, asks: asks, now: deps.now, log: log, answers: make(map[heldKey]heldAnswer, len(asks))}
}

// run makes a refresh now and every accessRefresh after, until ctx ends. On a nil heldAccess (no
// entry held to projects) it returns at once.
func (h *heldAccess) run(ctx context.Context) {
	if h == nil {
		return
	}
	ticker := time.NewTicker(accessRefresh)
	defer ticker.Stop()
	for {
		h.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// refresh asks Dispatch every held question, all at once, each bounded by accessFetchTimeout, and
// holds each answer it gets in place of the one held about its question, unless that one was read
// by a refresh that began later. It decides whether Dispatch refuses the broker's own account
// token when it got a refusal or an answer, unless a later refresh has decided that already. It
// holds all of that before it writes any log line, since a write can block (report.go). It logs a
// refusal at error, naming the project whose question Dispatch refused, since every held
// connection is then refused; else each failed question at warn, since the answer held about it
// is kept.
func (h *heldAccess) refresh(ctx context.Context) {
	began := h.now()
	answers := make([]heldAnswer, len(h.asks))
	// errs is each question's failure, nil for one answered.
	errs := make([]error, len(h.asks))
	var wait sync.WaitGroup
	for i, ask := range h.asks {
		wait.Go(func() {
			fetch, cancel := context.WithTimeout(ctx, accessFetchTimeout)
			defer cancel()
			answer, err := h.access(fetch, ask.project, ask.person)
			switch {
			case errors.Is(err, projectaccess.ErrProjectNotFound):
				answers[i] = heldAnswer{notFound: true, began: began}
			case err != nil:
				errs[i] = err
			default:
				answers[i] = heldAnswer{visible: answer.Visible, began: began}
			}
		})
	}
	wait.Wait()
	refused := slices.IndexFunc(errs, func(err error) bool { return errors.Is(err, projectaccess.ErrBrokerRefused) })
	answered := slices.Contains(errs, nil)
	h.mu.Lock()
	for i, ask := range h.asks {
		if errs[i] != nil {
			continue
		}
		if held, ok := h.answers[ask]; !ok || !began.Before(held.began) {
			h.answers[ask] = answers[i]
		}
	}
	if (refused >= 0 || answered) && !began.Before(h.decided) {
		h.brokerRefused, h.decided = refused >= 0, began
	}
	h.mu.Unlock()
	if refused >= 0 {
		h.log.Error("natscallout: Dispatch refuses the broker's own account token (BROKER_PROJECT_ACCESS_TOKEN_FILE), so every daemon held to its exchange projects is refused until a later refresh is answered with no such refusal", "question", "project "+h.asks[refused].project, "error", errs[refused].Error())
		return
	}
	if ctx.Err() != nil {
		return
	}
	for i, err := range errs {
		if err != nil {
			h.log.Warn("natscallout: Dispatch could not be asked about a project, so the answer held about it is kept", "question", "project "+h.asks[i].project, "error", err.Error())
		}
	}
}

// refused reports whether the newest refresh that decided it found Dispatch refusing the broker's
// own account token: false on a nil heldAccess.
func (h *heldAccess) refused() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.brokerRefused
}

// answer is the answer held about key, and false when no refresh has answered it yet: on a nil
// heldAccess too.
func (h *heldAccess) answer(key heldKey) (heldAnswer, bool) {
	if h == nil {
		return heldAnswer{}, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	held, ok := h.answers[key]
	return held, ok
}
