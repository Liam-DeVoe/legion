package main

// project_scope.go is the listener's edge mode: the edge listener an outside person's agents
// reach Envoy through (spec R15), set by ENVOY_EDGE_PROJECTS (config.Service.EdgeProjects), which
// names the projects it serves. It is not a listener of either of plan C's modes: it holds every
// session to the projects of the token that admitted it, also where each project is public, and
// gives a session no message of no project but the GitHub events of its projects' repositories
// (R9's exception for a deployment that runs the edge listener).
//
// The rules, each held by a row of edge_mode_test.go:
//
//   - E1, admission: a broker token alone, whose projects are all among the ones the edge serves,
//     of project scope named or person (EdgeAdmits), decided in apiAuth before any handler. The
//     admitted request carries its token's projects (withHeldProjects), and every rule below
//     reads them there (heldProjects), so a request that is not held is one this listener
//     serves as every listener does.
//   - E2, the topics a session follows: its own agent subject, a topic of one of its projects, or
//     a GitHub topic of one repository that the broker's answer for its projects names, read fresh
//     for the registration (edgeFollowRefusal, edge_repositories.go). At relay a GitHub frame
//     reaches the session only while a fresh answer names its repository (sessionRelays.relayNATS),
//     and the refresh removes an interest in a repository that left (pruneEdgeInterests). What a
//     session publishes is narrower: a role subject of its project in the form that names it
//     (edgePublishRefusal).
//   - E3, roles: a claim or a read names a role that works for one of the token's projects
//     (edgeRoleRefusal).
//   - E4, sends: the target session's token lists one of the token's projects, else the send is
//     answered as to a missing session (sessionOfProjects).
//   - E5, reads: the session list, the registry, the session rows' roles and the Legion agent
//     routes answer only sessions and roles of the token's projects; the interest list and
//     another session's interests are 403, and the Legion agent registry is not written here.
//   - E6, the start: refused beside every other credential, beside webhooks, and without the
//     broker-token settings (validateEdgeMode).
//
// A project is one of the token's by the bus's rule for a broker token (access.BrokerTokenAdmits,
// projectHeld), read as for a private project so that a token listing none, which E1 never
// admits here, holds no project at all.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/config"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/legionagents"
	"github.com/sjawhar/envoy/internal/projectscope"
	"github.com/sjawhar/envoy/internal/session"
	"github.com/sjawhar/envoy/internal/store"
	"github.com/sjawhar/envoy/listenerwire"
)

// ErrEdgeNotAdmitted is every refusal EdgeAdmits answers; the refusal's text says which rule.
var ErrEdgeNotAdmitted = errors.New("this edge listener does not admit the access token")

// EdgeAdmits is E1: whether an edge listener serving edgeProjects admits caller, the session a
// broker token names. It admits a token of project scope named or person whose projects are all
// among edgeProjects, and at least one, and refuses a read-only token. nil is admitted; every
// refusal wraps ErrEdgeNotAdmitted and names the rule. It reads nothing but its arguments, so
// apiAuth asks it of each request's caller and the relay socket asks it again of every live
// answer about the token that opened it (K-E2).
func EdgeAdmits(edgeProjects []string, caller brokertoken.Caller) error {
	switch {
	case caller.ReadOnly:
		return fmt.Errorf("%w: a read-only token acts as no session here", ErrEdgeNotAdmitted)
	case caller.ProjectScope != brokertoken.ProjectScopeNamed && caller.ProjectScope != brokertoken.ProjectScopePerson:
		return fmt.Errorf("%w: its project scope is %q, and the edge admits only %s and %s",
			ErrEdgeNotAdmitted, caller.ProjectScope, brokertoken.ProjectScopeNamed, brokertoken.ProjectScopePerson)
	case len(caller.Projects) == 0:
		return fmt.Errorf("%w: it lists no project, and the edge serves only %s", ErrEdgeNotAdmitted, strings.Join(edgeProjects, ", "))
	}
	for _, project := range caller.Projects {
		if !slices.Contains(edgeProjects, project) {
			return fmt.Errorf("%w: it lists project %s, which this edge does not serve (it serves %s)",
				ErrEdgeNotAdmitted, project, strings.Join(edgeProjects, ", "))
		}
	}
	return nil
}

// The reasons an edge refusal carries (listenerwire.EdgeRefusal.Reason).
const (
	edgeTokenNotAdmitted         = "token_not_admitted"
	edgeTopicNotInProject        = "topic_not_in_project"
	edgeRepositoryNotInProject   = "repository_not_in_project"
	edgeRepositoriesUnknown      = "repositories_unknown"
	edgeTopicNotPublished        = "topic_not_published"
	edgeRoleNotInProject         = "role_not_in_project"
	edgeSessionNotInProject      = "session_not_in_project"
	edgeLegionRegistryNotWritten = "legion_registry_not_written"
)

// edgeRefusal is one answer an edge rule gives a request it refuses.
type edgeRefusal struct {
	status int
	body   listenerwire.EdgeRefusal
}

// refuseEdge is the refusal of reason under status, its sentence message, about the request
// fields expected.
func refuseEdge(status int, reason, message string, expected ...string) *edgeRefusal {
	return &edgeRefusal{status: status, body: listenerwire.EdgeRefusal{Error: message, Reason: reason, Expected: expected}}
}

func (e *edgeRefusal) write(w http.ResponseWriter) { writeJSON(w, e.status, e.body) }

// heldProjectsKey is the context key an edge admission carries its token's projects under.
type heldProjectsKey struct{}

// withHeldProjects marks a request an edge listener admitted: every rule here holds it to
// projects, the projects of the token apiAuth admitted it on.
func withHeldProjects(ctx context.Context, projects []string) context.Context {
	return context.WithValue(ctx, heldProjectsKey{}, slices.Clone(projects))
}

// heldProjects is the projects r is held to, and whether it is held at all: true only for a
// request an edge listener admitted (apiAuth), false on every other listener.
func heldProjects(r *http.Request) ([]string, bool) {
	projects, held := r.Context().Value(heldProjectsKey{}).([]string)
	return projects, held
}

// projectHeld reports whether project is one of projects, by the bus's rule for a broker token
// (projectscope.BrokerTokenAdmits), read as for a private project: a list holds what it names and
// nothing else, NoProject and a wildcard among the rest.
func projectHeld(projects []string, project string) bool {
	return projectscope.BrokerTokenAdmits(projects, project, true)
}

// sessionOfProjects reports whether a session whose entry is entry is one of projects': the token
// that registered it (K6, session.SessionEntry.Projects) lists one of them. An entry no token's
// list was recorded on, one an earlier build wrote among them, is no project's.
func sessionOfProjects(entry session.SessionEntry, projects []string) bool {
	return slices.ContainsFunc(entry.Projects, func(project string) bool { return projectHeld(projects, project) })
}

// rolesOfProjects is the roles among roles, each named by its claim's key, that work for one of
// projects (contracts.ParseRoleClaimKey), in their order.
func rolesOfProjects(roles, projects []string) []string {
	kept := make([]string, 0, len(roles))
	for _, role := range roles {
		if project, _ := contracts.ParseRoleClaimKey(role); projectHeld(projects, project) {
			kept = append(kept, role)
		}
	}
	return kept
}

// legionAgentOfProjects reports whether a Legion agent registry entry is one of projects': the
// issue it works is.
func legionAgentOfProjects(agent legionagents.Agent, projects []string) bool {
	project, ok := contracts.IssueProject(agent.Issue)
	return ok && projectHeld(projects, project)
}

// legionAgentsOfProjects is agents with only projects' entries, in their order.
func legionAgentsOfProjects(agents []legionagents.Agent, projects []string) []legionagents.Agent {
	return slices.DeleteFunc(slices.Clone(agents), func(agent legionagents.Agent) bool { return !legionAgentOfProjects(agent, projects) })
}

// unknownSession is the 404 a session the edge does not show gets: the answer of a session no
// listener has heard of, naming no state and no last registration, so it tells the caller
// nothing of a session of another project.
func unknownSession(sessionID string) listenerwire.NoLiveSessionRefusal {
	return listenerwire.NoLiveSessionRefusal{
		Error: fmt.Sprintf("no live session %s; this listener does not know when it last registered", sessionID),
		State: session.Gone,
	}
}

// edgeFollowRefusal is E2 for a registration of sessionID naming topics, or nil: each topic is
// the session's own agent subject in either form, a topic contracts.SubjectProject gives one of
// the token's projects, or a GitHub topic of one repository that the broker's answer for the
// token's projects names, read fresh for this registration within ctx. A GitHub topic naming no
// single repository, or one not in the answer, is refused repository_not_in_project; every other
// topic, another session's agent subject, the listener's own notifications.envoy. lanes and every
// subject of no project among them, is refused topic_not_in_project. The 403 names every refused
// topic (listenerwire.EdgeRefusal.Refused), so the client drops exactly those and registers the
// rest again. A registration whose read gave no whole answer is 503 repositories_unknown, which
// names no topic: the edge cannot say which of its GitHub topics to refuse. Nothing is stored for
// a refused registration.
func edgeFollowRefusal(ctx context.Context, r *http.Request, repositories *edgeRepositories, sessionID string, topics []string) *edgeRefusal {
	projects, held := heldProjects(r)
	if !held {
		return nil
	}
	type githubTopic struct{ topic, owner, name string }
	// Each topic is judged once and named once, in the request's order; a GitHub topic that
	// names one repository waits for the answer that judges it.
	verdicts := make(map[string]string, len(topics))
	var github []githubTopic
	for _, topic := range topics {
		if _, judged := verdicts[topic]; judged {
			continue
		}
		verdicts[topic] = ""
		switch {
		case ownAgentSubject(sessionID, projects, topic):
		case strings.HasPrefix(topic, contracts.GithubTopicPrefix):
			if owner, name, single := githubSubjectRepository(topic); single {
				github = append(github, githubTopic{topic: topic, owner: owner, name: name})
			} else {
				verdicts[topic] = edgeRepositoryNotInProject
			}
		case strings.HasPrefix(topic, contracts.AgentTopicPrefix), strings.HasPrefix(topic, listenerLanePrefix):
			verdicts[topic] = edgeTopicNotInProject
		default:
			if project, _, ok := contracts.SubjectProject(topic); !ok || !projectHeld(projects, project) {
				verdicts[topic] = edgeTopicNotInProject
			}
		}
	}
	if len(github) > 0 {
		unknown := func(err error) *edgeRefusal {
			return refuseEdge(http.StatusServiceUnavailable, edgeRepositoriesUnknown,
				fmt.Sprintf("the repositories of %s's issues could not be read, so no GitHub topic is followed now: %v", strings.Join(projects, ", "), err))
		}
		if repositories == nil {
			return unknown(errors.New("this listener reads no repository answer"))
		}
		answered, err := repositories.readFresh(ctx, projects)
		if err != nil {
			return unknown(err)
		}
		for _, topic := range github {
			if !repositoryAmong(answered, topic.owner, topic.name) {
				verdicts[topic.topic] = edgeRepositoryNotInProject
			}
		}
	}
	refusal := refuseEdge(http.StatusForbidden, edgeRepositoryNotInProject, "", "topics")
	for _, topic := range topics {
		reason := verdicts[topic]
		if reason == "" || slices.Contains(refusal.body.Refused, topic) {
			continue
		}
		refusal.body.Refused = append(refusal.body.Refused, topic)
		if reason == edgeTopicNotInProject {
			refusal.body.Reason = edgeTopicNotInProject
		}
	}
	if len(refusal.body.Refused) == 0 {
		return nil
	}
	refusal.body.Error = fmt.Sprintf("%s not followed here: a session at the edge follows its own agent subject, the topics of %s, and the GitHub topics of one repository each that an issue of %s resolves to",
		strings.Join(refusal.body.Refused, ", "), strings.Join(projects, ", "), strings.Join(projects, ", "))
	return refusal
}

// listenerLanePrefix begins every lane of the listener's own (the kept, resume and exception
// lanes, the relay subjects and their receipts), which no session follows at the edge.
const listenerLanePrefix = bus.NotificationTopicPrefix + "envoy."

// ownAgentSubject reports whether topic is sessionID's own agent subject in either form: the old
// form, the new form under every project (contracts.CanonicalTopic writes it with a wildcard), or
// the new form under one of projects.
func ownAgentSubject(sessionID string, projects []string, topic string) bool {
	if topic == contracts.LegacyAgentSubject(sessionID) {
		return true
	}
	rest, found := strings.CutPrefix(topic, contracts.AgentTopicPrefix)
	if !found {
		return false
	}
	project, target, found := strings.Cut(rest, ".")
	return found && target == sessionID && (project == "*" || projectHeld(projects, project))
}

// edgePublishRefusal is E2's publish rule and E4's role publish, or nil: a session at the edge
// publishes only a role subject in the form that names the project
// (notifications.role.<KEY>.<role>), of one of its token's projects, for a role that works for
// that project (contracts.ClaimProject): a Legion role of the project, or a free-form role under
// it, whose claim is keyed on the project (store.RoleClaimKeyOf). A Legion role of another project
// named under this one is refused, since its claim is its own project's. A message to a session
// goes through the send route (E4), and nothing else is published here: a Dispatch event of the
// project, a GitHub, Slack or team subject, and a lane of the listener's own are 403
// topic_not_published.
func edgePublishRefusal(r *http.Request, topic string) *edgeRefusal {
	projects, held := heldProjects(r)
	if !held {
		return nil
	}
	if lane, isRole := strings.CutPrefix(topic, contracts.RoleTopicPrefix); isRole {
		project, role, legacy := contracts.CanonicalRole(lane)
		if claimed, err := contracts.ClaimProject(role, project); !legacy && err == nil && claimed == project && projectHeld(projects, project) {
			return nil
		}
	}
	return refuseEdge(http.StatusForbidden, edgeTopicNotPublished,
		fmt.Sprintf("%s is not published here: a session at the edge publishes only a role subject of %s, notifications.role.<project>.<role> for a role of that project, and sends to a session through /v1/messages/send",
			topic, strings.Join(projects, ", ")), "topic")
}

// edgeRoleRefusal is E3 for a claim or a read of role, in either form, or nil: the project the
// claim works for (contracts.ClaimProject, given the project the new form names) is one of the
// token's. What passes is claimed and read under the key of that project (store.RoleClaimKeyOf):
// the bare name for a Legion role, and <project>.<role> for a free-form role under the project.
func edgeRoleRefusal(r *http.Request, role string) *edgeRefusal {
	projects, held := heldProjects(r)
	if !held {
		return nil
	}
	requested, name, legacy := contracts.CanonicalRole(role)
	if legacy {
		requested = ""
	}
	project, err := contracts.ClaimProject(name, requested)
	if err == nil && projectHeld(projects, project) {
		return nil
	}
	return refuseEdge(http.StatusForbidden, edgeRoleNotInProject,
		fmt.Sprintf("role %s is not claimed or read here: it works for no project of %s", role, strings.Join(projects, ", ")), "role")
}

// edgeInterestReadRefusal is E5 for GET /v1/interests/ and GET /v1/interests/<session_id>, or nil:
// a session at the edge reads its own interest record and no other, and lists none.
func edgeInterestReadRefusal(r *http.Request, sessionID string) *edgeRefusal {
	if _, held := heldProjects(r); !held {
		return nil
	}
	if caller, ok := brokerCaller(r); ok && sessionID != "" && caller.Session == sessionID {
		return nil
	}
	return refuseEdge(http.StatusForbidden, edgeSessionNotInProject, "a session at the edge reads its own interests and no other session's")
}

// edgeLegionWriteRefusal is E5 for a write of the Legion agent registry, or nil: the edge records
// no Legion agent, since a Legion daemon writes the registry through a listener of its own.
func edgeLegionWriteRefusal(r *http.Request) *edgeRefusal {
	if _, held := heldProjects(r); !held {
		return nil
	}
	return refuseEdge(http.StatusForbidden, edgeLegionRegistryNotWritten, "the Legion agent registry is not written at the edge")
}

// registeringProjects is the projects the registration r makes records on its session's entry
// (K6): its broker token's list, sorted and each once as the broker answers it, and [] for a
// registration that presented no broker token or one listing none.
func registeringProjects(r *http.Request) []string {
	if caller, ok := brokerCaller(r); ok && len(caller.Projects) > 0 {
		return slices.Clone(caller.Projects)
	}
	return []string{}
}

// The variables E6 refuses an edge listener beside: each admits a caller by something other than
// a broker token, or serves a route the edge does not.
var edgeRefusedSettings = []string{
	"ENVOY_API_TOKEN",
	"ENVOY_WEBHOOKS",
	"ENVOY_GITHUB_WEBHOOK_SECRET",
	"ENVOY_SLACK_SIGNING_SECRET",
	"ENVOY_GHOSTWISPR_SIGNING_SECRET",
}

// edgeRefusedPrefix begins every setting of the identity-token credentials (ENVOY_OIDC_ISSUER,
// ENVOY_OIDC_AUDIENCE, ENVOY_OIDC_ACCOUNTS, ENVOY_OIDC_GOOGLE_AUDIENCE, ENVOY_OIDC_GOOGLE_ACCOUNTS
// and any later one), which E6 refuses beside an edge listener as a group.
const edgeRefusedPrefix = "ENVOY_OIDC_"

// validateEdgeMode is E6: an edge listener, one serving edgeProjects, refuses to start beside the
// shared token, any ENVOY_OIDC_* setting, the webhooks or a webhook secret, or with the broker-token
// settings incomplete or unset, since it admits a broker token alone; each refusal names the
// variable. environ is the process's environment as os.Environ writes it. A listener serving no
// project is not checked.
func validateEdgeMode(edgeProjects []string, environ []string) error {
	if len(edgeProjects) == 0 {
		return nil
	}
	env := make(map[string]string, len(environ))
	var oidc []string
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		env[name] = value
		if strings.HasPrefix(name, edgeRefusedPrefix) && strings.TrimSpace(value) != "" {
			oidc = append(oidc, name)
		}
	}
	slices.Sort(oidc)
	refuse := func(names ...string) error {
		return fmt.Errorf("%s set beside %s=%s: an edge listener admits a broker token alone and serves no webhook, so unset it",
			strings.Join(names, ", "), config.EdgeProjectsEnvVar, strings.Join(edgeProjects, ","))
	}
	for _, name := range edgeRefusedSettings {
		if strings.TrimSpace(env[name]) != "" {
			return refuse(name)
		}
	}
	if len(oidc) > 0 {
		return refuse(oidc...)
	}
	broker, err := resolveListenerBrokerConfig(func(name string) string { return env[name] })
	if err != nil {
		return fmt.Errorf("%s: %w", config.EdgeProjectsEnvVar, err)
	}
	if !broker.configured() {
		return fmt.Errorf("%s is set and %s is not: an edge listener admits a broker token alone, so it needs %s, %s or %s, and %s",
			config.EdgeProjectsEnvVar, EnvBrokerURL, EnvBrokerURL, EnvBrokerAccountTokenFile, EnvBrokerGoogleAudience, EnvPublicURL)
	}
	return nil
}

// runEdgeRepositories runs the edge listener's repository refresh until ctx ends: one read of
// every project's answer at once, then a pass on each tick (every edgeRepositoryRefresh in main).
// A pass first removes the interests that the answers read before it no longer cover
// (pruneEdgeInterests), then reads every answer again, so a repository that left a project stops
// reaching its sessions at the pass that reads the new answer (the relay's verdict) and leaves
// their interests at the pass after: within two refreshes of the change, and never on the word of
// an answer that is not fresh.
func runEdgeRepositories(ctx context.Context, d *listenerDeps, machineID string, ticks <-chan time.Time) {
	d.repositories.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		pruneEdgeInterests(ctx, d, machineID)
		d.repositories.refresh(ctx)
		d.repositories.passes.Add(1)
	}
}

// pruneEdgeInterests removes, from every interest record this listener registered, each GitHub
// topic whose repository no fresh answer of its session's projects names, and refollows that
// session's consumer, as an unsubscribe does. A session one of whose projects has no fresh
// answer keeps every topic: an answer that is not there says nothing of a repository's leaving,
// and the relay withholds the session's GitHub frames meanwhile. A record it cannot list, or a
// removal that fails, is logged and left to the next pass.
func pruneEdgeInterests(ctx context.Context, d *listenerDeps, machineID string) {
	logger := d.repositories.logger
	items, err := d.registry.List()
	if err != nil {
		logger.Warn("edge listener could not list the interests a repository may have left", slog.String("error", err.Error()))
		return
	}
	for _, item := range items {
		if item.MachineID != machineID {
			continue
		}
		entry, _ := d.sessions.Lookup(item.SessionID)
		repositories, complete := d.repositories.standing(entry.Projects)
		if !complete {
			continue
		}
		var leaving []string
		for _, topic := range item.Topics {
			if !strings.HasPrefix(topic, contracts.GithubTopicPrefix) {
				continue
			}
			if owner, name, single := githubSubjectRepository(topic); !single || !repositoryAmong(repositories, owner, name) {
				leaving = append(leaving, topic)
			}
		}
		if len(leaving) == 0 {
			continue
		}
		removeCtx, cancel := context.WithTimeout(ctx, sendRequestBudget)
		removed, err := d.registry.Remove(removeCtx, item.SessionID, leaving, store.RoleReleaseAuthority{})
		if err != nil {
			cancel()
			logger.Warn("edge listener could not remove the interests of a repository that left the session's projects",
				slog.String("session_id", item.SessionID), slog.Any("topics", leaving), slog.String("error", err.Error()))
			continue
		}
		recordFollowedTopics(removeCtx, d, item.SessionID, logger)
		cancel()
		d.consumers.refollow(ctx, d, item.SessionID)
		logger.Info("edge listener removed the interests of a repository that left the session's projects",
			slog.String("session_id", item.SessionID), slog.Any("topics", removed))
	}
}
