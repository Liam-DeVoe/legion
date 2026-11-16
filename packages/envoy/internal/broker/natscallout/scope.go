package natscallout

import (
	"context"
	"errors"
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
// the one list that also decides the projects of a token minted for that account, so the two
// cannot disagree, and to the repositories Dispatch maps them to (Deps.Access); WithinProjects
// writes that same reply prefix in place of _INBOX.>. It is refused, naming why: an account no
// exchange entry lists; a project Dispatch holds no answer about yet, or holds no such project;
// and a grant entry natsgrant.WithinProjects cannot hold, which in practice is every dispatch and
// listener entry, whose grants carry KV calls.
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
		return natsgrant.Permissions{}, refuse(credentialService, labelHeldToProjects, "held to exchange projects, which this broker cannot read: service "+entry.name())
	}
	projects, listed := deps.Exchange(entry.Account)
	if !listed {
		return natsgrant.Permissions{}, refuse(credentialService, labelNoExchangeEntry, "account has no exchange entry: "+entry.Account)
	}
	var repositories []string
	for _, project := range projects {
		held, known := deps.held.answer(project)
		switch {
		case !known:
			return natsgrant.Permissions{}, refuse(credentialService, labelProjectNotKnown, "project access not yet known: "+project)
		case held.notFound:
			return natsgrant.Permissions{}, refuse(credentialService, labelProjectNotFound, "project not found: "+project)
		}
		repositories = append(repositories, held.answer.Repositories...)
	}
	scoped, err := natsgrant.WithinProjects(grant, natsgrant.Scope{Projects: projects, Repositories: repositories, Identity: entry.Account})
	if err != nil {
		return natsgrant.Permissions{}, refuse(credentialService, labelNotHeldToProjects, "grant cannot be held to its projects: service "+entry.name()+": "+err.Error())
	}
	return scoped, nil
}

// accessRefresh is how often the service asks Dispatch again about every project it holds an
// answer for, and accessFetchTimeout how long one ask may take. accessRefresh is a var so a test
// can shorten it.
var accessRefresh = 20 * time.Second

const accessFetchTimeout = 10 * time.Second

// heldAccess is Dispatch's last answer about each project a service entry held to its exchange
// projects can name, kept in memory so that no decision waits on Dispatch: the decision budget is
// 1.5 s (decideBudget), and a Dispatch call may take ten. run asks about every such project at the
// start and every accessRefresh after, in parallel. A project with no answer held yet (the first
// ask has not come back, or Dispatch was down at the start) is refused as not yet known, and the
// client retries. While Dispatch answers, a held answer is at most 60 s old: the 30 s
// (projectaccess.CacheTTL) the Dispatch client may have kept it, the accessRefresh to the next ask,
// and the accessFetchTimeout that ask may take. An ask that fails keeps the last answer, as plan E
// says (an answer is used however old it is), so while Dispatch cannot be reached a project's
// answer admits or refuses as Dispatch last said, and the broker logs each failed ask at warn.
type heldAccess struct {
	access   func(ctx context.Context, project, person string) (projectaccess.Answer, error)
	projects []string
	log      func(message string, args ...any)

	mu      sync.RWMutex
	answers map[string]heldAnswer
}

// heldAnswer is one project's answer: Dispatch's, or that Dispatch holds no such project.
type heldAnswer struct {
	answer   projectaccess.Answer
	notFound bool
}

// newHeldAccess holds the answers about every project deps.Exchange gives an entry of services
// that is not an all-projects one; nil when the broker cannot read them (no exchange list or no
// Dispatch client) or no entry can name a project.
func newHeldAccess(deps Deps, services []ServiceEntry) *heldAccess {
	if deps.Exchange == nil || deps.Access == nil {
		return nil
	}
	var projects []string
	for _, entry := range services {
		if entry.AllProjects {
			continue
		}
		listed, _ := deps.Exchange(entry.Account)
		projects = append(projects, listed...)
	}
	slices.Sort(projects)
	projects = slices.Compact(projects)
	if len(projects) == 0 {
		return nil
	}
	held := &heldAccess{access: deps.Access, projects: projects, answers: make(map[string]heldAnswer, len(projects))}
	held.log = func(string, ...any) {}
	if deps.Log != nil {
		held.log = deps.Log.Warn
	}
	return held
}

// run asks about every held project now and every accessRefresh after, until ctx ends.
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

// refresh asks Dispatch about every held project at once, each ask bounded by
// accessFetchTimeout, and returns when every ask has come back.
func (h *heldAccess) refresh(ctx context.Context) {
	var wait sync.WaitGroup
	for _, project := range h.projects {
		wait.Go(func() {
			fetch, cancel := context.WithTimeout(ctx, accessFetchTimeout)
			defer cancel()
			answer, err := h.access(fetch, project, "")
			switch {
			case errors.Is(err, projectaccess.ErrProjectNotFound):
				h.keep(project, heldAnswer{notFound: true})
			case err != nil:
				if ctx.Err() == nil {
					h.log("natscallout: Dispatch's answer about a project could not be read, so its last answer is kept", "project", project, "error", err.Error())
				}
			default:
				h.keep(project, heldAnswer{answer: answer})
			}
		})
	}
	wait.Wait()
}

func (h *heldAccess) keep(project string, answer heldAnswer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.answers[project] = answer
}

// answer is the answer held about project, and false when none is: on a nil heldAccess too.
func (h *heldAccess) answer(project string) (heldAnswer, bool) {
	if h == nil {
		return heldAnswer{}, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	answer, ok := h.answers[project]
	return answer, ok
}
