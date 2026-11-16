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
	"strings"
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
// answers. A secret with no tag never meets it.
var ErrProjectAccessUnavailable = errors.New("the broker could not ask Dispatch about the project of a requested secret")

// errProjectNotNamed is stillAllowed's refusal of a granted name whose private project the
// session no longer names, or whose project Dispatch no longer holds: the project turned
// private, its person left the project's access list, or the tag now names another project.
// Values, the airlock and reuseLiveGrant end such a grant for good (endGrant), so making the
// project public again, or putting the person back, never revives it; the session asks again.
var errProjectNotNamed = errors.New("project not named")

// airlockProjectRecheck bounds how long the airlock proxy may hold the values of a grant that
// holds a project-tagged name: every AirlockSecret of such a grant expires no later than the read
// time plus this, and the proxy drops a value within its own mintAgainBefore of that expiry, so a
// pod holding values re-runs the release checks (AirlockValues, checkRelease) within this bound of
// the read. Whether a project is private is Dispatch's, not the policy version's, so nothing else
// would make the proxy read a held value again when access is lost.
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

// channelsMissingLogged is when each project's ChannelsMissingMessage was last written, so it is
// written once per project per projectaccess.CacheTTL, not on every release.
var channelsMissingLogged = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

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
			logChannelsMissing(project)
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
func logChannelsMissing(project string) {
	channelsMissingLogged.Lock()
	defer channelsMissingLogged.Unlock()
	if at, ok := channelsMissingLogged.at[project]; ok && time.Since(at) < projectaccess.CacheTTL {
		return
	}
	channelsMissingLogged.at[project] = time.Now()
	slog.Warn(ChannelsMissingMessage, "project", project)
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

// JudgeProject answers whether the model judge must leave the request of enrollmentID for name to
// a person for a project's sake. It must when the secret's dispatch-project tag names a private
// project or a key Dispatch holds no project for, and when a project the credential that asked
// names is private (namedProjects): a private project's pod asking for an untagged secret is that
// project's request, whose reason is that project's text, and the judge's own token names no
// person, so it may see no private project.
//
// A broker with no project-access client answers false for an untagged secret, exactly as the
// merge base decides (R9): such a broker loads no tagged secret (project-unchecked) and cannot know
// any project is private, so referring a pod only because its exchange entry names a project
// would change a deployment with nothing private and protect nothing. A request whose secret
// carries no tag and whose credential names no project answers false and asks Dispatch nothing
// (judge.Requests).
func (m *Machine) JudgeProject(ctx context.Context, enrollmentID, name string) (bool, error) {
	projects, err := m.SecretProjects(ctx, []string{name})
	if err != nil {
		return false, err
	}
	if len(projects) > 0 && projects[0].Private {
		return true, nil
	}
	if m.Projects == nil {
		return false, nil
	}
	enr, err := m.enrollment(ctx, enrollmentID)
	if err != nil {
		return false, err
	}
	named := m.namedProjects(enr)
	if len(named) == 0 {
		return false, nil
	}
	answers, err := m.Projects.States(ctx, named)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrProjectAccessUnavailable, err)
	}
	for _, project := range named {
		if answer, held := answers[project]; held && answer.Private() {
			return true, nil
		}
	}
	return false, nil
}

// MaySee reports whether login may see rec: every private project among its tagged secrets
// (rec.SecretProjects), the projects its session names (rec.SessionProjects), and the project of
// the first Dispatch issue key its signed reason names. A person outside one of them may neither
// read the record nor decide it, since its names and its reason are that project's. A project
// Dispatch does not hold hides nothing. A record touching no project asks Dispatch nothing, and a
// broker with no project-access client hides nothing, since it serves no tagged secret and
// enrolls no session naming a project.
func (m *Machine) MaySee(ctx context.Context, login string, rec RecordDetail) (bool, error) {
	var projects []string
	for _, p := range rec.SecretProjects {
		if p.Private {
			projects = append(projects, p.Project)
		}
	}
	return m.maySee(ctx, login, append(projects, rec.SessionProjects...), rec.Reason)
}

// maySee is MaySee over the record's parts: candidate projects and the reason whose issue key's
// project joins them.
func (m *Machine) maySee(ctx context.Context, login string, projects []string, reason string) (bool, error) {
	if key := record.IssueKeyInReason(reason); key != "" {
		project, _, _ := strings.Cut(key, "-")
		projects = append(projects, project)
	}
	if len(projects) == 0 || m.Projects == nil {
		return true, nil
	}
	slices.Sort(projects)
	for _, project := range slices.Compact(projects) {
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
func (m *Machine) mayDecide(ctx context.Context, login string, facts projectFacts, sessionProjects []string, reason string) (bool, error) {
	var projects []string
	for project, state := range facts.states {
		if state == policy.ProjectPrivate {
			projects = append(projects, project)
		}
	}
	return m.maySee(ctx, login, append(projects, sessionProjects...), reason)
}

// maySeeRecord is MaySee for a listing's row: names under the live policy, the projects of the
// enrollment that asked, and the reason. A row touching no project asks Dispatch nothing.
func (m *Machine) maySeeRecord(ctx context.Context, login string, names, sessionProjects []string, reason string) (bool, error) {
	secrets, err := m.SecretProjects(ctx, names)
	if err != nil {
		return false, err
	}
	return m.MaySee(ctx, login, RecordDetail{SecretProjects: secrets, SessionProjects: sessionProjects, Reason: reason})
}

// decisionRead is what the approve path reads of a record on the pool before its transaction: the
// enrollment the request hangs off now, the names it asks for, and its signed reason, from which
// projectFacts and mayDecide resolve what Dispatch holds before any lock is taken.
type decisionRead struct {
	enr    enrollmentRow
	names  []string
	reason string
}

// readDecision is the record's decisionRead, or pgx.ErrNoRows for a record no request names.
func (m *Machine) readDecision(ctx context.Context, recordID string) (decisionRead, error) {
	var d decisionRead
	err := m.Store.Pool.QueryRow(ctx, `select e.id, e.kind, e.operator, e.subject, c.service, e.projects, r.reason,
			coalesce((select array_agg(rs.name order by rs.name) from request_secrets rs where rs.request_id=r.id and rs.decision<>'deny'), '{}')
		from requests r join enrollments e on e.id = r.enrollment_id
		left join launcher_credentials c on c.id = e.launcher_credential_id where r.record_id=$1`, recordID).
		Scan(&d.enr.ID, &d.enr.Kind, &d.enr.Operator, &d.enr.Subject, &d.enr.Service, &d.enr.Projects, &d.reason, &d.names)
	return d, err
}
