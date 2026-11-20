package natsgrant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
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
	// project's subject at all, so a daemon's grant keeps no publish entry and WithinProjects
	// refuses it.
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
// identity with each byte outside [A-Za-z0-9-], including '_', escaped as '_' and two lowercase
// hex digits. It is one subject token and distinct identities have distinct prefixes. The client
// sets it with nats.CustomInboxPrefix and the auth service grants InboxPrefix(identity)+".>" in
// place of _INBOX.>; both ends call this function with the identity their credential names.
func InboxPrefix(identity string) string {
	return token(inboxPrefixBase, identity)
}

// GitHubConsumerName is the name of the durable consumer through which a Legion daemon of project
// hears repository's GitHub events: legion-go-<project>-github- and repository ("owner/name") in
// lower case, spelled as InboxPrefix spells an identity, so the name is one token whatever the
// repository holds. It lower-cases first because the daemon keeps a repository in GitHub's
// spelling (Acme/Widgets) and Dispatch, whose list the grant is built from, in lower case, and
// both must name one consumer. t-c-10's daemon is to name its consumer with it and WithinProjects
// grants it, so the two cannot drift apart.
//
// The name fits nats-server's limit for every repository GitHub serves. Where the spelled name
// would pass maxConsumerNameLen (an owner and name of escaped bytes, '_' and '.', reach 446 bytes
// under the longest project key), it is the longest head of that spelling that leaves room, cut
// between escapes, then "_z" and the first 32 hex digits of the SHA-256 of the lower-cased
// repository. The mapping stays one-to-one: two names that fit differ as their repositories do,
// "_z" never appears in a name that fits (its '_' opens only an escape of two hex digits), and
// two long names differ in their digests.
func GitHubConsumerName(project, repository string) string {
	repository = strings.ToLower(repository)
	name := token(daemonConsumerPrefix+project+"-github-", repository)
	if len(name) <= maxConsumerNameLen {
		return name
	}
	head := name[:maxConsumerNameLen-len(digestMark)-2*digestBytes]
	// An escape is '_' and two digits, and '_' opens nothing else, so a '_' in the last two bytes
	// begins an escape the cut split.
	if cut := strings.LastIndexByte(head, '_'); cut >= len(head)-2 {
		head = head[:cut]
	}
	digest := sha256.Sum256([]byte(repository))
	return head + digestMark + hex.EncodeToString(digest[:digestBytes])
}

// maxConsumerNameLen is the longest durable consumer name nats-server takes, JSMaxNameLen
// (server/jetstream_api.go:418-420): a longer one is refused with err_code 10102
// (server/consumer.go:1256-1261).
const maxConsumerNameLen = 255

// digestMark opens the digest of a GitHub consumer name whose spelling would not fit, and
// digestBytes is how much of the SHA-256 it carries, written as twice as many hex digits.
const (
	digestMark  = "_z"
	digestBytes = 16
)

// GitHubPrefix is what every subject Envoy's listener publishes repository's GitHub events under
// begins with: notifications.github.<owner>.<name>, each segment spelled by
// contracts.SanitizeSubjectSegment (a dot written "_") in the case it is given. repository is
// "owner/name". Lower-casing the subject is t-c-21's, at that one source.
func GitHubPrefix(repository string) string {
	owner, name, _ := strings.Cut(repository, "/")
	return contracts.GithubRepositoryPrefix(owner, name)
}

// GitHubFilter is the filter of repository's GitHub consumer (GitHubConsumerName): its prefix
// (GitHubPrefix) and ".>". t-c-10's daemon is to name it in its consumer create, as
// WithinProjects' grant does, and nats-server refuses a create whose body's filter is not the one
// its subject names, so the two must be built by this one function.
func GitHubFilter(repository string) string {
	return GitHubPrefix(repository) + ".>"
}

// token is prefix followed by s with each byte outside [A-Za-z0-9-], '_' included, written '_'
// and its two lowercase hex digits: '_' always opens an escape, so distinct values give distinct
// tokens, and the result holds no '.', wildcard, whitespace or control character.
func token(prefix, s string) string {
	const digits = "0123456789abcdef"
	var out strings.Builder
	out.Grow(len(prefix) + 3*len(s))
	out.WriteString(prefix)
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' {
			out.WriteByte(c)
		} else {
			out.WriteByte('_')
			out.WriteByte(digits[c>>4])
			out.WriteByte(digits[c&15])
		}
	}
	return out.String()
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
// when it cannot be. The grant handed in is not changed, and a subscribe entry's queue group
// follows each subject that replaces it. Per entry:
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
//     one wake lane every daemon shares, bus.LegionWakeTopic) and, for each repository of the
//     scope, its GitHub consumer (GitHubConsumerName) on that repository's lane (GitHubFilter).
//     The shared lane is a project's own only once the listener publishes its role and agent wake
//     reports on each project's lane (t-c-10, contracts.LegionWakeSubject) and keeps only
//     listener_ready there: until then it carries every project's role names and session ids,
//     and no daemon may take this grant before t-c-10, whose consumers it describes;
//   - the notification stream's info and stored-message get are dropped: the info lists every
//     stored subject with its count, the get reads any stored message, and t-c-10's daemon reads
//     its consumers' info instead (today's daemon opens the stream and reads stored messages, so
//     it cannot run under this grant).
//
// Refused: a stored-message read of any other stream and a direct get of this one; a consumer
// create whose subject cannot name a filter; any call on a KV bucket; notifications.>; and every
// entry not named above, since an entry this function does not know is one it cannot vouch for.
//
// A grant that comes out with no publish or no subscribe entry is refused too, naming the
// direction: nats-server holds a user JWT to nothing in a direction whose allow list is empty
// (server/accounts.go, buildPermissionsFromJwt), and the auth service writes these lists into the
// JWT as they are. A daemon's grant held to no project keeps no publish entry, so it is refused
// rather than answered as a grant that may publish to every subject.
//
// The deny lists are kept as they are handed in, entry for entry: a deny only narrows, so one a
// grant carries holds within any scope, and dropping it would let through what it refuses.
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
	narrowed := Permissions{
		Publish: publish, Subscribe: subscribe,
		PublishDeny: slices.Clone(grant.PublishDeny), SubscribeDeny: slices.Clone(grant.SubscribeDeny),
	}
	if direction, open := OpenDirection(narrowed); open {
		return Permissions{}, fmt.Errorf("natsgrant: grant held to scope keeps no %s entry, and a NATS user with no %s allow list may %s to every subject", direction, direction, direction)
	}
	return narrowed, nil
}

// heldScope is a Scope checked and laid out for the rewrite: the identity that names its reply
// prefix, its projects and repositories sorted with repeats taken out, each repository's lane,
// and the daemon's consumers under it.
type heldScope struct {
	identity  string
	projects  []string
	repoLanes []string // each repository's events, notifications.github.<owner>.<name>.>
	consumers []daemonConsumer
}

// daemonConsumer is one durable consumer the Legion daemon opens on the notification stream: its
// name and the one filter its create names.
type daemonConsumer struct {
	name, filter string
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
	if len(projects) == 0 && len(repositories) > 0 {
		return heldScope{}, fmt.Errorf("natsgrant: scope names repositories %q but no project they belong to", repositories)
	}
	held := heldScope{identity: scope.Identity, projects: projects}
	lanes := make([]string, len(repositories))
	for i, repository := range repositories {
		owner, name, found := strings.Cut(repository, "/")
		if !found || !repositoryPart(owner) || !repositoryPart(name) {
			return heldScope{}, fmt.Errorf("natsgrant: scope repository %q is not owner/name", repository)
		}
		lanes[i] = GitHubFilter(repository)
		// Two repositories can spell one lane (a.b and a_b): the lane is held once.
		if !slices.Contains(held.repoLanes, lanes[i]) {
			held.repoLanes = append(held.repoLanes, lanes[i])
		}
	}

	for _, project := range projects {
		named := func(kind, filter string) daemonConsumer {
			return daemonConsumer{name: daemonConsumerPrefix + project + "-" + kind, filter: filter}
		}
		held.consumers = append(held.consumers,
			named("dispatch", contracts.DispatchIssueTopicPrefix+project+".>"),
			named("mapping", dispatchProjectTopicPrefix+project+"."+repoMappingType),
			named("wake", contracts.LegionWakeSubject(strings.ToLower(project))),
			named("ready", bus.LegionWakeTopic),
		)
		for i, repository := range repositories {
			held.consumers = append(held.consumers, daemonConsumer{name: GitHubConsumerName(project, repository), filter: lanes[i]})
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
	seen := make(map[string]struct{})
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
			if _, repeat := seen[held]; !repeat {
				seen[held] = struct{}{}
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
		if h.identity == "" {
			return refuse("needs the scope's identity to name its reply prefix")
		}
		return []string{InboxPrefix(h.identity) + ".>"}, nil
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
				return []string{consumerCreate + c.name + "." + c.filter}
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
		return h.repoLanes, nil
	case subject == everyTopic:
		return refuse("reaches every project's notifications")
	}
	return refuse("cannot be held to projects")
}

// isKVCall reports whether subject is a call on a KV bucket: a write or read of its keys, a
// JetStream API call on its stream (KV_<bucket>), or its watchers' flow control or
// acknowledgements.
func isKVCall(subject string) bool {
	if strings.HasPrefix(subject, kvSubjects) ||
		strings.HasPrefix(subject, jsFlow+kvStream) || strings.HasPrefix(subject, jsAck+kvStream) {
		return true
	}
	api, found := strings.CutPrefix(subject, jsAPI)
	if !found {
		return false
	}
	// The API names a call's stream as its third token (STREAM.INFO.<stream>) or, when its verb is
	// two tokens, its fourth (STREAM.MSG.GET.<stream>, CONSUMER.DURABLE.CREATE.<stream>.<consumer>),
	// so a call with any token naming a KV stream is a call on a bucket. No entry WithinProjects
	// holds has one.
	return slices.ContainsFunc(strings.Split(api, "."), func(token string) bool { return strings.HasPrefix(token, kvStream) })
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
