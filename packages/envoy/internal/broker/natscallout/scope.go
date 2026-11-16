package natscallout

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
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
// (Deps.Access), but for each repository whose GitHub lane (natsgrant.GitHubPrefix) a project the
// entry does not name also maps, by a rename or by a second spelling of the lane
// (Deps.RepoProjects): such a repository is withheld, and counted, since serving it would hand one
// project's daemon another's events; a lane shared only among the entry's own projects is served.
// WithinProjects writes that same reply prefix in place of _INBOX.>. Every
// answer and the mapping the grant is built from come from one complete pass (heldAccess), never
// two mixed. It is refused, naming why: an account no exchange entry lists, or whose entry names
// no project; no complete pass held yet; Dispatch refusing the broker's own account token in that
// pass; a project Dispatch does not hold, or whose answer says the entry's person cannot see it;
// and a grant entry natsgrant.WithinProjects cannot hold.
// ParseServices refuses a dispatch or listener entry with "-" at start, since their grants carry KV
// calls.
//
// Dispatch is never asked inside a decision: a decision reads only the pass the service holds.
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
	if deps.Exchange == nil || deps.Access == nil || deps.RepoProjects == nil {
		return natsgrant.Permissions{}, refuse(labelHeldToProjects, "held to exchange projects, which this broker cannot read: service "+entry.name())
	}
	person, projects, listed := deps.Exchange(entry.Account)
	switch {
	case !listed:
		return natsgrant.Permissions{}, refuse(labelNoExchangeEntry, "account has no exchange entry: "+entry.Account)
	case len(projects) == 0:
		return natsgrant.Permissions{}, refuse(labelNoProjects, "account's exchange entry names no project to hold it to: "+entry.Account)
	}
	pass := deps.held.latest()
	if pass == nil {
		return natsgrant.Permissions{}, refuse(labelSharedLane, "no complete read of Dispatch's project answers and repository mapping is held yet: service "+entry.name())
	}
	if pass.mappingRefused {
		return natsgrant.Permissions{}, refuse(labelAccessRefused, "Dispatch refuses the broker's own account token (BROKER_PROJECT_ACCESS_TOKEN_FILE), so which projects map a repository's lane is not known: service "+entry.name())
	}
	var repositories, every []string // every repository the answers map, and those not withheld
	withheld := 0
	for _, project := range projects {
		held, known := pass.answers[heldKey{project: project, person: person}]
		switch {
		case !known:
			// A pass asks about every project an entry names (newHeldAccess), so a complete one
			// answers each; one that does not is no answer to admit on.
			return natsgrant.Permissions{}, refuse(labelSharedLane, "the complete read of Dispatch's answers holds none about project "+project)
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
		every = append(every, held.answer.Repositories...)
		for _, repository := range held.answer.Repositories {
			if pass.sharedWith(repository, projects) {
				withheld++
				continue
			}
			repositories = append(repositories, repository)
		}
	}
	deps.Metrics.withheldSharedLanes(withheld)
	scoped, err := natsgrant.WithinProjects(grant, natsgrant.Scope{Projects: projects, Repositories: repositories, Identity: entry.Account})
	if err == nil {
		return scoped, nil
	}
	// The refusal is the withheld lanes' only when the same grant held to every repository the
	// answers map is held: a scope that one refuses too is refused for a repository of its own (one
	// that is not owner/name, say), whatever was withheld beside it.
	if withheld > 0 {
		if _, whole := natsgrant.WithinProjects(grant, natsgrant.Scope{Projects: projects, Repositories: every, Identity: entry.Account}); whole == nil {
			return natsgrant.Permissions{}, refuse(labelSharedLane, "grant cannot be held to its projects once the repositories whose lane a project outside the entry maps are withheld: service "+entry.name()+": "+err.Error())
		}
	}
	return natsgrant.Permissions{}, refuse(labelGrantNotHoldable, "grant cannot be held to its projects: service "+entry.name()+": "+err.Error())
}

// accessRefresh is how often the service reads Dispatch again, and accessFetchTimeout how long one
// question may take. accessRefresh is a var so a test can shorten it.
var accessRefresh = 20 * time.Second

const accessFetchTimeout = 10 * time.Second

// heldAccess is what the service holds of Dispatch for the service entries held to their
// exchange projects, so that no decision waits on Dispatch: the decision budget is 1.5 s
// (decideBudget), and a Dispatch call may take ten. A pass (refresh) asks, in parallel, Dispatch's
// answer about every project such an entry can name, for the person that entry's exchange entry
// acts for, and reads Dispatch's whole repository mapping; run makes one at the start and one
// every accessRefresh after. A held entry's grant is built from one complete pass (heldPass) and
// never from two mixed, since a mapping from one pass beside answers from another could serve a
// repository neither alone would. A pass every question of which Dispatch answered replaces the
// one held; a pass any question of which failed (Dispatch unreachable, a timeout) replaces nothing,
// so the last complete pass stands, and each failure is logged at warn. Dispatch refusing the
// broker's own account token (a 401 or 403, projectaccess.ErrBrokerRefused) is an answer and no
// failure: the token exchange refuses every trade that asks Dispatch then (oauth's
// refusedByDispatch), so a pass holding it is complete, refuses every connection held to what it
// refused, and is logged at error once a pass. With no complete pass held yet, a held connection
// is refused, and its client retries; the newest complete pass is used however old it is, so while
// Dispatch cannot be reached a connection is admitted or refused as Dispatch last answered a whole
// pass. While Dispatch answers every question, the pass held is at most about a minute old: the
// 30 s (projectaccess.CacheTTL) the Dispatch client may have kept an answer, the accessRefresh to
// the next pass, and the accessFetchTimeout its questions may take.
type heldAccess struct {
	access       func(ctx context.Context, project, person string) (projectaccess.Answer, error)
	repoProjects func(ctx context.Context) ([]projectaccess.RepoProject, error)
	asks         []heldKey
	entries      [][]string // each held entry's projects
	now          func() time.Time
	log          *slog.Logger

	mu   sync.RWMutex
	pass *heldPass
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

// heldPass is one complete pass: every held question's answer and the lanes of Dispatch's
// repository mapping, as one pass read them, from began, read before its first question was
// sent. Nothing changes a pass once it is held.
type heldPass struct {
	began   time.Time
	answers map[heldKey]heldAnswer
	// lanes is each GitHub lane (natsgrant.GitHubPrefix) a row of the mapping names, by its
	// repository or by the repository it was renamed to, and the projects, sorted, that map one
	// to it (lanesOf).
	lanes map[string][]string
	// mappingRefused is Dispatch refusing the broker's own account token on the mapping's read.
	mappingRefused bool
}

// sharedWith reports whether a project entry does not name maps a repository to repository's lane
// in p's mapping.
func (p *heldPass) sharedWith(repository string, entry []string) bool {
	return outside(p.lanes[natsgrant.GitHubPrefix(repository)], entry)
}

// outside reports whether a project of projects is not one of entry's.
func outside(projects, entry []string) bool {
	return slices.ContainsFunc(projects, func(project string) bool { return !slices.Contains(entry, project) })
}

// lanesOf is each lane rows name, by a row's repository and by the repository it was renamed to,
// and the projects, sorted and each once, that map one to it.
func lanesOf(rows []projectaccess.RepoProject) map[string][]string {
	lanes := map[string][]string{}
	add := func(repository, project string) {
		lane := natsgrant.GitHubPrefix(repository)
		if !slices.Contains(lanes[lane], project) {
			lanes[lane] = append(lanes[lane], project)
		}
	}
	for _, row := range rows {
		add(row.Repo, row.Project)
		if row.RenamedTo != nil && *row.RenamedTo != "" {
			add(*row.RenamedTo, row.Project)
		}
	}
	for _, projects := range lanes {
		slices.Sort(projects)
	}
	return lanes
}

// newHeldAccess holds the passes over every project deps.Exchange gives an entry of services that
// is not an all-projects one, each asked for the person its exchange entry acts for; nil when the
// broker cannot read them (no exchange list, or no Dispatch client) or no entry can name a project.
func newHeldAccess(deps Deps, services []ServiceEntry) *heldAccess {
	if deps.Exchange == nil || deps.Access == nil || deps.RepoProjects == nil {
		return nil
	}
	var asks []heldKey
	var entries [][]string
	for _, entry := range services {
		if entry.AllProjects {
			continue
		}
		person, projects, _ := deps.Exchange(entry.Account)
		for _, project := range projects {
			asks = append(asks, heldKey{project: project, person: person})
		}
		entries = append(entries, projects)
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
	return &heldAccess{access: deps.Access, repoProjects: deps.RepoProjects, asks: asks, entries: entries, now: deps.now, log: log}
}

// run makes a pass now and every accessRefresh after, until ctx ends.
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

// refresh makes one pass: it asks Dispatch every held question and reads its repository mapping,
// all at once, each bounded by accessFetchTimeout, and holds the pass once every one has come back
// answered (hold).
func (h *heldAccess) refresh(ctx context.Context) {
	pass := &heldPass{began: h.now()}
	answers := make([]heldAnswer, len(h.asks))
	var failed atomic.Bool
	var refused sync.Once
	refusedBy := func(err error) {
		refused.Do(func() {
			h.log.Error("natscallout: Dispatch refuses the broker's own account token (BROKER_PROJECT_ACCESS_TOKEN_FILE), so every connection held to its exchange projects is refused until it answers", "error", err.Error())
		})
	}
	var wait sync.WaitGroup
	for i, ask := range h.asks {
		wait.Go(func() {
			fetch, cancel := context.WithTimeout(ctx, accessFetchTimeout)
			defer cancel()
			answer, err := h.access(fetch, ask.project, ask.person)
			switch {
			case errors.Is(err, projectaccess.ErrProjectNotFound):
				answers[i] = heldAnswer{notFound: true}
			case errors.Is(err, projectaccess.ErrBrokerRefused):
				answers[i] = heldAnswer{refused: true}
				refusedBy(err)
			case err != nil:
				failed.Store(true)
				if ctx.Err() == nil {
					h.log.Warn("natscallout: Dispatch's answer about a project could not be read, so the last complete pass is kept", "project", ask.project, "error", err.Error())
				}
			default:
				answers[i] = heldAnswer{answer: answer}
			}
		})
	}
	wait.Go(func() {
		fetch, cancel := context.WithTimeout(ctx, accessFetchTimeout)
		defer cancel()
		rows, err := h.repoProjects(fetch)
		switch {
		case errors.Is(err, projectaccess.ErrBrokerRefused):
			pass.mappingRefused = true
			refusedBy(err)
		case err != nil:
			failed.Store(true)
			if ctx.Err() == nil {
				h.log.Warn("natscallout: Dispatch's repository mapping could not be read, so the last complete pass is kept", "error", err.Error())
			}
		default:
			pass.lanes = lanesOf(rows)
		}
	})
	wait.Wait()
	if failed.Load() {
		return
	}
	pass.answers = make(map[heldKey]heldAnswer, len(h.asks))
	for i, ask := range h.asks {
		pass.answers[ask] = answers[i]
	}
	h.hold(pass)
}

// hold holds pass in place of the one held, unless that one began later, which a slower pass must
// not replace, and logs at error each lane pass's mapping names for a project of a held entry and
// a project outside that entry, since that entry's daemon is not served the lane.
func (h *heldAccess) hold(pass *heldPass) {
	h.mu.Lock()
	later := h.pass == nil || !pass.began.Before(h.pass.began)
	if later {
		h.pass = pass
	}
	h.mu.Unlock()
	if !later {
		return
	}
	for _, lane := range slices.Sorted(maps.Keys(pass.lanes)) {
		projects := pass.lanes[lane]
		withheld := func(entry []string) bool {
			return outside(projects, entry) && slices.ContainsFunc(projects, func(project string) bool { return slices.Contains(entry, project) })
		}
		if slices.ContainsFunc(h.entries, withheld) {
			h.log.Error("natscallout: a project outside a held entry maps a repository to one of that entry's GitHub lanes, so its daemon is not served the lane", "lane", lane, "projects", projects)
		}
	}
}

// latest is the pass held, nil when none is: on a nil heldAccess too.
func (h *heldAccess) latest() *heldPass {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pass
}
