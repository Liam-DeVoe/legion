package natscallout

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/natsgrant"
)

// scopeGrant is grant, entry's kind's grant, held to the projects entry may reach.
//
// Dispatch's grant gains the write of the private-projects bucket (bus.PrivateProjectsBucket),
// which Dispatch keeps, and a listener's its read, which a listener holds each session to its
// token's projects by. The static grants a hub renders do not change (natsgrant.Dispatch and
// natsgrant.Listener hold neither), so on a hub with no auth service a listener's read is refused
// and reads as no project being private, which is true there: a private project needs the auth
// service.
//
// An all-projects entry takes that grant whole; a daemon's gains its account's own reply prefix
// (natsgrant.InboxPrefix) beside _INBOX.>, because a Legion daemon presenting its token always
// takes that prefix (the daemon's natsauth.ConnectService), whatever its entry's scope. Any other
// entry is held to the projects the broker's own exchange list gives its account (Deps.Exchange),
// the one list that also decides the projects of a token minted for that account, and to what the
// token exchange holds that token to: the person its exchange entry acts for must see each
// project, as projectaccess.Visible requires before a mint. So a connection and that account's
// token cannot disagree. The grant is held to the repositories Dispatch maps the projects to
// (Deps.Access); WithinProjects writes that same reply prefix in place of _INBOX.>. It is refused,
// naming why: an account no exchange entry lists, or whose entry names no project; a project
// Dispatch holds no answer about yet, refused the broker's own account token when last asked
// about, holds no such project, or whose answer says the entry's person cannot see it; and a grant
// entry natsgrant.WithinProjects cannot hold. ParseServices refuses a dispatch or listener entry
// with "-" at start, since their grants carry KV calls.
//
// Dispatch is never asked inside a decision: the service holds Dispatch's last answer about each
// project an entry can name (heldAccess), and a decision reads only that.
//
// A daemon entry held this way is narrowed to natsgrant.WithinProjects(natsgrant.Daemon(), scope),
// whose consumers are the ones the Legion daemon opens after plan C's t-c-10 (dispatch://INFRA-3884).
// So no deployment may list a daemon entry with "-" until t-c-10's release gate opens: both
// `packages/envoy/scripts/daemon-release-gate.sh --listeners <the commit that landed t-c-10>` and
// `daemon-release-gate.sh <that commit>` print GATE OPEN, and the listener leg's GATE OPEN has been
// read twice, 72 hours apart, because the shared notifications.legion.wake subject keeps other
// projects' wakes for the stream's 72-hour retention after the last listener moves. Plan E's
// t-e-23 (dispatch://INFRA-840) is the one change that lists daemon entries, and its merge waits on
// that gate with both readings quoted in its pull request.
func scopeGrant(_ context.Context, deps Deps, entry ServiceEntry, grant natsgrant.Permissions) (natsgrant.Permissions, error) {
	switch entry.Kind {
	case KindDispatch:
		grant.Publish = append(slices.Clip(grant.Publish), natsgrant.KVWrite(bus.PrivateProjectsBucket)...)
	case KindListener:
		grant.Publish = append(slices.Clip(grant.Publish), natsgrant.KVRead(bus.PrivateProjectsBucket)...)
	}
	if entry.AllProjects {
		if entry.Kind == KindDaemon {
			grant.Subscribe = append(slices.Clip(grant.Subscribe), natsgrant.InboxPrefix(entry.Account)+".>")
		}
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
	var repositories []string
	for _, project := range projects {
		held, known := deps.held.answer(project, person)
		switch {
		case !known:
			return natsgrant.Permissions{}, refuse(labelProjectNotKnown, "project access not yet known: "+project)
		case held.refused:
			// The token exchange refuses every trade that asks Dispatch while it refuses the
			// broker's token (oauth's refusedByDispatch), so the connection is refused too.
			return natsgrant.Permissions{}, refuse(labelAccessRefused, "Dispatch refuses the broker's own account token (BROKER_PROJECT_ACCESS_TOKEN_FILE), so whether the account's person may see the project is not known: "+project)
		case held.notFound:
			return natsgrant.Permissions{}, refuse(labelProjectNotFound, "project not found: "+project)
		case !held.answer.Visible:
			// The token exchange refuses this entry's token the same way (projectaccess.Visible):
			// its person cannot see the project, so neither may the connection.
			return natsgrant.Permissions{}, refuse(labelProjectNotVisible, "project not visible to the account's person: "+project)
		}
		repositories = append(repositories, held.answer.Repositories...)
	}
	scoped, err := natsgrant.WithinProjects(grant, natsgrant.Scope{Projects: projects, Repositories: repositories, Identity: entry.Account})
	if err != nil {
		return natsgrant.Permissions{}, refuse(labelGrantNotHoldable, "grant cannot be held to its projects: service "+entry.name()+": "+err.Error())
	}
	return scoped, nil
}

// accessRefresh is how often the service asks Dispatch again about every project it holds an
// answer for, and accessFetchTimeout how long one ask may take. accessRefresh is a var so a test
// can shorten it.
var accessRefresh = 20 * time.Second

const accessFetchTimeout = 10 * time.Second

// heldAccess is Dispatch's last answer about each project a service entry held to its exchange
// projects can name, asked for the person that entry's exchange entry acts for, kept in memory so
// that no decision waits on Dispatch: the decision budget is 1.5 s (decideBudget), and a Dispatch
// call may take ten. run asks about every such project and person at the start and every
// accessRefresh after, in parallel. A project with no answer held yet (the first ask has not come
// back, or Dispatch was down at the start) is refused as not yet known, and the client retries.
// While Dispatch answers a project's ask, its held answer is at most 60 s old: the 30 s
// (projectaccess.CacheTTL) the Dispatch client may have kept it, the accessRefresh to the next
// ask, and the accessFetchTimeout that ask may take. Dispatch refusing the broker's own account
// token (a 401 or 403, projectaccess.ErrBrokerRefused) is no outage: the token exchange refuses
// every trade that asks Dispatch then (oauth's refusedByDispatch), so that refusal replaces the
// held answer, refuses every connection held to the project until an ask is answered again, and is
// logged at error once a round of asks. Any other ask that fails keeps the last answer, as plan E
// says (an answer is used however old it is), so while Dispatch cannot be reached a project's
// answer admits or refuses as Dispatch last said, and the broker logs each failed ask at warn.
type heldAccess struct {
	access func(ctx context.Context, project, person string) (projectaccess.Answer, error)
	asks   []heldKey
	log    *slog.Logger

	mu      sync.RWMutex
	answers map[heldKey]heldAnswer
}

// heldKey is one question the service asks Dispatch: whether person may see project, and the
// repositories it maps to the project.
type heldKey struct{ project, person string }

// heldAnswer is one question's answer: Dispatch's, that Dispatch holds no such project, or that
// Dispatch refused the broker's own account token when asked.
type heldAnswer struct {
	answer   projectaccess.Answer
	notFound bool
	refused  bool
}

// newHeldAccess holds the answers about every project deps.Exchange gives an entry of services
// that is not an all-projects one, each for the person its exchange entry acts for; nil when the
// broker cannot read them (no exchange list or no Dispatch client) or no entry can name a project.
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
	return &heldAccess{access: deps.Access, asks: asks, log: log, answers: make(map[heldKey]heldAnswer, len(asks))}
}

// run asks every held question now and every accessRefresh after, until ctx ends.
func (h *heldAccess) run(ctx context.Context) {
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

// refresh asks Dispatch every held question at once, each ask bounded by accessFetchTimeout, and
// returns when every ask has come back.
func (h *heldAccess) refresh(ctx context.Context) {
	var wait sync.WaitGroup
	var refused sync.Once
	for _, ask := range h.asks {
		wait.Go(func() {
			fetch, cancel := context.WithTimeout(ctx, accessFetchTimeout)
			defer cancel()
			answer, err := h.access(fetch, ask.project, ask.person)
			switch {
			case errors.Is(err, projectaccess.ErrProjectNotFound):
				h.keep(ask, heldAnswer{notFound: true})
			case errors.Is(err, projectaccess.ErrBrokerRefused):
				h.keep(ask, heldAnswer{refused: true})
				refused.Do(func() {
					h.log.Error("natscallout: Dispatch refuses the broker's own account token (BROKER_PROJECT_ACCESS_TOKEN_FILE), so every connection held to a project it was asked about is refused until it answers", "error", err.Error())
				})
			case err != nil:
				if ctx.Err() == nil {
					h.log.Warn("natscallout: Dispatch's answer about a project could not be read, so its last answer is kept", "project", ask.project, "error", err.Error())
				}
			default:
				h.keep(ask, heldAnswer{answer: answer})
			}
		})
	}
	wait.Wait()
}

func (h *heldAccess) keep(ask heldKey, answer heldAnswer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.answers[ask] = answer
}

// answer is the answer held about project for person, and false when none is: on a nil
// heldAccess too.
func (h *heldAccess) answer(project, person string) (heldAnswer, bool) {
	if h == nil {
		return heldAnswer{}, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	answer, ok := h.answers[heldKey{project: project, person: person}]
	return answer, ok
}
