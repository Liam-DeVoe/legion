package natsgrant

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
)

// Scope is what one credential may reach on the bus: the projects its token lists, the GitHub
// repositories Dispatch maps to them, and the account the token names. The auth service builds it
// for a service whose entry is held to projects (natscallout, from the broker's exchange list and
// Dispatch's project answers) and hands it to WithinProjects with that service's grant.
type Scope struct {
	// Projects are project keys (contracts.ProjectKeyPattern). None means the grant names no
	// project's subject at all.
	Projects []string
	// Repositories are the "owner/name" repositories Dispatch maps to Projects, each mapping's
	// name and its renamed_to both.
	Repositories []string
	// Identity is the account the token names: its sub, or a Google token's email. It names the
	// reply prefix (InboxPrefix).
	Identity string
}

// inboxPrefixBase begins every reply prefix InboxPrefix makes: nats.go's own prefix, "_INBOX.",
// with its dot written "_", so the token differs from nats.go's "_INBOX" and no prefix reaches
// another's.
var inboxPrefixBase = strings.TrimSuffix(nats.InboxPrefix, ".") + "_"

// InboxPrefix is the reply prefix of the client whose credential names identity: "_INBOX_" and
// identity with each byte outside [A-Za-z0-9_-] written "_", so it is one subject token whatever
// the identity holds. The client sets it with nats.CustomInboxPrefix from its own credential's
// identity, and the auth service grants InboxPrefix(identity)+".>" in place of _INBOX.>, so the two
// ends compute one string from one input. Two identities that differ only in the bytes it writes
// "_" share a prefix: an operator who lists both for the auth service gives each the other's
// replies, so the list must not hold two such accounts.
func InboxPrefix(identity string) string {
	prefix := []byte(inboxPrefixBase)
	for i := range len(identity) {
		c := identity[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '-' {
			prefix = append(prefix, c)
		} else {
			prefix = append(prefix, '_')
		}
	}
	return string(prefix)
}

// The subjects of Dispatch and the Legion daemon that the daemon's consumers filter on and that
// no Envoy package exports a name for: Dispatch's project events (the Dispatch outbox's envelope,
// notifications.dispatch.project.<KEY>.<type>) and the type of the one the daemon hears there, a
// repository's mapping to a project changing (packages/daemon/internal/intake/decode.go
// mappingSubject).
const (
	dispatchProjectTopicPrefix = bus.NotificationTopicPrefix + "dispatch.project."
	repoMappingType            = "settings.repo_project.updated"
)

// daemonConsumerPrefix begins the name of every durable consumer a Legion daemon opens on the
// notification stream, legion-go-<project>-<kind> (packages/daemon/internal/intake/consume.go).
const daemonConsumerPrefix = "legion-go-"

// The entries of a grant WithinProjects rewrites, each the family's subjects under every project,
// and the API calls on the notification stream it drops or holds to the daemon's consumers.
const (
	issueLane        = contracts.DispatchIssueTopicPrefix + ">"
	roleExceptions   = bus.ExceptionTopicPrefix + contracts.RoleTopicPrefix + ">"
	legionLanes      = contracts.LegionNoticeTopicPrefix + ">"
	githubLanes      = contracts.GithubTopicPrefix + ">"
	everyTopic       = bus.NotificationTopicPrefix + ">"
	streamInfo       = jsAPI + "STREAM.INFO." + bus.Stream
	streamMsgGet     = jsAPI + "STREAM.MSG.GET." + bus.Stream
	consumerCreate   = jsAPI + "CONSUMER.CREATE." + bus.Stream + "."
	consumerInfo     = jsAPI + "CONSUMER.INFO." + bus.Stream + "."
	consumerNext     = jsAPI + "CONSUMER.MSG.NEXT." + bus.Stream + "."
	consumerAck      = jsAck + bus.Stream + "."
	durableCreate    = jsAPI + "CONSUMER.DURABLE.CREATE."
	streamMsgGetCall = jsAPI + "STREAM.MSG.GET."
	directGetCall    = jsAPI + "DIRECT.GET."
)

var projectKey = regexp.MustCompile(contracts.ProjectKeyPattern)

// WithinProjects is grant held to scope: each entry that reaches every project's subjects
// rewritten to the subjects of scope's projects and repositories, and refused, naming the entry,
// when it cannot be. The grant handed in is not changed. Per entry, a subscribe entry's queue
// group kept after the rewritten subject:
//
//   - _INBOX.>, where every client's replies arrive, becomes InboxPrefix(scope.Identity)+".>";
//   - each family whose names carry the project as a token of their own becomes that family under
//     each project: an issue's events (notifications.dispatch.issue.<P>.>), the role lanes'
//     delivery exceptions (notifications.envoy.exceptions.notifications.role.<P>.>) and the Legion
//     daemon's lanes (notifications.legion.<p>.>, p the project key in lower case, the token
//     contracts.SubjectProject reads back as the key); a repository's events become each
//     repository's (notifications.github.<owner>.<name>.>);
//   - a consumer call on the notification stream that reaches every consumer (its consumer token a
//     wildcard) becomes that call on each consumer the Legion daemon opens under each project,
//     legion-go-<P>-<kind>, and nothing else: the create (its subject naming the consumer's one
//     filter, which nats-server holds the request's body to), the info, the pull
//     (CONSUMER.MSG.NEXT) and the acknowledgements. The kinds are dispatch (the project's issue
//     events), mapping (its repository mapping changes), wake (its Legion wake lane), ready (the
//     one wake lane every daemon shares, which carries no project's message) and github-<n> for
//     each n below the number of repositories, each creatable on any repository of the scope: the
//     daemon numbers its own sorted list, which need not be the scope's;
//   - the notification stream's info and stored-message get are dropped: the info lists every
//     stored subject with its count, the get reads any stored message, and the daemon reads its
//     consumers' info instead.
//
// Refused: a stored-message read of any other stream and a direct get of this one; a consumer
// create whose subject cannot name a filter; any call on a KV bucket; notifications.>; and every
// entry not named above, since an entry this function does not know is one it cannot vouch for.
func WithinProjects(grant Permissions, scope Scope) (Permissions, error) {
	held, err := newHeldScope(scope)
	if err != nil {
		return Permissions{}, err
	}
	publish, err := held.entries(grant.Publish)
	if err != nil {
		return Permissions{}, err
	}
	subscribe, err := held.entries(grant.Subscribe)
	if err != nil {
		return Permissions{}, err
	}
	return Permissions{Publish: publish, Subscribe: subscribe}, nil
}

// heldScope is a Scope checked and laid out for the rewrite: its projects and repositories sorted
// with repeats taken out, each repository's subject prefix, and the daemon's consumers under it.
type heldScope struct {
	scope     Scope
	projects  []string
	repos     []string // each repository's subject prefix, notifications.github.<owner>.<name>
	consumers []daemonConsumer
}

// daemonConsumer is one durable consumer the Legion daemon opens on the notification stream: its
// name and the filters its create may name.
type daemonConsumer struct {
	name    string
	filters []string
}

func newHeldScope(scope Scope) (heldScope, error) {
	projects := slices.Clone(scope.Projects)
	for _, project := range projects {
		if !projectKey.MatchString(project) {
			return heldScope{}, fmt.Errorf("natsgrant: scope project %q is not a project key", project)
		}
	}
	slices.Sort(projects)
	projects = slices.Compact(projects)

	repositories := slices.Clone(scope.Repositories)
	slices.Sort(repositories)
	repositories = slices.Compact(repositories)
	var repos []string
	for _, repository := range repositories {
		owner, name, found := strings.Cut(repository, "/")
		if !found || !repositoryPart(owner) || !repositoryPart(name) {
			return heldScope{}, fmt.Errorf("natsgrant: scope repository %q is not owner/name", repository)
		}
		if prefix := contracts.GithubRepositoryPrefix(owner, name); !slices.Contains(repos, prefix) {
			repos = append(repos, prefix)
		}
	}

	held := heldScope{scope: scope, projects: projects, repos: repos}
	githubFilters := make([]string, len(repos))
	for i, repo := range repos {
		githubFilters[i] = repo + ".>"
	}
	for _, project := range projects {
		named := func(kind string, filters ...string) daemonConsumer {
			return daemonConsumer{name: daemonConsumerPrefix + project + "-" + kind, filters: filters}
		}
		held.consumers = append(held.consumers,
			named("dispatch", contracts.DispatchIssueTopicPrefix+project+".>"),
			named("mapping", dispatchProjectTopicPrefix+project+"."+repoMappingType),
			named("wake", contracts.LegionWakeSubject(strings.ToLower(project))),
			named("ready", bus.LegionWakeTopic),
		)
		for n := range repositories {
			held.consumers = append(held.consumers, named("github-"+strconv.Itoa(n), githubFilters...))
		}
	}
	return held, nil
}

// repositoryPart reports whether part is a repository owner or name a subject can spell: not
// empty, and holding no '/', wildcard, whitespace or control character. A dot is allowed: the
// subject writes it "_" (contracts.SanitizeSubjectSegment).
func repositoryPart(part string) bool {
	return part != "" && !strings.ContainsFunc(part, func(r rune) bool {
		return r == '/' || r == '*' || r == '>' || unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

// entries is each of entries held to the scope (WithinProjects), in order, with no entry twice.
func (h heldScope) entries(entries []string) ([]string, error) {
	var out []string
	for _, entry := range entries {
		subject, queue, _ := strings.Cut(strings.TrimSpace(entry), " ")
		subjects, err := h.subjects(entry, subject)
		if err != nil {
			return nil, err
		}
		for _, held := range subjects {
			if queue != "" {
				held += " " + queue
			}
			if !slices.Contains(out, held) {
				out = append(out, held)
			}
		}
	}
	return out, nil
}

// subjects is subject, the subject of entry, held to the scope: the subjects that replace it,
// none when it is dropped, or the refusal naming entry.
func (h heldScope) subjects(entry, subject string) ([]string, error) {
	refuse := func(why string) ([]string, error) {
		return nil, fmt.Errorf("natsgrant: grant entry %q %s", entry, why)
	}
	switch {
	case isKVCall(subject):
		return refuse("is a KV bucket's call, which no project's grant holds")
	case subject == inboxes:
		if h.scope.Identity == "" {
			return refuse("needs the scope's identity to name its reply prefix")
		}
		return []string{InboxPrefix(h.scope.Identity) + ".>"}, nil
	case subject == streamInfo, subject == streamMsgGet:
		return nil, nil
	case strings.HasPrefix(subject, streamMsgGetCall), strings.HasPrefix(subject, directGetCall):
		return refuse("reads any stored message of a stream")
	case strings.HasPrefix(subject, durableCreate):
		return refuse("creates a consumer whose subject names no filter")
	case strings.HasPrefix(subject, consumerCreate):
		switch rest := strings.TrimPrefix(subject, consumerCreate); {
		case rest == ">" || rest == "*.>":
			return h.consumerCalls(func(c daemonConsumer) []string {
				calls := make([]string, len(c.filters))
				for i, filter := range c.filters {
					calls[i] = consumerCreate + c.name + "." + filter
				}
				return calls
			}), nil
		case !strings.Contains(rest, "."):
			return refuse("creates a consumer whose subject names no filter")
		}
	case strings.HasPrefix(subject, consumerInfo), strings.HasPrefix(subject, consumerNext):
		call := consumerInfo
		if strings.HasPrefix(subject, consumerNext) {
			call = consumerNext
		}
		if rest := strings.TrimPrefix(subject, call); rest == ">" || rest == "*" {
			return h.consumerCalls(func(c daemonConsumer) []string { return []string{call + c.name} }), nil
		}
	case strings.HasPrefix(subject, consumerAck):
		if rest := strings.TrimPrefix(subject, consumerAck); rest == ">" || rest == "*.>" {
			return h.consumerCalls(func(c daemonConsumer) []string { return []string{consumerAck + c.name + ".>"} }), nil
		}
	case subject == issueLane:
		return h.perProject(func(project string) string { return contracts.DispatchIssueTopicPrefix + project + ".>" }), nil
	case subject == roleExceptions:
		return h.perProject(func(project string) string {
			return bus.ExceptionTopicPrefix + contracts.RoleTopicPrefix + project + ".>"
		}), nil
	case subject == legionLanes:
		return h.perProject(func(project string) string {
			return contracts.LegionNoticeTopicPrefix + strings.ToLower(project) + ".>"
		}), nil
	case subject == githubLanes:
		repos := make([]string, len(h.repos))
		for i, repo := range h.repos {
			repos[i] = repo + ".>"
		}
		return repos, nil
	case subject == everyTopic:
		return refuse("reaches every project's notifications")
	}
	return refuse("cannot be held to projects")
}

// isKVCall reports whether subject is a call on a KV bucket: a write or read of its keys, a
// JetStream API call on its stream (KV_<bucket>), or its watchers' flow control or
// acknowledgements.
func isKVCall(subject string) bool {
	if strings.HasPrefix(subject, kvSubjects) {
		return true
	}
	for _, prefix := range []string{jsFlow, jsAck} {
		if strings.HasPrefix(subject, prefix+kvStream) {
			return true
		}
	}
	api, found := strings.CutPrefix(subject, jsAPI)
	if !found {
		return false
	}
	// A stream call names the stream as its third token (STREAM.INFO.<stream>,
	// CONSUMER.CREATE.<stream>, DIRECT.GET.<stream>), and STREAM.MSG.GET and CONSUMER.MSG.NEXT as
	// their fourth.
	stream := 2
	if strings.HasPrefix(api, "STREAM.MSG.GET.") || strings.HasPrefix(api, "CONSUMER.MSG.NEXT.") {
		stream = 3
	}
	tokens := strings.Split(api, ".")
	return len(tokens) > stream && strings.HasPrefix(tokens[stream], kvStream)
}

// perProject is subject(project) for each project of the scope.
func (h heldScope) perProject(subject func(project string) string) []string {
	out := make([]string, len(h.projects))
	for i, project := range h.projects {
		out[i] = subject(project)
	}
	return out
}

// consumerCalls is calls(consumer) for each consumer the daemon opens under the scope.
func (h heldScope) consumerCalls(calls func(daemonConsumer) []string) []string {
	var out []string
	for _, consumer := range h.consumers {
		out = append(out, calls(consumer)...)
	}
	return out
}
