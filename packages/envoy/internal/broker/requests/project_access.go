// packages/envoy/internal/broker/requests/project_access.go
// project_access.go is the request machine's half of a private project's secrets
// (dispatch://INFRA-3862, R11): what a credential names, and what Dispatch holds of each project a
// requested secret is tagged with, resolved on the pool before any transaction begins and handed
// to policy.Set.Evaluate as plain data. No call to Dispatch is made inside a transaction, under a
// row lock or under an advisory lock, and none at all for a request, release or airlock
// connection that holds no tagged secret.
package requests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/broker/exchange"
	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/record"
)

// ErrProjectAccessUnavailable is a request, release, decision or airlock connection that holds a
// secret tagged with a project the broker could not ask Dispatch about: its project-access client
// failed, or the broker holds none. Nothing is released; the session asks again once Dispatch
// answers. A request or release of a secret with no tag never meets it. A person's decision meets
// it too when Dispatch cannot say whether that person may see the record (MaySee, which reads the
// projects its reason's issue keys name): nothing is decided and the request stays pending. An
// approver's list never fails with it; it leaves that one row out (logRowUnanswered).
var ErrProjectAccessUnavailable = errors.New("the broker could not ask Dispatch about the project of a requested secret")

// errProjectNotNamed is stillAllowed's refusal of a granted name whose private project the
// session no longer names, or whose project Dispatch no longer holds: the project turned
// private, its person left the project's access list, or the tag now names another project.
// Values, the airlock and reuseLiveGrant end such a grant for good (endGrant), so making the
// project public again, or putting the person back, never revives it; the session asks again.
var errProjectNotNamed = errors.New("project not named")

// airlockProjectRecheck bounds how long the airlock proxy may hold the values of a grant that
// holds a project-tagged name, and a project-channels value released to a credential that names
// any project: every such AirlockSecret expires no later than the read time plus this, and the
// proxy drops a value within its own mintAgainBefore of that expiry, so a pod holding values
// re-runs the release checks (AirlockValues, checkRelease) within this bound of the read. Whether
// a project is private, and which channels its owner allows, are Dispatch's, not the policy
// version's, so nothing else would make the proxy read a held value again when access is lost or
// a named project turns private.
const airlockProjectRecheck = 10 * time.Minute

// projectFacts is what a release's policy check reads of Dispatch for one credential and one set
// of names: the projects the credential names that its person may see, and the state of each
// project a tagged name among them carries. It is plain data, resolved before any transaction.
type projectFacts struct {
	projects []string
	states   map[string]policy.ProjectState
	// named are the projects the credential names, before the visibility filter projects is the
	// result of, resolved only when a requested name carries a project-channels tag, which is the
	// one rule that reads them (policy.Requester.ListsPrivate).
	named []string
	// person is who the credential acts for, for the channel limit's own reads.
	person string
}

// requester is r with these facts, the form policy.Set.Evaluate reads them in.
func (f projectFacts) requester(r policy.Requester) policy.Requester {
	r.Projects, r.ProjectStates, r.Named = f.projects, f.states, f.named
	return r
}

// anyChannelLimited reports whether any of names carries a project-channels tag, the one tag
// whose rule reads the credential's own projects rather than the secret's.
func anyChannelLimited(set *policy.Set, names []string) bool {
	return slices.ContainsFunc(names, func(name string) bool { return set.Secrets[name].ProjectChannels != "" })
}

// taggedProjects is each distinct project a name of names is tagged with in set, sorted, and nil
// when none is.
func taggedProjects(set *policy.Set, names []string) []string {
	var tagged []string
	for _, name := range names {
		if project := set.Secrets[name].Project; project != "" && !slices.Contains(tagged, project) {
			tagged = append(tagged, project)
		}
	}
	slices.Sort(tagged)
	return tagged
}

// anyProjectTagged reports whether set tags any of granted with a project: a grant of such a name
// is checked again on every release, since whether its project is private is Dispatch's state and
// not in the policy version.
func anyProjectTagged(set *policy.Set, granted []requestedSecret) bool {
	return slices.ContainsFunc(granted, func(g requestedSecret) bool { return set.Secrets[g.name].Project != "" })
}

// credentialProjects is the person a credential acts for and the projects it names: a person's
// session names the projects its enrollment recorded; a pod names the one project its operator's
// entry maps its service account to (exchange.Set.MappedProjects), checked against that entry's
// person; a machine's own session the one its fleet's Google account maps to; anything else names
// none.
func (m *Machine) credentialProjects(e enrollmentRow) (person string, projects []string) {
	switch {
	case e.Kind == "session" && e.Operator != nil:
		return *e.Operator, e.Projects
	case m.Exchange == nil || e.Subject == nil:
		return "", nil
	case e.Kind == "pod":
		person, projects, _ := m.Exchange.MappedProjects(exchange.AccountPrincipal(*e.Subject))
		return person, projects
	case e.Kind == "session":
		if read := exchange.ReadSubject(*e.Subject); read.Kind == exchange.SubjectMachine {
			person, projects, _ := m.Exchange.MappedProjects(exchange.GooglePrincipal(read.Account))
			return person, projects
		}
	}
	return "", nil
}

// projectFacts resolves, on the pool and before any transaction, what Evaluate needs of Dispatch
// for e asking for names under set: one state per distinct project tag among names
// (projectaccess.Access.States: present, private or public; absent, missing), and the projects
// the credential names that its person may still see, of those tags alone, since Evaluate reads
// the credential's list for a tagged private secret and for nothing else. Where a requested name
// carries a project-channels tag, the states of the credential's own named projects are resolved
// too, because that rule reads them whatever project the secret belongs to
// (policy.Requester.ListsPrivate). A request holding neither tag asks Dispatch nothing at all. A
// failure to ask is ErrProjectAccessUnavailable.
func (m *Machine) projectFacts(ctx context.Context, e enrollmentRow, names []string, set *policy.Set) (projectFacts, error) {
	tagged := taggedProjects(set, names)
	person, named := m.credentialProjects(e)
	limited := anyChannelLimited(set, names)
	// A project-channels name read by a credential that names no project is unlimited, and so is
	// decided without asking Dispatch anything, as an untagged name is.
	if len(tagged) == 0 && (!limited || len(named) == 0) {
		return projectFacts{}, nil
	}
	if m.Projects == nil {
		// A broker with no project-access client loads no dispatch-project secret (the loader
		// refuses one project-unchecked) and cannot know any project is private, so a
		// project-channels name is unlimited here, as at the merge base (R9), whatever projects
		// the credential names. A tagged name cannot reach this point, and fails closed if it does.
		if len(tagged) == 0 {
			return projectFacts{}, nil
		}
		return projectFacts{}, fmt.Errorf("%w: this broker holds no client of Dispatch's project access", ErrProjectAccessUnavailable)
	}
	asking := tagged
	if limited {
		asking = slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(tagged), named...))))
	}
	answers, err := m.Projects.States(ctx, asking)
	if err != nil {
		return projectFacts{}, fmt.Errorf("%w: %v", ErrProjectAccessUnavailable, err)
	}
	facts := projectFacts{states: make(map[string]policy.ProjectState, len(asking)), person: person}
	if limited {
		facts.named = named
	}
	for _, project := range asking {
		answer, held := answers[project]
		switch {
		case !held:
			facts.states[project] = policy.ProjectMissing
		case answer.Private():
			facts.states[project] = policy.ProjectPrivate
		default:
			facts.states[project] = policy.ProjectPublic
		}
	}
	for _, project := range named {
		if facts.states[project] != policy.ProjectPrivate {
			continue
		}
		err := projectaccess.Visible(ctx, m.Projects, person, []string{project})
		switch {
		case err == nil:
			facts.projects = append(facts.projects, project)
		case !errors.Is(err, projectaccess.ErrNotVisible):
			return projectFacts{}, fmt.Errorf("%w: %v", ErrProjectAccessUnavailable, err)
		}
	}
	return facts, nil
}

// ChannelsMissingMessage is the log line for a private project whose Dispatch answered no
// slack_channels key at all, a Dispatch from before dispatch://INFRA-3862's #t-a-31: the broker
// limits that project's credentials to no channel rather than read the missing key as no limit,
// so Dispatch and the broker deploy in either order safely.
const ChannelsMissingMessage = "a private project's access answer carries no slack_channels; its credentials post to no channel until Dispatch has dispatch://INFRA-3862's #t-a-31"

// logThrottle is when each key's log line was last written, so a line a release or a listing
// would repeat on every call is written once per key per projectaccess.CacheTTL, the age after
// which the broker asks Dispatch again anyway. A Machine holds one per line it throttles.
type logThrottle struct {
	mu sync.Mutex
	at map[string]time.Time
}

// due reports whether key's line may be written now, and if so counts it written. Keys last
// written longer than projectaccess.CacheTTL ago are dropped as it goes, so the map holds only
// the keys of the last CacheTTL.
func (l *logThrottle) due(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if at, ok := l.at[key]; ok && now.Sub(at) < projectaccess.CacheTTL {
		return false
	}
	if l.at == nil {
		l.at = map[string]time.Time{}
	}
	for held, at := range l.at {
		if now.Sub(at) >= projectaccess.CacheTTL {
			delete(l.at, held)
		}
	}
	l.at[key] = now
	return true
}

// channelLimit is the channels a grant's credential may post to, and whether it is limited at
// all. It is limited when the grant holds a project-channels name and one of the projects its
// credential names is private (policy.Requester.ListsPrivate, read from the unfiltered list). The
// channels are then those every such project's owner allowed: the intersection, sorted, never
// nil, so content of any named project lands only where all of their owners allow. They are empty
// when the person can no longer see one of those projects, when a project's answer carries no
// slack_channels key, when Dispatch did not answer, and when the lists share no channel; they
// are never unlimited. A grant holding no project-channels name, or a credential naming no
// private project, is unlimited and asks Dispatch nothing here; the answers it reads otherwise
// are the ones projectFacts already put in the client's cache.
func (m *Machine) channelLimit(ctx context.Context, checked grantRelease) ([]string, bool) {
	names := make([]string, len(checked.granted))
	for i, g := range checked.granted {
		names[i] = g.name
	}
	if !anyChannelLimited(checked.set, names) || !checked.facts.requester(policy.Requester{}).ListsPrivate() {
		return nil, false
	}
	none := []string{}
	var allowed []string
	first := true
	for _, project := range checked.facts.named {
		if checked.facts.states[project] != policy.ProjectPrivate {
			continue
		}
		if !slices.Contains(checked.facts.projects, project) {
			return none, true
		}
		answer, err := m.Projects.Access(ctx, project, checked.facts.person)
		if err != nil {
			return none, true
		}
		if !answer.SlackChannelsPresent {
			m.logChannelsMissing(project)
			return none, true
		}
		listed := slices.Compact(slices.Sorted(slices.Values(answer.SlackChannels)))
		if first {
			allowed, first = listed, false
			continue
		}
		allowed = slices.DeleteFunc(allowed, func(channel string) bool { return !slices.Contains(listed, channel) })
	}
	if allowed == nil {
		return none, true
	}
	return allowed, true
}

// logChannelsMissing writes ChannelsMissingMessage for project once per projectaccess.CacheTTL.
func (m *Machine) logChannelsMissing(project string) {
	if m.channelsMissingLogged.due(project) {
		slog.Warn(ChannelsMissingMessage, "project", project)
	}
}

// rowUnansweredMessage is the log line for a row an approver's list leaves out because the
// broker could not ask Dispatch whether the reader may see it (ErrProjectAccessUnavailable): the
// row is hidden from that list alone, failing closed, and the rest of the list is answered.
const rowUnansweredMessage = "an approver's list leaves out a row whose project Dispatch could not be asked about; it is listed again once Dispatch answers"

// logRowUnanswered writes rowUnansweredMessage for the one row key names, with attrs and err,
// once per projectaccess.CacheTTL however often its list is read: a list is read on every 15 s
// poll of every open Inbox tab, and one line per row says all an operator needs.
func (m *Machine) logRowUnanswered(key string, err error, attrs ...any) {
	if m.rowsUnansweredLogged.due(key) {
		slog.Warn(rowUnansweredMessage, append(attrs, "error", err)...)
	}
}

// ReasonUnreadMessage is the log line for a stored row whose reason names the issue keys of more
// projects than a new request may (record.MaxReasonProjects): the row can only be one stored before
// Create refused such a reason, so maySee reads none of its keys and counts them as naming no
// project, which is how every reader saw the row when it was written. Its other projects are
// still asked.
const ReasonUnreadMessage = "a stored request's reason names the issue keys of more projects than a new request may; its keys are not read, and count as naming no project"

// logReasonUnread writes ReasonUnreadMessage for the one row row names ("record <id>" or
// "grant <id>"), at info and once per projectaccess.CacheTTL however often the row is read. It
// names the row and the count, never the reason's text.
func (m *Machine) logReasonUnread(row string, projects int) {
	if m.reasonsUnreadLogged.due(row) {
		slog.Info(ReasonUnreadMessage, "row", row, "projects", projects, "bound", record.MaxReasonProjects)
	}
}

// projectNotNamed is the refusal of a granted name the session may no longer have for its
// project's sake, which ends the grant for good (errProjectNotNamed).
func projectNotNamed(name string) error {
	return fmt.Errorf("%w: %w: %s belongs to a private project this session does not name", ErrGrantNotLive, errProjectNotNamed, name)
}

// projectDeny reports whether d is a Deny the project tag decided.
func projectDeny(d policy.Decision) bool {
	return d.Outcome == policy.Deny && (d.Reason == policy.ReasonProjectNotNamed || d.Reason == policy.ReasonProjectMissing)
}

// SecretProject is one requested secret's project, for the approval page: the secret, the key its
// dispatch-project tag names, and whether that project is private (or one Dispatch does not hold,
// which the page shows the same way).
type SecretProject struct {
	Name, Project string
	Private       bool
}

// SecretProjects answers the project of each of names the live policy tags with one, in names'
// order; a name of no project is left out, and a set of names none of which is tagged asks
// Dispatch nothing.
func (m *Machine) SecretProjects(ctx context.Context, names []string) ([]SecretProject, error) {
	set := m.Policy.Get()
	tagged := taggedProjects(set, names)
	if len(tagged) == 0 {
		return nil, nil
	}
	facts, err := m.projectFacts(ctx, enrollmentRow{}, names, set)
	if err != nil {
		return nil, err
	}
	var out []SecretProject
	for _, name := range names {
		if project := set.Secrets[name].Project; project != "" {
			out = append(out, SecretProject{Name: name, Project: project, Private: facts.states[project] != policy.ProjectPublic})
		}
	}
	return out, nil
}

// JudgeProject answers whether the model judge must leave the request of enrollmentID for name,
// signed with reason, to a person for a project's sake (judgeMayDecide): the secret's
// dispatch-project tag names a private project or a key Dispatch holds no project for, or the
// person rule (maySee) would hide the request from a reader who names no person, because the
// credential that asked names a private project (namedProjects) or an issue key its reason names
// is a private project's. The judge's own token names no person, so it may see no private
// project, and deciding such a request would grant in the name of an approver a private project's
// rule may hide it from. The decision checks the same rule again (applyDecisionAttempt).
//
// Its reads of Dispatch run under JudgeDispatch, whatever ctx the caller passed: the judge's own
// Dispatch phase when the judge asks. A request a broker with this check creates names no more
// projects than record.CheckReasonProjects allows, so the judge, which runs as the request is
// created, meets no reason past that bound.
//
// A broker with no project-access client answers false for an untagged secret, exactly as the
// merge base decides (R9): such a broker loads no tagged secret (project-unchecked) and cannot know
// any project is private, so referring a request only because its credential or its reason names
// a project would change a deployment with nothing private and protect nothing. A request whose
// secret carries no tag, whose credential names no project and whose reason names no issue key
// answers false and asks Dispatch nothing (judge.Requests).
func (m *Machine) JudgeProject(ctx context.Context, enrollmentID, name, reason string) (bool, error) {
	ctx, cancel := JudgeDispatch(ctx)
	defer cancel()
	facts, err := m.projectFacts(ctx, enrollmentRow{}, []string{name}, m.Policy.Get())
	if err != nil {
		return false, err
	}
	if m.Projects == nil {
		return false, nil
	}
	enr, err := m.enrollment(ctx, enrollmentID)
	if err != nil {
		return false, err
	}
	may, err := m.judgeMayDecide(ctx, "enrollment "+enrollmentID, facts.states, m.namedProjects(enr), reason)
	return !may, err
}

// MaySee reports whether login may see rec: every private project among its tagged secrets
// (rec.SecretProjects), the projects its session names (rec.SessionProjects), and the project of
// every Dispatch issue key its signed reason names. A person outside one of them may neither read
// the record nor decide it, since its names and its reason are that project's. A project Dispatch
// does not hold hides nothing, so a word of a key's form that is no project ("SHA-256") hides
// nothing and hides no key after it. A record touching no project asks Dispatch nothing, and a
// broker with no project-access client hides nothing, since it serves no tagged secret and
// enrolls no session naming a project. An error is ErrProjectAccessUnavailable: whether login may
// see rec is unknown, and a caller shows rec to nobody until Dispatch answers.
func (m *Machine) MaySee(ctx context.Context, login string, rec RecordDetail) (bool, error) {
	return m.maySee(ctx, "record "+rec.RecordID, login, append(privateProjects(rec.SecretProjects), rec.SessionProjects...), rec.Reason)
}

// privateProjects is the project of each of secrets that belongs to a private project.
func privateProjects(secrets []SecretProject) []string {
	var projects []string
	for _, p := range secrets {
		if p.Private {
			projects = append(projects, p.Project)
		}
	}
	return projects
}

// JudgeDispatchBound is how long the model judge may wait on Dispatch at one point of a run: all of
// its reads before the model together — the policy check's (JudgePolicy), the project check's
// (JudgeProject) and the issue check's, which package judge's Dispatch phase holds under one
// JudgeDispatch — and, at the decision, the reads its approval makes before its transaction
// (applyDecisionAttempt). A read it cuts short fails its check, and the request waits for its
// person.
const JudgeDispatchBound = 5 * time.Second

// JudgeRowReserve is the part of a judge run's time no read of Dispatch may spend: JudgeDispatch
// ends that long before its context does, so a read Dispatch is slow to answer fails while there
// is still time to write the run's request.judged row (judge.Runner.write).
const JudgeRowReserve = time.Second

// judgeDispatchKey marks a context JudgeDispatch made.
type judgeDispatchKey struct{}

// JudgeDispatch is the one bound every read of Dispatch on the model judge's path runs under: ctx,
// ended JudgeDispatchBound from now or JudgeRowReserve before ctx's own end, whichever comes first,
// and marked so a test can tell each read of a run made under it (UnderJudgeDispatch) from one that
// bypassed it. A context it already made is answered as it is, so a read inside the judge's phase
// shares the phase's bound rather than starting one of its own.
func JudgeDispatch(ctx context.Context) (context.Context, context.CancelFunc) {
	if UnderJudgeDispatch(ctx) {
		return ctx, func() {}
	}
	deadline := time.Now().Add(JudgeDispatchBound)
	if end, ok := ctx.Deadline(); ok && end.Add(-JudgeRowReserve).Before(deadline) {
		deadline = end.Add(-JudgeRowReserve)
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	return context.WithValue(bounded, judgeDispatchKey{}, true), cancel
}

// UnderJudgeDispatch reports whether ctx is one JudgeDispatch made, or derived from one.
func UnderJudgeDispatch(ctx context.Context) bool {
	marked, _ := ctx.Value(judgeDispatchKey{}).(bool)
	return marked
}

// maySee is MaySee over the record's parts: candidate projects and the reason, the project of each
// of whose issue keys (record.ReasonProjects) joins them. It is the one statement of whose request
// this is, for a person (MaySee, mayDecide, maySeeRecord) and for the model judge, which asks it
// with no person (judgeMayDecide). A word of a key's form that is no project ("SHA-256") joins them
// too, and Dispatch answers it as no project, which hides nothing. It sorts a list of its own: the
// caller's may be an enrollment's or an operator entry's, which nothing here owns.
//
// A reason naming the issue keys of more than record.MaxReasonProjects projects is one stored
// before Create refused such a reason (record.CheckReasonProjects). Its keys are not read: they
// count as naming no project, which is how every reader saw the record when it was written, and a
// line naming the row says so (logReasonUnread; row is "record <id>", "grant <id>" or, for the
// judge's own check, "enrollment <id>"). The record's other projects are still asked, since its
// secrets' tags come of the live policy, and a secret it names may have been tagged with a
// private project since it was stored.
func (m *Machine) maySee(ctx context.Context, row, login string, projects []string, reason string) (bool, error) {
	if m.Projects == nil {
		return true, nil
	}
	reasonProjects := record.ReasonProjects(reason)
	if len(reasonProjects) > record.MaxReasonProjects {
		m.logReasonUnread(row, len(reasonProjects))
		reasonProjects = nil
	}
	asking := slices.Concat(projects, reasonProjects)
	slices.Sort(asking)
	for _, project := range slices.Compact(asking) {
		answer, err := m.Projects.Access(ctx, project, record.CanonicalLogin(login))
		switch {
		case errors.Is(err, projectaccess.ErrProjectNotFound):
			continue
		case err != nil:
			return false, fmt.Errorf("%w: %v", ErrProjectAccessUnavailable, err)
		case !answer.Visible:
			return false, nil
		}
	}
	return true, nil
}

// judgeMayDecide is the model judge's project rule, the one its run checks before the model
// (JudgeProject) and its decision checks again before the transaction (decisionFacts). states is
// what Dispatch holds of each project the request's secrets are tagged with, and of those alone,
// and named the projects its credential names; the request is the judge's only when each tagged
// project is public and the person rule shows it to a reader naming no person, which Dispatch
// answers by visibility alone. The judge's own token names no person, so it may see no private
// project.
func (m *Machine) judgeMayDecide(ctx context.Context, row string, states map[string]policy.ProjectState, named []string, reason string) (bool, error) {
	for _, state := range states {
		if state != policy.ProjectPublic {
			return false, nil
		}
	}
	return m.maySee(ctx, row, "", named, reason)
}

// namedProjects is the projects e's credential names: a person's session's enrollment list, the
// project a pod's operator entry maps its account to, or the one a machine's fleet maps to
// (credentialProjects). It is the one source every reader uses — the release (projectFacts), the
// decision (mayDecide), the record a person reads (ReadRecord) and both approver lists — so the
// path that releases a secret and the path that shows a request cannot disagree on whose it is:
// a pod's enrollment row names no project of its own, and reading that column alone showed a
// private project's pod requests to everyone.
func (m *Machine) namedProjects(e enrollmentRow) []string {
	_, named := m.credentialProjects(e)
	return named
}

// mayDecide is the approve path's MaySee, read before its transaction: the record's tagged
// secrets under set, the projects the credential that asked names (namedProjects), and its
// reason.
func (m *Machine) mayDecide(ctx context.Context, row, login string, facts projectFacts, sessionProjects []string, reason string) (bool, error) {
	var projects []string
	for project, state := range facts.states {
		if state == policy.ProjectPrivate {
			projects = append(projects, project)
		}
	}
	return m.maySee(ctx, row, login, append(projects, sessionProjects...), reason)
}

// decisionFacts is what the approve path reads of Dispatch for recordID before its transaction:
// the state of each project the request's names are tagged with and the projects its credential
// may still name (projectFacts), which the transaction's evaluation reads, and whether who may
// decide the record at all. A person may when the person rule shows them the record (mayDecide).
// The model judge may when the request is still of no private project (judgeMayDecide, over the
// tagged projects' states alone, the ones its run's check read), which its run checked before the
// model, and which a project turning private during the run or an adoption by an enrollment
// naming a private project can have changed since. Both rules read the projects of two
// credentials: the one the request hangs off now, which an adoption rewrites, and the one that
// asked, which the append-only record names and the pending lists and the approval page read, so a
// request either of them would hide from a reader is one that reader does not decide. For the
// judge every read is under JudgeDispatch, so a slow Dispatch leaves the run the time its row
// needs, and asked of Dispatch afresh (projectaccess.Fresh): the run's own check filled the cache
// well inside projectaccess.CacheTTL, and an answer read back from it could not show a change made
// since. A refusal is record.ErrNotApprover.
func (m *Machine) decisionFacts(ctx context.Context, recordID string, who decider, pre decisionRead, set *policy.Set) (projectFacts, error) {
	if who.isJudge() {
		bounded, cancel := JudgeDispatch(ctx)
		defer cancel()
		ctx = projectaccess.Fresh(bounded)
	}
	facts, err := m.projectFacts(ctx, pre.enr, pre.names, set)
	if err != nil {
		return projectFacts{}, err
	}
	named := slices.Concat(m.namedProjects(pre.enr), pre.asked)
	var may bool
	if who.isJudge() {
		tagged := make(map[string]policy.ProjectState)
		for _, project := range taggedProjects(set, pre.names) {
			tagged[project] = facts.states[project]
		}
		may, err = m.judgeMayDecide(ctx, "record "+recordID, tagged, named, pre.reason)
	} else {
		may, err = m.mayDecide(ctx, "record "+recordID, who.login, facts, named, pre.reason)
	}
	switch {
	case err != nil:
		return projectFacts{}, err
	case may:
		return facts, nil
	case who.isJudge():
		return projectFacts{}, fmt.Errorf("%w: the request now belongs to a private project, which the model judge leaves to a person", record.ErrNotApprover)
	}
	return projectFacts{}, fmt.Errorf("%w: the request belongs to a private project %s may not see", record.ErrNotApprover, record.CanonicalLogin(who.login))
}

// maySeeRecord is MaySee for a listing's row: names under the live policy, the projects of the
// enrollment that asked, and the reason. A row touching no project asks Dispatch nothing.
func (m *Machine) maySeeRecord(ctx context.Context, row, login string, names, sessionProjects []string, reason string) (bool, error) {
	secrets, err := m.SecretProjects(ctx, names)
	if err != nil {
		return false, err
	}
	return m.maySee(ctx, row, login, append(privateProjects(secrets), sessionProjects...), reason)
}

// decisionRead is what the approve path reads of a record on the pool before its transaction: the
// enrollment the request hangs off now, the projects the credential that asked names (the
// enrollment the record froze, read through credentialColumns as the pending lists read it), the
// names the request asks for, and its signed reason, from which projectFacts and mayDecide resolve
// what Dispatch holds before any lock is taken.
type decisionRead struct {
	enr    enrollmentRow
	asked  []string
	names  []string
	reason string
}

// readDecision is the record's decisionRead, or pgx.ErrNoRows for a record no request names.
func (m *Machine) readDecision(ctx context.Context, recordID string) (decisionRead, error) {
	var d decisionRead
	var asked enrollmentRow
	err := m.Store.Pool.QueryRow(ctx, `select e.id, e.kind, e.operator, e.subject, c.service, e.projects, r.reason,
			coalesce((select array_agg(rs.name order by rs.name) from request_secrets rs where rs.request_id=r.id and rs.decision<>'deny'), '{}'),
			`+credentialColumns+`
		from requests r join enrollments e on e.id = r.enrollment_id
		left join launcher_credentials c on c.id = e.launcher_credential_id
		left join credential_requests cr on cr.id = r.record_id
		left join enrollments enr on enr.id = cr.enrollment_id where r.record_id=$1`, recordID).
		Scan(&d.enr.ID, &d.enr.Kind, &d.enr.Operator, &d.enr.Subject, &d.enr.Service, &d.enr.Projects, &d.reason, &d.names,
			&asked.Kind, &asked.Operator, &asked.Subject, &asked.Projects)
	d.asked = m.namedProjects(asked)
	return d, err
}
