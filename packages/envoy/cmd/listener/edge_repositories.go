package main

// edge_repositories.go is the edge listener's reader of contract K-E5: which repositories the
// issues of each project it serves resolve to, as the secrets broker relays Dispatch's one rule
// (GET /v1/project-repositories, internal/broker/api/project_repositories.go). The edge asks it
// with its own broker account, fresh at each registration that names a GitHub topic and every
// edgeRepositoryRefresh for each project it serves, and fails closed on any answer that is not a
// whole list: a registration it cannot read for is 503 repositories_unknown, and while no answer
// of a session's projects is younger than edgeRepositoryAnswerLife that session is relayed no
// GitHub frame (project_scope.go, session_relay.go).
//
// The list is the whole of what it knows of a repository. It reads no repository mapping of its
// own, and no project of a repository is ever derived here: a repository is a project's only
// while the broker's answer for that project names it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/metrics"
)

const (
	// edgeRepositoriesPath is the broker's route K-E5 names, asked as ?project=<KEY>.
	edgeRepositoriesPath = "/v1/project-repositories"
	// edgeRepositoryRefresh is how often the edge reads every project's answer again (R15: at
	// least every 30 seconds).
	edgeRepositoryRefresh = 30 * time.Second
	// edgeRepositoryAnswerLife is how long an answer holds for the relay (R15: a GitHub event is
	// relayed only while its repository is in a list no older than 60 seconds). Two refreshes
	// fit in it, so one failed read alone withholds nothing.
	edgeRepositoryAnswerLife = 60 * time.Second
	// edgeRepositoryReadTimeout bounds one read of the broker: its own read of Dispatch is
	// bounded at 2 s (K-E5), and a registration's budget (sendRequestBudget) bounds the whole of
	// a registration's reads below this.
	edgeRepositoryReadTimeout = 5 * time.Second
	// edgeRepositoryBodyLimit bounds an answer's body; a list past it is no answer.
	edgeRepositoryBodyLimit = 1 << 20
)

// The reasons envoy_edge_github_withheld_total counts a GitHub frame under.
const (
	// withheldNotInProject is a frame of a repository that no fresh answer of the session's
	// projects names.
	withheldNotInProject = "not_in_project"
	// withheldAnswerUnknown is a frame read while some project of the session has no answer
	// younger than edgeRepositoryAnswerLife, so the edge cannot say the repository is its.
	withheldAnswerUnknown = "answer_unknown"
)

// errRepositoriesUnknown is a read of a project's repositories that gave no whole answer: the
// broker's refusal or 503, a transport failure, a body that does not decode, or the listener's
// own account unread. It is never a list, so nothing is admitted on it.
var errRepositoriesUnknown = errors.New("the repositories of the project's issues could not be read")

// repositoryAnswer is one project's answer and when it was read.
type repositoryAnswer struct {
	repositories []string
	readAt       time.Time
}

// edgeRepositories reads and holds the answer of each project the edge serves.
type edgeRepositories struct {
	brokerURL string
	account   brokertoken.Account
	projects  []string
	client    *http.Client
	// now is the clock answers are dated and aged by; time.Now outside a test.
	now func() time.Time
	// withheld is envoy_edge_github_withheld_total{reason}.
	withheld *metrics.Counter
	logger   *logging.Logger

	mu      sync.Mutex
	answers map[string]repositoryAnswer
	// passes counts the refresh passes that have finished (runEdgeRepositories), so a test
	// orders a publish after one.
	passes atomic.Uint64
}

// newEdgeRepositories is the reader of the broker at brokerURL, asking as account, for the
// projects the edge serves.
func newEdgeRepositories(brokerURL string, account brokertoken.Account, projects []string, withheld *metrics.Counter, logger *logging.Logger) *edgeRepositories {
	return &edgeRepositories{
		brokerURL: strings.TrimSuffix(brokerURL, "/"),
		account:   account,
		projects:  slices.Clone(projects),
		client:    &http.Client{Timeout: edgeRepositoryReadTimeout},
		now:       time.Now,
		withheld:  withheld,
		logger:    logger,
		answers:   map[string]repositoryAnswer{},
	}
}

// newEdgeWithheldCounter registers envoy_edge_github_withheld_total.
func newEdgeWithheldCounter(met *metrics.Registry) *metrics.Counter {
	return met.NewCounter("envoy_edge_github_withheld_total",
		"GitHub frames the edge listener acknowledged undelivered, by reason (not_in_project, answer_unknown)")
}

// read asks the broker for project's repositories now, and keeps the answer when it is a whole
// one. Every other outcome is errRepositoriesUnknown, naming what went wrong, and keeps nothing.
func (e *edgeRepositories) read(ctx context.Context, project string) ([]string, error) {
	bearer, err := e.account.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: read the listener's own account: %w", errRepositoriesUnknown, err)
	}
	ctx, cancel := context.WithTimeout(ctx, edgeRepositoryReadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, e.brokerURL+edgeRepositoriesPath+"?project="+url.QueryEscape(project), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRepositoriesUnknown, err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	response, err := e.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: ask the broker: %w", errRepositoriesUnknown, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, edgeRepositoryBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read the broker's answer: %w", errRepositoriesUnknown, err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: the broker answered %d: %s", errRepositoriesUnknown, response.StatusCode, strings.TrimSpace(string(body)))
	}
	if len(body) > edgeRepositoryBodyLimit {
		return nil, fmt.Errorf("%w: the broker's answer is longer than %d bytes", errRepositoriesUnknown, edgeRepositoryBodyLimit)
	}
	var answer struct {
		Project      string    `json:"project"`
		Repositories *[]string `json:"repositories"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("%w: decode the broker's answer: %w", errRepositoriesUnknown, err)
	}
	if answer.Project != project || answer.Repositories == nil {
		return nil, fmt.Errorf("%w: the broker answered for project %q with repositories %v, not a list for %s", errRepositoriesUnknown, answer.Project, answer.Repositories, project)
	}
	for _, repository := range *answer.Repositories {
		if owner, name, ok := strings.Cut(repository, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return nil, fmt.Errorf("%w: the broker's answer names %q, which is no owner/name", errRepositoriesUnknown, repository)
		}
	}
	e.mu.Lock()
	e.answers[project] = repositoryAnswer{repositories: *answer.Repositories, readAt: e.now()}
	e.mu.Unlock()
	return *answer.Repositories, nil
}

// readFresh reads the answer of each of projects now, for a registration: the union of their
// repositories, or errRepositoriesUnknown when any one of them could not be read.
func (e *edgeRepositories) readFresh(ctx context.Context, projects []string) ([]string, error) {
	var repositories []string
	for _, project := range projects {
		answered, err := e.read(ctx, project)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, answered...)
	}
	return repositories, nil
}

// refresh reads the answer of every project the edge serves again. A read that fails keeps the
// last answer, which ages out at edgeRepositoryAnswerLife, and is logged.
func (e *edgeRepositories) refresh(ctx context.Context) {
	for _, project := range e.projects {
		if _, err := e.read(ctx, project); err != nil {
			e.logger.Warn("edge listener could not read its project's repositories; GitHub events wait for the next answer",
				slog.String("project", project), slog.String("error", err.Error()))
		}
	}
}

// standing is the repositories the fresh answers of projects name, and whether every one of
// projects has an answer younger than edgeRepositoryAnswerLife. With complete false the list is
// only what the fresh answers name, so a caller may admit by it but never take an absence from it
// as the repository's leaving.
func (e *edgeRepositories) standing(projects []string) (repositories []string, complete bool) {
	now := e.now()
	complete = len(projects) > 0
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, project := range projects {
		answer, ok := e.answers[project]
		if !ok || now.Sub(answer.readAt) >= edgeRepositoryAnswerLife {
			complete = false
			continue
		}
		repositories = append(repositories, answer.repositories...)
	}
	return repositories, complete
}

// relayVerdict is whether the relay withholds a frame on subject from a session whose token
// lists projects, and why: a frame that is no GitHub event's is never withheld; a GitHub event is
// relayed while a fresh answer of the session's projects names its repository, withheld
// not_in_project once every one of those answers is fresh and none names it, and withheld
// answer_unknown otherwise.
func (e *edgeRepositories) relayVerdict(projects []string, subject string) (withhold bool, reason string) {
	if !strings.HasPrefix(subject, contracts.GithubTopicPrefix) {
		return false, ""
	}
	owner, name, single := githubSubjectRepository(subject)
	if !single {
		return true, withheldNotInProject
	}
	repositories, complete := e.standing(projects)
	if repositoryAmong(repositories, owner, name) {
		return false, ""
	}
	if complete {
		return true, withheldNotInProject
	}
	return true, withheldAnswerUnknown
}

// githubSubjectRepository is the owner and name a GitHub subject or topic names, its third and
// fourth tokens, and false for one that names no single repository: fewer tokens, or a wildcard
// or an empty token in either place.
func githubSubjectRepository(subject string) (owner, name string, ok bool) {
	rest, found := strings.CutPrefix(subject, contracts.GithubTopicPrefix)
	if !found {
		return "", "", false
	}
	tokens := strings.SplitN(rest, ".", 3)
	if len(tokens) < 2 || !isRepositoryToken(tokens[0]) || !isRepositoryToken(tokens[1]) {
		return "", "", false
	}
	return tokens[0], tokens[1], true
}

// isRepositoryToken reports whether token names one owner or one name: neither empty nor a
// wildcard.
func isRepositoryToken(token string) bool {
	return token != "" && token != "*" && token != ">"
}

// repositoryAmong reports whether one of repositories, each owner/name as Dispatch answers it,
// is the repository whose subject tokens are owner and name: the tokens
// contracts.GithubRepositoryPrefix writes for it, compared ignoring case. Dispatch answers a
// repository lower-cased (canonicalRepo) while the intake writes GitHub's own case and replaces
// only dots and white space (contracts.SanitizeSubjectSegment), and GitHub's names are
// case-insensitive, so Acme/Pub's events are acme/pub's.
func repositoryAmong(repositories []string, owner, name string) bool {
	for _, repository := range repositories {
		answerOwner, answerName, _ := strings.Cut(repository, "/")
		tokens := strings.Split(strings.TrimPrefix(contracts.GithubRepositoryPrefix(answerOwner, answerName), contracts.GithubTopicPrefix), ".")
		if len(tokens) == 2 && strings.EqualFold(tokens[0], owner) && strings.EqualFold(tokens[1], name) {
			return true
		}
	}
	return false
}
